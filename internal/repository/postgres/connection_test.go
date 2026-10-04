package postgres_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/config"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository/postgres"
)

func TestOpenCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db, err := postgres.Open(ctx, config.DatabaseConfig{})
	if db != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, %v", db, err)
	}
}

func TestOpenInvalidSSLMode(t *testing.T) {
	db, err := postgres.Open(context.Background(), config.DatabaseConfig{
		URL: "postgres://user:secret-password@localhost/quotes?sslmode=enable",
	})
	if db != nil || err == nil {
		t.Fatalf("got %v, %v", db, err)
	}
	if strings.Contains(err.Error(), "secret-password") {
		t.Fatal("password leaked")
	}
}

func TestOpenFailedHandshakeReturnsError(t *testing.T) {
	// A local endpoint closes the connection without speaking PostgreSQL.
	// No external database or personal environment configuration is required.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	defer func() { listener.Close(); <-done }()
	db, err := postgres.Open(context.Background(), config.DatabaseConfig{
		URL:              "postgres://user:secret-password@" + listener.Addr().String() + "/quotes?sslmode=disable",
		MaxOpenConns:     1,
		OperationTimeout: time.Second,
	})
	if db != nil {
		db.Close()
		t.Fatal("returned pool after failed ping")
	}
	if err == nil {
		t.Fatal("failed ping returned nil error")
	}
	if strings.Contains(err.Error(), "secret-password") {
		t.Fatal("password leaked")
	}
}
