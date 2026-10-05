package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
)

var _ repository.AttemptCompleter = (*Repository)(nil)

func (r *Repository) Succeed(ctx context.Context, attempt domain.Attempt, quote domain.Quote) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = attempt.Validate(); err != nil {
		return fmt.Errorf("complete attempt: %w", err)
	}
	if err = quote.Validate(); err != nil {
		return fmt.Errorf("complete quote: %w", err)
	}
	if attempt.Pair != quote.Pair {
		return fmt.Errorf("complete pair mismatch: %w", domain.ErrInvalidQuote)
	}
	ctx, cancel := context.WithTimeout(ctx, r.operationTimeout)
	defer cancel()
	defer func() { err = classifyError(ctx, err) }()
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin completion: %w", err)
	}
	defer func() {
		if e := tx.Rollback(); e != nil && !errors.Is(e, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rollback completion: %w", e))
		}
	}()
	if _, err = tx.ExecContext(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
		return fmt.Errorf("set completion durability: %w", err)
	}
	// Lock before evaluating the clock-dependent predicate. A direct UPDATE
	// may evaluate it before waiting on an unchanged, locked tuple.
	if _, err = tx.ExecContext(ctx, "SELECT id FROM quote_updates WHERE id=$1 FOR UPDATE", attempt.ID.String()); err != nil {
		return fmt.Errorf("lock attempt for transition: %w", err)
	}
	const query = `UPDATE quote_updates
 SET status = 'succeeded', price = $3::numeric, source = $4, source_date = $5::date,
     completed_at = clock_timestamp(), lease_until = NULL, last_error_code = NULL
 WHERE id = $1 AND status = 'processing' AND attempts = $2
   AND lease_until > clock_timestamp() AND pair = $6`
	result, err := tx.ExecContext(ctx, query, attempt.ID.String(), attempt.Number, quote.Price.String(), quote.Source, quote.SourceDate.Format("2006-01-02"), attempt.Pair.String())
	if err != nil {
		return fmt.Errorf("save successful attempt: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("completion affected rows: %w", err)
	}
	if changed == 0 {
		return repository.ErrLeaseLost
	}
	if changed != 1 {
		return fmt.Errorf("completion affected unexpected number of rows: %d", changed)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit completion: %w", err)
	}
	return nil
}
