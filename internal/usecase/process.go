package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
)

type ProcessOutcome uint8

const (
	ProcessDone ProcessOutcome = iota + 1
	ProcessIdle
	ProcessWait
)

type ProcessResult struct {
	Outcome ProcessOutcome
	Wait    time.Duration
	// Attempt is available after claim, including on persistence errors.
	Attempt *domain.Attempt
}

type ProcessorConfig struct {
	Provider          string
	RequestsPerSecond int
	MaxAttempts       int
	LeaseDuration     time.Duration
	PollInterval      time.Duration
	// Now defaults to time.Now (monotonic). A custom clock must be concurrency safe.
	Now func() time.Time
}

type Processor struct {
	claimer   repository.AttemptClaimer
	completer repository.AttemptCompleter
	provider  QuoteProvider
	limiter   repository.PermitIssuer
	failures  *FailureHandler
	config    ProcessorConfig
	interval  time.Duration
}

func NewProcessor(claimer repository.AttemptClaimer, completer repository.AttemptCompleter, provider QuoteProvider, limiter repository.PermitIssuer, failures *FailureHandler, cfg ProcessorConfig) (*Processor, error) {
	if claimer == nil || completer == nil || provider == nil || limiter == nil || failures == nil || failures.policy == nil {
		return nil, errors.New("processor: missing dependency")
	}
	if cfg.Provider == "" || strings.TrimSpace(cfg.Provider) != cfg.Provider || cfg.Provider != failures.provider || cfg.MaxAttempts <= 0 || int64(cfg.MaxAttempts) != failures.policy.maxAttempts || cfg.LeaseDuration < time.Microsecond || cfg.PollInterval <= 0 || cfg.RequestsPerSecond <= 0 || cfg.RequestsPerSecond > int(time.Second/time.Microsecond) {
		return nil, errors.New("processor: invalid configuration")
	}
	// Round up: the integer division must not grant more than the configured RPS.
	interval := time.Second / time.Duration(cfg.RequestsPerSecond)
	if time.Second%time.Duration(cfg.RequestsPerSecond) != 0 {
		interval++
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Processor{claimer, completer, provider, limiter, failures, cfg, interval}, nil
}

// Process performs at most one Fetch. ctx belongs to the application lifetime,
// never to the HTTP request which originally created the job. No waits occur here.
func (p *Processor) Process(ctx context.Context) (result ProcessResult, err error) {
	if err = ctx.Err(); err != nil {
		return result, err
	}
	started := p.config.Now()
	permit, err := p.limiter.TryPermit(ctx, p.config.Provider, p.interval)
	if err != nil {
		return result, fmt.Errorf("obtain provider permit: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if !permit.Granted {
		if permit.RetryAfter <= 0 || permit.ValidFor != 0 {
			return result, errors.New("processor: invalid denied permit")
		}
		return ProcessResult{Outcome: ProcessWait, Wait: permit.RetryAfter}, nil
	}
	if permit.ValidFor <= 0 || permit.ValidFor > p.interval || permit.RetryAfter != 0 {
		return result, errors.New("processor: invalid granted permit")
	}
	fresh := func() bool { elapsed := p.config.Now().Sub(started); return elapsed >= 0 && elapsed < permit.ValidFor }
	if !fresh() {
		return ProcessResult{Outcome: ProcessWait, Wait: p.config.PollInterval}, nil
	}
	attempt, err := p.claimer.Claim(ctx, p.config.MaxAttempts, p.config.LeaseDuration)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if errors.Is(err, repository.ErrNoJob) {
			return ProcessResult{Outcome: ProcessIdle, Wait: p.config.PollInterval}, nil
		}
		return result, fmt.Errorf("claim attempt: %w", err)
	}
	result.Attempt = attempt
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if attempt == nil {
		return result, errors.New("processor: claim returned nil attempt")
	}
	if err = attempt.Validate(); err != nil {
		return result, fmt.Errorf("claimed attempt: %w", err)
	}
	if attempt.Number > int64(p.config.MaxAttempts) {
		return result, errors.New("processor: claimed attempt exceeds limit")
	}
	if !fresh() {
		result.Outcome = ProcessWait
		result.Wait = p.config.PollInterval
		return result, nil
	}
	quote, fetchErr := p.provider.Fetch(ctx, attempt.Pair)
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if fetchErr == nil {
		if quote == nil {
			fetchErr = &ProviderError{Code: domain.CodeInvalidResponse, Cause: domain.ErrInvalidQuote}
		} else if e := quote.Validate(); e != nil {
			fetchErr = &ProviderError{Code: domain.CodeInvalidResponse, Cause: e}
		} else if quote.Pair != attempt.Pair {
			fetchErr = &ProviderError{Code: domain.CodeInvalidResponse, Cause: domain.ErrInvalidQuote}
		}
	}
	if fetchErr != nil {
		err = p.failures.Handle(ctx, *attempt, fetchErr)
	} else {
		err = p.completer.Succeed(ctx, *attempt, *quote)
	}
	// Losing ownership is an ordinary end to this attempt, not a new Fetch.
	// An accompanying storage failure must still reach the worker for backoff.
	if errors.Is(err, repository.ErrLeaseLost) && !errors.Is(err, repository.ErrUnavailable) {
		err = nil
	}
	if err != nil {
		return result, fmt.Errorf("finish attempt: %w", err)
	}
	result.Outcome = ProcessDone
	return result, nil
}
