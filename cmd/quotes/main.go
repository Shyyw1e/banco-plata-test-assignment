package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/config"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/logger"
)

func main() {
	envFile := flag.String("env-file", "", "optional local env file; process environment takes precedence")
	flag.Parse()
	cfg, err := config.Load(*envFile)
	if err != nil {
		logger.New(0, "quotes", os.Stderr).Error("configuration failed", "error", err.Error())
		os.Exit(1)
	}
	log := logger.New(cfg.LogLevel, "quotes", os.Stdout)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err = run(ctx, cfg, log); err != nil {
		log.Error("application failed", "error", err.Error())
		os.Exit(1)
	}
}
