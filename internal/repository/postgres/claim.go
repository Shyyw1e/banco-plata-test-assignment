package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
)

var _ repository.AttemptClaimer = (*Repository)(nil)

func (r *Repository) Claim(ctx context.Context, maxAttempts int, lease time.Duration) (attempt *domain.Attempt, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if maxAttempts <= 0 || int64(maxAttempts) > math.MaxInt32 || lease < time.Microsecond {
		return nil, errors.New("postgres: invalid claim limits")
	}
	ctx, cancel := context.WithTimeout(ctx, r.operationTimeout)
	defer cancel()
	defer func() { err = classifyError(ctx, err) }()
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin claim: %w", err)
	}
	defer func() {
		if e := tx.Rollback(); e != nil && !errors.Is(e, sql.ErrTxDone) {
			attempt = nil
			err = errors.Join(err, fmt.Errorf("rollback claim: %w", e))
		}
	}()
	if _, err = tx.ExecContext(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
		return nil, fmt.Errorf("set claim durability: %w", err)
	}
	const query = `WITH candidate AS (
 SELECT id FROM quote_updates
 WHERE status = 'queued' AND next_attempt_at <= clock_timestamp() AND attempts < $1
 ORDER BY next_attempt_at, created_at, id
 LIMIT 1 FOR UPDATE SKIP LOCKED
 )
 UPDATE quote_updates AS job
 SET status = 'processing', attempts = job.attempts + 1,
     lease_until = clock_timestamp() + $2::interval
 FROM candidate WHERE job.id = candidate.id
 RETURNING job.id::text, job.pair, job.attempts, job.lease_until`
	var rawID, rawPair string
	var number int64
	var leaseUntil time.Time
	duration := strconv.FormatInt(lease.Microseconds(), 10) + " microseconds"
	err = tx.QueryRowContext(ctx, query, maxAttempts, duration).Scan(&rawID, &rawPair, &number, &leaseUntil)
	if errors.Is(err, sql.ErrNoRows) {
		if err = tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit empty claim: %w", err)
		}
		return nil, repository.ErrNoJob
	}
	if err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}
	id, err := domain.ParseUpdateID(rawID)
	if err != nil {
		return nil, fmt.Errorf("claimed ID: %w", err)
	}
	pair, err := domain.ParsePair(rawPair)
	if err != nil {
		return nil, fmt.Errorf("claimed pair: %w", err)
	}
	attempt, err = domain.NewAttempt(id, *pair, number, leaseUntil)
	if err != nil {
		return nil, fmt.Errorf("claimed attempt: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}
	return attempt, nil
}
