package usecase

import (
	"errors"
	"sync"
	"time"
)

type breakerState uint8

const (
	closed breakerState = iota
	open
	halfOpen
)

// CircuitBreaker is shared by every worker in one process. It observes Fetch
// outcomes only, in observation order; database failures do not affect it.
type CircuitBreaker struct {
	mu                  sync.Mutex
	state               breakerState
	failures, threshold int
	generation          uint64
	until               time.Time
	pause               time.Duration
	now                 func() time.Time
}

func NewCircuitBreaker(threshold int, pause time.Duration, now func() time.Time) (*CircuitBreaker, error) {
	if threshold <= 0 || pause <= 0 {
		return nil, errors.New("breaker: invalid limits")
	}
	if now == nil {
		now = time.Now
	}
	return &CircuitBreaker{threshold: threshold, pause: pause, now: now}, nil
}

type BreakerTicket struct {
	breaker         *CircuitBreaker
	generation      uint64
	probe, finished bool
}

// Acquire reserves the sole half-open probe. A zero wait with a nil ticket means
// another probe is active: the caller should use its regular polling interval.
func (b *CircuitBreaker) Acquire() (*BreakerTicket, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == halfOpen {
		return nil, 0
	}
	if b.state == open {
		if remaining := b.until.Sub(b.now()); remaining > 0 {
			return nil, remaining
		}
		b.state = halfOpen
		return &BreakerTicket{breaker: b, generation: b.generation, probe: true}, 0
	}
	return &BreakerTicket{breaker: b, generation: b.generation}, 0
}
func (t *BreakerTicket) Valid() bool {
	b := t.breaker
	b.mu.Lock()
	defer b.mu.Unlock()
	return !t.finished && t.generation == b.generation && (b.state == closed || (t.probe && b.state == halfOpen))
}

// Release abandons a reservation without attributing an outcome to the provider.
func (t *BreakerTicket) Release() {
	b := t.breaker
	b.mu.Lock()
	defer b.mu.Unlock()
	if t.finished {
		return
	}
	t.finished = true
	if t.probe && t.generation == b.generation && b.state == halfOpen {
		b.state = open
		b.until = b.now()
		b.generation++
	}
}

// Observe must be called only after Fetch and validation, with a live application
// context. Unknown errors release the probe without counting a provider failure.
func (t *BreakerTicket) Observe(err error) {
	var pe *ProviderError
	if err != nil && (!errors.As(err, &pe) || pe == nil) {
		t.Release()
		return
	}
	b := t.breaker
	b.mu.Lock()
	defer b.mu.Unlock()
	if t.finished {
		return
	}
	t.finished = true
	if t.generation != b.generation {
		return
	}
	if t.probe {
		if err == nil {
			b.state = closed
			b.failures = 0
			b.generation++
		} else {
			b.trip()
		}
		return
	}
	if b.state != closed {
		return
	}
	if err == nil || !pe.Retryable {
		b.failures = 0
		return
	}
	b.failures++
	if b.failures >= b.threshold {
		b.trip()
	}
}
func (b *CircuitBreaker) trip() { b.state = open; b.until = b.now().Add(b.pause); b.generation++ }
