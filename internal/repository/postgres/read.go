package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
)

func (r *Repository) GetByID(ctx context.Context, id domain.UpdateID) (*domain.QuoteUpdate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	ctxDB, cancel := context.WithTimeout(ctx, r.operationTimeout)
	defer cancel()
	const query = "SELECT " + updateColumns + " FROM quote_updates WHERE id = $1"
	row := r.db.QueryRowContext(ctxDB, query, id.String())
	qupdate, err := scanUpdate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get update by ID: %w", classifyError(ctxDB, err))
	}

	return qupdate, nil
}

func (r *Repository) GetLatest(ctx context.Context, pair domain.Pair) (*domain.QuoteUpdate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	ctxDB, cancel := context.WithTimeout(ctx, r.operationTimeout)
	defer cancel()
	const query = "SELECT " + updateColumns + ` FROM quote_updates
WHERE pair = $1 AND status = 'succeeded'
ORDER BY source_date DESC, completed_at DESC, id DESC LIMIT 1`
	row := r.db.QueryRowContext(ctxDB, query, pair.String())
	qupdate, err := scanUpdate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get latest update: %w", classifyError(ctxDB, err))
	}

	return qupdate, nil
}
