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

type failureConnector struct{ c *failureConn }

func (c failureConnector) Connect(context.Context) (driver.Conn, error) { return c.c, nil }
func (c failureConnector) Driver() driver.Driver                        { return admissionDriver{} }

type failureConn struct {
	admissionConn
	affected          int64
	writeErr, rowsErr error
}

func (c *failureConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if q == "SELECT id FROM quote_updates WHERE id=$1 FOR UPDATE" {
		c.calls = append(c.calls, "lock")
		if c.lockErr != nil {
			return nil, c.lockErr
		}
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, "UPDATE quote_updates") {
		c.calls = append(c.calls, "failure")
		if len(args) != 6 || args[2].Value != true || args[3].Value != "1000001 microseconds" || args[4].Value != "provider_unavailable" {
			return nil, errors.New("incorrect failure arguments")
		}
		if c.writeErr != nil {
			return nil, c.writeErr
		}
		return completionResult{c.affected, c.rowsErr}, nil
	}
	return c.admissionConn.ExecContext(ctx, q, args)
}
func TestFailureFaults(t *testing.T) {
	writeErr, rowsErr, rollbackErr := errors.New("write"), errors.New("rows"), errors.New("rollback")
	for _, tc := range []struct {
		name    string
		c       *failureConn
		want    error
		commits int
	}{
		{"commit", &failureConn{admissionConn: admissionConn{commitErr: io.ErrUnexpectedEOF}, affected: 1}, io.ErrUnexpectedEOF, 1},
		{"write and rollback", &failureConn{admissionConn: admissionConn{rollbackErr: rollbackErr}, writeErr: writeErr}, writeErr, 0},
		{"rows", &failureConn{affected: 1, rowsErr: rowsErr}, rowsErr, 0},
		{"lost lease", &failureConn{}, repository.ErrLeaseLost, 0},
		{"success", &failureConn{affected: 1}, nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := sql.OpenDB(failureConnector{tc.c})
			defer db.Close()
			a, _ := completionInput(t)
			err := postgres.New(db, time.Second, 1).RecordFailure(context.Background(), a, repository.FailureDecision{Disposition: repository.RetryAttempt, Code: domain.CodeUnavailable, Delay: time.Second + time.Nanosecond})
			if !errors.Is(err, tc.want) || tc.c.commits != tc.commits {
				t.Fatalf("%v commits %d", err, tc.c.commits)
			}
			if tc.c.rollbackErr != nil && !errors.Is(err, rollbackErr) {
				t.Fatal("rollback cause lost")
			}
			if tc.want == io.ErrUnexpectedEOF && !errors.Is(err, repository.ErrUnavailable) {
				t.Fatal("commit classification lost")
			}
			if strings.Count(strings.Join(tc.c.calls, ","), "failure") != 1 {
				t.Fatal("write was retried")
			}
		})
	}
}
func TestFailureValidationBeforeDB(t *testing.T) {
	r := postgres.New(nil, time.Second, 1)
	a, _ := completionInput(t)
	valid := repository.FailureDecision{Disposition: repository.FailAttempt, Code: domain.CodeUnavailable}
	if err := r.RecordFailure(context.Background(), domain.Attempt{}, valid); !errors.Is(err, domain.ErrInvalidAttempt) {
		t.Fatal(err)
	}
	for _, d := range []repository.FailureDecision{
		{}, {Disposition: repository.FailAttempt, Code: "unknown"},
		{Disposition: repository.RetryAttempt, Code: domain.CodeUnavailable},
		{Disposition: repository.RetryAttempt, Code: domain.CodeUnavailable, Delay: -1},
		{Disposition: repository.FailAttempt, Code: domain.CodeUnavailable, Delay: 1},
		{Disposition: repository.RetryAttempt, Code: domain.CodeAttemptsExhausted, Delay: 1},
	} {
		if err := r.RecordFailure(context.Background(), a, d); !errors.Is(err, repository.ErrInvalidFailureDecision) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.RecordFailure(ctx, a, valid); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
