package repository

import (
	"context"
	"time"
)

type PermitResult struct {
	Granted bool
	ValidFor   time.Duration
	RetryAfter time.Duration
}

type PermitIssuer interface {
	TryPermit(ctx context.Context, provider string, interval time.Duration) (PermitResult, error)
}

type ProviderBlocker interface {
	BlockProvider(ctx context.Context, provider string, pause time.Duration) error
}
