package usecase

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
)

const RecoveryBatchSize = 100

type Recovery struct {
	store       repository.AttemptRecoverer
	maxAttempts int
	delay       time.Duration
}

func NewRecovery(store repository.AttemptRecoverer, maxAttempts int, delay time.Duration) (*Recovery, error) {
	if store == nil || maxAttempts <= 0 || int64(maxAttempts) > math.MaxInt32 || delay <= 0 {
		return nil, errors.New("recovery: invalid dependencies or limits")
	}
	return &Recovery{store, maxAttempts, delay}, nil
}

func (r *Recovery) Recover(ctx context.Context) (repository.RecoveryResult, error) {
	if err := ctx.Err(); err != nil {
		return repository.RecoveryResult{}, err
	}
	return r.store.RecoverExpired(ctx, r.maxAttempts, RecoveryBatchSize, r.delay)
}
