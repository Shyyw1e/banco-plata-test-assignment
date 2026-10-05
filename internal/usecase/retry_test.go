package usecase_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
)

func retryAttempt(t *testing.T, number int64) domain.Attempt {
	t.Helper()
	id, err := domain.NewUpdateID()
	if err != nil {
		t.Fatal(err)
	}
	return domain.Attempt{ID: id, Pair: domain.Pair{Base: domain.EUR, Quote: domain.USD}, Number: number, LeaseUntil: time.Now().Add(time.Minute)}
}
func retryPolicy(t *testing.T, attempts int, base time.Duration, jitter usecase.JitterFunc) *usecase.RetryPolicy {
	t.Helper()
	p, err := usecase.NewRetryPolicy(attempts, base, jitter)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestRetryPolicy(t *testing.T) {
	p := retryPolicy(t, 3, time.Second, func(limit time.Duration) time.Duration { return limit })
	for _, tc := range []struct {
		name         string
		number       int64
		failure      usecase.ProviderError
		disposition  repository.FailureDisposition
		delay, block time.Duration
	}{
		{"first transient", 1, usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true}, repository.RetryAttempt, 1250 * time.Millisecond, 0},
		{"second transient", 2, usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true}, repository.RetryAttempt, 2500 * time.Millisecond, 0},
		{"last transient", 3, usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true}, repository.FailAttempt, 0, 0},
		{"lowered attempt limit", 4, usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true}, repository.FailAttempt, 0, 0},
		{"rejected", 1, usecase.ProviderError{Code: domain.CodeRejected}, repository.FailAttempt, 0, 0},
		{"invalid response", 1, usecase.ProviderError{Code: domain.CodeInvalidResponse}, repository.FailAttempt, 0, 0},
		{"HTTP client timeout", 1, usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true, Cause: context.DeadlineExceeded}, repository.RetryAttempt, 1250 * time.Millisecond, 0},
		{"503 Retry-After is not 429", 1, usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true, RetryAfter: 2 * time.Minute}, repository.RetryAttempt, 2 * time.Minute, 0},
		{"429 no header", 1, usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true, RateLimited: true}, repository.RetryAttempt, 1250 * time.Millisecond, 1250 * time.Millisecond},
		{"429 shorter header", 2, usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true, RateLimited: true, RetryAfter: time.Second}, repository.RetryAttempt, 2500 * time.Millisecond, 2500 * time.Millisecond},
		{"last 429 no header", 3, usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true, RateLimited: true}, repository.FailAttempt, 0, 5 * time.Second},
		{"last 429 long header", 3, usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true, RateLimited: true, RetryAfter: 2 * time.Minute}, repository.FailAttempt, 0, 2 * time.Minute},
		{"negative header", 1, usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true, RetryAfter: -time.Second}, repository.RetryAttempt, 1250 * time.Millisecond, 0},
		{"maximum header", 1, usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true, RetryAfter: time.Duration(math.MaxInt64)}, repository.RetryAttempt, time.Duration(math.MaxInt64), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := p.Plan(context.Background(), retryAttempt(t, tc.number), fmt.Errorf("fetch: %w", &tc.failure))
			if err != nil {
				t.Fatal(err)
			}
			want := usecase.FailurePlan{Decision: repository.FailureDecision{Disposition: tc.disposition, Code: tc.failure.Code, Delay: tc.delay}, BlockFor: tc.block}
			if plan != want {
				t.Fatalf("got %+v; want %+v", plan, want)
			}
			if err = plan.Decision.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestRetryBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		base   time.Duration
		number int64
		jitter time.Duration
		want   time.Duration
	}{
		{"max base", time.Duration(math.MaxInt64), 1, 0, time.Minute},
		{"huge attempt", time.Nanosecond, math.MaxInt64, 0, time.Minute},
		{"cap addition", 50 * time.Second, 1, time.Hour, time.Minute},
		{"negative jitter", time.Second, 1, -time.Hour, time.Second},
		{"oversized jitter", time.Second, 1, time.Hour, 1250 * time.Millisecond},
		{"tiny base", time.Nanosecond, 1, 0, time.Nanosecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := retryPolicy(t, 3, tc.base, func(time.Duration) time.Duration { return tc.jitter })
			plan, err := p.Plan(context.Background(), retryAttempt(t, tc.number), &usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true, RateLimited: true})
			if err != nil || plan.BlockFor != tc.want {
				t.Fatalf("%+v %v", plan, err)
			}
		})
	}
}
func TestRetryCancellationAndUnknownErrors(t *testing.T) {
	p := retryPolicy(t, 3, time.Second, nil)
	a := retryAttempt(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Plan(ctx, a, &usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true, Cause: context.DeadlineExceeded}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := p.Plan(ctx, a, &usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	unknown := errors.New("unknown failure")
	for _, cause := range []error{unknown, context.Canceled, context.DeadlineExceeded} {
		if _, err := p.Plan(context.Background(), a, cause); !errors.Is(err, cause) {
			t.Fatal(err)
		}
	}
	var nilProvider *usecase.ProviderError
	for _, cause := range []error{nil, nilProvider, &usecase.ProviderError{Code: domain.CodeAttemptsExhausted}, &usecase.ProviderError{Code: "bogus"}} {
		if _, err := p.Plan(context.Background(), a, cause); err == nil {
			t.Fatal("invalid failure accepted")
		}
	}
	if _, err := p.Plan(context.Background(), domain.Attempt{}, unknown); !errors.Is(err, domain.ErrInvalidAttempt) {
		t.Fatal(err)
	}
	for _, base := range []time.Duration{0, -1} {
		if _, err := usecase.NewRetryPolicy(3, base, nil); err == nil {
			t.Fatal("invalid base accepted")
		}
	}
	if _, err := usecase.NewRetryPolicy(0, time.Second, nil); err == nil {
		t.Fatal("invalid attempts accepted")
	}
}
func TestRetryDefaultJitterConcurrent(t *testing.T) {
	p := retryPolicy(t, 3, time.Second, nil)
	a := retryAttempt(t, 1)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				plan, err := p.Plan(context.Background(), a, &usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true})
				if err != nil || plan.Decision.Delay < time.Second || plan.Decision.Delay > 1250*time.Millisecond {
					t.Errorf("%+v %v", plan, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
func TestFailureHandler(t *testing.T) {
	for _, tc := range []struct {
		name                string
		number              int64
		limited             bool
		blockErr, recordErr error
		cancelAfterBlock    bool
		wantCalls           []string
	}{
		{name: "retry", number: 1, wantCalls: []string{"record"}},
		{name: "429", number: 1, limited: true, wantCalls: []string{"block", "record"}},
		{name: "last 429", number: 3, limited: true, wantCalls: []string{"block", "record"}},
		{name: "block fails", number: 3, limited: true, blockErr: repository.ErrUnavailable, wantCalls: []string{"block"}},
		{name: "lease lost", number: 1, recordErr: repository.ErrLeaseLost, wantCalls: []string{"record"}},
		{name: "commit unknown", number: 1, recordErr: repository.ErrUnavailable, wantCalls: []string{"record"}},
		{name: "cancel after block", number: 1, limited: true, cancelAfterBlock: true, wantCalls: []string{"block"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a := retryAttempt(t, tc.number)
			var calls []string
			store := &attemptStoreFake{
				block: func(c context.Context, provider string, pause time.Duration) error {
					calls = append(calls, "block")
					if c != ctx || provider != "frankfurter" || pause <= 0 {
						t.Fatal("invalid block")
					}
					if tc.cancelAfterBlock {
						cancel()
					}
					return tc.blockErr
				},
				failure: func(c context.Context, got domain.Attempt, d repository.FailureDecision) error {
					calls = append(calls, "record")
					if c != ctx || got != a || d.Code != domain.CodeUnavailable {
						t.Fatal("invalid record")
					}
					want := repository.RetryAttempt
					if tc.number >= 3 {
						want = repository.FailAttempt
					}
					if d.Disposition != want {
						t.Fatal(d)
					}
					return tc.recordErr
				},
			}
			h, err := usecase.NewFailureHandler(store, store, retryPolicy(t, 3, time.Second, nil), "frankfurter")
			if err != nil {
				t.Fatal(err)
			}
			err = h.Handle(ctx, a, &usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true, RateLimited: tc.limited})
			wantErr := tc.recordErr
			if tc.blockErr != nil {
				wantErr = tc.blockErr
			}
			if tc.cancelAfterBlock {
				wantErr = context.Canceled
			}
			if !errors.Is(err, wantErr) || !reflect.DeepEqual(calls, tc.wantCalls) {
				t.Fatalf("%v calls %v", err, calls)
			}
		})
	}
}
func TestFailureHandlerCanceledDoesNotWrite(t *testing.T) {
	store := &attemptStoreFake{} // Any persistence call panics.
	h, err := usecase.NewFailureHandler(store, store, retryPolicy(t, 3, time.Second, nil), "frankfurter")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = h.Handle(ctx, retryAttempt(t, 1), &usecase.ProviderError{Code: domain.CodeRejected}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
