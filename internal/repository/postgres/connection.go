package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/config"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func Open(ctx context.Context, cfg config.DatabaseConfig) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	connCfg, err := pgx.ParseConfig(cfg.URL)
	if err != nil {
		return nil, errors.New("postgres: invalid connection configuration")
	}

	db := stdlib.OpenDB(*connCfg)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	pingCtx, cancel := context.WithTimeout(ctx, cfg.OperationTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		closeErr := db.Close()
		var cause error
		switch {
		case errors.Is(err, context.Canceled):
			cause = context.Canceled
		case errors.Is(err, context.DeadlineExceeded):
			cause = context.DeadlineExceeded
		default:
			cause = errors.New("postgres: connection check failed")
		}

		if closeErr != nil {
			return nil, errors.Join(
				cause,
				errors.New("failed to close pool after connection error"),
			)
		}

		return nil, cause
	}

	return db, nil
}
