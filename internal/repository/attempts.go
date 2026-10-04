package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
)

var (
	ErrNoJob = errors.New("repository: no eligible job")
	ErrLeaseLost = errors.New("repository: attempt lease lost")
)

type AttemptClaimer interface {
	Claim(ctx context.Context, maxAttempts int, lease time.Duration) (*domain.Attempt, error)
}

type AttemptCompleter interface {
	Succeed(ctx context.Context, attempt domain.Attempt, quote domain.Quote) error
}

type FailureDisposition uint8

const (
	RetryAttempt FailureDisposition = iota + 1
	FailAttempt
)

type FailureDecision struct {
	Disposition FailureDisposition
	Code        domain.FailureCode
	Delay time.Duration
}

type AttemptFailureRecorder interface {
	RecordFailure(ctx context.Context, attempt domain.Attempt, decision FailureDecision) error
}

type RecoveryResult struct {
	Requeued int64
	Failed   int64
}

type AttemptRecoverer interface {
	RecoverExpired(ctx context.Context, maxAttempts, batchSize int, delay time.Duration) (RecoveryResult, error)
}
