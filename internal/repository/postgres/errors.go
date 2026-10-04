package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"strings"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/jackc/pgx/v5/pgconn"
)

// classifyError preserves the original cause for errors.Is/As. It does not
// authorize retrying a write: a failed commit may have an unknown outcome.
// NoRows is interpreted by the caller, since it also means an unused key.
func classifyError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "57014" && ctx.Err() != nil {
			return errors.Join(ctx.Err(), err)
		}
		switch pgErr.Code {
		case "53300", "53100", "53200", "53400", "57P01", "57P02", "57P03", "40001", "40P01", "55P03", "57014":
			return errors.Join(repository.ErrUnavailable, err)
		}
		if strings.HasPrefix(pgErr.Code, "08") {
			return errors.Join(repository.ErrUnavailable, err)
		}
		// In particular, 23505 does not prove an idempotency conflict: neither
		// a duplicate UUID nor a duplicate key establishes a different pair.
		return err
	}
	var networkErr net.Error
	var connectErr *pgconn.ConnectError
	if errors.As(err, &networkErr) || errors.As(err, &connectErr) ||
		errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		if ctx.Err() != nil {
			return errors.Join(ctx.Err(), err)
		}
		return errors.Join(repository.ErrUnavailable, err)
	}
	if errors.Is(err, sql.ErrTxDone) && ctx.Err() != nil {
		return errors.Join(ctx.Err(), err)
	}
	return err
}
