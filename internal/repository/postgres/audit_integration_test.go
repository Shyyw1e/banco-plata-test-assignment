//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/config"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/logger"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository/postgres"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/transport/httpapi"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
	"github.com/jackc/pgx/v5/pgconn"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAuditReadOnlyIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := config.DatabaseConfig{URL: dsn, MaxOpenConns: 2, OperationTimeout: time.Second}
	db, err := postgres.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := domain.NewUpdateID()
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO quote_updates(id,pair,status,attempts,completed_at,price,source,source_date) VALUES($1,'EUR/USD','succeeded',1,now(),1.2,'frankfurter:ecb','2026-10-01')`, id.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(context.Background(), "DELETE FROM quote_updates WHERE id=$1", id.String())
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("default_transaction_read_only", "on")
	u.RawQuery = q.Encode()
	cfg.URL = u.String()
	ro, err := postgres.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	repo := postgres.New(ro, time.Second, 1000)
	got, err := repo.GetByID(ctx, id)
	if err != nil || got == nil || got.Result == nil {
		t.Fatalf("read: %v %v", got, err)
	}
	next, err := domain.NewUpdateID()
	if err != nil {
		t.Fatal(err)
	}
	got, err = repo.CreateOrGet(ctx, next, domain.Pair{Base: domain.EUR, Quote: domain.USD}, nil)
	var pg *pgconn.PgError
	if got != nil || !errors.Is(err, repository.ErrUnavailable) || !errors.As(err, &pg) || pg.Code != "25006" {
		t.Fatalf("write: %v %v", got, err)
	}
	h := httpapi.New(usecase.NewService(repo, repo), logger.New(slog.LevelInfo, "test", io.Discard)).Routes()
	req := httptest.NewRequest("POST", "/v1/quote-updates", strings.NewReader(`{"pair":"EUR/USD"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" || !strings.Contains(w.Body.String(), "database_unavailable") || strings.Contains(w.Body.String(), "25006") {
		t.Fatalf("HTTP: %d %s", w.Code, w.Body.String())
	}
}
