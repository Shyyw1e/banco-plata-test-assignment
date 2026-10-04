package frankfurter

import (
	"errors"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
}

var _ usecase.QuoteProvider = (*Client)(nil)

func New(baseURL string, timeout time.Duration) (*Client, error) {
	if timeout <= 0 {
		return nil, errors.New("frankfurter: timeout must be positive")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, errors.New("frankfurter: invalid base URL")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("frankfurter: invalid base URL")
	}
	return &Client{baseURL: u, httpClient: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
