package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestACPTunnelHubClaimFallsBackToAgentTunnel(t *testing.T) {
	hub := NewACPTunnelHub()
	agentConn := &ACPTunnelAgent{agentID: "agent-1"}
	hub.add("agent-1", "", agentConn)

	got, err := hub.claim("agent-1", "sess-1")
	if err != nil {
		t.Fatalf("claim session tunnel through agent tunnel: %v", err)
	}
	if got != agentConn {
		t.Fatalf("claim returned %#v, want %#v", got, agentConn)
	}
}

func TestACPTunnelHubClaimPrefersExactSessionTunnel(t *testing.T) {
	hub := NewACPTunnelHub()
	agentConn := &ACPTunnelAgent{agentID: "agent-1"}
	sessionConn := &ACPTunnelAgent{agentID: "agent-1", sessionID: "sess-1"}
	hub.add("agent-1", "", agentConn)
	hub.add("agent-1", "sess-1", sessionConn)

	got, err := hub.claim("agent-1", "sess-1")
	if err != nil {
		t.Fatalf("claim exact session tunnel: %v", err)
	}
	if got != sessionConn {
		t.Fatalf("claim returned %#v, want %#v", got, sessionConn)
	}
}

func TestACPTunnelHubClaimAnyTriesNativeBeforeAgentTunnel(t *testing.T) {
	hub := NewACPTunnelHub()
	agentConn := &ACPTunnelAgent{agentID: "agent-1"}
	nativeConn := &ACPTunnelAgent{agentID: "agent-1", sessionID: "native-1"}
	hub.add("agent-1", "", agentConn)
	hub.add("agent-1", "native-1", nativeConn)

	got, err := hub.claimAny("agent-1", "sess_manager_1", "native-1", "")
	if err != nil {
		t.Fatalf("claim native session tunnel: %v", err)
	}
	if got != nativeConn {
		t.Fatalf("claim returned %#v, want %#v", got, nativeConn)
	}
}

func TestACPTunnelAgentSessionContextRestoresPreviousSession(t *testing.T) {
	agent := &ACPTunnelAgent{sessionID: ""}

	restore := agent.withSessionContext("sess_1")
	if agent.sessionID != "sess_1" {
		t.Fatalf("sessionID = %q, want temporary context", agent.sessionID)
	}
	restore()
	if agent.sessionID != "" {
		t.Fatalf("sessionID = %q, want restored empty context", agent.sessionID)
	}
}

func TestACPRequestPermissionAddsAllowAlwaysOption(t *testing.T) {
	params := map[string]any{
		"options": []any{
			map[string]any{"kind": "reject_once", "name": "Reject", "optionId": "reject"},
			map[string]any{"kind": "allow_once", "name": "Allow", "optionId": "allow"},
		},
	}

	if !appendACPAllowAlwaysOption(params) {
		t.Fatal("append option changed = false")
	}
	options, ok := params["options"].([]any)
	if !ok {
		t.Fatalf("options type = %T", params["options"])
	}
	if got := acpOptionID(options[len(options)-1]); got != "allow_always_on_all_agents" {
		t.Fatalf("last option id = %q", got)
	}
	if appendACPAllowAlwaysOption(params) {
		t.Fatal("duplicate allow always option was appended")
	}
}

func TestACPAllowOnceResponseUsesExistingAllowOnceOption(t *testing.T) {
	msg := acpJSONRPCMessage{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`7`),
		Method:  "session/request_permission",
	}
	params := map[string]any{
		"options": []any{
			map[string]any{"kind": "reject_once", "name": "Reject", "optionId": "reject"},
			map[string]any{"kind": "allow_once", "name": "Allow", "optionId": "allow"},
		},
	}

	raw, err := acpAllowOnceResponse(msg, params)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      int            `json:"id"`
		Result  map[string]any `json:"result"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.JSONRPC != "2.0" || response.ID != 7 {
		t.Fatalf("unexpected response frame: %s", raw)
	}
	if got := acpOptionKind(response.Result); got != "allow_once" {
		t.Fatalf("selected option kind = %q", got)
	}
}

func TestACPPermissionFingerprintUsesStableToolCallFields(t *testing.T) {
	first := map[string]any{
		"sessionId": "session-a",
		"options":   []any{map[string]any{"kind": "allow_once", "optionId": "allow"}},
		"toolCall": map[string]any{
			"toolCallId": "toolu_01first",
			"kind":       "execute",
			"title":      "curl -s https://api.example.com/v1/status",
			"rawInput": map[string]any{
				"command":     "curl -s https://api.example.com/v1/status",
				"description": "Fetch status from example API endpoint",
			},
		},
	}
	second := map[string]any{
		"sessionId": "session-b",
		"options":   []any{map[string]any{"kind": "allow_once", "optionId": "allow"}},
		"toolCall": map[string]any{
			"toolCallId": "toolu_01second",
			"kind":       "execute",
			"title":      "curl -s https://api.example.com/v1/status",
			"rawInput": map[string]any{
				"command":     "curl -s https://api.example.com/v1/status",
				"description": "Fetch status from example API endpoint",
			},
		},
	}

	firstFingerprint, err := acpPermissionFingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	secondFingerprint, err := acpPermissionFingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstFingerprint != "acp:tool_call:execute:curl -s https://api.example.com/v1/status" {
		t.Fatalf("fingerprint = %q", firstFingerprint)
	}
	if firstFingerprint != secondFingerprint {
		t.Fatalf("fingerprints differ: %q != %q", firstFingerprint, secondFingerprint)
	}
}

func TestRunACPActorsRecoversPanic(t *testing.T) {
	err := runACPActors(context.Background(), acpActor{
		name: "panic_actor",
		run: func(context.Context) error {
			panic("boom")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("err = %v, want panic error", err)
	}
}

func TestUserTunnelMetadataUsesHeadersAndFallbackTunnelID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/self/agents/a/tunnel", nil)
	req.Header.Set("X-Pax-Client-ID", "client_1")
	req.Header.Set("X-Pax-Device-ID", "device_1")

	got := userTunnelMetadata(req)
	if got.ClientID != "client_1" || got.DeviceID != "device_1" {
		t.Fatalf("metadata = %+v", got)
	}
	if !strings.HasPrefix(got.TunnelID, "log_") {
		t.Fatalf("tunnel id = %q, want generated id", got.TunnelID)
	}
}

func TestUserTunnelMetadataPrefersExplicitTunnelID(t *testing.T) {
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/agents/a/tunnel?client_id=query_client&device_id=query_device&tunnel_id=tunnel_1",
		nil,
	)

	got := userTunnelMetadata(req)
	if got.ClientID != "query_client" ||
		got.DeviceID != "query_device" ||
		got.TunnelID != "tunnel_1" {
		t.Fatalf("metadata = %+v", got)
	}
}
