package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	HTTP            HTTPConfig
	Database        DatabaseConfig
	Worker          WorkerConfig
	Provider        ProviderConfig
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
}

type HTTPConfig struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

type DatabaseConfig struct {
	URL              string
	MaxOpenConns     int
	MaxIdleConns     int
	ConnMaxLifetime  time.Duration
	ConnMaxIdleTime  time.Duration
	OperationTimeout time.Duration
}

type WorkerConfig struct {
	Count            int
	PollInterval     time.Duration
	MaxPending       int
	LeaseDuration    time.Duration
	MaxAttempts      int
	RetryBaseDelay   time.Duration
	RecoveryInterval time.Duration
}

type ProviderConfig struct {
	BaseURL                 string
	HTTPTimeout             time.Duration
	RequestsPerSecond       int
	CircuitFailureThreshold int
	CircuitOpenDuration     time.Duration
}

func Load(envFile string) (Config, error) {
	return load(envFile, os.LookupEnv)
}

func load(envFile string, lookup func(string) (string, bool)) (Config, error) {
	fileValues := map[string]string{}
	if envFile != "" {
		file, err := os.Open(envFile)
		if err != nil {
			return Config{}, fmt.Errorf("config: open env file: %w", err)
		}
		defer file.Close()
		fileValues, err = godotenv.Parse(file)
		if err != nil {
			// Parser errors may include a line containing credentials.
			return Config{}, errors.New("config: invalid env file syntax")
		}
	}

	p := parser{lookup: func(key string) (string, bool) {
		if value, ok := lookup(key); ok {
			return value, true
		}
		value, ok := fileValues[key]
		return value, ok
	}}
	cfg := Config{
		HTTP: HTTPConfig{
			Addr:              p.text("HTTP_ADDR", ":8080"),
			ReadHeaderTimeout: p.duration("HTTP_READ_HEADER_TIMEOUT", "5s"),
			ReadTimeout:       p.duration("HTTP_READ_TIMEOUT", "10s"),
			WriteTimeout:      p.duration("HTTP_WRITE_TIMEOUT", "10s"),
			IdleTimeout:       p.duration("HTTP_IDLE_TIMEOUT", "60s"),
		},
		Database: DatabaseConfig{
			URL:              p.text("DATABASE_URL", ""),
			MaxOpenConns:     p.integer("DB_MAX_OPEN_CONNS", "10", 1),
			MaxIdleConns:     p.integer("DB_MAX_IDLE_CONNS", "5", 0),
			ConnMaxLifetime:  p.duration("DB_CONN_MAX_LIFETIME", "30m"),
			ConnMaxIdleTime:  p.duration("DB_CONN_MAX_IDLE_TIME", "5m"),
			OperationTimeout: p.duration("DB_OPERATION_TIMEOUT", "2s"),
		},
		Worker: WorkerConfig{
			Count:            p.integer("WORKER_COUNT", "2", 1),
			PollInterval:     p.duration("QUEUE_POLL_INTERVAL", "500ms"),
			MaxPending:       p.integer("QUEUE_MAX_PENDING", "1000", 1),
			LeaseDuration:    p.duration("JOB_LEASE_DURATION", "30s"),
			MaxAttempts:      p.integer("JOB_MAX_ATTEMPTS", "3", 1),
			RetryBaseDelay:   p.duration("JOB_RETRY_BASE_DELAY", "1s"),
			RecoveryInterval: p.duration("RECOVERY_INTERVAL", "1s"),
		},
		Provider: ProviderConfig{
			BaseURL:                 p.text("PROVIDER_BASE_URL", "https://api.frankfurter.dev/v2/providers/ecb"),
			HTTPTimeout:             p.duration("PROVIDER_HTTP_TIMEOUT", "5s"),
			RequestsPerSecond:       p.integer("PROVIDER_REQUESTS_PER_SECOND", "2", 1),
			CircuitFailureThreshold: p.integer("PROVIDER_CIRCUIT_FAILURE_THRESHOLD", "5", 1),
			CircuitOpenDuration:     p.duration("PROVIDER_CIRCUIT_OPEN_DURATION", "30s"),
		},
		ShutdownTimeout: p.duration("SHUTDOWN_TIMEOUT", "15s"),
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(p.text("LOG_LEVEL", "info"))); err != nil {
		p.errs = append(p.errs, errors.New("config: LOG_LEVEL must be a valid slog level"))
	}
	if err := errors.Join(p.errs...); err != nil {
		return Config{}, err
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	var errs []error
	if _, port, err := net.SplitHostPort(c.HTTP.Addr); err != nil || !validPort(port) {
		errs = append(errs, errors.New("config: HTTP_ADDR must be host:port with a port between 1 and 65535"))
	}
	if c.HTTP.ReadTimeout < c.HTTP.ReadHeaderTimeout {
		errs = append(errs, errors.New("config: HTTP_READ_TIMEOUT must be at least HTTP_READ_HEADER_TIMEOUT"))
	}
	if c.Database.URL == "" {
		errs = append(errs, errors.New("config: DATABASE_URL is required"))
	} else if !validDatabaseURL(c.Database.URL) {
		errs = append(errs, errors.New("config: DATABASE_URL must be a postgres or postgresql URL with a host and database name"))
	}
	if c.Database.MaxIdleConns > c.Database.MaxOpenConns {
		errs = append(errs, errors.New("config: DB_MAX_IDLE_CONNS must not exceed DB_MAX_OPEN_CONNS"))
	}
	if !validProviderURL(c.Provider.BaseURL) {
		errs = append(errs, errors.New("config: PROVIDER_BASE_URL must be an absolute HTTP(S) URL without credentials, query or fragment"))
	}
	// Reserve time for the claim commit and result persistence as well as HTTP.
	// Subtraction avoids overflowing time.Duration for very large input values.
	remaining := c.Worker.LeaseDuration - c.Provider.HTTPTimeout
	if remaining <= 0 || c.Database.OperationTimeout >= remaining ||
		c.Database.OperationTimeout >= remaining-c.Database.OperationTimeout {
		errs = append(errs, errors.New("config: JOB_LEASE_DURATION must exceed PROVIDER_HTTP_TIMEOUT plus two DB_OPERATION_TIMEOUT intervals"))
	}
	return errors.Join(errs...)
}

func validDatabaseURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		u.Hostname() == "" || strings.Trim(u.Path, "/") == "" || u.Fragment != "" {
		return false
	}
	if u.Port() != "" && !validPort(u.Port()) {
		return false
	}
	_, err = url.ParseQuery(u.RawQuery)
	return err == nil
}

func validProviderURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") &&
		u.Hostname() != "" && (u.Port() == "" || validPort(u.Port())) &&
		u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

func validPort(raw string) bool {
	port, err := strconv.ParseUint(raw, 10, 16)
	return err == nil && port > 0
}

type parser struct {
	lookup func(string) (string, bool)
	errs   []error
}

func (p *parser) text(key, fallback string) string {
	if value, ok := p.lookup(key); ok {
		return value
	}
	return fallback
}

func (p *parser) integer(key, fallback string, minimum int) int {
	value, err := strconv.Atoi(p.text(key, fallback))
	if err != nil || value < minimum {
		p.errs = append(p.errs, fmt.Errorf("config: %s must be an integer >= %d", key, minimum))
	}
	return value
}

func (p *parser) duration(key, fallback string) time.Duration {
	value, err := time.ParseDuration(p.text(key, fallback))
	if err != nil || value <= 0 {
		p.errs = append(p.errs, fmt.Errorf("config: %s must be a positive duration such as 500ms or 5s", key))
	}
	return value
}
