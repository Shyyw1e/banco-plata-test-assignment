package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestClassifyError(t *testing.T) {
	if classifyError(context.Background(), nil) != nil {
		t.Fatal("nil changed")
	}
	for _, code := range []string{"08000", "08006", "53300", "53100", "53200", "53400", "57P01", "57P02", "57P03", "40001", "40P01", "55P03", "57014"} {
		t.Run(code, func(t *testing.T) {
			cause := &pgconn.PgError{Code: code}
			got := classifyError(context.Background(), fmt.Errorf("query: %w", cause))
			var pg *pgconn.PgError
			if !errors.Is(got, repository.ErrUnavailable) || !errors.As(got, &pg) || pg != cause {
				t.Fatalf("classification lost: %v", got)
			}
		})
	}
	for _, cause := range []error{io.EOF, io.ErrUnexpectedEOF, driver.ErrBadConn, sql.ErrConnDone, &net.OpError{Op: "read", Net: "tcp", Err: io.EOF}} {
		got := classifyError(context.Background(), fmt.Errorf("storage: %w", cause))
		if !errors.Is(got, repository.ErrUnavailable) || !errors.Is(got, cause) {
			t.Fatalf("network classification: %v", got)
		}
	}
	for _, cause := range []error{sql.ErrNoRows, sql.ErrTxDone, domain.ErrInvalidUpdate, repository.ErrQueueFull, repository.ErrIdempotencyConflict, context.Canceled, context.DeadlineExceeded, &pgconn.PgError{Code: "23505", ConstraintName: "quote_updates_idempotency_key_unique"}, &pgconn.PgError{Code: "23505", ConstraintName: "quote_updates_pkey"}, &pgconn.PgError{Code: "23514"}, &pgconn.PgError{Code: "42601"}, errors.New("scan conversion failure")} {
		wrapped := fmt.Errorf("operation: %w", cause)
		if got := classifyError(context.Background(), wrapped); got != wrapped {
			t.Fatalf("unexpected reclassification: %v", got)
		}
	}
}

func TestClassifyCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, cause := range []error{&pgconn.PgError{Code: "57014"}, sql.ErrTxDone, &net.OpError{Op: "read", Err: io.EOF}} {
		got := classifyError(ctx, cause)
		if !errors.Is(got, context.Canceled) || !errors.Is(got, cause) || errors.Is(got, repository.ErrUnavailable) {
			t.Fatalf("cancellation: %v", got)
		}
	}
	// An unrelated validation failure is not replaced by a late cancellation.
	if got := classifyError(ctx, domain.ErrInvalidUpdate); got != domain.ErrInvalidUpdate {
		t.Fatal(got)
	}
}
