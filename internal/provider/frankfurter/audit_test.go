package frankfurter

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAuditRateLimited(t *testing.T) {
	for _, status := range []int{429, 503, 408, 400, 403} {
		for _, header := range []string{"", "12", "garbage", time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)} {
			var pe *usecase.ProviderError
			errors.As(statusError(status, parseRetryAfter(header, time.Now())), &pe)
			if pe.RateLimited != (status == 429) {
				t.Errorf("status %d missing or incorrect RateLimited", status)
			}
		}
	}
}
func TestAuditProviderFields(t *testing.T) {
	for _, body := range []string{
		`{"date":"2026-10-02","date":null,"base":"EUR","quote":"USD","rate":1.2}`,
		`{"date":"2026-10-02","base":"EUR","base":null,"quote":"USD","rate":1.2}`,
		`{"date":"2026-10-02","base":"EUR","quote":"USD","quote":null,"rate":1.2}`,
		`{"date":"2026-10-02","base":"EUR","quote":"USD","rate":1.2,"rate":2}`,
		`{"date":"2026-10-02","base":"EUR","quote":"USD","rate":1.2,"\u0064ate":"2026-10-02"}`,
	} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
			defer s.Close()
			c, _ := New(s.URL, time.Second)
			q, e := c.Fetch(context.Background(), testPair)
			if q != nil {
				t.Error("accepted ambiguous quote")
			}
			assertProvider(t, e, domain.CodeInvalidResponse, false)
		})
	}
}

func TestAuditRequiredFields(t *testing.T) {
	for _, field := range []string{"date", "base", "quote", "rate"} {
		for _, remove := range []bool{true, false} {
			fields := map[string]json.RawMessage{"date": json.RawMessage(`"2026-10-02"`), "base": json.RawMessage(`"EUR"`), "quote": json.RawMessage(`"USD"`), "rate": json.RawMessage(`1.2`)}
			if remove {
				delete(fields, field)
			} else {
				fields[field] = json.RawMessage(`null`)
			}
			data, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = decodeRate(data); err == nil {
				t.Errorf("accepted missing/null %s", field)
			}
		}
	}
}
