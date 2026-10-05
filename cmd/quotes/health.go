package main

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"
)

func healthRoutes(api http.Handler, ping func(context.Context) error, stopping *atomic.Bool, timeout time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { healthResponse(w, http.StatusOK, "live") })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if stopping.Load() {
			healthResponse(w, http.StatusServiceUnavailable, "stopping")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), min(timeout, time.Second))
		defer cancel()
		if err := ping(ctx); err != nil {
			healthResponse(w, http.StatusServiceUnavailable, "not_ready")
			return
		}
		if stopping.Load() {
			healthResponse(w, http.StatusServiceUnavailable, "stopping")
			return
		}
		healthResponse(w, http.StatusOK, "ready")
	})
	mux.Handle("/", api)
	return mux
}
func healthResponse(w http.ResponseWriter, status int, state string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"status":"` + state + `"}`))
}
