package postgres_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/logger"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository/postgres"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/transport/httpapi"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
	"github.com/jackc/pgx/v5/pgconn"
)

// admissionConnector only models the admission transaction. It verifies calls,
// not PostgreSQL isolation or WAL durability; those require a real database.
type admissionConnector struct{ conn *admissionConn }

func (c admissionConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c admissionConnector) Driver() driver.Driver                        { return admissionDriver{} }

type admissionDriver struct{}

func (admissionDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type admissionConn struct {
	replay                                        bool
	commitErr, rollbackErr, lockErr, reconfirmErr error
	calls                                         []string
	commits, rollbacks                            int
}

func (c *admissionConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected Prepare")
}
func (c *admissionConn) Close() error              { return nil }
func (c *admissionConn) Begin() (driver.Tx, error) { c.calls = append(c.calls, "begin"); return c, nil }
func (c *admissionConn) Commit() error {
	c.calls = append(c.calls, "commit")
	c.commits++
	return c.commitErr
}
func (c *admissionConn) Rollback() error {
	c.calls = append(c.calls, "rollback")
	c.rollbacks++
	return c.rollbackErr
}
func (c *admissionConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	switch {
	case q == "SET LOCAL synchronous_commit = on":
		c.calls = append(c.calls, "sync")
	case strings.Contains(q, "pg_advisory_xact_lock"):
		c.calls = append(c.calls, "lock")
		if c.lockErr != nil {
			return nil, c.lockErr
		}
	case strings.HasPrefix(q, "UPDATE quote_updates SET idempotency_key = idempotency_key"):
		c.calls = append(c.calls, "reconfirm")
		if c.reconfirmErr != nil {
			return nil, c.reconfirmErr
		}
	default:
		return nil, fmt.Errorf("unexpected Exec: %s", q)
	}
	return driver.RowsAffected(1), nil
}
func (c *admissionConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(q, "count(*)"):
		c.calls = append(c.calls, "count")
		return &admissionRows{columns: []string{"count"}, values: []driver.Value{int64(0)}}, nil
	case strings.Contains(q, "WHERE idempotency_key"):
		c.calls = append(c.calls, "lookup")
		if !c.replay {
			return nil, errors.New("unexpected lookup")
		}
		return admissionRow("00000000-0000-4000-8000-000000000001"), nil
	case strings.HasPrefix(q, "INSERT INTO quote_updates"):
		c.calls = append(c.calls, "insert")
		return admissionRow(args[0].Value.(string)), nil
	default:
		return nil, fmt.Errorf("unexpected Query: %s", q)
	}
}

type admissionRows struct {
	columns []string
	values  []driver.Value
	done    bool
}

func (r *admissionRows) Columns() []string { return r.columns }
func (r *admissionRows) Close() error      { return nil }
func (r *admissionRows) Next(dst []driver.Value) error {
	if r.done {
		return io.EOF
	}
	copy(dst, r.values)
	r.done = true
	return nil
}
func admissionRow(id string) *admissionRows {
	return &admissionRows{columns: []string{"id", "pair", "status", "created_at", "completed_at", "price", "source_date", "source", "last_error_code"}, values: []driver.Value{id, "EUR/USD", "queued", time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), nil, nil, nil, nil, nil}}
}
func faultRepo(t *testing.T, c *admissionConn) *postgres.Repository {
	t.Helper()
	db := sql.OpenDB(admissionConnector{c})
	t.Cleanup(func() { db.Close() })
	return postgres.New(db, time.Second, 1000)
}
func faultInput() (domain.UpdateID, domain.Pair) {
	return domain.UpdateID{6: 0x40, 8: 0x80, 15: 2}, domain.Pair{Base: domain.EUR, Quote: domain.USD}
}
func faultKey(replay bool) *string {
	if !replay {
		return nil
	}
	key := "replay"
	return &key
}
func assertAdmissionCalls(t *testing.T, c *admissionConn, replay bool) {
	t.Helper()
	want := "begin,sync,lock,count,insert,commit"
	if replay {
		want = "begin,sync,lock,lookup,reconfirm,commit"
	}
	if got := strings.Join(c.calls, ","); got != want {
		t.Fatalf("calls %s, want %s", got, want)
	}
}

func TestAdmissionCommitFaults(t *testing.T) {
	for _, replay := range []bool{false, true} {
		for _, cause := range []error{io.ErrUnexpectedEOF, errors.New("programming fault")} {
			t.Run(fmt.Sprintf("replay=%t/%s", replay, cause), func(t *testing.T) {
				c := &admissionConn{replay: replay, commitErr: cause}
				r := faultRepo(t, c)
				id, pair := faultInput()
				u, err := r.CreateOrGet(context.Background(), id, pair, faultKey(replay))
				if u != nil || !errors.Is(err, cause) {
					t.Fatalf("result %v, error %v", u, err)
				}
				if errors.Is(err, repository.ErrUnavailable) != errors.Is(cause, io.ErrUnexpectedEOF) {
					t.Fatalf("wrong classification: %v", err)
				}
				assertAdmissionCalls(t, c, replay)
			})
		}
	}
}
func TestAdmissionRollbackFault(t *testing.T) {
	cause := &pgconn.PgError{Code: "55P03"}
	rollback := errors.New("rollback fault")
	c := &admissionConn{lockErr: cause, rollbackErr: rollback}
	r := faultRepo(t, c)
	id, pair := faultInput()
	u, err := r.CreateOrGet(context.Background(), id, pair, nil)
	var pg *pgconn.PgError
	if u != nil || !errors.Is(err, cause) || !errors.Is(err, rollback) || !errors.As(err, &pg) || pg != cause || !errors.Is(err, repository.ErrUnavailable) {
		t.Fatalf("lost cause: %v %v", u, err)
	}
	if c.commits != 0 || c.rollbacks != 1 {
		t.Fatal("unexpected transaction completion")
	}
}
func TestAdmissionSuccessfulCommitIgnoresDeferredTxDone(t *testing.T) {
	for _, replay := range []bool{false, true} {
		c := &admissionConn{replay: replay}
		r := faultRepo(t, c)
		id, pair := faultInput()
		u, err := r.CreateOrGet(context.Background(), id, pair, faultKey(replay))
		if err != nil || u == nil {
			t.Fatalf("%v %v", u, err)
		}
		assertAdmissionCalls(t, c, replay)
		if c.rollbacks != 0 {
			t.Fatal("driver rollback after commit")
		}
	}
}
func TestAdmissionReplayWriteFault(t *testing.T) {
	cause := io.ErrUnexpectedEOF
	c := &admissionConn{replay: true, reconfirmErr: cause}
	r := faultRepo(t, c)
	id, pair := faultInput()
	u, err := r.CreateOrGet(context.Background(), id, pair, faultKey(true))
	if u != nil || !errors.Is(err, cause) || !errors.Is(err, repository.ErrUnavailable) || c.commits != 0 || c.rollbacks != 1 {
		t.Fatalf("%v %v", u, err)
	}
}
func TestHTTPCommitFailure(t *testing.T) {
	for _, replay := range []bool{false, true} {
		for _, network := range []bool{false, true} {
			t.Run(fmt.Sprintf("replay=%t/network=%t", replay, network), func(t *testing.T) {
				cause := errors.New("SECRET internal failure")
				var commitErr error = cause
				want := 500
				code := "internal_error"
				if network {
					commitErr = io.ErrUnexpectedEOF
					want = 503
					code = "database_unavailable"
				}
				c := &admissionConn{replay: replay, commitErr: commitErr}
				r := faultRepo(t, c)
				h := httpapi.New(usecase.NewService(r, r), logger.New(slog.LevelInfo, "test", io.Discard)).Routes()
				req := httptest.NewRequest("POST", "/v1/quote-updates", strings.NewReader(`{"pair":"EUR/USD"}`))
				req.Header.Set("Content-Type", "application/json")
				if replay {
					req.Header.Set("Idempotency-Key", "replay")
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				var body struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if w.Code != want || body.Error.Code != code || w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "SECRET") {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
				if network && w.Header().Get("Retry-After") != "1" {
					t.Fatal("missing Retry-After")
				}
				assertAdmissionCalls(t, c, replay)
			})
		}
	}
}

func (c *admissionConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if opts.Isolation != driver.IsolationLevel(sql.LevelReadCommitted) || opts.ReadOnly {
		return nil, errors.New("unexpected transaction options")
	}
	return c.Begin()
}
