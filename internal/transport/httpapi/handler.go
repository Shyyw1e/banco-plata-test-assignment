package httpapi

import (
	"context"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/logger"
)

type QuoteService interface {
	CreateUpdate(
		ctx context.Context,
		pair domain.Pair,
		idempotencyKey *string,
	) (*domain.QuoteUpdate, error)

	GetByID(
		ctx context.Context,
		id domain.UpdateID,
	) (*domain.QuoteUpdate, error)

	GetLatest(
		ctx context.Context,
		pair domain.Pair,
	) (*domain.QuoteUpdate, error)
}

type Handler struct {
	service QuoteService
	log     logger.Logger
}

func New(service QuoteService, log logger.Logger) *Handler {
	return &Handler{
		service: service,
		log:     log,
	}
}
