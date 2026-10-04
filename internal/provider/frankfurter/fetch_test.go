package frankfurter

import (
	"context"
	"errors"
	"fmt"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var testPair = domain.Pair{Base: domain.EUR, Quote: domain.USD}

const validBody = `{"date":"2026-10-02","base":"EUR","quote":"USD","rate":1.12345678905,"extra":true}`

func assertProvider(t *testing.T, err error, code domain.FailureCode, retry bool) *usecase.ProviderError {
	t.Helper()
	var pe *usecase.ProviderError
	if !errors.As(err, &pe) || pe.Code != code || pe.Retryable != retry {
		t.Fatalf("unexpected error: %#v", err)
	}
	return pe
}
func TestFetchSuccessConcurrent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/providers/ecb/rate/eur/usd" || r.Method != "GET" || r.Header.Get("Accept") != "application/json" || r.UserAgent() != "banco-plata-quotes/1.0" {
			t.Error("incorrect upstream request")
		}
		io.WriteString(w, validBody)
	}))
	defer server.Close()
	c, err := New(server.URL+"/v2/providers/ecb/", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q, e := c.Fetch(context.Background(), testPair)
			if e != nil {
				t.Error(e)
				return
			}
			if q.Pair != testPair || q.Price.String() != "1.1234567890" || q.Source != "frankfurter:ecb" || q.SourceDate.Format("2006-01-02") != "2026-10-02" {
				t.Errorf("quote: %+v", q)
			}
		}()
	}
	wg.Wait()
}
func TestInvalidResponses(t *testing.T) {
	bodies := []string{"", `null`, `[]`, `{}`, `{} {}`, strings.Repeat(" ", int(maxResponseBytes)+1), strings.Replace(validBody, "EUR", "MXN", 1), strings.Replace(validBody, "2026-10-02", "not-a-date", 1)}
	for _, rate := range []string{`null`, `"1.2"`, `0`, `-1`, `true`, `{}`, `1e100`, `0.000000000001`} {
		bodies = append(bodies, strings.Replace(validBody, "1.12345678905", rate, 1))
	}
	for i, body := range bodies {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
			defer s.Close()
			c, _ := New(s.URL, time.Second)
			q, e := c.Fetch(context.Background(), testPair)
			if q != nil {
				t.Fatal("quote with error")
			}
			assertProvider(t, e, domain.CodeInvalidResponse, false)
		})
	}
}
func TestStatuses(t *testing.T) {
	for _, status := range []int{204, 301, 400, 403, 404, 408, 422, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "12")
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(status)
				io.WriteString(w, "private upstream body")
			}))
			defer s.Close()
			c, _ := New(s.URL, time.Second)
			_, err := c.Fetch(context.Background(), testPair)
			code, retry := domain.CodeInvalidResponse, false
			if status == 408 || status == 429 || status >= 500 {
				code, retry = domain.CodeUnavailable, true
			} else if status >= 400 {
				code = domain.CodeRejected
			}
			pe := assertProvider(t, err, code, retry)
			if retry && pe.RetryAfter != 12*time.Second {
				t.Fatal(pe.RetryAfter)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("body leaked")
			}
		})
	}
}
func TestContextAndTimeout(t *testing.T) {
	t.Run("parent cancellation", func(t *testing.T) {
		started := make(chan struct{})
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
		defer s.Close()
		c, _ := New(s.URL, 5*time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := c.Fetch(ctx, testPair); done <- err }()
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("request did not arrive")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancellation ignored")
		}
	})
	t.Run("client timeout", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		defer s.Close()
		c, _ := New(s.URL, 50*time.Millisecond)
		_, err := c.Fetch(context.Background(), testPair)
		assertProvider(t, err, domain.CodeUnavailable, true)
	})
}
func TestBrokenBody(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		io.WriteString(w, "{")
	}))
	defer s.Close()
	c, _ := New(s.URL, time.Second)
	_, err := c.Fetch(context.Background(), testPair)
	assertProvider(t, err, domain.CodeUnavailable, true)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal("cause lost")
	}
}
func TestInputValidation(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected request") }))
	defer s.Close()
	c, _ := New(s.URL, time.Second)
	if _, err := c.Fetch(context.Background(), domain.Pair{}); !errors.Is(err, domain.ErrInvalidPair) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Fetch(ctx, testPair); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, u := range []string{"", "://", "ftp://example.com", "https://user:secret@example.com", "https://example.com?x=1", "https://example.com#fragment", "/relative"} {
		if _, err := New(u, time.Second); err == nil {
			t.Errorf("accepted %q", u)
		}
	}
	if _, err := New(s.URL, 0); err == nil {
		t.Fatal("zero timeout")
	}
}
