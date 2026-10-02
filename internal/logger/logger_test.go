package logger_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/config"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/logger"
)

func records(t *testing.T, output *bytes.Buffer) []map[string]any {
	t.Helper()
	var result []map[string]any
	decoder := json.NewDecoder(output)
	for {
		var record map[string]any
		if err := decoder.Decode(&record); err == io.EOF {
			return result
		} else if err != nil {
			t.Fatal(err)
		}
		if _, err := time.Parse(time.RFC3339Nano, record["time"].(string)); err != nil {
			t.Fatalf("invalid timestamp: %v", err)
		}
		result = append(result, record)
	}
}

func TestJSONAndLevelFiltering(t *testing.T) {
	for _, tc := range []struct {
		level slog.Level
		want  []string
	}{
		{slog.LevelDebug, []string{"DEBUG", "INFO", "WARN", "ERROR"}},
		{slog.LevelInfo, []string{"INFO", "WARN", "ERROR"}},
		{slog.LevelWarn, []string{"WARN", "ERROR"}},
		{slog.LevelError, []string{"ERROR"}},
	} {
		t.Run(tc.level.String(), func(t *testing.T) {
			var output bytes.Buffer
			log := logger.New(tc.level, "quotes", &output)
			log.Debug("debug message")
			log.Info("info message")
			log.Warn("warn message")
			log.Error("ошибка\nс кавычками: \"quoted\"", "attempt", 2, slog.String("pair", "EUR/MXN"))
			got := records(t, &output)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d records, want %d", len(got), len(tc.want))
			}
			for i, record := range got {
				if record["level"] != tc.want[i] || record["service"] != "quotes" {
					t.Fatalf("unexpected record: %v", record)
				}
			}
			last := got[len(got)-1]
			if last["msg"] != "ошибка\nс кавычками: \"quoted\"" || last["attempt"] != float64(2) || last["pair"] != "EUR/MXN" {
				t.Fatalf("lost attributes: %v", last)
			}
		})
	}
}

func TestWithDoesNotChangeParentOrSiblings(t *testing.T) {
	var output bytes.Buffer
	base := logger.New(slog.LevelInfo, "quotes", &output)
	worker := base.With("component", "worker")
	http := base.With("component", "http")
	worker.With("update_id", "test-id").Info("started")
	worker.Info("idle")
	http.Info("request")
	base.Info("ready")
	got := records(t, &output)
	if len(got) != 4 {
		t.Fatalf("got %d records", len(got))
	}
	if got[0]["component"] != "worker" || got[0]["update_id"] != "test-id" {
		t.Fatal(got[0])
	}
	if got[1]["component"] != "worker" || got[1]["update_id"] != nil {
		t.Fatal(got[1])
	}
	if got[2]["component"] != "http" || got[2]["update_id"] != nil {
		t.Fatal(got[2])
	}
	if got[3]["component"] != nil || got[3]["update_id"] != nil {
		t.Fatal(got[3])
	}
}

func TestConcurrentChildren(t *testing.T) {
	var output bytes.Buffer
	base := logger.New(slog.LevelInfo, "quotes", &output)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			base.With("worker", i).Info("ready")
		}(i)
	}
	wg.Wait()
	got := records(t, &output)
	seen := make(map[float64]bool)
	for _, record := range got {
		seen[record["worker"].(float64)] = true
	}
	if len(got) != 20 || len(seen) != 20 {
		t.Fatal("lost or duplicated records")
	}
}

func TestOptionalServiceAndDefaultWriter(t *testing.T) {
	var output bytes.Buffer
	logger.New(slog.LevelInfo, "", &output).Info("ready")
	if _, ok := records(t, &output)[0]["service"]; ok {
		t.Fatal("unexpected service field")
	}
	// Constructing a stdout logger does not emit a record or alter global slog.
	before := slog.Default()
	if logger.New(slog.LevelInfo, "quotes", nil) == nil || slog.Default() != before {
		t.Fatal("unexpected global logger change")
	}
}

func ExampleNew() {
	// In cmd/quotes, cfg will come from config.Load. Tests can supply a buffer.
	cfg := config.Config{LogLevel: slog.LevelInfo}
	var output bytes.Buffer
	log := logger.New(cfg.LogLevel, "quotes", &output)
	log.With("component", "worker").Info("started")
	var record map[string]any
	_ = json.Unmarshal(output.Bytes(), &record)
	fmt.Println(record["level"], record["service"], record["component"], record["msg"])
	// Output: INFO quotes worker started
}
