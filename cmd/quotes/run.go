package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/config"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/logger"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/provider/frankfurter"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository/postgres"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/transport/httpapi"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/worker"
)

func run(ctx context.Context, cfg config.Config, log logger.Logger) error {
	db, err := postgres.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	repo := postgres.New(db, cfg.Database.OperationTimeout, cfg.Worker.MaxPending)
	if err = repo.CheckSchema(ctx); err != nil {
		return err
	}
	provider, err := frankfurter.New(cfg.Provider.BaseURL, cfg.Provider.HTTPTimeout)
	if err != nil {
		return err
	}
	policy, err := usecase.NewRetryPolicy(cfg.Worker.MaxAttempts, cfg.Worker.RetryBaseDelay, nil)
	if err != nil {
		return err
	}
	failures, err := usecase.NewFailureHandler(repo, repo, policy, "frankfurter")
	if err != nil {
		return err
	}
	processor, err := usecase.NewProcessor(repo, repo, provider, repo, failures, usecase.ProcessorConfig{
		Provider: "frankfurter", RequestsPerSecond: cfg.Provider.RequestsPerSecond, MaxAttempts: cfg.Worker.MaxAttempts, LeaseDuration: cfg.Worker.LeaseDuration, PollInterval: cfg.Worker.PollInterval,
	})
	if err != nil {
		return err
	}
	recovery, err := usecase.NewRecovery(repo, cfg.Worker.MaxAttempts, cfg.Worker.RetryBaseDelay)
	if err != nil {
		return err
	}
	pool, err := worker.New(processor, recovery, log, worker.PoolConfig{Count: cfg.Worker.Count, PollInterval: cfg.Worker.PollInterval, RecoveryInterval: cfg.Worker.RecoveryInterval})
	if err != nil {
		return err
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", cfg.HTTP.Addr)
	if err != nil {
		return errors.New("http: cannot bind configured address")
	}
	defer listener.Close()
	var stopping atomic.Bool
	api := httpapi.New(usecase.NewService(repo, repo), log).Routes()
	handler := healthRoutes(api, db.PingContext, &stopping, cfg.Database.OperationTimeout)
	return serve(ctx, cfg, listener, handler, pool, &stopping, log)
}

type backgroundRunner interface {
	Run(work, stop context.Context)
}

// The signal context triggers draining; it does not own active HTTP/worker work.
func serve(signalCtx context.Context, cfg config.Config, listener net.Listener, handler http.Handler, pool backgroundRunner, stopping *atomic.Bool, log logger.Logger) error {
	work, cancelWork := context.WithCancel(context.Background())
	defer cancelWork()
	stop, cancelStop := context.WithCancel(context.Background())
	defer cancelStop()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout, ReadTimeout: cfg.HTTP.ReadTimeout, WriteTimeout: cfg.HTTP.WriteTimeout, IdleTimeout: cfg.HTTP.IdleTimeout, BaseContext: func(net.Listener) context.Context { return work }}
	if signalCtx.Err() != nil {
		return nil
	}
	workersDone := make(chan struct{})
	go func() { pool.Run(work, stop); close(workersDone) }()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	log.Info("application started", "address", listener.Addr().String(), "workers", cfg.Worker.Count)
	var serveErr error
	select {
	case <-signalCtx.Done():
	case serveErr = <-serverDone:
	}
	stopping.Store(true)
	cancelStop()
	log.Info("application stopping")
	// Both HTTP and worker drain share the same deadline, starting now.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancelShutdown()
	httpDone := make(chan error, 1)
	go func() { httpDone <- server.Shutdown(shutdownCtx) }()
	workerWait := workersDone
	for workerWait != nil || httpDone != nil {
		select {
		case <-workerWait:
			workerWait = nil
		case err := <-httpDone:
			httpDone = nil
			if err != nil {
				log.Warn("HTTP drain incomplete; canceling active work")
				cancelWork()
				_ = server.Close()
			}
		case <-shutdownCtx.Done():
			log.Warn("shutdown deadline reached; canceling active work")
			cancelWork()
			_ = server.Close()
			if workerWait != nil {
				<-workerWait
				workerWait = nil
			}
			if httpDone != nil {
				<-httpDone
				httpDone = nil
			}
		}
	}
	cancelWork()
	// Serve has exited after Shutdown/Close. If it was consumed above, no wait.
	if serveErr == nil {
		serveErr = <-serverDone
	}
	log.Info("application stopped")
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return errors.New("http: server stopped unexpectedly")
	}
	return nil
}
