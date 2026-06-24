package manager

import (
	"encoding/json"
	"testing"
)

func TestACPPendingSessionNewsTracksAndConsumesByRequestID(t *testing.T) {
	var pending acpPendingSessionNews

	pending.track("", "sess_ignored")
	pending.track("req_ignored", "")
	pending.track("req_1", "sess_1")

	sessionID, ok := pending.take("req_1")
	if !ok {
		t.Fatal("expected tracked request to be present")
	}
	if sessionID != "sess_1" {
		t.Fatalf("expected sess_1, got %q", sessionID)
	}
	if _, ok := pending.take("req_1"); ok {
		t.Fatal("expected request to be consumed")
	}
	if _, ok := pending.take(""); ok {
		t.Fatal("expected empty request id to miss")
	}
}

func TestACPHistoryGroupsReuseTextGroupUntilResultBoundary(t *testing.T) {
	var groups acpHistoryGroups
	text := json.RawMessage(
		`{"method":"session/update","params":{"update":{"content":{"text":"hello","type":"text"},"sessionUpdate":"agent_message_chunk"},"sessionId":"sess_1"}}`,
	)
	result := json.RawMessage(`{"id":1,"result":{"stopReason":"end_turn"}}`)

	first := groups.groupID(1, "", text)
	if first != "seq:1" {
		t.Fatalf("expected first group id to use first seq, got %q", first)
	}
	second := groups.groupID(2, "", text)
	if second != first {
		t.Fatalf("expected same text stream to reuse group %q, got %q", first, second)
	}

	groups.observeBoundary(result)

	third := groups.groupID(3, "", text)
	if third != "seq:3" {
		t.Fatalf("expected result boundary to reset group, got %q", third)
	}
}

func TestACPHistoryGroupsIgnoreNonTextProjection(t *testing.T) {
	var groups acpHistoryGroups
	raw := json.RawMessage(
		`{"method":"session/update","params":{"update":{"sessionUpdate":"tool_call"},"sessionId":"sess_1"}}`,
	)

	if groupID := groups.groupID(1, "", raw); groupID != "" {
		t.Fatalf("expected non-text projection to skip group, got %q", groupID)
	}
}
