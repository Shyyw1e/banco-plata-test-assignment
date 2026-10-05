//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/config"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository/postgres"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
)

func TestThrottleIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("dedicated migrated TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := config.DatabaseConfig{URL: dsn, MaxOpenConns: 5, OperationTimeout: 2 * time.Second}
	db, err := postgres.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db2, err := postgres.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	r, other := postgres.New(db, 2*time.Second, 100), postgres.New(db2, 2*time.Second, 100)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	reset := func() {
		exec("UPDATE provider_throttle SET next_allowed_at=clock_timestamp()-interval '1 day',blocked_until=clock_timestamp()-interval '1 day' WHERE provider='frankfurter'")
	}
	reset()
	defer reset()
	now := func() time.Time {
		t.Helper()
		var v time.Time
		if err := db.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	t.Run("two RPS no accumulated burst", func(t *testing.T) {
		reset()
		before := now()
		v, err := r.TryPermit(ctx, "frankfurter", 500*time.Millisecond)
		after := now()
		if err != nil || !v.Granted || v.ValidFor != 500*time.Millisecond || v.RetryAfter != 0 {
			t.Fatal(v, err)
		}
		var next time.Time
		if err = db.QueryRowContext(ctx, "SELECT next_allowed_at FROM provider_throttle WHERE provider='frankfurter'").Scan(&next); err != nil {
			t.Fatal(err)
		}
		if next.Before(before.Add(500*time.Millisecond)) || next.After(after.Add(500*time.Millisecond)) {
			t.Fatal("schedule not based on database now", next)
		}
		v, err = other.TryPermit(ctx, "frankfurter", 500*time.Millisecond)
		if err != nil || v.Granted || v.ValidFor != 0 || v.RetryAfter <= 0 || v.RetryAfter > 500*time.Millisecond {
			t.Fatal("burst after idle", v, err)
		}
		exec("UPDATE provider_throttle SET next_allowed_at=clock_timestamp()-interval '1 second' WHERE provider='frankfurter'")
		if v, err = other.TryPermit(ctx, "frankfurter", 500*time.Millisecond); err != nil || !v.Granted {
			t.Fatal("new slot unavailable", v, err)
		}
	})
	t.Run("independent pools compete for one slot", func(t *testing.T) {
		reset()
		type outcome struct {
			v   repository.PermitResult
			err error
		}
		done := make(chan outcome, 20)
		start := make(chan struct{})
		for i := range 20 {
			store := r
			if i%2 == 1 {
				store = other
			}
			go func() { <-start; v, e := store.TryPermit(ctx, "frankfurter", time.Minute); done <- outcome{v, e} }()
		}
		close(start)
		granted := 0
		for range 20 {
			v := <-done
			if v.err != nil {
				t.Fatal(v.err)
			}
			if v.v.Granted {
				granted++
			} else if v.v.RetryAfter <= 0 || v.v.ValidFor != 0 {
				t.Fatal(v)
			}
		}
		if granted != 1 {
			t.Fatal("multiple grants", granted)
		}
	})
	t.Run("pause shared monotonic and expired pause releases", func(t *testing.T) {
		reset()
		before := now()
		if err := r.BlockProvider(ctx, "frankfurter", time.Hour+time.Nanosecond); err != nil {
			t.Fatal(err)
		}
		after := now()
		var initial, shorter time.Time
		if err = db.QueryRowContext(ctx, "SELECT blocked_until FROM provider_throttle WHERE provider='frankfurter'").Scan(&initial); err != nil {
			t.Fatal(err)
		}
		if initial.Before(before.Add(time.Hour+time.Nanosecond)) || initial.After(after.Add(time.Hour+time.Microsecond)) {
			t.Fatal("incorrect pause timestamp")
		}
		if err = other.BlockProvider(ctx, "frankfurter", time.Second); err != nil {
			t.Fatal(err)
		}
		if err = db.QueryRowContext(ctx, "SELECT blocked_until FROM provider_throttle WHERE provider='frankfurter'").Scan(&shorter); err != nil {
			t.Fatal(err)
		}
		if !initial.Equal(shorter) {
			t.Fatal("shortened pause")
		}
		v, err := other.TryPermit(ctx, "frankfurter", 500*time.Millisecond)
		if err != nil || v.Granted || v.RetryAfter < 59*time.Minute {
			t.Fatal("pause not shared", v, err)
		}
		// The later next_allowed_at also wins over blocked_until.
		exec("UPDATE provider_throttle SET next_allowed_at=clock_timestamp()+interval '2 hours' WHERE provider='frankfurter'")
		v, err = r.TryPermit(ctx, "frankfurter", 500*time.Millisecond)
		if err != nil || v.Granted || v.RetryAfter < 119*time.Minute {
			t.Fatal("wrong deadline selected", v, err)
		}
		reset()
		v, err = other.TryPermit(ctx, "frankfurter", 500*time.Millisecond)
		if err != nil || !v.Granted {
			t.Fatal(v, err)
		}
	})
	t.Run("missing seed fails closed", func(t *testing.T) {
		v, err := r.TryPermit(ctx, "unconfigured", 500*time.Millisecond)
		if !errors.Is(err, repository.ErrProviderNotConfigured) || v != (repository.PermitResult{}) {
			t.Fatal(v, err)
		}
		if err = r.BlockProvider(ctx, "unconfigured", time.Second); !errors.Is(err, repository.ErrProviderNotConfigured) {
			t.Fatal(err)
		}
	})
	t.Run("database lock failure does not grant", func(t *testing.T) {
		reset()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, "SELECT provider FROM provider_throttle WHERE provider='frankfurter' FOR UPDATE"); err != nil {
			t.Fatal(err)
		}
		short := postgres.New(db2, 30*time.Millisecond, 100)
		v, err := short.TryPermit(ctx, "frankfurter", 500*time.Millisecond)
		if !errors.Is(err, context.DeadlineExceeded) || v != (repository.PermitResult{}) {
			t.Fatal(v, err)
		}
		if err = short.BlockProvider(ctx, "frankfurter", time.Hour); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if err = tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		v, err = r.TryPermit(ctx, "frankfurter", 500*time.Millisecond)
		if err != nil || !v.Granted {
			t.Fatal(v, err)
		}
	})
	t.Run("last 429 without header blocks another pool", func(t *testing.T) {
		reset()
		exec("TRUNCATE quote_updates")
		defer exec("TRUNCATE quote_updates")
		a, _ := completionInput(t)
		if _, err = r.CreateOrGet(ctx, a.ID, a.Pair, nil); err != nil {
			t.Fatal(err)
		}
		current, err := r.Claim(ctx, 3, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		exec("UPDATE quote_updates SET attempts=3 WHERE id=$1", current.ID.String())
		current.Number = 3
		policy, err := usecase.NewRetryPolicy(3, 10*time.Second, func(time.Duration) time.Duration { return 0 })
		if err != nil {
			t.Fatal(err)
		}
		handler, err := usecase.NewFailureHandler(r, r, policy, "frankfurter")
		if err != nil {
			t.Fatal(err)
		}
		if err = handler.Handle(ctx, *current, &usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true, RateLimited: true}); err != nil {
			t.Fatal(err)
		}
		u, err := r.GetByID(ctx, current.ID)
		if err != nil || u.Status != domain.StatusFailed || u.ErrorCode == nil || *u.ErrorCode != domain.CodeUnavailable {
			t.Fatal(u, err)
		}
		v, err := other.TryPermit(ctx, "frankfurter", 500*time.Millisecond)
		if err != nil || v.Granted || v.RetryAfter < 30*time.Second {
			t.Fatal("429 pause missing", v, err)
		}
	})
}
