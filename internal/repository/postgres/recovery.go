package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
)

var _ repository.AttemptRecoverer = (*Repository)(nil)

// RecoverExpired changes at most batchSize jobs across both eligible states.
// Selection, row locks and transitions belong to the same short transaction.
func (r *Repository) RecoverExpired(ctx context.Context, maxAttempts, batchSize int, delay time.Duration) (result repository.RecoveryResult, err error) {
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if maxAttempts <= 0 || int64(maxAttempts) > math.MaxInt32 || batchSize <= 0 || int64(batchSize) > math.MaxInt32 || delay <= 0 {
		return result, errors.New("postgres: invalid recovery limits")
	}
	ctx, cancel := context.WithTimeout(ctx, r.operationTimeout)
	defer cancel()
	defer func() {
		err = classifyError(ctx, err)
		if err != nil {
			result = repository.RecoveryResult{}
		}
	}()
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return result, fmt.Errorf("begin recovery: %w", err)
	}
	defer func() {
		if e := tx.Rollback(); e != nil && !errors.Is(e, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rollback recovery: %w", e))
		}
	}()
	if _, err = tx.ExecContext(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
		return result, fmt.Errorf("set recovery durability: %w", err)
	}
	const query = `WITH candidates AS (
 SELECT id FROM quote_updates
 WHERE (status = 'processing' AND lease_until <= clock_timestamp())
    OR (status = 'queued' AND attempts >= $1)
 ORDER BY CASE WHEN status = 'processing' THEN lease_until ELSE next_attempt_at END, id
 LIMIT $2 FOR UPDATE SKIP LOCKED
 ), changed AS (
 UPDATE quote_updates AS job
 SET status = CASE WHEN job.attempts >= $1 THEN 'failed' ELSE 'queued' END,
     next_attempt_at = CASE WHEN job.attempts < $1 THEN clock_timestamp() + $3::interval ELSE job.next_attempt_at END,
     completed_at = CASE WHEN job.attempts >= $1 THEN clock_timestamp() ELSE NULL END,
     last_error_code = CASE WHEN job.attempts >= $1 THEN 'attempts_exhausted' ELSE job.last_error_code END,
     lease_until = NULL, price = NULL, source = NULL, source_date = NULL
 FROM candidates WHERE job.id = candidates.id
 RETURNING job.status
 )
 SELECT count(*) FILTER (WHERE status = 'queued'), count(*) FILTER (WHERE status = 'failed') FROM changed`
	// A positive delay stays positive at PostgreSQL's microsecond precision.
	micros := delay.Microseconds()
	if delay%time.Microsecond != 0 {
		micros++
	}
	interval := strconv.FormatInt(micros, 10) + " microseconds"
	if err = tx.QueryRowContext(ctx, query, maxAttempts, batchSize, interval).Scan(&result.Requeued, &result.Failed); err != nil {
		return result, fmt.Errorf("recover expired attempts: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return result, fmt.Errorf("commit recovery: %w", err)
	}
	return result, nil
}
