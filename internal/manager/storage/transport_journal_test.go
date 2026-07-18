package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pax-beehive/paxkit/reliablemq"

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

func TestMemoryReliableTransportJournalOutboundLifecycle(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	ctx := context.Background()

	if _, err := store.AppendOutboundData(
		ctx,
		"queue_1",
		reliablemq.StreamACP,
		json.RawMessage(`{bad-json}`),
		nil,
	); err == nil {
		t.Fatal("append invalid outbound data succeeded")
	}
	frame, err := store.AppendOutboundData(
		ctx,
		"queue_1",
		reliablemq.StreamACP,
		json.RawMessage(`{"method":"session/update"}`),
		reliablemq.Metadata{"agent_id": "agent_1"},
	)
	if err != nil {
		t.Fatalf("append outbound data: %v", err)
	}
	if frame.Key.Seq != 1 || frame.Status != reliablemq.StatusPending {
		t.Fatalf("first frame = %+v", frame)
	}
	tombstone, err := store.AppendOutboundTombstone(
		ctx,
		"queue_1",
		reliablemq.StreamACP,
		"closed",
		reliablemq.Metadata{"agent_id": "agent_1"},
	)
	if err != nil {
		t.Fatalf("append tombstone: %v", err)
	}
	if tombstone.Key.Seq != 2 || tombstone.Kind != reliablemq.FrameKindTombstone ||
		tombstone.ErrorMessage != "closed" {
		t.Fatalf("tombstone = %+v", tombstone)
	}

	now = now.Add(time.Minute)
	if err := store.MarkSent(ctx, frame.Key); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	if err := store.RecordSendFailure(ctx, frame.Key, "network failed"); err != nil {
		t.Fatalf("record send failure: %v", err)
	}
	replay, err := store.ListOutboundReplay(ctx, "queue_1", reliablemq.StreamACP, 10)
	if err != nil {
		t.Fatalf("list outbound replay: %v", err)
	}
	if len(replay) != 2 || replay[0].Status != reliablemq.StatusSent ||
		replay[0].ErrorMessage != "network failed" {
		t.Fatalf("outbound replay = %+v", replay)
	}
}

func TestMemoryReliableTransportJournalSeqSurvivesSweptRows(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	ctx := context.Background()

	first, err := store.AppendOutboundData(
		ctx,
		"queue_1",
		reliablemq.StreamACP,
		json.RawMessage(`{"n":1}`),
		nil,
	)
	if err != nil {
		t.Fatalf("append first: %v", err)
	}
	second, err := store.AppendOutboundData(
		ctx,
		"queue_1",
		reliablemq.StreamACP,
		json.RawMessage(`{"n":2}`),
		nil,
	)
	if err != nil {
		t.Fatalf("append second: %v", err)
	}
	if first.Key.Seq != 1 || second.Key.Seq != 2 {
		t.Fatalf("initial seqs = %d,%d", first.Key.Seq, second.Key.Seq)
	}
	store.mu.Lock()
	for key := range store.transportJournal {
		if key.QueueID == "queue_1" && key.Stream == string(reliablemq.StreamACP) &&
			key.Direction == string(reliablemq.DirectionOutbound) {
			delete(store.transportJournal, key)
		}
	}
	store.mu.Unlock()

	third, err := store.AppendOutboundData(
		ctx,
		"queue_1",
		reliablemq.StreamACP,
		json.RawMessage(`{"n":3}`),
		nil,
	)
	if err != nil {
		t.Fatalf("append third: %v", err)
	}
	if third.Key.Seq != 3 {
		t.Fatalf("third seq = %d, want 3", third.Key.Seq)
	}
}

func TestMemoryReliableTransportJournalInboundLifecycle(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	ctx := context.Background()

	now = now.Add(time.Minute)
	inbound := reliablemq.Frame{
		Key: reliablemq.FrameKey{
			QueueID:   "queue_1",
			Stream:    reliablemq.StreamACP,
			Seq:       1,
			Direction: reliablemq.DirectionInbound,
		},
		Kind:     reliablemq.FrameKindData,
		Payload:  json.RawMessage(`{"result":true}`),
		Metadata: reliablemq.Metadata{"agent_id": "agent_1"},
	}
	inserted, stored, err := store.SaveInboundIfAbsent(ctx, inbound)
	if err != nil {
		t.Fatalf("save inbound: %v", err)
	}
	if !inserted || stored.Status != reliablemq.StatusReceived {
		t.Fatalf("stored inbound inserted=%v frame=%+v", inserted, stored)
	}
	inserted, stored, err = store.SaveInboundIfAbsent(ctx, inbound)
	if err != nil {
		t.Fatalf("save duplicate inbound: %v", err)
	}
	if inserted || stored.Status != reliablemq.StatusReceived {
		t.Fatalf("duplicate inserted=%v frame=%+v", inserted, stored)
	}
	if err := store.MarkApplied(ctx, inbound.Key); err != nil {
		t.Fatalf("mark applied: %v", err)
	}
	inboundReplay, err := store.ListInboundReplay(ctx, "queue_1", reliablemq.StreamACP, 10)
	if err != nil {
		t.Fatalf("list inbound replay: %v", err)
	}
	if len(inboundReplay) != 0 {
		t.Fatalf("applied inbound replay = %+v, want none", inboundReplay)
	}
}

func TestMemoryReliableTransportJournalAppliedInboundSurvivesSweptRow(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	ctx := context.Background()
	inbound := reliablemq.Frame{
		Key: reliablemq.FrameKey{
			QueueID:   "queue_1",
			Stream:    reliablemq.StreamACP,
			Seq:       1,
			Direction: reliablemq.DirectionInbound,
		},
		Kind:     reliablemq.FrameKindData,
		Payload:  json.RawMessage(`{"result":true}`),
		Metadata: reliablemq.Metadata{"agent_id": "agent_1"},
	}
	inserted, stored, err := store.SaveInboundIfAbsent(ctx, inbound)
	if err != nil || !inserted {
		t.Fatalf("save inbound inserted=%v frame=%+v err=%v", inserted, stored, err)
	}
	if err := store.MarkApplied(ctx, inbound.Key); err != nil {
		t.Fatalf("mark applied: %v", err)
	}
	store.mu.Lock()
	delete(store.transportJournal, makeReliableTransportFrameKey(inbound.Key))
	store.mu.Unlock()

	inserted, stored, err = store.SaveInboundIfAbsent(ctx, inbound)
	if err != nil {
		t.Fatalf("save swept duplicate: %v", err)
	}
	if inserted || stored.Status != reliablemq.StatusApplied {
		t.Fatalf("swept duplicate inserted=%v frame=%+v", inserted, stored)
	}
}

func TestMemoryReliableTransportJournalFailureAndMetadataUpdates(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	ctx := context.Background()

	frame, err := store.AppendOutboundData(
		ctx,
		"queue_1",
		reliablemq.StreamACP,
		json.RawMessage(`{"method":"session/update"}`),
		reliablemq.Metadata{"agent_id": "agent_1"},
	)
	if err != nil {
		t.Fatalf("append outbound data: %v", err)
	}
	now = now.Add(time.Minute)
	if err := store.UpdateMetadata(ctx, frame.Key, reliablemq.Metadata{
		"agent_id": "agent_2",
		"trace_id": "trace_1",
	}); err != nil {
		t.Fatalf("update metadata: %v", err)
	}
	updated, err := store.GetTransportFrame(
		ctx,
		"queue_1",
		domain.TransportStreamACP,
		frame.Key.Seq,
		domain.TransportDirectionOutbound,
	)
	if err != nil {
		t.Fatalf("get updated frame: %v", err)
	}
	if updated.Metadata["trace_id"] != "trace_1" || updated.AgentID != "agent_2" {
		t.Fatalf("updated frame = %+v", updated)
	}
	if err := store.RecordDispatchFailure(ctx, frame.Key, "dispatch failed"); err != nil {
		t.Fatalf("record dispatch failure: %v", err)
	}
	updated, err = store.GetTransportFrame(
		ctx,
		"queue_1",
		domain.TransportStreamACP,
		frame.Key.Seq,
		domain.TransportDirectionOutbound,
	)
	if err != nil {
		t.Fatalf("get failed frame: %v", err)
	}
	if updated.ErrorMessage != "dispatch failed" {
		t.Fatalf("dispatch error = %+v", updated)
	}
	if err := store.UpdateTransportFrameStatus(
		ctx,
		"queue_1",
		domain.TransportStreamACP,
		frame.Key.Seq,
		domain.TransportDirectionOutbound,
		domain.TransportStatusRejected,
		"rejected by peer",
	); err != nil {
		t.Fatalf("update compat status: %v", err)
	}
	updated, err = store.GetTransportFrame(
		ctx,
		"queue_1",
		domain.TransportStreamACP,
		frame.Key.Seq,
		domain.TransportDirectionOutbound,
	)
	if err != nil {
		t.Fatalf("get rejected frame: %v", err)
	}
	if updated.Status != domain.TransportStatusRejected ||
		updated.ErrorMessage != "rejected by peer" {
		t.Fatalf("rejected frame = %+v", updated)
	}
	if err := store.MarkRejected(ctx, frame.Key, "rejected again"); err != nil {
		t.Fatalf("mark rejected: %v", err)
	}
}

func TestTransportFrameReliableConversionPreservesCompatibilityFields(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	frame := TransportFrame{
		AgentID:        "agent_1",
		Stream:         domain.TransportStreamManagerToPaxd,
		Seq:            5,
		LocalDirection: domain.TransportDirectionOutbound,
		PayloadJSON:    json.RawMessage(`{"id":1}`),
		Metadata:       map[string]string{"purpose": "test"},
		UpdatedAt:      now,
	}
	reliableFrame, err := transportFrameToReliable(&frame)
	if err != nil {
		t.Fatalf("convert to reliable: %v", err)
	}
	if reliableFrame.Key.QueueID != "agent_1" ||
		reliableFrame.Key.Stream != reliablemq.StreamACP ||
		reliableFrame.Status != reliablemq.StatusPending ||
		reliableFrame.Metadata["agent_id"] != "agent_1" {
		t.Fatalf("reliable frame = %+v", reliableFrame)
	}

	compat := transportFrameFromReliable(reliableFrame, "")
	if compat.AgentID != "agent_1" ||
		compat.QueueID != "agent_1" ||
		compat.Direction != domain.TransportDirectionOutbound ||
		compat.LocalDirection != domain.TransportDirectionOutbound ||
		string(compat.PayloadJSON) != `{"id":1}` {
		t.Fatalf("compat frame = %+v", compat)
	}

	if _, err := transportFrameToReliable(nil); err == nil {
		t.Fatal("nil transport frame converted without error")
	}
}

func TestScanReliableFrameAndHelpers(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	updated := now.Add(time.Minute)
	frame, err := scanReliableFrame(fakeRow{
		int64(9),
		"queue_1",
		"agent_1",
		string(reliablemq.StreamACP),
		int64(12),
		string(reliablemq.DirectionInbound),
		string(reliablemq.FrameKindData),
		[]byte(`{"result":true}`),
		[]byte(`{"trace_id":"trace_1"}`),
		string(reliablemq.StatusReceived),
		"",
		now,
		updated,
	})
	if err != nil {
		t.Fatalf("scan reliable frame: %v", err)
	}
	if frame.Key.QueueID != "queue_1" ||
		frame.Key.Seq != 12 ||
		frame.Metadata["agent_id"] != "agent_1" ||
		frame.Metadata["trace_id"] != "trace_1" ||
		string(frame.Payload) != `{"result":true}` {
		t.Fatalf("frame = %+v", frame)
	}

	emptyPayload, err := scanReliableFrame(fakeRow{
		int64(10),
		"queue_2",
		"",
		string(reliablemq.StreamACP),
		int64(1),
		string(reliablemq.DirectionOutbound),
		string(reliablemq.FrameKindTombstone),
		[]byte(`{}`),
		[]byte(`{}`),
		string(reliablemq.StatusAcked),
		"closed",
		now,
		updated,
	})
	if err != nil {
		t.Fatalf("scan empty reliable frame: %v", err)
	}
	if len(emptyPayload.Payload) != 0 ||
		emptyPayload.Metadata == nil ||
		emptyPayload.ErrorMessage != "closed" {
		t.Fatalf("empty payload frame = %+v", emptyPayload)
	}

	if reliableFrameAgentID(frame) != "agent_1" {
		t.Fatalf("agent id = %q", reliableFrameAgentID(frame))
	}
	if reliableMetadataAgentID(nil, "queue_1") != "queue_1" {
		t.Fatal("metadata fallback was not used")
	}
	if string(mustMarshalReliableMetadata(nil)) != `{}` {
		t.Fatal("nil metadata did not marshal to empty object")
	}
	if nullablePayload(nil) != nil {
		t.Fatal("empty payload did not map to nil")
	}
	if string(nullablePayload(json.RawMessage(`{"ok":true}`)).(json.RawMessage)) != `{"ok":true}` {
		t.Fatal("non-empty payload was not preserved")
	}
}

func TestReliableTimestampHelpers(t *testing.T) {
	created := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	updated := created.Add(time.Minute)
	frame := reliablemq.Frame{
		Status:    reliablemq.StatusSent,
		CreatedAt: created,
		UpdatedAt: updated,
	}
	if got := transportTimestamp(frame, reliablemq.StatusAcked); got != nil {
		t.Fatalf("mismatched timestamp = %v, want nil", got)
	}
	if got := transportTimestamp(frame, reliablemq.StatusSent); got == nil || !got.Equal(updated) {
		t.Fatalf("updated timestamp = %v, want %v", got, updated)
	}
	frame.UpdatedAt = time.Time{}
	if got := transportTimestamp(frame, reliablemq.StatusSent); got == nil || !got.Equal(created) {
		t.Fatalf("created timestamp = %v, want %v", got, created)
	}

	cases := map[reliablemq.Status]string{
		reliablemq.StatusSent:     "sent_at",
		reliablemq.StatusAcked:    "acked_at",
		reliablemq.StatusReceived: "received_at",
		reliablemq.StatusApplied:  "applied_at",
		reliablemq.StatusPending:  "",
	}
	for status, want := range cases {
		if got := timestampColumnForReliableStatus(status); got != want {
			t.Fatalf("status %q column = %q, want %q", status, got, want)
		}
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
