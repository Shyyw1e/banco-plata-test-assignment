package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
)

var (
	_ repository.PermitIssuer    = (*Repository)(nil)
	_ repository.ProviderBlocker = (*Repository)(nil)
)

func (r *Repository) TryPermit(ctx context.Context, provider string, interval time.Duration) (repository.PermitResult, error) {
	if err := ctx.Err(); err != nil {
		return repository.PermitResult{}, err
	}
	if interval < time.Microsecond {
		return repository.PermitResult{}, errors.New("postgres: permit interval must be at least one microsecond")
	}
	var result repository.PermitResult
	err := r.withThrottleLock(ctx, provider, func(ctx context.Context, tx *sql.Tx) error {
		var next, blocked, now time.Time
		if err := tx.QueryRowContext(ctx, "SELECT next_allowed_at, blocked_until, clock_timestamp() FROM provider_throttle WHERE provider=$1", provider).Scan(&next, &blocked, &now); err != nil {
			return fmt.Errorf("read permit schedule: %w", err)
		}
		if blocked.After(next) {
			next = blocked
		}
		if next.After(now) {
			result.RetryAfter = next.Sub(now)
			return nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE provider_throttle SET next_allowed_at=clock_timestamp()+$2::interval WHERE provider=$1", provider, throttleInterval(interval)); err != nil {
			return fmt.Errorf("issue permit: %w", err)
		}
		result = repository.PermitResult{Granted: true, ValidFor: interval}
		return nil
	})
	if err != nil {
		return repository.PermitResult{}, err
	}
	return result, nil
}

func (r *Repository) BlockProvider(ctx context.Context, provider string, pause time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if pause <= 0 {
		return errors.New("postgres: provider pause must be positive")
	}
	return r.withThrottleLock(ctx, provider, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE provider_throttle SET blocked_until=GREATEST(blocked_until,clock_timestamp()+$2::interval) WHERE provider=$1", provider, throttleInterval(pause))
		if err != nil {
			return fmt.Errorf("save provider pause: %w", err)
		}
		return nil
	})
}

// Both operations serialize on the same existing seed row. Read database time
// only after acquiring the lock, so waiting on a competing operation is included.
func (r *Repository) withThrottleLock(ctx context.Context, provider string, operation func(context.Context, *sql.Tx) error) (err error) {
	if provider == "" || strings.TrimSpace(provider) != provider {
		return errors.New("postgres: invalid provider key")
	}
	ctx, cancel := context.WithTimeout(ctx, r.operationTimeout)
	defer cancel()
	defer func() { err = classifyError(ctx, err) }()
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin provider coordination: %w", err)
	}
	defer func() {
		if e := tx.Rollback(); e != nil && !errors.Is(e, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rollback provider coordination: %w", e))
		}
	}()
	if _, err = tx.ExecContext(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
		return fmt.Errorf("set provider coordination durability: %w", err)
	}
	var key string
	err = tx.QueryRowContext(ctx, "SELECT provider FROM provider_throttle WHERE provider=$1 FOR UPDATE", provider).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return repository.ErrProviderNotConfigured
	}
	if err != nil {
		return fmt.Errorf("lock provider coordination: %w", err)
	}
	if err = operation(ctx, tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit provider coordination: %w", err)
	}
	return nil
}

// Round up without overflowing duration, including for a MaxInt64 pause.
func throttleInterval(d time.Duration) string {
	micros := d.Microseconds()
	if d%time.Microsecond != 0 {
		micros++
	}
	return strconv.FormatInt(micros, 10) + " microseconds"
}
