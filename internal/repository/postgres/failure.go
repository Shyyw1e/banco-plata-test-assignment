package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
)

var _ repository.AttemptFailureRecorder = (*Repository)(nil)

func (r *Repository) RecordFailure(ctx context.Context, attempt domain.Attempt, decision repository.FailureDecision) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = attempt.Validate(); err != nil {
		return fmt.Errorf("failed attempt: %w", err)
	}
	if err = decision.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, r.operationTimeout)
	defer cancel()
	defer func() { err = classifyError(ctx, err) }()
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin failure: %w", err)
	}
	defer func() {
		if e := tx.Rollback(); e != nil && !errors.Is(e, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rollback failure: %w", e))
		}
	}()
	if _, err = tx.ExecContext(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
		return fmt.Errorf("set failure durability: %w", err)
	}
	// Lock before evaluating the clock-dependent predicate. A direct UPDATE
	// may evaluate it before waiting on an unchanged, locked tuple.
	if _, err = tx.ExecContext(ctx, "SELECT id FROM quote_updates WHERE id=$1 FOR UPDATE", attempt.ID.String()); err != nil {
		return fmt.Errorf("lock attempt for transition: %w", err)
	}
	const query = `UPDATE quote_updates
 SET status = CASE WHEN $3 THEN 'queued' ELSE 'failed' END,
     next_attempt_at = CASE WHEN $3 THEN clock_timestamp() + $4::interval ELSE next_attempt_at END,
     completed_at = CASE WHEN $3 THEN NULL ELSE clock_timestamp() END,
     lease_until = NULL, price = NULL, source = NULL, source_date = NULL,
     last_error_code = $5
 WHERE id = $1 AND status = 'processing' AND attempts = $2
   AND lease_until > clock_timestamp() AND pair = $6`
	// Round up to PostgreSQL's microsecond precision so Retry-After is never
	// shortened. Divide before adding to avoid overflow for MaxInt64 duration.
	micros := decision.Delay.Microseconds()
	if decision.Delay%time.Microsecond != 0 {
		micros++
	}
	delay := strconv.FormatInt(micros, 10) + " microseconds"
	result, err := tx.ExecContext(ctx, query, attempt.ID.String(), attempt.Number,
		decision.Disposition == repository.RetryAttempt, delay, string(decision.Code), attempt.Pair.String())
	if err != nil {
		return fmt.Errorf("save failed attempt: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failure affected rows: %w", err)
	}
	if changed == 0 {
		return repository.ErrLeaseLost
	}
	if changed != 1 {
		return fmt.Errorf("failure affected unexpected number of rows: %d", changed)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit failure: %w", err)
	}
	return nil
}
