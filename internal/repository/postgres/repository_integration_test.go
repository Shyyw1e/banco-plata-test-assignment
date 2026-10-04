//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/config"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository/postgres"
	"os"
	"sync"
	"testing"
	"time"
)

func TestRepositoryIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, config.DatabaseConfig{URL: dsn, MaxOpenConns: 8, MaxIdleConns: 2, OperationTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Dedicated test database only; each case starts with an empty queue.
	reset := func() {
		t.Helper()
		if _, e := db.ExecContext(ctx, "TRUNCATE quote_updates"); e != nil {
			t.Fatal(e)
		}
	}
	reset()
	defer reset()
	repo := postgres.New(db, 5*time.Second, 2)
	pair := domain.Pair{Base: domain.EUR, Quote: domain.USD}
	newID := func() domain.UpdateID {
		v, e := domain.NewUpdateID()
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	t.Run("concurrent idempotent admission", func(t *testing.T) {
		key := "same-key"
		var wg sync.WaitGroup
		ids := make(chan domain.UpdateID, 12)
		errs := make(chan error, 12)
		for i := 0; i < 12; i++ {
			id := newID()
			wg.Add(1)
			go func() {
				defer wg.Done()
				u, e := repo.CreateOrGet(ctx, id, pair, &key)
				if e != nil {
					errs <- e
					return
				}
				ids <- u.ID
			}()
		}
		wg.Wait()
		close(ids)
		close(errs)
		for e := range errs {
			t.Fatal(e)
		}
		var first domain.UpdateID
		for id := range ids {
			if first == (domain.UpdateID{}) {
				first = id
			}
			if id != first {
				t.Fatal("duplicate admission")
			}
		}
		u, e := repo.GetByID(ctx, first)
		if e != nil || u.Status != domain.StatusQueued {
			t.Fatalf("read: %v %v", u, e)
		}
		if _, e = repo.CreateOrGet(ctx, newID(), domain.Pair{Base: domain.USD, Quote: domain.EUR}, &key); !errors.Is(e, repository.ErrIdempotencyConflict) {
			t.Fatalf("conflict: %v", e)
		}
		if _, e = repo.GetLatest(ctx, pair); !errors.Is(e, repository.ErrNotFound) {
			t.Fatalf("latest without success: %v", e)
		}
		if _, e = repo.GetByID(ctx, newID()); !errors.Is(e, repository.ErrNotFound) {
			t.Fatalf("missing: %v", e)
		}
	})
	t.Run("concurrent capacity", func(t *testing.T) {
		reset()
		var wg sync.WaitGroup
		results := make(chan error, 12)
		for i := 0; i < 12; i++ {
			id := newID()
			wg.Add(1)
			go func() { defer wg.Done(); _, e := repo.CreateOrGet(ctx, id, pair, nil); results <- e }()
		}
		wg.Wait()
		close(results)
		accepted := 0
		for e := range results {
			if e == nil {
				accepted++
			} else if !errors.Is(e, repository.ErrQueueFull) {
				t.Fatal(e)
			}
		}
		if accepted != 2 {
			t.Fatalf("accepted %d, want 2", accepted)
		}
	})
	t.Run("latest and all states", func(t *testing.T) {
		reset()
		key := "result-key"
		id := newID()
		if _, e := repo.CreateOrGet(ctx, id, pair, &key); e != nil {
			t.Fatal(e)
		}
		exec := func(q string, args ...any) {
			t.Helper()
			if _, e := db.ExecContext(ctx, q, args...); e != nil {
				t.Fatal(e)
			}
		}
		exec("UPDATE quote_updates SET status='processing', attempts=1, lease_until=now()+interval '30 seconds', last_error_code='provider_unavailable' WHERE id=$1", id.String())
		u, e := repo.GetByID(ctx, id)
		if e != nil || u.Status != domain.StatusProcessing || u.ErrorCode != nil {
			t.Fatalf("processing: %v %v", u, e)
		}
		exec("UPDATE quote_updates SET status='failed',lease_until=NULL,completed_at=now(),last_error_code='attempts_exhausted' WHERE id=$1", id.String())
		u, e = repo.GetByID(ctx, id)
		if e != nil || u.ErrorCode == nil || *u.ErrorCode != domain.CodeAttemptsExhausted {
			t.Fatalf("failed: %v %v", u, e)
		}
		exec("UPDATE quote_updates SET status='succeeded',last_error_code=NULL,price=1.1234567890,source='frankfurter',source_date='2026-10-02' WHERE id=$1", id.String())
		older := newID()
		exec("INSERT INTO quote_updates(id,pair,status,attempts,completed_at,price,source,source_date) VALUES($1,'EUR/USD','succeeded',1,now()+interval '1 hour',2,'frankfurter','2026-10-01')", older.String())
		u, e = repo.GetLatest(ctx, pair)
		if e != nil || u.ID != id || u.Result.Quote.Price.String() != "1.1234567890" {
			t.Fatalf("latest: %v %v", u, e)
		}
		// Replays bypass the capacity check, including terminal results.
		full := postgres.New(db, 5*time.Second, 0)
		u, e = full.CreateOrGet(ctx, newID(), pair, &key)
		if e != nil || u.ID != id || u.Status != domain.StatusSucceeded {
			t.Fatalf("terminal replay: %v %v", u, e)
		}
	})
}
