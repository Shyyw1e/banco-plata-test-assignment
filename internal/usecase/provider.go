package usecase

import (
	"context"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"time"
)

// QuoteProvider performs one attempt; scheduling and retries belong to the caller.
type QuoteProvider interface {
	Fetch(context.Context, domain.Pair) (*domain.Quote, error)
}

type ProviderError struct {
	Code       domain.FailureCode
	Retryable  bool
	RetryAfter time.Duration
	Cause      error
}

// Error deliberately excludes the upstream body, URL and underlying error text.
func (e *ProviderError) Error() string { return "provider: " + string(e.Code) }
func (e *ProviderError) Unwrap() error { return e.Cause }
