package usecase

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
)

// MaxRetryBackoff caps exponential backoff including jitter, but not Retry-After.
const MaxRetryBackoff = time.Minute

// JitterFunc returns an addition in [0, limit]. Custom functions must be safe
// for concurrent calls. Out-of-range results are clamped defensively.
type JitterFunc func(limit time.Duration) time.Duration

type RetryPolicy struct {
	maxAttempts int64
	baseDelay   time.Duration
	jitter      JitterFunc
}

func NewRetryPolicy(maxAttempts int, baseDelay time.Duration, jitter JitterFunc) (*RetryPolicy, error) {
	if maxAttempts <= 0 || int64(maxAttempts) > math.MaxInt32 || baseDelay <= 0 {
		return nil, errors.New("retry: invalid limits")
	}
	if jitter == nil {
		jitter = func(limit time.Duration) time.Duration {
			return time.Duration(rand.Int64N(int64(limit) + 1))
		}
	}
	return &RetryPolicy{int64(maxAttempts), min(baseDelay, MaxRetryBackoff), jitter}, nil
}

type FailurePlan struct {
	Decision repository.FailureDecision
	// BlockFor is positive only for 429, including the final attempt.
	BlockFor time.Duration
}

// Plan receives the application context, not an expired Fetch timeout context.
// It never sleeps, accesses storage, or starts another Fetch.
func (p *RetryPolicy) Plan(ctx context.Context, attempt domain.Attempt, fetchErr error) (FailurePlan, error) {
	if err := ctx.Err(); err != nil {
		return FailurePlan{}, err
	}
	if err := attempt.Validate(); err != nil {
		return FailurePlan{}, fmt.Errorf("retry attempt: %w", err)
	}
	if p == nil || p.maxAttempts <= 0 || p.baseDelay <= 0 || p.jitter == nil {
		return FailurePlan{}, errors.New("retry: uninitialized policy")
	}
	var providerErr *ProviderError
	if !errors.As(fetchErr, &providerErr) || providerErr == nil {
		if fetchErr == nil {
			return FailurePlan{}, errors.New("retry: missing provider error")
		}
		return FailurePlan{}, fmt.Errorf("unclassified fetch error: %w", fetchErr)
	}
	switch providerErr.Code {
	case domain.CodeUnavailable, domain.CodeRejected, domain.CodeInvalidResponse:
	default:
		return FailurePlan{}, fmt.Errorf("retry: invalid provider failure code: %w", fetchErr)
	}
	plan := FailurePlan{Decision: repository.FailureDecision{
		Disposition: repository.FailAttempt, Code: providerErr.Code,
	}}
	retry := providerErr.Retryable && attempt.Number < p.maxAttempts
	if retry || providerErr.RateLimited {
		delay := max(p.backoff(attempt.Number), providerErr.RetryAfter)
		if retry {
			plan.Decision.Disposition = repository.RetryAttempt
			plan.Decision.Delay = delay
		}
		if providerErr.RateLimited {
			plan.BlockFor = delay
		}
	}
	return plan, nil
}

func (p *RetryPolicy) backoff(number int64) time.Duration {
	delay := p.baseDelay
	// Stop at the cap before multiplying: bounded work even for a huge number.
	for n := int64(1); n < number && delay < MaxRetryBackoff; n++ {
		if delay > MaxRetryBackoff/2 {
			delay = MaxRetryBackoff
			break
		}
		delay *= 2
	}
	limit := min(delay/4, MaxRetryBackoff-delay)
	if limit == 0 {
		return delay
	}
	return delay + max(0, min(p.jitter(limit), limit))
}

// FailureHandler persists a shared 429 pause before recording the job outcome.
// A blocker error is propagated; the processor must stop new attempts until
// coordination recovers (T07/T09). It must not fall back to a local limiter.
type FailureHandler struct {
	recorder repository.AttemptFailureRecorder
	blocker  repository.ProviderBlocker
	policy   *RetryPolicy
	provider string
}

func NewFailureHandler(recorder repository.AttemptFailureRecorder, blocker repository.ProviderBlocker, policy *RetryPolicy, provider string) (*FailureHandler, error) {
	if recorder == nil || blocker == nil || policy == nil || policy.maxAttempts <= 0 || strings.TrimSpace(provider) == "" {
		return nil, errors.New("failure handler: invalid dependencies")
	}
	return &FailureHandler{recorder, blocker, policy, provider}, nil
}

func (h *FailureHandler) Handle(ctx context.Context, attempt domain.Attempt, fetchErr error) error {
	plan, err := h.policy.Plan(ctx, attempt, fetchErr)
	if err != nil {
		return err
	}
	if plan.BlockFor > 0 {
		if err = h.blocker.BlockProvider(ctx, h.provider, plan.BlockFor); err != nil {
			return fmt.Errorf("block provider after rate limit: %w", err)
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = h.recorder.RecordFailure(ctx, attempt, plan.Decision); err != nil {
		return fmt.Errorf("record attempt failure: %w", err)
	}
	return nil
}
