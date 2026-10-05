//go:build integration

package postgres_test

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/config"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository/postgres"
)

func TestStartupSchemaCheck(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("dedicated TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := config.DatabaseConfig{URL: dsn, MaxOpenConns: 1, OperationTimeout: time.Second}
	db, err := postgres.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, "CREATE SCHEMA p05_schema_check"); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(context.Background(), "DROP SCHEMA p05_schema_check CASCADE")
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", "p05_schema_check")
	u.RawQuery = q.Encode()
	cfg.URL = u.String()
	isolated, err := postgres.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	r := postgres.New(isolated, time.Second, 100)
	if err = r.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "apply migrations") {
		t.Fatal("missing schema accepted", err)
	}
	for _, file := range []string{"000001_create_quote_updates.up.sql", "000002_create_provider_throttle.up.sql"} {
		data, err := os.ReadFile("../../../migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = isolated.ExecContext(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err = r.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = isolated.ExecContext(ctx, "DELETE FROM provider_throttle"); err != nil {
		t.Fatal(err)
	}
	if err = r.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "seed missing") {
		t.Fatal("missing seed accepted", err)
	}
}
