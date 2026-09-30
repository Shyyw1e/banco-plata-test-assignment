// Package repository defines persistence contracts without depending on SQL or a driver.
package repository

import (
	"context"
	"errors"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
)

var (
	ErrNotFound            = errors.New("repository: not found")
	ErrIdempotencyConflict = errors.New("repository: idempotency key belongs to a different pair")
	ErrQueueFull           = errors.New("repository: update queue is full")
	ErrUnavailable         = errors.New("repository: storage unavailable")
)

type UpdateCreator interface {
	CreateOrGet(ctx context.Context, id domain.UpdateID, pair domain.Pair, idempotencyKey *string) (*domain.QuoteUpdate, error)
}

type UpdateReader interface {
	GetByID(ctx context.Context, id domain.UpdateID) (*domain.QuoteUpdate, error)
	GetLatest(ctx context.Context, pair domain.Pair) (*domain.QuoteUpdate, error)
}
