//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/config"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository/postgres"
)

func TestOpenIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL must be set for integration tests")
	}
	cfg := config.DatabaseConfig{
		URL: dsn, MaxOpenConns: 1, MaxIdleConns: 1,
		ConnMaxLifetime: time.Minute, ConnMaxIdleTime: 30 * time.Second,
		OperationTimeout: 3 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if db == nil {
		t.Fatal("Open returned nil pool without error")
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close pool: %v", err)
		}
	})

	t.Run("query after Open returns", func(t *testing.T) {
		var value int
		if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&value); err != nil {
			t.Fatalf("query: %v", err)
		}
		if value != 1 {
			t.Fatalf("got %d, want 1", value)
		}
	})
	t.Run("pool limit and cancellation while waiting", func(t *testing.T) {
		if got := db.Stats().MaxOpenConnections; got != cfg.MaxOpenConns {
			t.Fatalf("limit: got %d, want %d", got, cfg.MaxOpenConns)
		}
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("reserve only connection: %v", err)
		}
		defer conn.Close()
		waitCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
		defer stop()
		var value int
		err = db.QueryRowContext(waitCtx, "SELECT 1").Scan(&value)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("waiting for occupied pool: got %v", err)
		}
	})
	t.Run("pool remains usable after timeout", func(t *testing.T) {
		var value int
		if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&value); err != nil {
			t.Fatalf("query after timeout: %v", err)
		}
		if value != 1 {
			t.Fatalf("got %d, want 1", value)
		}
	})
}
