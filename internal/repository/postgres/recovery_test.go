package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository/postgres"
)

func TestRecoveryValidationBeforeDB(t *testing.T) {
	r := postgres.New(nil, time.Second, 100)
	for _, tc := range []struct {
		attempts, batch int
		delay           time.Duration
	}{{0, 100, time.Second}, {-1, 100, time.Second}, {3, 0, time.Second}, {3, -1, time.Second}, {3, 100, 0}, {3, 100, -1}} {
		got, err := r.RecoverExpired(context.Background(), tc.attempts, tc.batch, tc.delay)
		if err == nil || got != (repository.RecoveryResult{}) {
			t.Fatal(got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := r.RecoverExpired(ctx, 3, 100, time.Second)
	if !errors.Is(err, context.Canceled) || got != (repository.RecoveryResult{}) {
		t.Fatal(got, err)
	}
}
