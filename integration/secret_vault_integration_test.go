//go:build integration

package integration_test

import (
	"bytes"
	"net/http"
	"testing"
)

type secret struct {
	SecretID         string `json:"secret_id"`
	OwnerUserID      string `json:"owner_user_id"`
	Name             string `json:"name"`
	CurrentVersionID string `json:"current_version_id"`
	CurrentVersion   int64  `json:"current_version"`
}

type secretVersion struct {
	VersionID     string `json:"version_id"`
	SecretID      string `json:"secret_id"`
	VersionNumber int64  `json:"version_number"`
	KeyID         string `json:"key_id"`
	State         string `json:"state"`
}

type createSecretResponse struct {
	Secret  secret        `json:"secret"`
	Version secretVersion `json:"version"`
}

type secretListResponse struct {
	Secrets []secret `json:"secrets"`
}

type approval struct {
	ApprovalID        string `json:"approval_id"`
	Domain            string `json:"domain"`
	Operation         string `json:"operation"`
	ResourceType      string `json:"resource_type"`
	ResourceRef       string `json:"resource_ref"`
	Status            string `json:"status"`
	ActionFingerprint string `json:"action_fingerprint"`
}

type approvalResponse struct {
	Approval approval `json:"approval"`
}

type resolveSecretResponse struct {
	Status        string   `json:"status"`
	ApprovalID    string   `json:"approval_id"`
	SecretID      string   `json:"secret_id"`
	VersionID     string   `json:"version_id"`
	VersionNumber int64    `json:"version_number"`
	Value         string   `json:"value"`
	Approval      approval `json:"approval"`
}

type writeSecretVersionResponse struct {
	Status        string   `json:"status"`
	ApprovalID    string   `json:"approval_id"`
	SecretID      string   `json:"secret_id"`
	VersionID     string   `json:"version_id"`
	VersionNumber int64    `json:"version_number"`
	Current       bool     `json:"current"`
	Approval      approval `json:"approval"`
}

func TestSecretVaultApprovalAndOptimisticLockIntegration(t *testing.T) {
	fixture := newIntegrationFixture(t)
	fixture.waitForHealth(t)
	ownerHeaders := fixture.userHeaders()

	node := createBoundaryNode(t, fixture, ownerHeaders, "secret-node")
	agent := createBoundaryAgent(t, fixture, ownerHeaders, node.NodeID, "secret-agent")
	sessionID := "sess-secret-agent"
	createBoundarySession(t, fixture, ownerHeaders, node.NodeID, agent.Agent.AgentID, sessionID)

	createdRaw := postRaw(
		t,
		fixture,
		"/api/v1/user/self/secrets",
		map[string]any{
			"name":        "github-token",
			"kind":        "token",
			"description": "integration secret",
			"value":       "initial-secret-value",
		},
		ownerHeaders,
		http.StatusOK,
	)
	if bytes.Contains(createdRaw, []byte("initial-secret-value")) {
		t.Fatalf("create secret response leaked plaintext: %s", createdRaw)
	}
	created := decodeEnvelope[createSecretResponse](t, createdRaw).Data
	if created.Secret.SecretID == "" ||
		created.Secret.CurrentVersionID == "" ||
		created.Version.VersionNumber != 1 {
		t.Fatalf("bad create secret response: %+v", created)
	}

	listRaw := fixture.getRaw(t, "/api/v1/user/self/secrets", http.StatusOK, ownerHeaders)
	if bytes.Contains(listRaw, []byte("initial-secret-value")) {
		t.Fatalf("list secrets response leaked plaintext: %s", listRaw)
	}
	listed := decodeEnvelope[secretListResponse](t, listRaw).Data
	if len(listed.Secrets) != 1 || listed.Secrets[0].SecretID != created.Secret.SecretID {
		t.Fatalf("created secret not listed: %+v", listed.Secrets)
	}

	readRequest := map[string]any{
		"secret_id":  created.Secret.SecretID,
		"version":    "latest",
		"agent_id":   agent.Agent.AgentID,
		"session_id": sessionID,
	}
	readPending := postJSON[resolveSecretResponse](
		t,
		fixture,
		"/api/v1/node/secrets/resolve",
		readRequest,
		fixture.agentHeaders(node.APIKey),
		http.StatusAccepted,
	)
	if readPending.Status != "approval_required" || readPending.ApprovalID == "" {
		t.Fatalf("read should require approval: %+v", readPending)
	}
	approveSecretAction(t, fixture, readPending.ApprovalID)

	readAllowed := postJSON[resolveSecretResponse](
		t,
		fixture,
		"/api/v1/node/secrets/resolve",
		readRequest,
		fixture.agentHeaders(node.APIKey),
		http.StatusOK,
	)
	if readAllowed.Value != "initial-secret-value" ||
		readAllowed.VersionID != created.Secret.CurrentVersionID {
		t.Fatalf("unexpected resolved secret: %+v", readAllowed)
	}

	writeRequest := map[string]any{
		"agent_id":                    agent.Agent.AgentID,
		"session_id":                  sessionID,
		"value":                       "rotated-secret-value",
		"make_current":                true,
		"expected_current_version_id": created.Secret.CurrentVersionID,
		"idempotency_key":             "rotate-once",
		"reason":                      "integration rotation",
	}
	writePending := postJSON[writeSecretVersionResponse](
		t,
		fixture,
		"/api/v1/node/secrets/"+created.Secret.SecretID+"/versions",
		writeRequest,
		fixture.agentHeaders(node.APIKey),
		http.StatusAccepted,
	)
	if writePending.Status != "approval_required" || writePending.ApprovalID == "" {
		t.Fatalf("write should require approval: %+v", writePending)
	}
	approveSecretAction(t, fixture, writePending.ApprovalID)

	written := postJSON[writeSecretVersionResponse](
		t,
		fixture,
		"/api/v1/node/secrets/"+created.Secret.SecretID+"/versions",
		writeRequest,
		fixture.agentHeaders(node.APIKey),
		http.StatusOK,
	)
	if !written.Current || written.VersionNumber != 2 || written.VersionID == "" {
		t.Fatalf("unexpected write response: %+v", written)
	}

	idempotent := postJSON[writeSecretVersionResponse](
		t,
		fixture,
		"/api/v1/node/secrets/"+created.Secret.SecretID+"/versions",
		writeRequest,
		fixture.agentHeaders(node.APIKey),
		http.StatusOK,
	)
	if idempotent.VersionID != written.VersionID || !idempotent.Current {
		t.Fatalf("idempotent retry created a different version: %+v vs %+v", idempotent, written)
	}

	readRotated := postJSON[resolveSecretResponse](
		t,
		fixture,
		"/api/v1/node/secrets/resolve",
		readRequest,
		fixture.agentHeaders(node.APIKey),
		http.StatusOK,
	)
	if readRotated.Value != "rotated-secret-value" ||
		readRotated.VersionID != written.VersionID {
		t.Fatalf("latest did not resolve rotated value: %+v", readRotated)
	}

	conflictRequest := map[string]any{
		"agent_id":                    agent.Agent.AgentID,
		"session_id":                  sessionID,
		"value":                       "conflicting-secret-value",
		"make_current":                true,
		"expected_current_version_id": created.Secret.CurrentVersionID,
		"idempotency_key":             "stale-rotate",
	}
	fixture.postExpectError(
		t,
		"/api/v1/node/secrets/"+created.Secret.SecretID+"/versions",
		conflictRequest,
		fixture.agentHeaders(node.APIKey),
		http.StatusConflict,
	)
}

func approveSecretAction(t *testing.T, fixture *integrationFixture, approvalID string) {
	t.Helper()
	approved := postJSON[approvalResponse](
		t,
		fixture,
		"/api/v1/user/self/approvals/"+approvalID+"/decision",
		map[string]any{"decision_option": "allow_for_this_agent"},
		fixture.userHeaders(),
		http.StatusOK,
	)
	if approved.Approval.Status != "decided" {
		t.Fatalf("approval was not decided: %+v", approved)
	}
}

func postRaw(
	t *testing.T,
	f *integrationFixture,
	path string,
	body any,
	headers map[string]string,
	wantStatus int,
) []byte {
	t.Helper()
	req := f.newRequest(t, http.MethodPost, path, body)
	addHeaders(req, headers)
	return f.do(t, req, wantStatus)
}
