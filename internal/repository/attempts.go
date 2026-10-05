package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
)

var (
	ErrNoJob                  = errors.New("repository: no eligible job")
	ErrLeaseLost              = errors.New("repository: attempt lease lost")
	ErrInvalidFailureDecision = errors.New("repository: invalid failure decision")
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
	Delay       time.Duration
}

func (d FailureDecision) Validate() error {
	switch d.Code {
	case domain.CodeUnavailable, domain.CodeRejected, domain.CodeInvalidResponse:
	case domain.CodeAttemptsExhausted:
		if d.Disposition != FailAttempt {
			return ErrInvalidFailureDecision
		}
	default:
		return ErrInvalidFailureDecision
	}
	if (d.Disposition == RetryAttempt && d.Delay > 0) || (d.Disposition == FailAttempt && d.Delay == 0) {
		return nil
	}
	return ErrInvalidFailureDecision
}

type AttemptFailureRecorder interface {
	RecordFailure(ctx context.Context, attempt domain.Attempt, decision FailureDecision) error
}

type RecoveryResult struct {
	Requeued int64
	Failed   int64
}

// AttemptRecoverer changes at most batchSize rows in total: expired processing
// and queued jobs that exhausted maxAttempts. Counters describe committed changes;
// on any error the result is zero. A positive delay uses the database clock.
type AttemptRecoverer interface {
	RecoverExpired(ctx context.Context, maxAttempts, batchSize int, delay time.Duration) (RecoveryResult, error)
}
