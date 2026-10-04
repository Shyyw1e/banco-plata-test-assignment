//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/config"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository/postgres"
)

func TestClaimIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL required; dedicated migrated test DB only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	open := func() *sql.DB {
		t.Helper()
		db, err := postgres.Open(ctx, config.DatabaseConfig{URL: dsn, MaxOpenConns: 3, OperationTimeout: 2 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	db1, db2 := open(), open()
	r1, r2 := postgres.New(db1, 2*time.Second, 100), postgres.New(db2, 2*time.Second, 100)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db1.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	reset := func() { exec("TRUNCATE quote_updates") }
	reset()
	defer reset()
	id := func(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }
	seed := func(n, attempts int, next string) {
		exec(`INSERT INTO quote_updates(id,pair,attempts,created_at,next_attempt_at) VALUES($1,'EUR/USD',$2,'2026-01-01',clock_timestamp()+$3::interval)`, id(n), attempts, next)
	}
	noJob := func(r *postgres.Repository) {
		t.Helper()
		a, err := r.Claim(ctx, 3, 30*time.Second)
		if a != nil || !errors.Is(err, repository.ErrNoJob) {
			t.Fatalf("expected no job: %v %v", a, err)
		}
	}
	t.Run("empty future exhausted and processing skipped", func(t *testing.T) {
		reset()
		noJob(r1)
		seed(1, 0, "1 hour")
		seed(2, 3, "-1 hour")
		seed(3, 1, "-1 hour")
		exec("UPDATE quote_updates SET status='processing',lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", id(3))
		noJob(r1)
		var status string
		var n int
		if err := db1.QueryRowContext(ctx, "SELECT status,attempts FROM quote_updates WHERE id=$1", id(2)).Scan(&status, &n); err != nil {
			t.Fatal(err)
		}
		if status != "queued" || n != 3 {
			t.Fatal("exhausted row changed; recovery belongs to T06")
		}
	})
	t.Run("two pools claim distinct rows once", func(t *testing.T) {
		reset()
		seed(1, 0, "-1 hour")
		seed(2, 1, "-1 hour")
		start := make(chan struct{})
		results := make(chan *domain.Attempt, 2)
		errs := make(chan error, 2)
		for _, r := range []*postgres.Repository{r1, r2} {
			go func(r *postgres.Repository) {
				<-start
				a, e := r.Claim(ctx, 3, 30*time.Second)
				results <- a
				errs <- e
			}(r)
		}
		close(start)
		first, second := <-results, <-results
		for i := 0; i < 2; i++ {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		if first == nil || second == nil || first.ID == second.ID {
			t.Fatal("duplicate/missing claim")
		}
		for _, a := range []*domain.Attempt{first, second} {
			want := int64(1)
			if a.ID.String() == id(2) {
				want = 2
			}
			if a.Number != want || a.Pair != (domain.Pair{Base: domain.EUR, Quote: domain.USD}) {
				t.Fatalf("attempt %+v", a)
			}
			var status string
			var number int64
			var lease time.Time
			if err := db2.QueryRowContext(ctx, "SELECT status,attempts,lease_until FROM quote_updates WHERE id=$1", a.ID.String()).Scan(&status, &number, &lease); err != nil {
				t.Fatal(err)
			}
			if status != "processing" || number != want || !lease.Equal(a.LeaseUntil) {
				t.Fatal("returned uncommitted or inconsistent attempt")
			}
		}
		noJob(r1)
	})
	t.Run("locked first row skipped and admission lock not used", func(t *testing.T) {
		reset()
		seed(1, 0, "-2 hours")
		seed(2, 0, "-1 hour")
		tx, err := db1.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var locked string
		if err = tx.QueryRowContext(ctx, "SELECT id::text FROM quote_updates WHERE id=$1 FOR UPDATE", id(1)).Scan(&locked); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(1)"); err != nil {
			t.Fatal(err)
		}
		a, err := r2.Claim(ctx, 3, 30*time.Second)
		if err != nil || a == nil || a.ID.String() != id(2) {
			t.Fatalf("did not skip lock: %v %v", a, err)
		}
		if err = tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		a, err = r2.Claim(ctx, 3, 30*time.Second)
		if err != nil || a == nil || a.ID.String() != id(1) {
			t.Fatalf("unlocked job: %v %v", a, err)
		}
	})
	t.Run("ordering and database lease clock", func(t *testing.T) {
		reset()
		for n := 1; n <= 4; n++ {
			seed(n, 0, "-1 hour")
		}
		exec("UPDATE quote_updates SET next_attempt_at='2026-01-01',created_at='2026-01-01'")
		exec("UPDATE quote_updates SET next_attempt_at='2025-12-31' WHERE id=$1", id(4))
		exec("UPDATE quote_updates SET created_at='2025-12-31' WHERE id=$1", id(3))
		for _, n := range []int{4, 3, 1, 2} {
			var before, after time.Time
			if err := db1.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&before); err != nil {
				t.Fatal(err)
			}
			a, err := r2.Claim(ctx, 3, 30*time.Second)
			if err != nil || a == nil || a.ID.String() != id(n) {
				t.Fatalf("order %d: %v %v", n, a, err)
			}
			if err = db1.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&after); err != nil {
				t.Fatal(err)
			}
			if a.LeaseUntil.Before(before.Add(30*time.Second)) || a.LeaseUntil.After(after.Add(30*time.Second)) {
				t.Fatalf("lease outside DB time bounds: %v", a.LeaseUntil)
			}
		}
	})
	t.Run("operation timeout while table locked", func(t *testing.T) {
		reset()
		seed(1, 0, "-1 hour")
		tx, err := db1.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, "LOCK TABLE quote_updates IN ACCESS EXCLUSIVE MODE"); err != nil {
			t.Fatal(err)
		}
		short := postgres.New(db2, 100*time.Millisecond, 100)
		a, err := short.Claim(ctx, 3, 30*time.Second)
		if a != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout: %v %v", a, err)
		}
		if ctx.Err() != nil {
			t.Fatal("overall test budget expired")
		}
		if err = tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		a, err = r2.Claim(ctx, 3, 30*time.Second)
		if err != nil || a == nil || a.Number != 1 {
			t.Fatalf("timed out claim changed job: %v %v", a, err)
		}
	})
}
