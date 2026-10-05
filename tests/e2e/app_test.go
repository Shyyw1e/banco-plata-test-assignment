//go:build e2e

package e2e_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type fixture struct {
	t           *testing.T
	db          *sql.DB
	dsn, binary string
	provider    *httptest.Server
	mu          sync.RWMutex
	handler     http.HandlerFunc
	calls       atomic.Int32
}
type app struct {
	t       *testing.T
	cmd     *exec.Cmd
	base    string
	done    chan struct{}
	err     error
	logPath string
}

func eventually(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}
func newFixture(t *testing.T, binary string) *fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("dedicated TEST_DATABASE_URL required")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	schema := "e2e_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("cleanup schema: %v", err)
		}
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, file := range []string{"000001_create_quote_updates.up.sql", "000002_create_provider_throttle.up.sql"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "migrations", file))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	f := &fixture{t: t, db: db, dsn: u.String(), binary: binary, handler: success}
	f.provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		f.mu.RLock()
		h := f.handler
		f.mu.RUnlock()
		h(w, r)
	}))
	t.Cleanup(f.provider.Close)
	return f
}
func success(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "rate" {
		http.Error(w, "unexpected path", 404)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"date":"2026-10-01","base":%q,"quote":%q,"rate":1.25}`, strings.ToUpper(parts[1]), strings.ToUpper(parts[2]))
}
func (f *fixture) set(h http.HandlerFunc) { f.mu.Lock(); f.handler = h; f.mu.Unlock() }
func (f *fixture) sql(q string, args ...any) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := f.db.ExecContext(ctx, q, args...); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) start(overrides map[string]string) *app {
	f.t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		f.t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	values := map[string]string{
		"DATABASE_URL": f.dsn, "HTTP_ADDR": addr, "PROVIDER_BASE_URL": f.provider.URL,
		"DB_OPERATION_TIMEOUT": "200ms", "PROVIDER_HTTP_TIMEOUT": "1s", "JOB_LEASE_DURATION": "2s",
		"SHUTDOWN_TIMEOUT": "200ms", "WORKER_COUNT": "2", "QUEUE_POLL_INTERVAL": "10ms",
		"JOB_MAX_ATTEMPTS": "3", "JOB_RETRY_BASE_DELAY": "20ms", "RECOVERY_INTERVAL": "20ms",
		"PROVIDER_REQUESTS_PER_SECOND": "20", "LOG_LEVEL": "info",
	}
	for k, v := range overrides {
		values[k] = v
	}
	logPath := filepath.Join(f.t.TempDir(), "app.log")
	log, err := os.Create(logPath)
	if err != nil {
		f.t.Fatal(err)
	}
	cmd := exec.Command(f.binary)
	cmd.Stdout = log
	cmd.Stderr = log
	// Start with only runtime environment; never inherit app settings or read .env.
	for _, key := range []string{"PATH", "HOME", "TMPDIR"} {
		if v, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}
	for k, v := range values {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	a := &app{t: f.t, cmd: cmd, base: "http://" + addr, done: make(chan struct{}), logPath: logPath}
	if err = cmd.Start(); err != nil {
		log.Close()
		f.t.Fatal(err)
	}
	go func() { a.err = cmd.Wait(); log.Close(); close(a.done) }()
	f.t.Cleanup(func() {
		select {
		case <-a.done:
		default:
			_ = cmd.Process.Kill()
			<-a.done
		}
	})
	eventually(f.t, func() bool {
		select {
		case <-a.done:
			data, _ := os.ReadFile(a.logPath)
			f.t.Fatalf("app exited: %v\n%s", a.err, data)
		default:
		}
		status, _, err := a.request("GET", "/health/ready", "")
		return err == nil && status == 200
	})
	return a
}
func (a *app) stop() {
	a.t.Helper()
	if err := a.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		a.t.Fatal(err)
	}
	select {
	case <-a.done:
		if a.err != nil {
			data, _ := os.ReadFile(a.logPath)
			a.t.Fatalf("exit: %v\n%s", a.err, data)
		}
	case <-time.After(3 * time.Second):
		a.t.Fatal("shutdown exceeded deadline")
	}
}
func (a *app) request(method, path, body string) (int, map[string]any, error) {
	req, err := http.NewRequest(method, a.base+path, strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c := http.Client{Timeout: time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var v map[string]any
	err = json.NewDecoder(resp.Body).Decode(&v)
	return resp.StatusCode, v, err
}
func (a *app) post(pair string) string {
	a.t.Helper()
	status, v, err := a.request("POST", "/v1/quote-updates", fmt.Sprintf(`{"pair":%q}`, pair))
	if err != nil || status != 202 {
		a.t.Fatal(status, v, err)
	}
	id, ok := v["id"].(string)
	if !ok {
		a.t.Fatal("missing ID")
	}
	return id
}
func (a *app) get(path string) map[string]any {
	a.t.Helper()
	status, v, err := a.request("GET", path, "")
	if err != nil || status != 200 {
		a.t.Fatal(status, v, err)
	}
	return v
}
func (a *app) terminal(id, want string) map[string]any {
	a.t.Helper()
	var result map[string]any
	eventually(a.t, func() bool {
		result = a.get("/v1/quote-updates/" + id)
		status := result["status"]
		if status == "failed" || status == "succeeded" {
			if status != want {
				a.t.Fatalf("unexpected terminal result: %+v", result)
			}
			return true
		}
		return false
	})
	return result
}

func TestApplicationE2E(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Fatal("set dedicated TEST_DATABASE_URL")
	}
	binary := filepath.Join(t.TempDir(), "quotes")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-race", "-o", binary, "./cmd/quotes")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	t.Run("six pairs async result and latest", func(t *testing.T) {
		f := newFixture(t, binary)
		a := f.start(nil)
		for _, pair := range []string{"EUR/USD", "USD/EUR", "EUR/MXN", "MXN/EUR", "USD/MXN", "MXN/USD"} {
			entered, release := make(chan struct{}), make(chan struct{})
			f.set(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				select {
				case <-release:
					success(w, r)
				case <-r.Context().Done():
				}
			})
			id := a.post(pair) // This must finish while the provider's response is withheld.
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("provider was not called")
			}
			pending := a.get("/v1/quote-updates/" + id)
			if pending["status"] != "processing" {
				t.Fatal(pending)
			}
			close(release)
			result := a.terminal(id, "succeeded")
			if result["pair"] != pair || result["price"] != "1.2500000000" || result["source"] != "frankfurter:ecb" || result["source_date"] != "2026-10-01" {
				t.Fatal(result)
			}
			if _, err := time.Parse(time.RFC3339Nano, result["updated_at"].(string)); err != nil {
				t.Fatal(err)
			}
			latest := a.get("/v1/quotes/latest?pair=" + url.QueryEscape(pair))
			if latest["id"] != id || latest["updated_at"] != result["updated_at"] {
				t.Fatal(latest)
			}
		}
		if f.calls.Load() != 6 {
			t.Fatal("unexpected Fetch count", f.calls.Load())
		}
		a.stop()
	})
	t.Run("provider failures and previous latest", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			status  int
			recover bool
			want    string
			count   int32
			code    string
		}{
			{"transient then success", 503, true, "succeeded", 2, ""},
			{"permanent", 400, false, "failed", 1, "provider_rejected"},
			{"attempt budget", 503, false, "failed", 3, "provider_unavailable"},
			{"invalid response", 200, false, "failed", 1, "provider_invalid_response"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newFixture(t, binary)
				a := f.start(nil)
				old := a.post("EUR/USD")
				a.terminal(old, "succeeded")
				var calls atomic.Int32
				f.set(func(w http.ResponseWriter, r *http.Request) {
					n := calls.Add(1)
					if tc.recover && n > 1 {
						success(w, r)
						return
					}
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte("invalid response"))
				})
				id := a.post("EUR/USD")
				result := a.terminal(id, tc.want)
				if calls.Load() != tc.count {
					t.Fatal("wrong attempt count", calls.Load())
				}
				if tc.want == "failed" {
					e := result["error"].(map[string]any)
					if e["code"] != tc.code {
						t.Fatal(e)
					}
					if a.get("/v1/quotes/latest?pair=EUR%2FUSD")["id"] != old {
						t.Fatal("failed hid previous latest")
					}
				}
				a.stop()
			})
		}
	})
	t.Run("breaker pauses and probes", func(t *testing.T) {
		f := newFixture(t, binary)
		times := make(chan time.Time, 4)
		var calls atomic.Int32
		f.set(func(w http.ResponseWriter, r *http.Request) {
			times <- time.Now()
			if calls.Add(1) <= 2 {
				w.WriteHeader(503)
				return
			}
			success(w, r)
		})
		a := f.start(map[string]string{"PROVIDER_CIRCUIT_FAILURE_THRESHOLD": "2", "PROVIDER_CIRCUIT_OPEN_DURATION": "300ms"})
		id := a.post("EUR/USD")
		a.terminal(id, "succeeded")
		if calls.Load() != 3 {
			t.Fatal("unexpected probe calls", calls.Load())
		}
		<-times
		second, third := <-times, <-times
		if third.Sub(second) < 300*time.Millisecond {
			t.Fatal("breaker pause bypassed", third.Sub(second))
		}
		a.stop()
	})

	t.Run("restart queued processing and exhausted", func(t *testing.T) {
		f := newFixture(t, binary)
		entered, ended := make(chan struct{}), make(chan struct{})
		f.set(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(ended) })
		a := f.start(map[string]string{"WORKER_COUNT": "1"})
		interrupted := a.post("EUR/USD")
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("Fetch not started")
		}
		queued := a.post("EUR/MXN")
		if a.get("/v1/quote-updates/" + queued)["status"] != "queued" {
			t.Fatal("not queued")
		}
		a.stop()
		select {
		case <-ended:
		case <-time.After(time.Second):
			t.Fatal("Fetch was not canceled")
		}
		f.sql("UPDATE quote_updates SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", interrupted)
		exhausted := uuid.NewString()
		f.sql("INSERT INTO quote_updates(id,pair,status,attempts,lease_until) VALUES($1,'USD/MXN','processing',3,clock_timestamp()-interval '1 second')", exhausted)
		f.set(success)
		b := f.start(nil)
		b.terminal(interrupted, "succeeded")
		b.terminal(queued, "succeeded")
		failed := b.terminal(exhausted, "failed")
		if failed["error"].(map[string]any)["code"] != "attempts_exhausted" {
			t.Fatal(failed)
		}
		if f.calls.Load() != 3 {
			t.Fatal("unexpected restart Fetch count", f.calls.Load())
		}
		b.stop()
	})
	t.Run("two instances share permits and 429 pause", func(t *testing.T) {
		f := newFixture(t, binary)
		// Test-only audit of actual grants, not timestamps of HTTP network packets.
		f.sql(`CREATE TABLE permit_observations (scheduled_at timestamptz NOT NULL);
   CREATE FUNCTION observe_permit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO permit_observations VALUES (NEW.next_allowed_at); RETURN NEW; END $$;
   CREATE TRIGGER observe_permit AFTER UPDATE ON provider_throttle FOR EACH ROW
   WHEN (OLD.next_allowed_at IS DISTINCT FROM NEW.next_allowed_at) EXECUTE FUNCTION observe_permit()`)
		var calls atomic.Int32
		f.set(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(429)
				return
			}
			success(w, r)
		})
		a := f.start(map[string]string{"PROVIDER_REQUESTS_PER_SECOND": "2"})
		first := a.post("EUR/USD")
		eventually(t, func() bool {
			var blocked bool
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := f.db.QueryRowContext(ctx, "SELECT blocked_until>clock_timestamp()+interval '500 milliseconds' FROM provider_throttle WHERE provider='frankfurter'").Scan(&blocked)
			return err == nil && blocked
		})
		b := f.start(map[string]string{"PROVIDER_REQUESTS_PER_SECOND": "2"})
		if b.get("/v1/quote-updates/" + first)["id"] != first {
			t.Fatal("cross-instance read failed")
		}
		second := b.post("EUR/MXN")
		// Observe the remaining DB pause, rather than an arbitrary long sleep.
		pauseDeadline := time.Now().Add(3 * time.Second)
		for {
			if time.Now().After(pauseDeadline) {
				t.Fatal("shared pause did not expire")
			}
			var stillBlocked bool
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err := f.db.QueryRowContext(ctx, "SELECT blocked_until>clock_timestamp()+interval '50 milliseconds' FROM provider_throttle WHERE provider='frankfurter'").Scan(&stillBlocked)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			if !stillBlocked {
				break
			}
			if calls.Load() != 1 {
				t.Fatal("Fetch bypassed shared pause")
			}
			time.Sleep(10 * time.Millisecond)
		}
		b.terminal(first, "succeeded")
		a.terminal(second, "succeeded")
		a.stop()
		b.stop()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var total, bad int
		err := f.db.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE gap<interval '500 milliseconds') FROM
   (SELECT scheduled_at-lag(scheduled_at) OVER(ORDER BY scheduled_at) AS gap FROM permit_observations) s`).Scan(&total, &bad)
		if err != nil || total < 3 || bad != 0 {
			t.Fatal("global permit spacing", total, bad, err)
		}
	})
}
