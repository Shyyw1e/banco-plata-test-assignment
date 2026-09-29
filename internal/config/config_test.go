package config

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

const testDatabaseURL = "postgres://quotes:test_password@localhost:5432/quotes?sslmode=disable"

func env(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func writeEnv(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultsAndExampleAgree(t *testing.T) {
	defaults, err := load("", env(map[string]string{"DATABASE_URL": "postgres://quotes:quotes_local@localhost:5432/quotes?sslmode=disable"}))
	if err != nil {
		t.Fatal(err)
	}
	example, err := load("../../.env.example", env(nil))
	if err != nil {
		t.Fatalf("example configuration is not usable: %v", err)
	}
	if !reflect.DeepEqual(defaults, example) {
		t.Fatal(".env.example and application defaults differ")
	}
	if defaults.Worker.LeaseDuration <= defaults.Provider.HTTPTimeout+2*defaults.Database.OperationTimeout {
		t.Fatal("default lease does not cover the attempt budget")
	}
}

func TestEnvironmentOverridesFileWithoutChangingProcessEnvironment(t *testing.T) {
	const sentinel = "QUOTES_CONFIG_TEST_SENTINEL"
	before, existed := os.LookupEnv(sentinel)
	file := writeEnv(t, "DATABASE_URL='"+testDatabaseURL+"'\nexport WORKER_COUNT=7\nHTTP_ADDR='127.0.0.1:9090'\nLOG_LEVEL=debug\n"+sentinel+"=from_file\n")
	cfg, err := load(file, env(map[string]string{"WORKER_COUNT": "4", "QUEUE_POLL_INTERVAL": "250ms"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Worker.Count != 4 || cfg.Worker.PollInterval != 250*time.Millisecond {
		t.Fatal("environment did not override file and defaults")
	}
	if cfg.HTTP.Addr != "127.0.0.1:9090" || cfg.LogLevel != slog.LevelDebug || cfg.Database.URL != testDatabaseURL {
		t.Fatal("file values were not loaded")
	}
	if after, exists := os.LookupEnv(sentinel); after != before || exists != existed {
		t.Fatal("loading configuration changed the process environment")
	}
}

func TestEmptyEnvironmentValueDoesNotFallBackToFile(t *testing.T) {
	file := writeEnv(t, "DATABASE_URL="+testDatabaseURL+"\n")
	_, err := load(file, env(map[string]string{"DATABASE_URL": ""}))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("expected required-value error, got %v", err)
	}
}

func TestLoadUsesProcessEnvironment(t *testing.T) {
	values, err := godotenv.Read("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	t.Setenv("WORKER_COUNT", "6")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Worker.Count != 6 {
		t.Fatal("Load did not use the process environment")
	}
}

func TestInvalidSettings(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
		want   string
	}{
		{"missing database", map[string]string{"DATABASE_URL": ""}, "DATABASE_URL"},
		{"wrong database scheme", map[string]string{"DATABASE_URL": "https://host/db"}, "DATABASE_URL"},
		{"missing database name", map[string]string{"DATABASE_URL": "postgres://localhost"}, "DATABASE_URL"},
		{"invalid database port", map[string]string{"DATABASE_URL": "postgres://localhost:99999/db"}, "DATABASE_URL"},
		{"invalid database query", map[string]string{"DATABASE_URL": "postgres://localhost/db?password=%zz"}, "DATABASE_URL"},
		{"invalid HTTP address", map[string]string{"HTTP_ADDR": "localhost"}, "HTTP_ADDR"},
		{"out of range HTTP port", map[string]string{"HTTP_ADDR": ":65536"}, "HTTP_ADDR"},
		{"zero workers", map[string]string{"WORKER_COUNT": "0"}, "WORKER_COUNT"},
		{"non integer workers", map[string]string{"WORKER_COUNT": "1.5"}, "WORKER_COUNT"},
		{"unbounded pool", map[string]string{"DB_MAX_OPEN_CONNS": "0"}, "DB_MAX_OPEN_CONNS"},
		{"idle larger than pool", map[string]string{"DB_MAX_IDLE_CONNS": "11"}, "DB_MAX_IDLE_CONNS"},
		{"zero duration", map[string]string{"QUEUE_POLL_INTERVAL": "0s"}, "QUEUE_POLL_INTERVAL"},
		{"negative duration", map[string]string{"SHUTDOWN_TIMEOUT": "-1s"}, "SHUTDOWN_TIMEOUT"},
		{"duration without unit", map[string]string{"PROVIDER_HTTP_TIMEOUT": "5"}, "PROVIDER_HTTP_TIMEOUT"},
		{"empty explicit duration", map[string]string{"JOB_RETRY_BASE_DELAY": ""}, "JOB_RETRY_BASE_DELAY"},
		{"invalid log level", map[string]string{"LOG_LEVEL": "verbose"}, "LOG_LEVEL"},
		{"short read timeout", map[string]string{"HTTP_READ_TIMEOUT": "1s"}, "HTTP_READ_TIMEOUT"},
		{"relative provider URL", map[string]string{"PROVIDER_BASE_URL": "/rates"}, "PROVIDER_BASE_URL"},
		{"provider credentials", map[string]string{"PROVIDER_BASE_URL": "https://user:password@example.com"}, "PROVIDER_BASE_URL"},
		{"provider query", map[string]string{"PROVIDER_BASE_URL": "https://example.com?token=secret"}, "PROVIDER_BASE_URL"},
		{"provider fragment", map[string]string{"PROVIDER_BASE_URL": "https://example.com#rates"}, "PROVIDER_BASE_URL"},
		{"insufficient lease", map[string]string{"JOB_LEASE_DURATION": "8s"}, "JOB_LEASE_DURATION"},
		{"lease without margin", map[string]string{"JOB_LEASE_DURATION": "9s"}, "JOB_LEASE_DURATION"},
		{"lease shorter than HTTP", map[string]string{"PROVIDER_HTTP_TIMEOUT": "40s"}, "JOB_LEASE_DURATION"},
		{"zero provider limit", map[string]string{"PROVIDER_REQUESTS_PER_SECOND": "0"}, "PROVIDER_REQUESTS_PER_SECOND"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := map[string]string{"DATABASE_URL": testDatabaseURL}
			for key, value := range tt.values {
				values[key] = value
			}
			cfg, err := load("", env(values))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error for %s, got %v", tt.want, err)
			}
			if cfg != (Config{}) {
				t.Fatal("an invalid load returned a partially usable configuration")
			}
		})
	}
}

func TestZeroIdleConnectionsAndLocalProviderAreAllowed(t *testing.T) {
	cfg, err := load("", env(map[string]string{
		"DATABASE_URL":      "postgresql://localhost:5432/quotes",
		"DB_MAX_IDLE_CONNS": "0",
		"PROVIDER_BASE_URL": "http://127.0.0.1:8081/rates",
		"HTTP_ADDR":         "[::1]:8080",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.MaxIdleConns != 0 {
		t.Fatal("zero idle connections were replaced with a default")
	}
}

func TestFileErrors(t *testing.T) {
	t.Run("missing explicitly requested file", func(t *testing.T) {
		_, err := load(filepath.Join(t.TempDir(), "missing.env"), env(nil))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected missing-file error, got %v", err)
		}
	})
	t.Run("invalid syntax hides file contents", func(t *testing.T) {
		const secret = "do_not_print_this_password"
		file := writeEnv(t, "DATABASE_URL='"+secret+"\n")
		_, err := load(file, env(nil))
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("expected a sanitized syntax error, got %v", err)
		}
	})
}

func TestErrorsDoNotExposeValues(t *testing.T) {
	const secret = "do_not_print_this_password"
	for _, key := range []string{"DATABASE_URL", "PROVIDER_BASE_URL", "WORKER_COUNT", "LOG_LEVEL", "QUEUE_POLL_INTERVAL"} {
		t.Run(key, func(t *testing.T) {
			values := map[string]string{"DATABASE_URL": testDatabaseURL, key: secret}
			_, err := load("", env(values))
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("error exposed a configuration value")
			}
		})
	}
}

func TestErrorsCollectInvalidSettings(t *testing.T) {
	_, err := load("", env(map[string]string{
		"DATABASE_URL": testDatabaseURL,
		"WORKER_COUNT": "0",
		"LOG_LEVEL":    "invalid",
	}))
	if err == nil || !strings.Contains(err.Error(), "WORKER_COUNT") || !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Fatalf("expected both configuration errors, got %v", err)
	}
}
