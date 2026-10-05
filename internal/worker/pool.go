// Package worker runs bounded processing and recovery loops without an in-memory job queue.
package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/logger"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
)

type Processor interface {
	Process(context.Context) (usecase.ProcessResult, error)
}
type Recoverer interface {
	Recover(context.Context) (repository.RecoveryResult, error)
}

type PoolConfig struct {
	Count            int
	PollInterval     time.Duration
	RecoveryInterval time.Duration
}

type Pool struct {
	processor Processor
	recovery  Recoverer
	log       logger.Logger
	config    PoolConfig
}

func New(processor Processor, recovery Recoverer, log logger.Logger, cfg PoolConfig) (*Pool, error) {
	if processor == nil || recovery == nil || log == nil || cfg.Count <= 0 || cfg.PollInterval <= 0 || cfg.RecoveryInterval <= 0 {
		return nil, errors.New("worker: invalid dependencies or configuration")
	}
	return &Pool{processor, recovery, log, cfg}, nil
}

// Run blocks until all loops finish. Call once per pool. Cancel stop to stop
// admitting new iterations and drain active calls; cancel work to interrupt them.
// A call already entering Process when stop is canceled is considered in flight.
func (p *Pool) Run(work, stop context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < p.config.Count; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); p.processLoop(work, stop) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); p.recoveryLoop(work, stop) }()
	wg.Wait()
}

func (p *Pool) processLoop(work, stop context.Context) {
	backoff := time.Duration(0)
	for running(work, stop) {
		result, err := p.processor.Process(work)
		if work.Err() != nil {
			return
		}
		delay := time.Duration(0)
		if err != nil {
			backoff = nextBackoff(backoff, p.config.PollInterval)
			delay = backoff
			// Raw errors can contain credentials or provider bodies. Log stable fields.
			p.log.Error("worker iteration failed", attemptFields(result)...)
		} else {
			backoff = 0
			switch result.Outcome {
			case usecase.ProcessDone:
				p.log.Info("worker attempt finished", attemptFields(result)...)
			case usecase.ProcessIdle:
				delay = p.config.PollInterval
			case usecase.ProcessWait:
				delay = result.Wait
				if delay <= 0 {
					delay = p.config.PollInterval
				}
			default:
				delay = p.config.PollInterval
				p.log.Error("worker received invalid outcome")
			}
		}
		if !wait(work, stop, delay) {
			return
		}
	}
}

func (p *Pool) recoveryLoop(work, stop context.Context) {
	backoff := time.Duration(0)
	// Recover immediately at startup, then wait between bounded calls.
	for running(work, stop) {
		result, err := p.recovery.Recover(work)
		if work.Err() != nil {
			return
		}
		delay := p.config.RecoveryInterval
		if err != nil {
			backoff = nextBackoff(backoff, p.config.RecoveryInterval)
			delay = backoff
			p.log.Error("recovery iteration failed")
		} else {
			backoff = 0
			if result.Requeued+result.Failed > 0 {
				p.log.Info("recovery finished", "requeued", result.Requeued, "failed", result.Failed)
			}
		}
		if !wait(work, stop, delay) {
			return
		}
	}
}

func attemptFields(r usecase.ProcessResult) []any {
	outcome := "error"
	switch r.Outcome {
	case usecase.ProcessDone:
		outcome = "processed"
	case usecase.ProcessIdle:
		outcome = "idle"
	case usecase.ProcessWait:
		outcome = "waiting"
	}
	if r.Attempt == nil {
		return []any{"outcome", outcome}
	}
	return []any{"outcome", outcome, "update_id", r.Attempt.ID.String(), "pair", r.Attempt.Pair.String(), "attempt", r.Attempt.Number}
}
func running(work, stop context.Context) bool { return work.Err() == nil && stop.Err() == nil }
func wait(work, stop context.Context, d time.Duration) bool {
	if !running(work, stop) {
		return false
	}
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-work.Done():
		return false
	case <-stop.Done():
		return false
	case <-timer.C:
		return running(work, stop)
	}
}

// Infrastructure failures back off from the configured polling interval to 30s.
func nextBackoff(previous, base time.Duration) time.Duration {
	const limit = 30 * time.Second
	if previous == 0 {
		return min(base, limit)
	}
	if previous >= limit/2 {
		return limit
	}
	return previous * 2
}
