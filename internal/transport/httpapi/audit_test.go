package httpapi

import (
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuditDuplicatePair(t *testing.T) {
	for _, body := range []string{`{"pair":"EUR/USD","pair":null}`, `{"pair":"EUR/USD","pair":"USD/EUR"}`, `{"pair":"EUR/USD","pair":"EUR/USD"}`, `{"pair":"EUR/USD","\u0070air":"EUR/USD"}`} {
		f := &fakeService{result: fixture(t, domain.StatusQueued)}
		r := httptest.NewRequest("POST", "/v1/quote-updates", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := serve(f, r)
		assertCode(t, w, 400, "invalid_json")
		if f.calls != 0 {
			t.Fatal("service called")
		}
	}
}
func TestAuditSource(t *testing.T) {
	for _, source := range []string{"frankfurter", "unknown"} {
		for _, path := range []string{"/v1/quotes/latest?pair=EUR/USD", "/v1/quote-updates/00000000-0000-4000-8000-000000000001"} {
			f := &fakeService{result: fixture(t, domain.StatusSucceeded)}
			f.result.Result.Quote.Source = source
			assertCode(t, serve(f, httptest.NewRequest("GET", path, nil)), 500, "internal_error")
		}
	}
}
