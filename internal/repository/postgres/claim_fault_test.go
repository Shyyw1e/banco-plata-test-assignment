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

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository/postgres"
)

type claimConnector struct{ c *claimConn }

func (c claimConnector) Connect(context.Context) (driver.Conn, error) { return c.c, nil }
func (c claimConnector) Driver() driver.Driver                        { return admissionDriver{} }

type claimConn struct {
	admissionConn
	empty    bool
	queryErr error
}

func (c *claimConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.HasPrefix(q, "WITH candidate AS") {
		return nil, errors.New("unexpected query")
	}
	c.calls = append(c.calls, "claim")
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	return &admissionRows{columns: []string{"id", "pair", "attempts", "lease_until"}, done: c.empty, values: []driver.Value{"00000000-0000-4000-8000-000000000001", "EUR/USD", int64(1), time.Date(2026, 10, 5, 0, 0, 30, 0, time.UTC)}}, nil
}
func TestClaimCommitFailure(t *testing.T) {
	for _, empty := range []bool{false, true} {
		c := &claimConn{empty: empty, admissionConn: admissionConn{commitErr: io.ErrUnexpectedEOF}}
		db := sql.OpenDB(claimConnector{c})
		r := postgres.New(db, time.Second, 1)
		a, err := r.Claim(context.Background(), 3, 30*time.Second)
		db.Close()
		if a != nil || !errors.Is(err, io.ErrUnexpectedEOF) || !errors.Is(err, repository.ErrUnavailable) || errors.Is(err, repository.ErrNoJob) {
			t.Fatalf("%v %v", a, err)
		}
		if got := strings.Join(c.calls, ","); got != "begin,sync,claim,commit" {
			t.Fatal(got)
		}
	}
}
func TestClaimRollbackFailure(t *testing.T) {
	cause := errors.New("query fault")
	rollback := errors.New("rollback fault")
	c := &claimConn{queryErr: cause, admissionConn: admissionConn{rollbackErr: rollback}}
	db := sql.OpenDB(claimConnector{c})
	defer db.Close()
	a, err := postgres.New(db, time.Second, 1).Claim(context.Background(), 3, 30*time.Second)
	if a != nil || !errors.Is(err, cause) || !errors.Is(err, rollback) || c.commits != 0 {
		t.Fatalf("%v %v", a, err)
	}
}
func TestClaimRejectsInvalidInputBeforeDB(t *testing.T) {
	r := postgres.New(nil, time.Second, 1)
	for _, tc := range []struct {
		n     int
		lease time.Duration
	}{{0, time.Second}, {-1, time.Second}, {3, 0}, {3, -time.Second}, {3, time.Nanosecond}} {
		if a, err := r.Claim(context.Background(), tc.n, tc.lease); a != nil || err == nil {
			t.Fatalf("%v %v", a, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if a, err := r.Claim(ctx, 3, time.Second); a != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("%v %v", a, err)
	}
}
