package usecase_test

import (
	"context"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"time"
)

// Callbacks stay test-local. Unexpected calls fail rather than silently succeed.
// Processor scenarios use these fakes to verify ordering and skipped operations.
type attemptStoreFake struct {
	claim   func(context.Context, int, time.Duration) (*domain.Attempt, error)
	succeed func(context.Context, domain.Attempt, domain.Quote) error
	failure func(context.Context, domain.Attempt, repository.FailureDecision) error
	recover func(context.Context, int, int, time.Duration) (repository.RecoveryResult, error)
	permit  func(context.Context, string, time.Duration) (repository.PermitResult, error)
	block   func(context.Context, string, time.Duration) error
}

func (f *attemptStoreFake) Claim(c context.Context, n int, d time.Duration) (*domain.Attempt, error) {
	return f.claim(c, n, d)
}
func (f *attemptStoreFake) Succeed(c context.Context, a domain.Attempt, q domain.Quote) error {
	return f.succeed(c, a, q)
}
func (f *attemptStoreFake) RecordFailure(c context.Context, a domain.Attempt, d repository.FailureDecision) error {
	return f.failure(c, a, d)
}
func (f *attemptStoreFake) RecoverExpired(c context.Context, n, b int, d time.Duration) (repository.RecoveryResult, error) {
	return f.recover(c, n, b, d)
}
func (f *attemptStoreFake) TryPermit(c context.Context, p string, d time.Duration) (repository.PermitResult, error) {
	return f.permit(c, p, d)
}
func (f *attemptStoreFake) BlockProvider(c context.Context, p string, d time.Duration) error {
	return f.block(c, p, d)
}

var (
	_ repository.AttemptClaimer         = (*attemptStoreFake)(nil)
	_ repository.AttemptCompleter       = (*attemptStoreFake)(nil)
	_ repository.AttemptFailureRecorder = (*attemptStoreFake)(nil)
	_ repository.AttemptRecoverer       = (*attemptStoreFake)(nil)
	_ repository.PermitIssuer           = (*attemptStoreFake)(nil)
	_ repository.ProviderBlocker        = (*attemptStoreFake)(nil)
)
