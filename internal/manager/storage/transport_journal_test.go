package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestMemoryTransportJournalDecisionTables(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	ctx := context.Background()

	for _, frame := range []TransportFrame{
		testTransportFrame("agent-1", domain.TransportStreamManagerToPaxd, 1, domain.TransportDirectionOutbound, domain.TransportStatusPending),
		testTransportFrame("agent-1", domain.TransportStreamManagerToPaxd, 2, domain.TransportDirectionOutbound, domain.TransportStatusSent),
		testTransportFrame("agent-1", domain.TransportStreamManagerToPaxd, 3, domain.TransportDirectionOutbound, domain.TransportStatusSent),
		testTransportFrame("agent-1", domain.TransportStreamPaxdToManager, 1, domain.TransportDirectionInbound, domain.TransportStatusReceived),
		testTransportFrame("agent-2", domain.TransportStreamManagerToPaxd, 1, domain.TransportDirectionOutbound, domain.TransportStatusSent),
	} {
		frame := frame
		if err := store.SaveTransportFrame(ctx, &frame); err != nil {
			t.Fatalf("save frame %+v: %v", frame, err)
		}
	}

	if err := store.AckOutboundTransportFrames(ctx, "agent-1", domain.TransportStreamManagerToPaxd, 2); err != nil {
		t.Fatalf("ack frames: %v", err)
	}
	cases := []struct {
		name      string
		agentID   string
		stream    string
		seq       int64
		direction string
		want      string
	}{
		{
			"acks matching outbound seq 1",
			"agent-1",
			domain.TransportStreamManagerToPaxd,
			1,
			domain.TransportDirectionOutbound,
			domain.TransportStatusAcked,
		},
		{
			"acks matching outbound seq 2",
			"agent-1",
			domain.TransportStreamManagerToPaxd,
			2,
			domain.TransportDirectionOutbound,
			domain.TransportStatusAcked,
		},
		{
			"leaves higher seq sent",
			"agent-1",
			domain.TransportStreamManagerToPaxd,
			3,
			domain.TransportDirectionOutbound,
			domain.TransportStatusSent,
		},
		{
			"leaves inbound received",
			"agent-1",
			domain.TransportStreamPaxdToManager,
			1,
			domain.TransportDirectionInbound,
			domain.TransportStatusReceived,
		},
		{
			"leaves other agent sent",
			"agent-2",
			domain.TransportStreamManagerToPaxd,
			1,
			domain.TransportDirectionOutbound,
			domain.TransportStatusSent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame, err := store.GetTransportFrame(ctx, tc.agentID, tc.stream, tc.seq, tc.direction)
			if err != nil {
				t.Fatalf("get frame: %v", err)
			}
			if frame == nil || frame.Status != tc.want {
				t.Fatalf("status = %+v, want %s", frame, tc.want)
			}
		})
	}
}

func TestMemoryTransportJournalSeqScopesAndDuplicateInbound(t *testing.T) {
	store := NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	})
	ctx := context.Background()

	frame := testTransportFrame(
		"agent-1",
		domain.TransportStreamPaxdToManager,
		1,
		domain.TransportDirectionInbound,
		"",
	)
	inserted, err := store.SaveTransportFrameIfAbsent(ctx, &frame)
	if err != nil {
		t.Fatalf("save first inbound: %v", err)
	}
	if !inserted || frame.Status != domain.TransportStatusReceived || frame.ReceivedAt == nil {
		t.Fatalf("first inbound inserted=%v frame=%+v", inserted, frame)
	}
	duplicate := testTransportFrame(
		"agent-1",
		domain.TransportStreamPaxdToManager,
		1,
		domain.TransportDirectionInbound,
		"",
	)
	inserted, err = store.SaveTransportFrameIfAbsent(ctx, &duplicate)
	if err != nil {
		t.Fatalf("save duplicate inbound: %v", err)
	}
	if inserted {
		t.Fatal("duplicate inbound inserted")
	}

	outbound := testTransportFrame(
		"agent-1",
		domain.TransportStreamManagerToPaxd,
		1,
		domain.TransportDirectionOutbound,
		"",
	)
	if err := store.SaveTransportFrame(ctx, &outbound); err != nil {
		t.Fatalf("save outbound: %v", err)
	}
	next, err := store.NextTransportSeq(
		ctx,
		"agent-1",
		domain.TransportStreamManagerToPaxd,
		domain.TransportDirectionOutbound,
	)
	if err != nil {
		t.Fatalf("next outbound seq: %v", err)
	}
	if next != 2 {
		t.Fatalf("next outbound seq = %d, want 2", next)
	}
	next, err = store.NextTransportSeq(
		ctx,
		"agent-1",
		domain.TransportStreamPaxdToManager,
		domain.TransportDirectionInbound,
	)
	if err != nil {
		t.Fatalf("next inbound seq: %v", err)
	}
	if next != 2 {
		t.Fatalf("next inbound seq = %d, want 2", next)
	}
	next, err = store.NextTransportSeq(
		ctx,
		"agent-2",
		domain.TransportStreamManagerToPaxd,
		domain.TransportDirectionOutbound,
	)
	if err != nil {
		t.Fatalf("next other agent seq: %v", err)
	}
	if next != 1 {
		t.Fatalf("next other agent seq = %d, want 1", next)
	}
}

func TestMemoryTransportJournalCleanupDecisionTable(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	ctx := context.Background()

	old := now.Add(-2 * time.Hour)
	frames := []TransportFrame{
		testTransportFrame(
			"agent-1",
			domain.TransportStreamManagerToPaxd,
			1,
			domain.TransportDirectionOutbound,
			domain.TransportStatusAcked,
		),
		testTransportFrame(
			"agent-1",
			domain.TransportStreamPaxdToManager,
			1,
			domain.TransportDirectionInbound,
			domain.TransportStatusApplied,
		),
		testTransportFrame(
			"agent-1",
			domain.TransportStreamManagerToPaxd,
			2,
			domain.TransportDirectionOutbound,
			domain.TransportStatusSent,
		),
		testTransportFrame(
			"agent-1",
			domain.TransportStreamPaxdToManager,
			2,
			domain.TransportDirectionInbound,
			domain.TransportStatusReceived,
		),
	}
	for i := range frames {
		frames[i].CreatedAt = old
		frames[i].UpdatedAt = old
		if err := store.SaveTransportFrame(ctx, &frames[i]); err != nil {
			t.Fatalf("save frame: %v", err)
		}
	}
	deleted, err := store.DeleteCompletedTransportFrames(ctx, now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2", deleted)
	}
	remaining, err := store.ListTransportFrames(
		ctx,
		"agent-1",
		domain.TransportStreamManagerToPaxd,
		domain.TransportDirectionOutbound,
		nil,
		100,
	)
	if err != nil {
		t.Fatalf("list outbound: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Seq != 2 {
		t.Fatalf("remaining outbound = %+v", remaining)
	}
	remaining, err = store.ListTransportFrames(
		ctx,
		"agent-1",
		domain.TransportStreamPaxdToManager,
		domain.TransportDirectionInbound,
		nil,
		100,
	)
	if err != nil {
		t.Fatalf("list inbound: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Seq != 2 {
		t.Fatalf("remaining inbound = %+v", remaining)
	}
}

func testTransportFrame(
	agentID, stream string,
	seq int64,
	direction string,
	status string,
) TransportFrame {
	payload := json.RawMessage(`{"jsonrpc":"2.0","id":1}`)
	return TransportFrame{
		AgentID:        agentID,
		Stream:         stream,
		Seq:            seq,
		LocalDirection: direction,
		PayloadJSON:    payload,
		Status:         status,
	}
}
