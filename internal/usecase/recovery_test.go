package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
)

func TestRecoveryPolicy(t *testing.T) {
	for _, failure := range []error{nil, repository.ErrUnavailable} {
		ctx := context.Background()
		calls := 0
		store := &attemptStoreFake{recover: func(c context.Context, max, batch int, delay time.Duration) (repository.RecoveryResult, error) {
			calls++
			if c != ctx || max != 3 || batch != 100 || delay != time.Second {
				t.Fatal("wrong recovery policy")
			}
			if failure != nil {
				return repository.RecoveryResult{}, failure
			}
			return repository.RecoveryResult{Requeued: 2, Failed: 1}, nil
		}}
		r, err := usecase.NewRecovery(store, 3, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		got, err := r.Recover(ctx)
		if !errors.Is(err, failure) || calls != 1 {
			t.Fatal(got, err, calls)
		}
		if failure == nil && got != (repository.RecoveryResult{Requeued: 2, Failed: 1}) {
			t.Fatal(got)
		}
	}
}
func TestRecoveryCanceledAndInvalid(t *testing.T) {
	store := &attemptStoreFake{}
	r, err := usecase.NewRecovery(store, 3, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = r.Recover(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = usecase.NewRecovery(nil, 3, time.Second); err == nil {
		t.Fatal("nil store")
	}
	if _, err = usecase.NewRecovery(store, 0, time.Second); err == nil {
		t.Fatal("invalid attempts")
	}
	if _, err = usecase.NewRecovery(store, 3, 0); err == nil {
		t.Fatal("invalid delay")
	}
}
