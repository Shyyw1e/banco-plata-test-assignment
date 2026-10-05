package postgres

import (
	"context"
	"errors"
)

func (r *Repository) CheckSchema(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, r.operationTimeout)
	defer cancel()
	rows, err := r.db.QueryContext(ctx, `SELECT id,pair,status,created_at,attempts,next_attempt_at,lease_until,
 completed_at,price,source_date,source,last_error_code,idempotency_key FROM quote_updates LIMIT 0`)
	if err != nil {
		return errors.New("postgres: quote schema unavailable; apply migrations before startup")
	}
	if err = rows.Close(); err != nil {
		return errors.New("postgres: schema check failed")
	}
	var exists bool
	err = r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM provider_throttle
 WHERE provider='frankfurter' AND isfinite(next_allowed_at) AND isfinite(blocked_until))`).Scan(&exists)
	if err != nil || !exists {
		return errors.New("postgres: provider throttle schema or seed missing; apply migrations before startup")
	}
	return nil
}
