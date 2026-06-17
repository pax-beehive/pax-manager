package manager

import (
	"encoding/json"
	"testing"
)

func TestACPRequestPermissionAddsAllowAlwaysOption(t *testing.T) {
	params := map[string]any{
		"options": []any{
			map[string]any{"optionId": "deny", "label": "Deny"},
			map[string]any{"optionId": "allow_once", "label": "Allow once"},
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
			map[string]any{"optionId": "deny", "label": "Deny"},
			map[string]any{"optionId": "allow_once", "label": "Allow once"},
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
	if got := acpOptionID(response.Result); got != "allow_once" {
		t.Fatalf("selected option = %q", got)
	}
}
