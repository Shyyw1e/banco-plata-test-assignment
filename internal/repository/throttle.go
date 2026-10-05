package repository

import (
	"context"
	"errors"
	"time"
)

var ErrProviderNotConfigured = errors.New("repository: provider throttle seed missing")

// PermitResult is valid only after commit. Measure ValidFor with monotonic time
// from BEFORE TryPermit, including the database round trip. It is not a promise
// of exact spacing between outbound network packets.
type PermitResult struct {
	Granted    bool
	ValidFor   time.Duration
	RetryAfter time.Duration
}

type PermitIssuer interface {
	TryPermit(ctx context.Context, provider string, interval time.Duration) (PermitResult, error)
}

type ProviderBlocker interface {
	BlockProvider(ctx context.Context, provider string, pause time.Duration) error
}
