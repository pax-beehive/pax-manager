package main

import (
	"os"
	"testing"
)

func TestBuildSpecFromThrift(t *testing.T) {
	data, err := os.ReadFile("../../api/pax_manager.thrift")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseThrift(string(data))
	if err != nil {
		t.Fatal(err)
	}
	spec := buildSpec(doc)

	if spec["openapi"] != "3.1.0" {
		t.Fatalf("openapi = %v", spec["openapi"])
	}
	paths := spec["paths"].(map[string]any)
	if _, ok := paths["/api/v1/user/{user_id}/api-keys"]; !ok {
		t.Fatal("missing /api/v1/user/{user_id}/api-keys")
	}
	for _, path := range []string{
		"/api/v1/node/messages/{message_id}/delivered",
		"/api/v1/node/messages/outbound",
		"/api/v1/user/{user_id}/me",
		"/api/v1/user/{user_id}/nodes/{node_id}/agents/{agent_id}/sessions",
	} {
		if _, ok := paths[path]; !ok {
			t.Fatalf("missing %s", path)
		}
	}
	register := paths["/api/v1/node/register"].(map[string]any)["post"].(map[string]any)
	parameters := register["parameters"].([]map[string]any)
	if len(parameters) != 1 || parameters[0]["name"] != "X-Registration-Token" {
		t.Fatalf("register parameters = %#v", parameters)
	}
	responses := register["responses"].(map[string]any)
	if _, ok := responses["200"]; !ok {
		t.Fatal("missing 200 register response")
	}
	components := spec["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	if _, ok := schemas["MailboxMessage"]; !ok {
		t.Fatal("missing MailboxMessage schema")
	}
}
