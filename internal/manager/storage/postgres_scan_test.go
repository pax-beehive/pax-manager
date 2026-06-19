package storage

import (
	"testing"
	"time"
)

type fakeRow []any

func (r fakeRow) Scan(dest ...any) error {
	for i := range dest {
		switch d := dest[i].(type) {
		case *string:
			*d = r[i].(string)
		case **time.Time:
			if v, ok := r[i].(*time.Time); ok {
				*d = v
			}
		case *time.Time:
			*d = r[i].(time.Time)
		case *[]byte:
			*d = r[i].([]byte)
		}
	}
	return nil
}

func TestScanAgentKeepsLifecycleStatusSeparateFromLiveness(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	agent, err := scanAgent(fakeRow{
		"agent_1",
		"node_1",
		"user_1",
		"codex",
		"workstation",
		"codex",
		"server",
		"linux",
		"0.1.0",
		"http://localhost:8642",
		"pending",
		"online",
		&now,
		now,
		[]byte(`{"ok":true}`),
	})
	if err != nil {
		t.Fatalf("scan agent: %v", err)
	}
	if agent.Status != "pending" {
		t.Fatalf("status = %q, want pending", agent.Status)
	}
	if !agent.Online {
		t.Fatal("online = false, want true")
	}
}
