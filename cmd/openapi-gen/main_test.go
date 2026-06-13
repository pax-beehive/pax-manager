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
	if _, ok := paths["/api/user/api-keys"]; !ok {
		t.Fatal("missing /api/user/api-keys")
	}
	register := paths["/api/agent/register"].(map[string]any)["post"].(map[string]any)
	parameters := register["parameters"].([]map[string]any)
	if len(parameters) != 1 || parameters[0]["name"] != "X-Registration-Token" {
		t.Fatalf("register parameters = %#v", parameters)
	}
	components := spec["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	if _, ok := schemas["MailboxMessage"]; !ok {
		t.Fatal("missing MailboxMessage schema")
	}
}
