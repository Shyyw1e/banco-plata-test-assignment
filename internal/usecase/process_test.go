package usecase_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
)

type fetchFunc func(context.Context, domain.Pair) (*domain.Quote, error)

func (f fetchFunc) Fetch(ctx context.Context, pair domain.Pair) (*domain.Quote, error) {
	return f(ctx, pair)
}

func TestProcessorScenarios(t *testing.T) {
	transient := &usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true, Cause: context.DeadlineExceeded}
	permanent := &usecase.ProviderError{Code: domain.CodeRejected}
	limited := &usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true, RateLimited: true, RetryAfter: time.Minute}
	unknown := errors.New("unknown fetch error")
	for _, tc := range []struct {
		name                                                      string
		permitErr, claimErr, fetchErr, saveErr, blockErr, wantErr error
		denied                                                    bool
		staleAt, cancelAt, invalidQuote                           string
		number                                                    int64
		outcome                                                   usecase.ProcessOutcome
		calls                                                     []string
	}{
		{name: "success", outcome: usecase.ProcessDone, calls: []string{"permit", "claim", "fetch", "succeed"}},
		{name: "retry timeout", fetchErr: transient, outcome: usecase.ProcessDone, calls: []string{"permit", "claim", "fetch", "failure"}},
		{name: "permanent", fetchErr: permanent, outcome: usecase.ProcessDone, calls: []string{"permit", "claim", "fetch", "failure"}},
		{name: "429", fetchErr: limited, outcome: usecase.ProcessDone, calls: []string{"permit", "claim", "fetch", "block", "failure"}},
		{name: "last 429", number: 3, fetchErr: limited, outcome: usecase.ProcessDone, calls: []string{"permit", "claim", "fetch", "block", "failure"}},
		{name: "last timeout", number: 3, fetchErr: transient, outcome: usecase.ProcessDone, calls: []string{"permit", "claim", "fetch", "failure"}},
		{name: "denied", denied: true, outcome: usecase.ProcessWait, calls: []string{"permit"}},
		{name: "permit error", permitErr: repository.ErrUnavailable, wantErr: repository.ErrUnavailable, calls: []string{"permit"}},
		{name: "empty", claimErr: repository.ErrNoJob, outcome: usecase.ProcessIdle, calls: []string{"permit", "claim"}},
		{name: "claim commit error", claimErr: repository.ErrUnavailable, wantErr: repository.ErrUnavailable, calls: []string{"permit", "claim"}},
		{name: "stale during permit", staleAt: "permit", outcome: usecase.ProcessWait, calls: []string{"permit"}},
		{name: "stale during claim", staleAt: "claim", outcome: usecase.ProcessWait, calls: []string{"permit", "claim"}},
		{name: "success lease lost", saveErr: repository.ErrLeaseLost, outcome: usecase.ProcessDone, calls: []string{"permit", "claim", "fetch", "succeed"}},
		{name: "failure lease lost", fetchErr: transient, saveErr: repository.ErrLeaseLost, outcome: usecase.ProcessDone, calls: []string{"permit", "claim", "fetch", "failure"}},
		{name: "success commit error", saveErr: repository.ErrUnavailable, wantErr: repository.ErrUnavailable, calls: []string{"permit", "claim", "fetch", "succeed"}},
		{name: "failure commit error", fetchErr: transient, saveErr: repository.ErrUnavailable, wantErr: repository.ErrUnavailable, calls: []string{"permit", "claim", "fetch", "failure"}},
		{name: "429 block error", fetchErr: limited, blockErr: repository.ErrUnavailable, wantErr: repository.ErrUnavailable, calls: []string{"permit", "claim", "fetch", "block"}},
		{name: "unknown fetch error", fetchErr: unknown, wantErr: unknown, calls: []string{"permit", "claim", "fetch"}},
		{name: "nil quote", invalidQuote: "nil", outcome: usecase.ProcessDone, calls: []string{"permit", "claim", "fetch", "failure"}},
		{name: "bad quote", invalidQuote: "invalid", outcome: usecase.ProcessDone, calls: []string{"permit", "claim", "fetch", "failure"}},
		{name: "wrong pair", invalidQuote: "pair", outcome: usecase.ProcessDone, calls: []string{"permit", "claim", "fetch", "failure"}},
		{name: "cancel before permit", cancelAt: "before", wantErr: context.Canceled},
		{name: "cancel during permit", cancelAt: "permit", wantErr: context.Canceled, calls: []string{"permit"}},
		{name: "cancel during claim", cancelAt: "claim", wantErr: context.Canceled, calls: []string{"permit", "claim"}},
		{name: "cancel during success", cancelAt: "fetch", wantErr: context.Canceled, calls: []string{"permit", "claim", "fetch"}},
		{name: "cancel during provider error", cancelAt: "fetch", fetchErr: permanent, wantErr: context.Canceled, calls: []string{"permit", "claim", "fetch"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			number := tc.number
			if number == 0 {
				number = 1
			}
			a := retryAttempt(t, number)
			price, err := domain.ParsePrice("1.25")
			if err != nil {
				t.Fatal(err)
			}
			quote := &domain.Quote{Pair: a.Pair, Price: price, Source: "frankfurter:ecb", SourceDate: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
			switch tc.invalidQuote {
			case "nil":
				quote = nil
			case "invalid":
				quote = &domain.Quote{}
			case "pair":
				quote.Pair = domain.Pair{Base: domain.USD, Quote: domain.EUR}
			}
			clock := time.Now()
			var calls []string
			called := func(c context.Context, stage string) {
				t.Helper()
				if c != ctx {
					t.Fatal("application context replaced")
				}
				calls = append(calls, stage)
				if tc.staleAt == stage {
					clock = clock.Add(500 * time.Millisecond)
				}
				if tc.cancelAt == stage {
					cancel()
				}
			}
			store := &attemptStoreFake{
				permit: func(c context.Context, key string, interval time.Duration) (repository.PermitResult, error) {
					called(c, "permit")
					if key != "frankfurter" || interval != 500*time.Millisecond {
						t.Fatal(key, interval)
					}
					if tc.permitErr != nil {
						return repository.PermitResult{}, tc.permitErr
					}
					if tc.denied {
						return repository.PermitResult{RetryAfter: time.Second}, nil
					}
					return repository.PermitResult{Granted: true, ValidFor: interval}, nil
				},
				claim: func(c context.Context, max int, lease time.Duration) (*domain.Attempt, error) {
					called(c, "claim")
					if max != 3 || lease != 30*time.Second {
						t.Fatal(max, lease)
					}
					if tc.claimErr != nil {
						return nil, tc.claimErr
					}
					return &a, nil
				},
				succeed: func(c context.Context, got domain.Attempt, q domain.Quote) error {
					called(c, "succeed")
					if got != a || q.Price.String() != "1.2500000000" {
						t.Fatal("wrong success arguments")
					}
					return tc.saveErr
				},
				block: func(c context.Context, key string, pause time.Duration) error {
					called(c, "block")
					if key != "frankfurter" || pause < time.Minute {
						t.Fatal(key, pause)
					}
					return tc.blockErr
				},
				failure: func(c context.Context, got domain.Attempt, d repository.FailureDecision) error {
					called(c, "failure")
					if c.Err() != nil {
						t.Fatal("using canceled fetch context")
					}
					if got != a {
						t.Fatal("wrong attempt")
					}
					wantCode := domain.CodeUnavailable
					wantDisposition := repository.RetryAttempt
					if tc.fetchErr == permanent {
						wantCode = domain.CodeRejected
						wantDisposition = repository.FailAttempt
					}
					if tc.invalidQuote != "" {
						wantCode = domain.CodeInvalidResponse
						wantDisposition = repository.FailAttempt
					}
					if number == 3 {
						wantDisposition = repository.FailAttempt
					}
					if d.Code != wantCode || d.Disposition != wantDisposition {
						t.Fatal(d)
					}
					return tc.saveErr
				},
			}
			handler, err := usecase.NewFailureHandler(store, store, retryPolicy(t, 3, time.Second, func(time.Duration) time.Duration { return 0 }), "frankfurter")
			if err != nil {
				t.Fatal(err)
			}
			provider := fetchFunc(func(c context.Context, pair domain.Pair) (*domain.Quote, error) {
				called(c, "fetch")
				if pair != a.Pair {
					t.Fatal(pair)
				}
				return quote, tc.fetchErr
			})
			p, err := usecase.NewProcessor(store, store, provider, store, handler, newTestBreaker(t), usecase.ProcessorConfig{Provider: "frankfurter", RequestsPerSecond: 2, MaxAttempts: 3, LeaseDuration: 30 * time.Second, PollInterval: 100 * time.Millisecond, Now: func() time.Time { return clock }})
			if err != nil {
				t.Fatal(err)
			}
			if tc.cancelAt == "before" {
				cancel()
			}
			result, err := p.Process(ctx)
			if !errors.Is(err, tc.wantErr) || result.Outcome != tc.outcome || !reflect.DeepEqual(calls, tc.calls) {
				t.Fatalf("result=%+v err=%v calls=%v", result, err, calls)
			}
			if tc.outcome == usecase.ProcessWait || tc.outcome == usecase.ProcessIdle {
				if result.Wait <= 0 {
					t.Fatal("missing delay")
				}
			} else if result.Wait != 0 {
				t.Fatal("unexpected wait")
			}
			if tc.staleAt == "claim" && result.Attempt != &a {
				t.Fatal("lost claimed attempt")
			}
		})
	}
}

func TestProcessorConfiguration(t *testing.T) {
	store := &attemptStoreFake{}
	handler, err := usecase.NewFailureHandler(store, store, retryPolicy(t, 3, time.Second, nil), "frankfurter")
	if err != nil {
		t.Fatal(err)
	}
	provider := fetchFunc(func(context.Context, domain.Pair) (*domain.Quote, error) { panic("unexpected fetch") })
	valid := usecase.ProcessorConfig{Provider: "frankfurter", RequestsPerSecond: 2, MaxAttempts: 3, LeaseDuration: time.Minute, PollInterval: time.Second}
	for _, mutate := range []func(*usecase.ProcessorConfig){
		func(c *usecase.ProcessorConfig) { c.RequestsPerSecond = 0 }, func(c *usecase.ProcessorConfig) { c.RequestsPerSecond = 1_000_001 }, func(c *usecase.ProcessorConfig) { c.MaxAttempts = 4 }, func(c *usecase.ProcessorConfig) { c.Provider = "other" }, func(c *usecase.ProcessorConfig) { c.LeaseDuration = 0 }, func(c *usecase.ProcessorConfig) { c.PollInterval = 0 },
	} {
		cfg := valid
		mutate(&cfg)
		if _, err := usecase.NewProcessor(store, store, provider, store, handler, newTestBreaker(t), cfg); err == nil {
			t.Fatal("invalid config accepted", cfg)
		}
	}
	if _, err := usecase.NewProcessor(nil, store, provider, store, handler, newTestBreaker(t), valid); err == nil {
		t.Fatal("nil dependency accepted")
	}
	// Three RPS rounds up to 333333334 ns rather than exceeding the budget.
	cfg := valid
	cfg.RequestsPerSecond = 3
	store.permit = func(_ context.Context, _ string, d time.Duration) (repository.PermitResult, error) {
		if d != 333333334*time.Nanosecond {
			t.Fatal(d)
		}
		return repository.PermitResult{RetryAfter: time.Second}, nil
	}
	p, err := usecase.NewProcessor(store, store, provider, store, handler, newTestBreaker(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Process(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProcessorInvalidAdapterResults(t *testing.T) {
	for _, tc := range []struct {
		name    string
		permit  repository.PermitResult
		attempt *domain.Attempt
	}{
		{name: "zero denied permit"},
		{name: "negative wait", permit: repository.PermitResult{RetryAfter: -1}},
		{name: "zero TTL", permit: repository.PermitResult{Granted: true}},
		{name: "excessive TTL", permit: repository.PermitResult{Granted: true, ValidFor: time.Second}},
		{name: "nil attempt", permit: repository.PermitResult{Granted: true, ValidFor: 500 * time.Millisecond}},
		{name: "invalid attempt", permit: repository.PermitResult{Granted: true, ValidFor: 500 * time.Millisecond}, attempt: &domain.Attempt{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &attemptStoreFake{
				permit: func(context.Context, string, time.Duration) (repository.PermitResult, error) { return tc.permit, nil },
				claim:  func(context.Context, int, time.Duration) (*domain.Attempt, error) { return tc.attempt, nil },
			}
			handler, err := usecase.NewFailureHandler(store, store, retryPolicy(t, 3, time.Second, nil), "frankfurter")
			if err != nil {
				t.Fatal(err)
			}
			provider := fetchFunc(func(context.Context, domain.Pair) (*domain.Quote, error) {
				t.Fatal("unexpected Fetch")
				return nil, nil
			})
			p, err := usecase.NewProcessor(store, store, provider, store, handler, newTestBreaker(t), usecase.ProcessorConfig{Provider: "frankfurter", RequestsPerSecond: 2, MaxAttempts: 3, LeaseDuration: time.Minute, PollInterval: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			result, err := p.Process(context.Background())
			if err == nil || result.Outcome != 0 {
				t.Fatal(result, err)
			}
		})
	}
}

func newTestBreaker(t *testing.T) *usecase.CircuitBreaker {
	t.Helper()
	b, err := usecase.NewCircuitBreaker(5, 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
