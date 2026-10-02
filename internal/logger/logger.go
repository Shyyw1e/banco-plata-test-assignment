// Package logger provides structured application logging backed by log/slog.
package logger

import (
	"io"
	"log/slog"
	"os"
)

type Logger interface {
	Debug(msg string, kv ...any)
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Error(msg string, kv ...any)
	With(kv ...any) Logger
}

type slogLogger struct {
	inner *slog.Logger
}

var _ Logger = (*slogLogger)(nil)

func New(level slog.Level, service string, output io.Writer) Logger {
	if output == nil {
		output = os.Stdout
	}
	base := slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level}))
	if service != "" {
		base = base.With("service", service)
	}
	return &slogLogger{inner: base}
}

func (l *slogLogger) Debug(msg string, kv ...any) { l.inner.Debug(msg, kv...) }
func (l *slogLogger) Info(msg string, kv ...any)  { l.inner.Info(msg, kv...) }
func (l *slogLogger) Warn(msg string, kv ...any)  { l.inner.Warn(msg, kv...) }
func (l *slogLogger) Error(msg string, kv ...any) { l.inner.Error(msg, kv...) }

func (l *slogLogger) With(kv ...any) Logger {
	return &slogLogger{inner: l.inner.With(kv...)}
}
