package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository/postgres"
)

func TestThrottleValidationBeforeDB(t *testing.T) {
	r := postgres.New(nil, time.Second, 1)
	for _, d := range []time.Duration{-1, 0, time.Nanosecond} {
		v, err := r.TryPermit(context.Background(), "frankfurter", d)
		if err == nil || v != (repository.PermitResult{}) {
			t.Fatal(v, err)
		}
	}
	for _, d := range []time.Duration{-1, 0} {
		if err := r.BlockProvider(context.Background(), "frankfurter", d); err == nil {
			t.Fatal("invalid pause")
		}
	}
	for _, key := range []string{"", " frankfurter", "frankfurter "} {
		if _, err := r.TryPermit(context.Background(), key, time.Second); err == nil {
			t.Fatal("invalid provider")
		}
		if err := r.BlockProvider(context.Background(), key, time.Second); err == nil {
			t.Fatal("invalid provider")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if v, err := r.TryPermit(ctx, "frankfurter", time.Second); !errors.Is(err, context.Canceled) || v != (repository.PermitResult{}) {
		t.Fatal(v, err)
	}
	if err := r.BlockProvider(ctx, "frankfurter", time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
