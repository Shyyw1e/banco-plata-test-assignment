package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/config"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/logger"
)

type runnerFunc func(context.Context, context.Context)

func (f runnerFunc) Run(work, stop context.Context) { f(work, stop) }
func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}
func TestServeDrainAndForcedCancellation(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "drain", true: "deadline"}[force], func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			signal, cancel := context.WithCancel(context.Background())
			defer cancel()
			workerEntered, workerStopped, workerCanceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
			httpEntered, httpCanceled := make(chan struct{}), make(chan struct{})
			release := make(chan struct{})
			pool := runnerFunc(func(work, stop context.Context) {
				close(workerEntered)
				<-stop.Done()
				close(workerStopped)
				select {
				case <-release:
				case <-work.Done():
					close(workerCanceled)
				}
			})
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(httpEntered)
				select {
				case <-release:
					w.WriteHeader(200)
				case <-r.Context().Done():
					close(httpCanceled)
				}
			})
			cfg := config.Config{ShutdownTimeout: time.Second, Worker: config.WorkerConfig{Count: 5}}
			if force {
				cfg.ShutdownTimeout = 40 * time.Millisecond
			}
			var stopping atomic.Bool
			done := make(chan error, 1)
			go func() {
				done <- serve(signal, cfg, ln, handler, pool, &stopping, logger.New(slog.LevelDebug, "test", io.Discard))
			}()
			await(t, workerEntered)
			clientDone := make(chan struct{})
			go func() {
				defer close(clientDone)
				c := http.Client{Timeout: 2 * time.Second}
				resp, e := c.Get("http://" + ln.Addr().String())
				if e == nil {
					resp.Body.Close()
				}
			}()
			await(t, httpEntered)
			cancel()
			await(t, workerStopped)
			if !stopping.Load() {
				t.Fatal("readiness not disabled")
			}
			if !force {
				select {
				case <-workerCanceled:
					t.Fatal("worker canceled before drain")
				default:
				}
				select {
				case <-httpCanceled:
					t.Fatal("HTTP canceled before drain")
				default:
				}
				close(release)
			} else {
				await(t, workerCanceled)
				await(t, httpCanceled)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("shutdown stuck")
			}
			await(t, clientDone)
		})
	}
}
func TestServeFailureStopsWorkers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	exited := make(chan struct{})
	pool := runnerFunc(func(work, stop context.Context) { <-stop.Done(); close(exited) })
	var stopping atomic.Bool
	err = serve(context.Background(), config.Config{ShutdownTimeout: time.Second}, ln, http.NewServeMux(), pool, &stopping, logger.New(slog.LevelInfo, "test", io.Discard))
	if err == nil {
		t.Fatal("Serve error lost")
	}
	await(t, exited)
}
func TestHealth(t *testing.T) {
	for _, tc := range []struct {
		name, path    string
		stopping      bool
		pingErr       error
		status, calls int
	}{
		{name: "live", path: "/health/live", status: 200},
		{name: "live despite database", path: "/health/live", pingErr: errors.New("database"), status: 200},
		{name: "ready", path: "/health/ready", status: 200, calls: 1},
		{name: "unavailable", path: "/health/ready", pingErr: errors.New("secret DSN"), status: 503, calls: 1},
		{name: "stopping", path: "/health/ready", stopping: true, status: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stopping atomic.Bool
			stopping.Store(tc.stopping)
			calls := 0
			h := healthRoutes(http.NotFoundHandler(), func(c context.Context) error {
				calls++
				d, ok := c.Deadline()
				if !ok || time.Until(d) > time.Second {
					t.Fatal("missing bounded ping")
				}
				return tc.pingErr
			}, &stopping, 2*time.Second)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			if w.Code != tc.status || calls != tc.calls {
				t.Fatal(w.Code, calls)
			}
			if w.Body.String() == "secret DSN" {
				t.Fatal("unsafe response")
			}
		})
	}
}
