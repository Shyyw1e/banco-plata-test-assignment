package postgres_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository/postgres"
)

type completeConnector struct{ c *completeConn }

func (c completeConnector) Connect(context.Context) (driver.Conn, error) { return c.c, nil }
func (c completeConnector) Driver() driver.Driver                        { return admissionDriver{} }

type completeConn struct {
	admissionConn
	affected          int64
	writeErr, rowsErr error
}
type completionResult struct {
	n   int64
	err error
}

func (r completionResult) LastInsertId() (int64, error) { return 0, errors.New("unsupported") }
func (r completionResult) RowsAffected() (int64, error) { return r.n, r.err }
func (c *completeConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if q == "SELECT id FROM quote_updates WHERE id=$1 FOR UPDATE" {
		c.calls = append(c.calls, "lock")
		if c.lockErr != nil {
			return nil, c.lockErr
		}
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, "UPDATE quote_updates") {
		c.calls = append(c.calls, "complete")
		if len(args) != 6 || args[2].Value != "1.1234567890" || args[3].Value != "frankfurter:ecb" {
			return nil, errors.New("incorrect quote arguments")
		}
		if c.writeErr != nil {
			return nil, c.writeErr
		}
		return completionResult{c.affected, c.rowsErr}, nil
	}
	return c.admissionConn.ExecContext(ctx, q, args)
}
func completionInput(t *testing.T) (domain.Attempt, domain.Quote) {
	t.Helper()
	id, pair := faultInput()
	p, err := domain.ParsePrice("1.1234567890")
	if err != nil {
		t.Fatal(err)
	}
	return domain.Attempt{ID: id, Pair: pair, Number: 1, LeaseUntil: time.Now().Add(time.Minute)}, domain.Quote{Pair: pair, Price: p, Source: "frankfurter:ecb", SourceDate: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
}
func TestCompletionFaults(t *testing.T) {
	writeErr, rowsErr, rollbackErr := errors.New("write"), errors.New("rows"), errors.New("rollback")
	for _, tc := range []struct {
		name   string
		c      *completeConn
		want   error
		commit int
	}{
		{"commit", &completeConn{admissionConn: admissionConn{commitErr: io.ErrUnexpectedEOF}, affected: 1}, io.ErrUnexpectedEOF, 1},
		{"write and rollback", &completeConn{admissionConn: admissionConn{rollbackErr: rollbackErr}, writeErr: writeErr}, writeErr, 0},
		{"rows", &completeConn{affected: 1, rowsErr: rowsErr}, rowsErr, 0},
		{"lost lease", &completeConn{}, repository.ErrLeaseLost, 0},
		{"success", &completeConn{affected: 1}, nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := sql.OpenDB(completeConnector{tc.c})
			defer db.Close()
			r := postgres.New(db, time.Second, 1)
			a, q := completionInput(t)
			err := r.Succeed(context.Background(), a, q)
			if !errors.Is(err, tc.want) || tc.c.commits != tc.commit {
				t.Fatalf("%v, commits %d", err, tc.c.commits)
			}
			if tc.c.rollbackErr != nil && !errors.Is(err, rollbackErr) {
				t.Fatal("rollback cause lost")
			}
			if tc.want == io.ErrUnexpectedEOF && !errors.Is(err, repository.ErrUnavailable) {
				t.Fatal("commit classification lost")
			}
			if tc.want == nil && tc.c.rollbacks != 0 {
				t.Fatal("rollback after success")
			}
			if strings.Count(strings.Join(tc.c.calls, ","), "complete") != 1 {
				t.Fatal("write was retried")
			}
		})
	}
}
func TestCompletionValidationBeforeDB(t *testing.T) {
	r := postgres.New(nil, time.Second, 1)
	a, q := completionInput(t)
	if err := r.Succeed(context.Background(), domain.Attempt{}, q); !errors.Is(err, domain.ErrInvalidAttempt) {
		t.Fatal(err)
	}
	if err := r.Succeed(context.Background(), a, domain.Quote{}); !errors.Is(err, domain.ErrInvalidQuote) {
		t.Fatal(err)
	}
	q.Pair = domain.Pair{Base: domain.USD, Quote: domain.EUR}
	if err := r.Succeed(context.Background(), a, q); !errors.Is(err, domain.ErrInvalidQuote) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Succeed(ctx, a, q); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
