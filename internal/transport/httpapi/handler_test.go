package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/logger"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeService struct {
	calls  int
	result *domain.QuoteUpdate
	err    error
	pair   domain.Pair
	key    *string
	id     domain.UpdateID
}

func (f *fakeService) CreateUpdate(_ context.Context, p domain.Pair, k *string) (*domain.QuoteUpdate, error) {
	f.calls++
	f.pair = p
	f.key = k
	return f.result, f.err
}
func (f *fakeService) GetByID(_ context.Context, id domain.UpdateID) (*domain.QuoteUpdate, error) {
	f.calls++
	f.id = id
	return f.result, f.err
}
func (f *fakeService) GetLatest(_ context.Context, p domain.Pair) (*domain.QuoteUpdate, error) {
	f.calls++
	f.pair = p
	return f.result, f.err
}
func fixture(t *testing.T, status domain.UpdateStatus) *domain.QuoteUpdate {
	t.Helper()
	id, err := domain.ParseUpdateID("00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	u := &domain.QuoteUpdate{ID: id, Pair: domain.Pair{Base: domain.EUR, Quote: domain.USD}, Status: status, CreatedAt: time.Now().UTC()}
	if status == domain.StatusFailed {
		code := domain.CodeUnavailable
		u.ErrorCode = &code
	}
	if status == domain.StatusSucceeded {
		p, err := domain.ParsePrice("1.1234567890")
		if err != nil {
			t.Fatal(err)
		}
		u.Result = &domain.UpdateResult{Quote: domain.Quote{Pair: u.Pair, Price: p, Source: "frankfurter:ecb", SourceDate: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}, UpdatedAt: time.Date(2026, 10, 2, 1, 2, 3, 0, time.UTC)}
	}
	return u
}
func serve(f *fakeService, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	New(f, logger.New(slog.LevelInfo, "test", io.Discard)).Routes().ServeHTTP(w, r)
	return w
}
func assertCode(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body errorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != code {
		t.Fatalf("code %q, want %q", body.Error.Code, code)
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Fatal("not JSON")
	}
}

func TestCreateValidation(t *testing.T) {
	cases := []struct {
		name, body, content string
		keys                []string
		status              int
		code                string
	}{
		{"case-sensitive property", `{"Pair":"EUR/USD"}`, "application/json", nil, 400, "invalid_json"},
		{"empty", "", "application/json", nil, 400, "invalid_json"},
		{"null", "null", "application/json", nil, 400, "invalid_json"},
		{"array", "[]", "application/json", nil, 400, "invalid_json"},
		{"unknown", `{"pair":"EUR/USD","x":1}`, "application/json", nil, 400, "invalid_json"},
		{"second", `{"pair":"EUR/USD"}{}`, "application/json", nil, 400, "invalid_json"},
		{"missing pair", `{}`, "application/json", nil, 400, "invalid_pair"},
		{"unsupported", `{"pair":"GBP/USD"}`, "application/json", nil, 422, "unsupported_pair"},
		{"same", `{"pair":"USD/USD"}`, "application/json", nil, 400, "invalid_pair"},
		{"large", strings.Repeat(" ", 4097), "application/json", nil, 413, "request_too_large"},
		{"type", `{}`, "text/plain", nil, 415, "unsupported_media_type"},
		{"charset", `{}`, "application/json; charset=latin1", nil, 415, "unsupported_media_type"},
		{"missing type", `{}`, "", nil, 415, "unsupported_media_type"},
		{"empty key", `{"pair":"EUR/USD"}`, "application/json", []string{""}, 400, "invalid_idempotency_key"},
		{"duplicate key", `{"pair":"EUR/USD"}`, "application/json", []string{"a", "b"}, 400, "invalid_idempotency_key"},
		{"invalid key", `{"pair":"EUR/USD"}`, "application/json", []string{"a b"}, 400, "invalid_idempotency_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeService{}
			r := httptest.NewRequest("POST", "/v1/quote-updates", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.content)
			for _, k := range tc.keys {
				r.Header.Add("Idempotency-Key", k)
			}
			w := serve(f, r)
			assertCode(t, w, tc.status, tc.code)
			if f.calls != 0 {
				t.Fatal("service called for invalid request")
			}
		})
	}
}
func TestCreateStatuses(t *testing.T) {
	for _, status := range []domain.UpdateStatus{domain.StatusQueued, domain.StatusProcessing, domain.StatusSucceeded, domain.StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			f := &fakeService{result: fixture(t, status)}
			body := `{"pair":"eur/usd"}`
			body += strings.Repeat(" ", 4096-len(body))
			r := httptest.NewRequest("POST", "/v1/quote-updates", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json; charset=utf-8")
			r.Header.Set("Idempotency-Key", "Mixed,Case")
			w := serve(f, r)
			want := 202
			if status == domain.StatusSucceeded || status == domain.StatusFailed {
				want = 200
			}
			if w.Code != want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			var fields map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
				t.Fatal(err)
			}
			if len(fields) != 2 || fields["status"] != string(status) {
				t.Fatal(fields)
			}
			if w.Header().Get("Location") != "/v1/quote-updates/"+f.result.ID.String() || f.pair != f.result.Pair || f.key == nil || *f.key != "Mixed,Case" || f.calls != 1 {
				t.Fatal("arguments or Location")
			}
		})
	}
}
func TestReadStates(t *testing.T) {
	for _, status := range []domain.UpdateStatus{domain.StatusQueued, domain.StatusProcessing, domain.StatusSucceeded, domain.StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			f := &fakeService{result: fixture(t, status)}
			w := serve(f, httptest.NewRequest("GET", "/v1/quote-updates/"+f.result.ID.String(), nil))
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			wantFields := 3
			if status == domain.StatusSucceeded {
				wantFields = 7
				if body["source"] != "frankfurter:ecb" {
					t.Fatal("invalid source")
				}
				if body["price"] != "1.1234567890" || body["updated_at"] != "2026-10-02T01:02:03Z" || body["source_date"] != "2026-10-01" {
					t.Fatal(body)
				}
			}
			if status == domain.StatusFailed {
				wantFields = 4
				if body["error"] == nil {
					t.Fatal("missing job error")
				}
			}
			if len(body) != wantFields || f.id != f.result.ID {
				t.Fatal(body)
			}
		})
	}
}
func TestLatestAndReadValidation(t *testing.T) {
	for _, tc := range []struct{ url, code string }{
		{"/v1/quote-updates/nope", "invalid_id"}, {"/v1/quotes/latest", "invalid_query"}, {"/v1/quotes/latest?pair=EUR/USD&pair=EUR/USD", "invalid_query"}, {"/v1/quotes/latest?pair=%ZZ", "invalid_query"}, {"/v1/quotes/latest?pair=", "invalid_pair"},
	} {
		f := &fakeService{}
		assertCode(t, serve(f, httptest.NewRequest("GET", tc.url, nil)), 400, tc.code)
		if f.calls != 0 {
			t.Fatal("invalid request reached service")
		}
	}
	f := &fakeService{result: fixture(t, domain.StatusSucceeded)}
	w := serve(f, httptest.NewRequest("GET", "/v1/quotes/latest?pair=eur%2Fusd", nil))
	if w.Code != 200 || f.pair != f.result.Pair {
		t.Fatal(w.Body.String())
	}
	f.result = fixture(t, domain.StatusQueued)
	assertCode(t, serve(f, httptest.NewRequest("GET", "/v1/quotes/latest?pair=EUR/USD", nil)), 500, "internal_error")
}
func TestServiceErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{repository.ErrNotFound, 404, "update_not_found"}, {repository.ErrUnavailable, 503, "database_unavailable"}, {repository.ErrQueueFull, 503, "queue_full"}, {repository.ErrIdempotencyConflict, 409, "idempotency_conflict"}, {context.DeadlineExceeded, 503, "database_unavailable"}, {errors.New("secret-password"), 500, "internal_error"},
	} {
		f := &fakeService{err: fmt.Errorf("wrapped: %w", tc.err)}
		w := serve(f, httptest.NewRequest("GET", "/v1/quote-updates/00000000-0000-4000-8000-000000000001", nil))
		assertCode(t, w, tc.status, tc.code)
		if strings.Contains(w.Body.String(), "secret-password") {
			t.Fatal("leak")
		}
		if tc.status == 503 && w.Header().Get("Retry-After") != "1" {
			t.Fatal("Retry-After")
		}
	}
	f := &fakeService{err: repository.ErrNotFound}
	assertCode(t, serve(f, httptest.NewRequest("GET", "/v1/quotes/latest?pair=EUR/USD", nil)), 404, "quote_not_found")
	f = &fakeService{}
	assertCode(t, serve(f, httptest.NewRequest("GET", "/v1/quote-updates/00000000-0000-4000-8000-000000000001", nil)), 500, "internal_error")
}
func TestRoutingAndCancellation(t *testing.T) {
	f := &fakeService{}
	w := serve(f, httptest.NewRequest("PUT", "/v1/quote-updates", nil))
	assertCode(t, w, 405, "method_not_allowed")
	if w.Header().Get("Allow") != "POST" {
		t.Fatal("Allow")
	}
	assertCode(t, serve(f, httptest.NewRequest("GET", "/unknown", nil)), 404, "route_not_found")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("GET", "/v1/quotes/latest?pair=EUR/USD", nil).WithContext(ctx)
	w = serve(f, r)
	if f.calls != 0 || w.Body.Len() != 0 {
		t.Fatal("canceled request processed")
	}
}
