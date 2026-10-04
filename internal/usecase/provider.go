package usecase

import (
	"context"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"time"
)

type QuoteProvider interface {
	Fetch(context.Context, domain.Pair) (*domain.Quote, error)
}

type ProviderError struct {
	RateLimited bool
	Code        domain.FailureCode
	Retryable   bool
	RetryAfter  time.Duration
	Cause       error
}

func (e *ProviderError) Error() string { return "provider: " + string(e.Code) }
func (e *ProviderError) Unwrap() error { return e.Cause }
