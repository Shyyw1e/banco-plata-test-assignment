package postgres

import (
	"context"
	"errors"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/jackc/pgx/v5/pgconn"
	"testing"
)

func TestAuditReadOnly(t *testing.T) {
	original := &pgconn.PgError{Code: "25006"}
	err := classifyError(context.Background(), original)
	var pg *pgconn.PgError
	if !errors.Is(err, repository.ErrUnavailable) || !errors.As(err, &pg) || pg != original {
		t.Fatal("read-only not classified")
	}
	other := &pgconn.PgError{Code: "25P02"}
	if errors.Is(classifyError(context.Background(), other), repository.ErrUnavailable) {
		t.Fatal("whole class mapped")
	}
}
