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
	"testing"
	"time"
)

func TestStorageBoundariesIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL required; dedicated migrated test DB only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, config.DatabaseConfig{URL: dsn, MaxOpenConns: 2, OperationTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reset := func() {
		t.Helper()
		if _, err := db.ExecContext(ctx, "TRUNCATE quote_updates"); err != nil {
			t.Fatal(err)
		}
	}
	reset()
	defer reset()
	repo := postgres.New(db, time.Second, 2)
	pair := domain.Pair{Base: domain.EUR, Quote: domain.USD}
	id1 := "00000000-0000-4000-8000-000000000001"
	id2 := "00000000-0000-4000-8000-000000000002"
	insert := func(id, date, completed string) {
		t.Helper()
		_, err := db.ExecContext(ctx, `INSERT INTO quote_updates(id,pair,status,attempts,completed_at,price,source,source_date) VALUES($1,'EUR/USD','succeeded',1,$2,1.2,'frankfurter:ecb',$3)`, id, completed, date)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ name, date1, date2, time1, time2, want string }{
		{"source date", "2026-10-02", "2026-10-01", "2026-10-03T00:00:00Z", "2026-10-04T00:00:00Z", id1},
		{"completion time", "2026-10-02", "2026-10-02", "2026-10-04T00:00:00Z", "2026-10-03T00:00:00Z", id1},
		{"UUID tie breaker", "2026-10-02", "2026-10-02", "2026-10-03T00:00:00Z", "2026-10-03T00:00:00Z", id2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset()
			insert(id2, tc.date2, tc.time2)
			insert(id1, tc.date1, tc.time1)
			u, err := repo.GetLatest(ctx, pair)
			if err != nil || u == nil || u.ID.String() != tc.want {
				t.Fatalf("latest: %v %v; want %s", u, err, tc.want)
			}
		})
	}
	t.Run("new failure preserves old success", func(t *testing.T) {
		reset()
		insert(id1, "2026-10-01", "2026-10-02T00:00:00Z")
		_, err := db.ExecContext(ctx, `INSERT INTO quote_updates(id,pair,status,attempts,completed_at,last_error_code) VALUES($1,'EUR/USD','failed',1,'2026-10-04T00:00:00Z','provider_unavailable')`, id2)
		if err != nil {
			t.Fatal(err)
		}
		u, err := repo.GetLatest(ctx, pair)
		if err != nil || u == nil || u.ID.String() != id1 {
			t.Fatalf("latest: %v %v", u, err)
		}
	})
	t.Run("independent requests and full queue replay", func(t *testing.T) {
		reset()
		a, _ := domain.ParseUpdateID(id1)
		b, _ := domain.ParseUpdateID(id2)
		first, err := repo.CreateOrGet(ctx, a, pair, nil)
		if err != nil {
			t.Fatal(err)
		}
		second, err := repo.CreateOrGet(ctx, b, pair, nil)
		if err != nil {
			t.Fatal(err)
		}
		if first.ID == second.ID || first.ID != a || second.ID != b {
			t.Fatal("independent requests combined")
		}
		// Both slots are occupied by distinct active jobs. A third terminal row
		// is an idempotent replay and must not consume capacity.
		terminal := "00000000-0000-4000-8000-000000000003"
		key := "terminal-replay"
		_, err = db.ExecContext(ctx, `INSERT INTO quote_updates(id,pair,status,attempts,completed_at,last_error_code,idempotency_key) VALUES($1,'EUR/USD','failed',1,now(),'provider_unavailable',$2)`, terminal, key)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := repo.CreateOrGet(ctx, a, pair, &key)
		if err != nil || replay == nil || replay.ID.String() != terminal || replay.Status != domain.StatusFailed {
			t.Fatalf("replay %v %v", replay, err)
		}
		_, err = repo.CreateOrGet(ctx, a, domain.Pair{Base: domain.USD, Quote: domain.EUR}, &key)
		if !errors.Is(err, repository.ErrIdempotencyConflict) {
			t.Fatalf("conflict: %v", err)
		}
		_, err = repo.CreateOrGet(ctx, a, pair, nil)
		if !errors.Is(err, repository.ErrQueueFull) {
			t.Fatalf("capacity: %v", err)
		}
	})
}
