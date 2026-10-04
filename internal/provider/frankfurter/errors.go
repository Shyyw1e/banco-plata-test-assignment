package frankfurter

import (
	"fmt"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
	"time"
)

func statusError(status int, after time.Duration) error {
	code, retry := domain.CodeInvalidResponse, false
	switch {
	case status == 408 || status == 429 || (status >= 500 && status <= 599):
		code, retry = domain.CodeUnavailable, true
	case status >= 400 && status <= 499:
		code = domain.CodeRejected
	}
	if !retry {
		after = 0
	}
	return &usecase.ProviderError{Code: code, RateLimited: status == 429, Retryable: retry, RetryAfter: after, Cause: fmt.Errorf("upstream HTTP status %d", status)}
}
func invalidResponse(cause error) error {
	return &usecase.ProviderError{Code: domain.CodeInvalidResponse, Cause: cause}
}
func unavailable(cause error) error {
	return &usecase.ProviderError{Code: domain.CodeUnavailable, Retryable: true, Cause: cause}
}
