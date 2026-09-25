package logger

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"info", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"nonsense", slog.LevelInfo},
	}
	for _, tt := range tests {
		if got := ParseLevel(tt.in); got != tt.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestNew_WritesJSON(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "info")
	log.Info("hello", "key", "value")

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, buf.String())
	}
	if record["msg"] != "hello" {
		t.Errorf("msg = %v, want %q", record["msg"], "hello")
	}
	if record["key"] != "value" {
		t.Errorf("key = %v, want %q", record["key"], "value")
	}
}

func TestNew_RespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "warn")
	log.Info("should be dropped")
	log.Warn("should appear")

	out := buf.String()
	if strings.Contains(out, "should be dropped") {
		t.Errorf("info line was logged despite warn level: %s", out)
	}
	if !strings.Contains(out, "should appear") {
		t.Errorf("warn line missing from output: %s", out)
	}
}
