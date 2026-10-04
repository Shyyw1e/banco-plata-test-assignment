//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/config"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/logger"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository/postgres"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/transport/httpapi"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
)

func TestCompletionIntegration(t *testing.T) {
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
	newAttempt := func() *domain.Attempt {
		t.Helper()
		reset()
		id, err := domain.NewUpdateID()
		if err != nil {
			t.Fatal(err)
		}
		key := "preserved-key"
		if _, err = r.CreateOrGet(ctx, id, quote.Pair, &key); err != nil {
			t.Fatal(err)
		}
		return claim()
	}
	now := func() time.Time {
		t.Helper()
		var value time.Time
		if err := db.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	t.Run("success timestamps fields and HTTP", func(t *testing.T) {
		a := newAttempt()
		beforeUpdate, err := r.GetByID(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		exec("UPDATE quote_updates SET last_error_code='provider_unavailable' WHERE id=$1", a.ID.String())
		before := now()
		if err = r.Succeed(ctx, *a, quote); err != nil {
			t.Fatal(err)
		}
		after := now()
		u, err := r.GetByID(ctx, a.ID)
		if err != nil || u == nil || u.Result == nil {
			t.Fatalf("read %v %v", u, err)
		}
		if u.Status != domain.StatusSucceeded || u.Result.Quote.Price.String() != quote.Price.String() || u.Result.Quote.Source != quote.Source || !u.CreatedAt.Equal(beforeUpdate.CreatedAt) || !u.Result.Quote.SourceDate.Equal(quote.SourceDate) {
			t.Fatalf("result %+v", u)
		}
		completed := u.Result.UpdatedAt
		if completed.Before(before) || completed.After(after) {
			t.Fatal("completion not using database time")
		}
		var lease, code sql.NullString
		var key string
		var number int64
		if err = db.QueryRowContext(ctx, "SELECT lease_until::text,last_error_code,idempotency_key,attempts FROM quote_updates WHERE id=$1", a.ID.String()).Scan(&lease, &code, &key, &number); err != nil {
			t.Fatal(err)
		}
		if lease.Valid || code.Valid || key != "preserved-key" || number != a.Number {
			t.Fatal("fields not preserved/cleared")
		}
		h := httpapi.New(usecase.NewService(r, r), logger.New(slog.LevelInfo, "test", io.Discard)).Routes()
		for _, path := range []string{"/v1/quote-updates/" + a.ID.String(), "/v1/quotes/latest?pair=EUR/USD"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			var body map[string]any
			if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || body["price"] != quote.Price.String() || body["source"] != quote.Source || body["updated_at"] != completed.UTC().Format(time.RFC3339Nano) {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
		}
		changed := quote
		changed.Price, _ = domain.ParsePrice("2")
		if err = r.Succeed(ctx, *a, changed); !errors.Is(err, repository.ErrLeaseLost) {
			t.Fatal(err)
		}
		u, err = r.GetByID(ctx, a.ID)
		if err != nil || u.Result.Quote.Price.String() != quote.Price.String() || !u.Result.UpdatedAt.Equal(completed) {
			t.Fatal("repeat overwrote result")
		}
	})
	t.Run("expired lease rejected before recovery", func(t *testing.T) {
		a := newAttempt()
		exec("UPDATE quote_updates SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", a.ID.String())
		if err = r.Succeed(ctx, *a, quote); !errors.Is(err, repository.ErrLeaseLost) {
			t.Fatal(err)
		}
		u, err := r.GetByID(ctx, a.ID)
		if err != nil || u.Status != domain.StatusProcessing || u.Result != nil {
			t.Fatal("expired write changed state")
		}
	})
	t.Run("old attempt rejected after new claim", func(t *testing.T) {
		old := newAttempt()
		// T06 recovery is not implemented yet: prepare its resulting queued state.
		exec("UPDATE quote_updates SET status='queued',lease_until=NULL,next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1", old.ID.String())
		current := claim()
		if current.Number != old.Number+1 {
			t.Fatal("attempt not advanced")
		}
		if err = r.Succeed(ctx, *old, quote); !errors.Is(err, repository.ErrLeaseLost) {
			t.Fatal(err)
		}
		if err = r.Succeed(ctx, *current, quote); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("failed terminal cannot change", func(t *testing.T) {
		a := newAttempt()
		exec("UPDATE quote_updates SET status='failed',lease_until=NULL,completed_at=clock_timestamp(),last_error_code='attempts_exhausted' WHERE id=$1", a.ID.String())
		if err = r.Succeed(ctx, *a, quote); !errors.Is(err, repository.ErrLeaseLost) {
			t.Fatal(err)
		}
		u, e := r.GetByID(ctx, a.ID)
		if e != nil || u.Status != domain.StatusFailed || u.Result != nil || u.ErrorCode == nil || *u.ErrorCode != domain.CodeAttemptsExhausted {
			t.Fatal("failed changed")
		}
	})
	t.Run("concurrent completion has one winner", func(t *testing.T) {
		a := newAttempt()
		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; results <- r.Succeed(ctx, *a, quote) }()
		}
		close(start)
		wg.Wait()
		close(results)
		successes, lost := 0, 0
		for e := range results {
			if e == nil {
				successes++
			} else if errors.Is(e, repository.ErrLeaseLost) {
				lost++
			} else {
				t.Fatal(e)
			}
		}
		if successes != 1 || lost != 1 {
			t.Fatalf("success=%d lost=%d", successes, lost)
		}
	})
	t.Run("decimal boundaries and source preserved", func(t *testing.T) {
		for _, price := range []string{"0.0000000001", "9999999999.9999999999"} {
			a := newAttempt()
			q := quote
			q.Price, err = domain.ParsePrice(price)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Succeed(ctx, *a, q); err != nil {
				t.Fatal(err)
			}
			u, e := r.GetByID(ctx, a.ID)
			if e != nil || u.Result.Quote.Price.String() != price {
				t.Fatalf("decimal %v %v", u, e)
			}
		}
		a := newAttempt()
		q := quote
		q.Source = "another-source"
		if err = r.Succeed(ctx, *a, q); err != nil {
			t.Fatal(err)
		}
		u, e := r.GetByID(ctx, a.ID)
		if e != nil || u.Result.Quote.Source != q.Source {
			t.Fatal("source renamed")
		}
		// Domain/storage preserve provenance; HTTP enforces its narrower public enum.
		h := httpapi.New(usecase.NewService(r, r), logger.New(slog.LevelInfo, "test", io.Discard)).Routes()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/quote-updates/"+a.ID.String(), nil))
		if w.Code != 500 {
			t.Fatal("unsupported source exposed")
		}
	})
}
