package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestEnsureLogIDUsesCandidate(t *testing.T) {
	ctx, logID := EnsureLogID(context.Background(), "", "req_123")
	if logID != "req_123" {
		t.Fatalf("logID = %q, want req_123", logID)
	}
	if got := LogID(ctx); got != "req_123" {
		t.Fatalf("LogID(ctx) = %q, want req_123", got)
	}
}

func TestLoggerIncludesContextFields(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	ctx, _ := EnsureLogID(context.Background(), "req_123")
	ctx = With(ctx, slog.String("agent_id", "agent_1"))

	Info(ctx, "hello", slog.String("event", "test"))

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal log entry: %v\n%s", err, buf.String())
	}
	if entry["log_id"] != "req_123" {
		t.Fatalf("log_id = %v, want req_123", entry["log_id"])
	}
	if entry["agent_id"] != "agent_1" {
		t.Fatalf("agent_id = %v, want agent_1", entry["agent_id"])
	}
	if entry["event"] != "test" {
		t.Fatalf("event = %v, want test", entry["event"])
	}
}
