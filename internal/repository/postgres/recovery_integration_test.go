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
)

func TestRecoveryIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("dedicated migrated TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	open := func() *sql.DB {
		t.Helper()
		db, err := postgres.Open(ctx, config.DatabaseConfig{URL: dsn, MaxOpenConns: 5, OperationTimeout: 2 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	db, otherDB := open(), open()
	r, other := postgres.New(db, 2*time.Second, 100), postgres.New(otherDB, 2*time.Second, 100)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	reset := func() { exec("TRUNCATE quote_updates") }
	reset()
	defer reset()
	insert := func(status string, number int, expired bool, code any) domain.UpdateID {
		t.Helper()
		id, err := domain.NewUpdateID()
		if err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO quote_updates (id,pair,status,attempts,lease_until,completed_at,price,source,source_date,last_error_code,idempotency_key)
  VALUES ($1::text::uuid,'EUR/USD',$2,$3,
   CASE WHEN $2='processing' THEN clock_timestamp() + CASE WHEN $4 THEN interval '-1 hour' ELSE interval '1 hour' END ELSE NULL END,
   CASE WHEN $2 IN ('succeeded','failed') THEN clock_timestamp() ELSE NULL END,
   CASE WHEN $2='succeeded' THEN 1.25 ELSE NULL END,
   CASE WHEN $2='succeeded' THEN 'frankfurter:ecb' ELSE NULL END,
   CASE WHEN $2='succeeded' THEN DATE '2026-10-01' ELSE NULL END,$5,$1)`, id.String(), status, number, expired, code)
		return id
	}
	snapshot := func(id domain.UpdateID) string {
		t.Helper()
		var s string
		if err := db.QueryRowContext(ctx, "SELECT row_to_json(q)::text FROM quote_updates q WHERE id=$1", id.String()).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	now := func() time.Time {
		t.Helper()
		var v time.Time
		if err := db.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	recover := func(batch int) repository.RecoveryResult {
		t.Helper()
		v, err := r.RecoverExpired(ctx, 3, batch, time.Hour+time.Nanosecond)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	t.Run("states timestamps and preserved fields", func(t *testing.T) {
		reset()
		retried := insert("processing", 1, true, "provider_unavailable")
		noCode := insert("processing", 2, true, nil)
		last := insert("processing", 3, true, "provider_rejected")
		lowered := insert("queued", 4, false, nil)
		// Even a future queued row must terminate when its attempt budget is gone.
		exec("UPDATE quote_updates SET next_attempt_at=clock_timestamp()+interval '1 day' WHERE id=$1", lowered.String())
		untouched := []domain.UpdateID{insert("processing", 3, false, nil), insert("queued", 0, false, nil), insert("succeeded", 1, false, nil), insert("failed", 3, false, "provider_unavailable")}
		previous := make(map[domain.UpdateID]string)
		for _, id := range untouched {
			previous[id] = snapshot(id)
		}
		before := now()
		got := recover(100)
		after := now()
		if got != (repository.RecoveryResult{Requeued: 2, Failed: 2}) {
			t.Fatal(got)
		}
		for _, tc := range []struct {
			id       domain.UpdateID
			attempts int
			status   string
			code     any
		}{{retried, 1, "queued", "provider_unavailable"}, {noCode, 2, "queued", nil}, {last, 3, "failed", "attempts_exhausted"}, {lowered, 4, "failed", "attempts_exhausted"}} {
			var state, key string
			var attempts int
			var next, created time.Time
			var completed sql.NullTime
			var lease, price, source, date, code sql.NullString
			err := db.QueryRowContext(ctx, `SELECT status,attempts,next_attempt_at,created_at,completed_at,lease_until::text,price::text,source,source_date::text,last_error_code,idempotency_key FROM quote_updates WHERE id=$1`, tc.id.String()).Scan(&state, &attempts, &next, &created, &completed, &lease, &price, &source, &date, &code, &key)
			if err != nil {
				t.Fatal(err)
			}
			if state != tc.status || attempts != tc.attempts || key != tc.id.String() || created.After(before) || lease.Valid || price.Valid || source.Valid || date.Valid {
				t.Fatal("invalid fields", state, attempts)
			}
			if (tc.code == nil && code.Valid) || (tc.code != nil && (!code.Valid || code.String != tc.code)) {
				t.Fatal("wrong error code", code)
			}
			if state == "queued" {
				if completed.Valid || next.Before(before.Add(time.Hour+time.Nanosecond)) || next.After(after.Add(time.Hour+time.Microsecond)) {
					t.Fatal("invalid retry time", next)
				}
			} else if !completed.Valid || completed.Time.Before(before) || completed.Time.After(after) {
				t.Fatal("invalid completion time")
			}
			if _, err = r.GetByID(ctx, tc.id); err != nil {
				t.Fatal("public model invalid", err)
			}
		}
		for _, id := range untouched {
			if snapshot(id) != previous[id] {
				t.Fatal("ineligible row changed", id)
			}
		}
		if got := recover(100); got != (repository.RecoveryResult{}) {
			t.Fatal("repeated recovery", got)
		}
	})
	t.Run("one shared batch limit and locked row skipped", func(t *testing.T) {
		reset()
		locked := insert("processing", 1, true, nil)
		insert("processing", 3, true, nil)
		insert("queued", 3, false, nil)
		insert("processing", 2, true, nil)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, "SELECT id FROM quote_updates WHERE id=$1 FOR UPDATE", locked.String()); err != nil {
			t.Fatal(err)
		}
		got := recover(2)
		if got.Requeued+got.Failed != 2 {
			t.Fatal("wrong batch size", got)
		}
		got = recover(2)
		if got.Requeued+got.Failed != 1 {
			t.Fatal("locked row not skipped", got)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if got = recover(2); got != (repository.RecoveryResult{Requeued: 1}) {
			t.Fatal(got)
		}
	})
	t.Run("independent pools no double recovery", func(t *testing.T) {
		reset()
		for range 20 {
			insert("processing", 1, true, nil)
		}
		start := make(chan struct{})
		type outcome struct {
			r   repository.RecoveryResult
			err error
		}
		done := make(chan outcome, 2)
		for _, store := range []*postgres.Repository{r, other} {
			go func() { <-start; v, e := store.RecoverExpired(ctx, 3, 7, time.Hour); done <- outcome{v, e} }()
		}
		close(start)
		total := int64(0)
		for range 2 {
			v := <-done
			if v.err != nil || v.r.Requeued != 7 || v.r.Failed != 0 {
				t.Fatal(v)
			}
			total += v.r.Requeued
		}
		var queued int64
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM quote_updates WHERE status='queued'").Scan(&queued); err != nil {
			t.Fatal(err)
		}
		if queued != total {
			t.Fatal("double recovery", queued, total)
		}
		if got := recover(100); got != (repository.RecoveryResult{Requeued: 6}) {
			t.Fatal(got)
		}
	})
	t.Run("old worker cannot finish reclaimed attempt", func(t *testing.T) {
		reset()
		id := insert("processing", 1, true, nil)
		old, quote := completionInput(t)
		old.ID = id
		if got := recover(100); got != (repository.RecoveryResult{Requeued: 1}) {
			t.Fatal(got)
		}
		if _, err := r.Claim(ctx, 3, time.Minute); !errors.Is(err, repository.ErrNoJob) {
			t.Fatal("early claim", err)
		}
		exec("UPDATE quote_updates SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1", id.String())
		current, err := r.Claim(ctx, 3, time.Minute)
		if err != nil || current.Number != 2 {
			t.Fatal(current, err)
		}
		if err = r.Succeed(ctx, old, quote); !errors.Is(err, repository.ErrLeaseLost) {
			t.Fatal("old success", err)
		}
		if err = r.RecordFailure(ctx, old, repository.FailureDecision{Disposition: repository.FailAttempt, Code: domain.CodeUnavailable}); !errors.Is(err, repository.ErrLeaseLost) {
			t.Fatal("old failure", err)
		}
		if err = r.Succeed(ctx, *current, quote); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("storage error has no result", func(t *testing.T) {
		reset()
		insert("processing", 1, true, nil)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, "LOCK TABLE quote_updates IN ACCESS EXCLUSIVE MODE"); err != nil {
			t.Fatal(err)
		}
		short := postgres.New(otherDB, 30*time.Millisecond, 100)
		got, err := short.RecoverExpired(ctx, 3, 100, time.Second)
		if !errors.Is(err, context.DeadlineExceeded) || got != (repository.RecoveryResult{}) {
			t.Fatal(got, err)
		}
		if err = tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if got = recover(100); got != (repository.RecoveryResult{Requeued: 1}) {
			t.Fatal("row changed on failure", got)
		}
	})
}
