package usecase

import (
	"context"
	"errors"
	"fmt"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
)

var ErrInvalidIdempotencyKey = errors.New("invalid idempotency key")

type Service struct {
	creator repository.UpdateCreator
	reader  repository.UpdateReader
}

func NewService(creator repository.UpdateCreator, reader repository.UpdateReader) *Service {
	return &Service{creator: creator, reader: reader}
}

func (s *Service) CreateUpdate(ctx context.Context, pair domain.Pair, idempotencyKey *string) (*domain.QuoteUpdate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := pair.Validate(); err != nil {
		return nil, err
	}

	if idempotencyKey != nil {
		key := *idempotencyKey
		if len(key) < 1 || len(key) > 128 {
			return nil, ErrInvalidIdempotencyKey
		}
		for i := 0; i < len(key); i++ {
			if key[i] < '!' || key[i] > '~' {
				return nil, ErrInvalidIdempotencyKey
			}
		}
	}

	id, err := domain.NewUpdateID()
	if err != nil {
		return nil, fmt.Errorf("generate update ID: %w", err)
	}

	qUpdate, err := s.creator.CreateOrGet(ctx, id, pair, idempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("create update: %w", err)
	}

	return qUpdate, nil
}

func (s *Service) GetByID(ctx context.Context, id domain.UpdateID) (*domain.QuoteUpdate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	qUpdate, err := s.reader.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get update by ID: %w", err)
	}

	return qUpdate, nil
}

func (s *Service) GetLatest(ctx context.Context, pair domain.Pair) (*domain.QuoteUpdate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := pair.Validate(); err != nil {
		return nil, err
	}

	qUpdate, err := s.reader.GetLatest(ctx, pair)
	if err != nil {
		return nil, fmt.Errorf("get latest update: %w", err)
	}

	return qUpdate, nil
}
