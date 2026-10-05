//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
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

func TestFailureIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("dedicated migrated TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, config.DatabaseConfig{URL: dsn, MaxOpenConns: 5, OperationTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := postgres.New(db, 2*time.Second, 10)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	reset := func() { exec("TRUNCATE quote_updates") }
	reset()
	defer reset()
	_, quote := completionInput(t)
	claim := func() *domain.Attempt {
		t.Helper()
		a, err := r.Claim(ctx, 3, 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	fresh := func() *domain.Attempt {
		t.Helper()
		reset()
		id, err := domain.NewUpdateID()
		if err != nil {
			t.Fatal(err)
		}
		key := "failure-key"
		if _, err = r.CreateOrGet(ctx, id, quote.Pair, &key); err != nil {
			t.Fatal(err)
		}
		return claim()
	}
	now := func() time.Time {
		t.Helper()
		var v time.Time
		if err := db.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	retry := repository.FailureDecision{Disposition: repository.RetryAttempt, Code: domain.CodeUnavailable, Delay: time.Hour + time.Nanosecond}
	fail := repository.FailureDecision{Disposition: repository.FailAttempt, Code: domain.CodeRejected}
	t.Run("retry stores database schedule and preserves identity", func(t *testing.T) {
		a := fresh()
		beforeUpdate, err := r.GetByID(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		before := now()
		if err = r.RecordFailure(ctx, *a, retry); err != nil {
			t.Fatal(err)
		}
		after := now()
		var next time.Time
		var attempts int64
		var key, code string
		var lease, completed, price, source, date sql.NullString
		err = db.QueryRowContext(ctx, `SELECT next_attempt_at,attempts,idempotency_key,last_error_code,lease_until::text,completed_at::text,price::text,source,source_date::text FROM quote_updates WHERE id=$1`, a.ID.String()).Scan(&next, &attempts, &key, &code, &lease, &completed, &price, &source, &date)
		if err != nil {
			t.Fatal(err)
		}
		if next.Before(before.Add(retry.Delay)) || next.After(after.Add(time.Hour+time.Microsecond)) {
			t.Fatal("invalid DB schedule", next, before, after)
		}
		if attempts != 1 || key != "failure-key" || code != string(domain.CodeUnavailable) || lease.Valid || completed.Valid || price.Valid || source.Valid || date.Valid {
			t.Fatal("invalid retry fields")
		}
		u, err := r.GetByID(ctx, a.ID)
		if err != nil || u.Status != domain.StatusQueued || u.ErrorCode != nil || u.Result != nil || !u.CreatedAt.Equal(beforeUpdate.CreatedAt) {
			t.Fatalf("%+v %v", u, err)
		}
		if _, err = r.Claim(ctx, 3, 30*time.Second); !errors.Is(err, repository.ErrNoJob) {
			t.Fatal("early claim", err)
		}
		if err = r.RecordFailure(ctx, *a, fail); !errors.Is(err, repository.ErrLeaseLost) {
			t.Fatal("repeated write", err)
		}
		exec("UPDATE quote_updates SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1", a.ID.String())
		current := claim()
		if current.Number != 2 {
			t.Fatal(current.Number)
		}
		if err = r.RecordFailure(ctx, *a, fail); !errors.Is(err, repository.ErrLeaseLost) {
			t.Fatal("stale write", err)
		}
		if err = r.RecordFailure(ctx, *current, fail); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("third transient fails with provider code", func(t *testing.T) {
		a := fresh()
		policy, err := usecase.NewRetryPolicy(3, time.Second, func(time.Duration) time.Duration { return 0 })
		if err != nil {
			t.Fatal(err)
		}
		for number := int64(1); number <= 3; number++ {
			if a.Number != number {
				t.Fatal("wrong claim number")
			}
			plan, err := policy.Plan(ctx, *a, &usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true, Cause: context.DeadlineExceeded})
			if err != nil {
				t.Fatal(err)
			}
			before := now()
			if err = r.RecordFailure(ctx, *a, plan.Decision); err != nil {
				t.Fatal(err)
			}
			after := now()
			if number < 3 {
				exec("UPDATE quote_updates SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1", a.ID.String())
				a = claim()
				continue
			}
			u, err := r.GetByID(ctx, a.ID)
			if err != nil || u.Status != domain.StatusFailed || u.ErrorCode == nil || *u.ErrorCode != domain.CodeUnavailable || u.Result != nil {
				t.Fatalf("%+v %v", u, err)
			}
			var completed time.Time
			var lease, price, source, date sql.NullString
			var attempts int
			err = db.QueryRowContext(ctx, "SELECT completed_at,lease_until::text,price::text,source,source_date::text,attempts FROM quote_updates WHERE id=$1", a.ID.String()).Scan(&completed, &lease, &price, &source, &date, &attempts)
			if err != nil || completed.Before(before) || completed.After(after) || lease.Valid || price.Valid || source.Valid || date.Valid || attempts != 3 {
				t.Fatal("invalid terminal fields", err)
			}
			if _, err = r.Claim(ctx, 3, 30*time.Second); !errors.Is(err, repository.ErrNoJob) {
				t.Fatal("fourth claim", err)
			}
			if err = r.RecordFailure(ctx, *a, retry); !errors.Is(err, repository.ErrLeaseLost) {
				t.Fatal("terminal mutated", err)
			}
		}
	})
	t.Run("expired lease", func(t *testing.T) {
		a := fresh()
		exec("UPDATE quote_updates SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", a.ID.String())
		if err := r.RecordFailure(ctx, *a, retry); !errors.Is(err, repository.ErrLeaseLost) {
			t.Fatal(err)
		}
		u, err := r.GetByID(ctx, a.ID)
		if err != nil || u.Status != domain.StatusProcessing {
			t.Fatal("expired write changed state", err)
		}
	})
	t.Run("success cannot become failed", func(t *testing.T) {
		a := fresh()
		if err := r.Succeed(ctx, *a, quote); err != nil {
			t.Fatal(err)
		}
		if err := r.RecordFailure(ctx, *a, fail); !errors.Is(err, repository.ErrLeaseLost) {
			t.Fatal(err)
		}
		u, err := r.GetByID(ctx, a.ID)
		if err != nil || u.Status != domain.StatusSucceeded || u.Result == nil {
			t.Fatal("success changed", err)
		}
	})
	// A SELECT FOR UPDATE can delay a writer without changing the tuple. The
	// lease must be checked after that wait, not only before acquiring the lock.
	for _, operation := range []string{"failure", "success"} {
		t.Run("lease expires while waiting for lock/"+operation, func(t *testing.T) {
			a := fresh()
			if err := db.QueryRowContext(ctx, "UPDATE quote_updates SET lease_until=clock_timestamp()+interval '500 milliseconds' WHERE id=$1 RETURNING lease_until", a.ID.String()).Scan(&a.LeaseUntil); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var blockerPID int
			if err = tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&blockerPID); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.ExecContext(ctx, "SELECT id FROM quote_updates WHERE id=$1 FOR UPDATE", a.ID.String()); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if operation == "failure" {
					done <- r.RecordFailure(ctx, *a, retry)
				} else {
					done <- r.Succeed(ctx, *a, quote)
				}
			}()
			// Wait for evidence that the writer actually reached the row lock.
			deadline := time.Now().Add(time.Second)
			for {
				var blocked bool
				if err = db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))", blockerPID).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("writer never waited on row lock")
				}
				time.Sleep(time.Millisecond)
			}
			if _, err = tx.ExecContext(ctx, "SELECT pg_sleep(GREATEST(0, EXTRACT(EPOCH FROM $1::timestamptz-clock_timestamp())) + 0.02)", a.LeaseUntil); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err = <-done; !errors.Is(err, repository.ErrLeaseLost) {
				t.Fatalf("expired writer accepted: %v", err)
			}
		})
	}

	t.Run("concurrent success and retry one winner", func(t *testing.T) {
		a := fresh()
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() { <-start; results <- r.RecordFailure(ctx, *a, retry) }()
		go func() { <-start; results <- r.Succeed(ctx, *a, quote) }()
		close(start)
		first, second := <-results, <-results
		if !((first == nil && errors.Is(second, repository.ErrLeaseLost)) || (second == nil && errors.Is(first, repository.ErrLeaseLost))) {
			t.Fatal(first, second)
		}
	})
}
