package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/logger"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
)

type processorFunc func(context.Context) (usecase.ProcessResult, error)

func (f processorFunc) Process(c context.Context) (usecase.ProcessResult, error) { return f(c) }

type recoveryFunc func(context.Context) (repository.RecoveryResult, error)

func (f recoveryFunc) Recover(c context.Context) (repository.RecoveryResult, error) { return f(c) }
func testPool(t *testing.T, p Processor, r Recoverer, c PoolConfig) *Pool {
	t.Helper()
	pool, err := New(p, r, logger.New(slog.LevelDebug, "test", io.Discard), c)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
func receive(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for goroutine")
	}
}
func emptyRecovery(context.Context) (repository.RecoveryResult, error) {
	return repository.RecoveryResult{}, nil
}

func TestPoolBoundedConcurrencyAndDrain(t *testing.T) {
	work, cancelWork := context.WithCancel(context.Background())
	defer cancelWork()
	stop, cancelStop := context.WithCancel(context.Background())
	defer cancelStop()
	entered := make(chan struct{}, 10)
	release := make(chan struct{})
	var calls, active atomic.Int32
	p := testPool(t, processorFunc(func(ctx context.Context) (usecase.ProcessResult, error) {
		calls.Add(1)
		active.Add(1)
		defer active.Add(-1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return usecase.ProcessResult{}, ctx.Err()
		}
		return usecase.ProcessResult{Outcome: usecase.ProcessDone}, nil
	}), recoveryFunc(emptyRecovery), PoolConfig{Count: 5, PollInterval: time.Hour, RecoveryInterval: time.Hour})
	done := make(chan struct{})
	go func() { p.Run(work, stop); close(done) }()
	for range 5 {
		receive(t, entered)
	}
	if calls.Load() != 5 || active.Load() != 5 {
		t.Fatal("wrong concurrency", calls.Load(), active.Load())
	}
	cancelStop()
	select {
	case <-done:
		t.Fatal("drain abandoned active calls")
	default:
	}
	if work.Err() != nil {
		t.Fatal("stop canceled active work")
	}
	close(release)
	receive(t, done)
	if calls.Load() != 5 || active.Load() != 0 {
		t.Fatal("new iteration during drain")
	}
}

func TestPoolCancelInterruptsActiveProcessingAndRecovery(t *testing.T) {
	work, cancelWork := context.WithCancel(context.Background())
	defer cancelWork()
	stop, cancelStop := context.WithCancel(context.Background())
	defer cancelStop()
	entered := make(chan struct{}, 3)
	p := testPool(t, processorFunc(func(ctx context.Context) (usecase.ProcessResult, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return usecase.ProcessResult{}, ctx.Err()
	}), recoveryFunc(func(ctx context.Context) (repository.RecoveryResult, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return repository.RecoveryResult{}, ctx.Err()
	}), PoolConfig{Count: 2, PollInterval: time.Hour, RecoveryInterval: time.Hour})
	done := make(chan struct{})
	go func() { p.Run(work, stop); close(done) }()
	for range 3 {
		receive(t, entered)
	}
	cancelStop()
	cancelWork()
	receive(t, done)
}

func TestPoolWaitsAndWakes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		result  usecase.ProcessResult
		err     error
		minimum time.Duration
	}{
		{"idle", usecase.ProcessResult{Outcome: usecase.ProcessIdle}, nil, 25 * time.Millisecond},
		{"permit wait", usecase.ProcessResult{Outcome: usecase.ProcessWait, Wait: 40 * time.Millisecond}, nil, 40 * time.Millisecond},
		{"storage failure", usecase.ProcessResult{}, errors.New("database unavailable"), 25 * time.Millisecond},
		{"invalid wait", usecase.ProcessResult{Outcome: usecase.ProcessWait}, nil, 25 * time.Millisecond},
		{"invalid outcome", usecase.ProcessResult{}, nil, 25 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work, cancel := context.WithCancel(context.Background())
			defer cancel()
			times := make(chan time.Time, 3)
			var count atomic.Int32
			p := testPool(t, processorFunc(func(context.Context) (usecase.ProcessResult, error) {
				times <- time.Now()
				if count.Add(1) == 2 {
					cancel()
				}
				return tc.result, tc.err
			}), recoveryFunc(emptyRecovery), PoolConfig{Count: 1, PollInterval: 25 * time.Millisecond, RecoveryInterval: time.Hour})
			done := make(chan struct{})
			go func() { p.Run(work, context.Background()); close(done) }()
			receive(t, done)
			if count.Load() != 2 {
				t.Fatal(count.Load())
			}
			first, second := <-times, <-times
			if second.Sub(first) < tc.minimum {
				t.Fatal("busy loop or shortened wait", second.Sub(first))
			}
		})
	}
}

func TestDoneDoesNotWaitForRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	p := testPool(t, processorFunc(func(context.Context) (usecase.ProcessResult, error) {
		if calls.Add(1) == 2 {
			cancel()
		}
		return usecase.ProcessResult{Outcome: usecase.ProcessDone}, nil
	}), recoveryFunc(emptyRecovery), PoolConfig{Count: 1, PollInterval: time.Hour, RecoveryInterval: time.Hour})
	done := make(chan struct{})
	go func() { p.Run(ctx, context.Background()); close(done) }()
	receive(t, done)
	if calls.Load() != 2 {
		t.Fatal("completed retry did not release worker")
	}
}

func TestStopInterruptsLongWaits(t *testing.T) {
	stop, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 2)
	p := testPool(t, processorFunc(func(context.Context) (usecase.ProcessResult, error) {
		entered <- struct{}{}
		return usecase.ProcessResult{Outcome: usecase.ProcessWait, Wait: time.Hour}, nil
	}), recoveryFunc(func(context.Context) (repository.RecoveryResult, error) {
		entered <- struct{}{}
		return repository.RecoveryResult{}, nil
	}), PoolConfig{Count: 1, PollInterval: time.Hour, RecoveryInterval: time.Hour})
	done := make(chan struct{})
	go func() { p.Run(context.Background(), stop); close(done) }()
	receive(t, entered)
	receive(t, entered)
	cancel()
	receive(t, done)
}

func TestRecoveryRepeatsAfterError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	times := make(chan time.Time, 3)
	p := testPool(t, processorFunc(func(context.Context) (usecase.ProcessResult, error) {
		return usecase.ProcessResult{Outcome: usecase.ProcessIdle}, nil
	}), recoveryFunc(func(context.Context) (repository.RecoveryResult, error) {
		times <- time.Now()
		n := calls.Add(1)
		if n == 3 {
			cancel()
		}
		if n == 1 {
			return repository.RecoveryResult{}, errors.New("storage failure")
		}
		return repository.RecoveryResult{Requeued: 1}, nil
	}), PoolConfig{Count: 1, PollInterval: time.Hour, RecoveryInterval: 20 * time.Millisecond})
	done := make(chan struct{})
	go func() { p.Run(ctx, context.Background()); close(done) }()
	receive(t, done)
	if calls.Load() != 3 {
		t.Fatal(calls.Load())
	}
	first, second, third := <-times, <-times, <-times
	if second.Sub(first) < 20*time.Millisecond || third.Sub(second) < 20*time.Millisecond {
		t.Fatal("recovery busy loop")
	}
}
func TestBackoffBounded(t *testing.T) {
	d := time.Duration(0)
	for _, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second} {
		d = nextBackoff(d, time.Second)
		if d != want {
			t.Fatal(d, want)
		}
	}
	if nextBackoff(0, time.Hour) != 30*time.Second {
		t.Fatal("unbounded initial delay")
	}
}
func TestPoolValidation(t *testing.T) {
	p := processorFunc(func(context.Context) (usecase.ProcessResult, error) { panic("unexpected") })
	r := recoveryFunc(emptyRecovery)
	l := logger.New(slog.LevelInfo, "test", io.Discard)
	for _, cfg := range []PoolConfig{{}, {Count: -1, PollInterval: time.Second, RecoveryInterval: time.Second}, {Count: 1, RecoveryInterval: time.Second}, {Count: 1, PollInterval: time.Second}} {
		if _, err := New(p, r, l, cfg); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	if _, err := New(nil, r, l, PoolConfig{Count: 1, PollInterval: time.Second, RecoveryInterval: time.Second}); err == nil {
		t.Fatal("nil dependency")
	}
}
