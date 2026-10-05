package usecase_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
)

func TestBreakerTransitionsAndOldResponses(t *testing.T) {
	now := time.Now()
	b, err := usecase.NewCircuitBreaker(5, 30*time.Second, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	acquire := func() *usecase.BreakerTicket {
		t.Helper()
		v, _ := b.Acquire()
		if v == nil {
			t.Fatal("unexpected rejection")
		}
		return v
	}
	transient := &usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true, Cause: context.DeadlineExceeded}
	permanent := &usecase.ProviderError{Code: domain.CodeRejected}
	for _, reset := range []error{nil, permanent} {
		for range 4 {
			acquire().Observe(transient)
		}
		acquire().Observe(reset)
	}
	old := acquire()
	for range 5 {
		acquire().Observe(transient)
	}
	if ticket, wait := b.Acquire(); ticket != nil || wait != 30*time.Second {
		t.Fatal(ticket, wait)
	}
	old.Observe(nil)
	if ticket, _ := b.Acquire(); ticket != nil {
		t.Fatal("old success closed open breaker")
	}
	now = now.Add(30 * time.Second)
	probe := acquire()
	if ticket, _ := b.Acquire(); ticket != nil {
		t.Fatal("multiple probes")
	}
	probe.Observe(permanent) // Any negative probe reopens, including permanent 4xx.
	if ticket, wait := b.Acquire(); ticket != nil || wait != 30*time.Second {
		t.Fatal("negative probe did not reopen", wait)
	}
	now = now.Add(30 * time.Second)
	abandoned := acquire()
	abandoned.Release()
	replacement := acquire()
	abandoned.Observe(nil)
	if ticket, _ := b.Acquire(); ticket != nil {
		t.Fatal("released probe changed next generation")
	}
	replacement.Observe(nil)
	first, second := acquire(), acquire()
	first.Release()
	second.Release()
}

func TestBreakerConcurrentSingleProbe(t *testing.T) {
	now := time.Now()
	b, err := usecase.NewCircuitBreaker(1, time.Second, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ticket, _ := b.Acquire()
	ticket.Observe(&usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true})
	now = now.Add(time.Second)
	var wg sync.WaitGroup
	got := make(chan *usecase.BreakerTicket, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticket, _ := b.Acquire()
			if ticket != nil {
				got <- ticket
			}
		}()
	}
	wg.Wait()
	close(got)
	var winner *usecase.BreakerTicket
	count := 0
	for ticket := range got {
		winner = ticket
		count++
	}
	if count != 1 {
		t.Fatal("probe count", count)
	}
	winner.Observe(nil)
}

func TestProcessorBreakerBlocksBeforePermitAndReleasesProbe(t *testing.T) {
	for _, scenario := range []string{"idle", "denied", "claim error", "permit error", "expired permit", "cancel", "unknown fetch"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now()
			breaker, err := usecase.NewCircuitBreaker(1, time.Second, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			ticket, _ := breaker.Acquire()
			ticket.Observe(&usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			a := retryAttempt(t, 1)
			store := &attemptStoreFake{
				permit: func(context.Context, string, time.Duration) (repository.PermitResult, error) {
					calls++
					if scenario == "cancel" {
						cancel()
					}
					if scenario == "permit error" {
						return repository.PermitResult{}, repository.ErrUnavailable
					}
					if scenario == "denied" {
						return repository.PermitResult{RetryAfter: time.Second}, nil
					}
					if scenario == "expired permit" {
						now = now.Add(time.Second)
					}
					return repository.PermitResult{Granted: true, ValidFor: 500 * time.Millisecond}, nil
				},
				claim: func(context.Context, int, time.Duration) (*domain.Attempt, error) {
					if scenario == "claim error" {
						return nil, repository.ErrUnavailable
					}
					if scenario == "unknown fetch" {
						return &a, nil
					}
					return nil, repository.ErrNoJob
				},
			}
			h, err := usecase.NewFailureHandler(store, store, retryPolicy(t, 3, time.Second, nil), "frankfurter")
			if err != nil {
				t.Fatal(err)
			}
			provider := fetchFunc(func(context.Context, domain.Pair) (*domain.Quote, error) { return nil, errors.New("unclassified") })
			p, err := usecase.NewProcessor(store, store, provider, store, h, breaker, usecase.ProcessorConfig{Provider: "frankfurter", RequestsPerSecond: 2, MaxAttempts: 3, LeaseDuration: time.Minute, PollInterval: time.Millisecond, Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			result, err := p.Process(ctx)
			if err != nil || result.Outcome != usecase.ProcessWait || calls != 0 {
				t.Fatal("open breaker reached limiter", result, err, calls)
			}
			now = now.Add(time.Second)
			_, _ = p.Process(ctx)
			if calls != 1 {
				t.Fatal("probe not run", calls)
			}
			next, _ := breaker.Acquire()
			if next == nil {
				t.Fatal("probe stuck after abandoned iteration")
			}
			next.Release()
		})
	}
}

func TestProcessorObservesFetchBeforeStorage(t *testing.T) {
	b, err := usecase.NewCircuitBreaker(1, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := retryAttempt(t, 1)
	calls := 0
	store := &attemptStoreFake{
		permit: func(context.Context, string, time.Duration) (repository.PermitResult, error) {
			calls++
			return repository.PermitResult{Granted: true, ValidFor: 500 * time.Millisecond}, nil
		},
		claim: func(context.Context, int, time.Duration) (*domain.Attempt, error) { return &a, nil },
		failure: func(context.Context, domain.Attempt, repository.FailureDecision) error {
			return repository.ErrUnavailable
		},
	}
	h, err := usecase.NewFailureHandler(store, store, retryPolicy(t, 3, time.Second, nil), "frankfurter")
	if err != nil {
		t.Fatal(err)
	}
	provider := fetchFunc(func(context.Context, domain.Pair) (*domain.Quote, error) {
		return nil, &usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true}
	})
	p, err := usecase.NewProcessor(store, store, provider, store, h, b, usecase.ProcessorConfig{Provider: "frankfurter", RequestsPerSecond: 2, MaxAttempts: 3, LeaseDuration: time.Minute, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Process(context.Background()); !errors.Is(err, repository.ErrUnavailable) {
		t.Fatal(err)
	}
	result, err := p.Process(context.Background())
	if err != nil || result.Outcome != usecase.ProcessWait || calls != 1 {
		t.Fatal("Fetch failure lost due to storage failure")
	}
}

func TestBreakerValidation(t *testing.T) {
	if _, err := usecase.NewCircuitBreaker(0, time.Second, nil); err == nil {
		t.Fatal("zero threshold")
	}
	if _, err := usecase.NewCircuitBreaker(5, 0, nil); err == nil {
		t.Fatal("zero pause")
	}
}
