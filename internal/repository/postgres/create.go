package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
)

// All admission paths must take this transaction-scoped lock before counting.
const admissionLockID int64 = 1

func (r *Repository) CreateOrGet(ctx context.Context, id domain.UpdateID, pair domain.Pair, key *string) (update *domain.QuoteUpdate, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.operationTimeout)
	defer cancel()
	// Registered before rollback: classify the final error after cleanup.
	defer func() { err = classifyError(ctx, err) }()
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin admission: %w", err)
	}
	defer func() {
		if e := tx.Rollback(); e != nil && !errors.Is(e, sql.ErrTxDone) {
			update = nil
			err = errors.Join(err, fmt.Errorf("rollback admission: %w", e))
		}
	}()
	// This requires synchronous replicas in the HA deployment; on a standalone
	// development DB it guarantees local WAL durability only.
	if _, err = tx.ExecContext(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
		return nil, fmt.Errorf("set commit durability: %w", err)
	}
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", admissionLockID); err != nil {
		return nil, fmt.Errorf("lock admission: %w", err)
	}
	if key != nil {
		existing, e := scanUpdate(tx.QueryRowContext(ctx, "SELECT "+updateColumns+" FROM quote_updates WHERE idempotency_key = $1 FOR UPDATE", *key))
		switch {
		case e == nil:
			if existing.Pair != pair {
				return nil, repository.ErrIdempotencyConflict
			}
			// Reconfirm durability after a previous commit with an unknown outcome.
			if _, err = tx.ExecContext(ctx, "UPDATE quote_updates SET idempotency_key = idempotency_key WHERE id = $1", existing.ID.String()); err != nil {
				return nil, fmt.Errorf("reconfirm admission: %w", err)
			}
			if err = tx.Commit(); err != nil {
				return nil, fmt.Errorf("commit existing admission: %w", err)
			}
			return existing, nil
		case errors.Is(e, sql.ErrNoRows):
		default:
			return nil, fmt.Errorf("find idempotent update: %w", e)
		}
	}
	var pending int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM quote_updates WHERE status IN ('queued', 'processing')").Scan(&pending); err != nil {
		return nil, fmt.Errorf("count pending: %w", err)
	}
	if pending >= int64(r.maxPending) {
		return nil, repository.ErrQueueFull
	}
	var keyValue any
	if key != nil {
		keyValue = *key
	}
	update, err = scanUpdate(tx.QueryRowContext(ctx, "INSERT INTO quote_updates (id, pair, idempotency_key) VALUES ($1, $2, $3) RETURNING "+updateColumns, id.String(), pair.String(), keyValue))
	if err != nil {
		return nil, fmt.Errorf("insert update: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit new admission: %w", err)
	}
	return update, nil
}
