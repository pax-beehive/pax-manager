package manager

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
	"github.com/pax-beehive/pax-manager/internal/manager/runtimeconfig"
)

func TestSandboxModelUsesOnlySandboxHostForVerifiedOwnerAgent(t *testing.T) {
	const email, hostID = "aws-pilot@example.com", "aws-pilot"
	sandboxKey, legacyKey := strings.Repeat("a", 32), strings.Repeat("l", 32)
	s, store := newConfigTestServer(t, Config{
		HostingSandboxHostKey: sandboxKey, HostingSandboxHostID: hostID,
		HostingSandboxCloudURL: "https://node.example.com",
		HostingHostKey:         legacyKey, HostingTokenSecret: strings.Repeat("s", 32),
		HostingPublicURL: "https://pax.example.com", HostingCloudURL: "https://node.example.com",
		HostingGatewayURL: "https://model.example.com/v1", HostingGatewayKey: strings.Repeat("g", 32),
		HostingModel: "test-model", HostingGrantMicrousd: 5_000_000,
		HostingGlobalMicrousd: 100_000_000, HostingMaxOutputTokens: 1024,
		HostingInputPrice: 1_000_000, HostingOutputPrice: 5_000_000,
	})
	require.True(t, s.hosting.Enabled())
	ctx := t.Context()
	gate := callAPI(
		t, s, "PUT",
		"/api/v1/admin/config/"+runtimeconfig.FeatureKey(runtimeconfig.FeatureHostedSandbox),
		"admin@example.com",
		map[string]any{
			"value":            map[string]any{"stage": "beta", "beta_emails": []string{email}},
			"expected_version": 0, "reason": "sandbox test",
		},
	)
	require.Equal(t, http.StatusOK, gate.Code)
	user, err := store.EnsureUser(ctx, email, "Pilot", "user")
	require.NoError(t, err)
	node, err := store.RegisterNode(ctx, user, domain.RegisterNodeRequest{
		Name: "Sandbox", Hostname: "sandbox",
	}, s.secrets.Hash("fixture-sandbox-node"))
	require.NoError(t, err)
	agent, _, err := store.CreateNodeAgent(
		ctx, domain.UserPrincipal{User: user},
		domain.CreateAgentRequest{NodeID: node.NodeID, Name: "Codex", AgentType: "codex"},
	)
	require.NoError(t, err)
	_, err = s.sandboxNodes.Status(ctx, user.UserID, true)
	require.NoError(t, err)
	parent, _, err := s.sandboxNodes.Claim(
		ctx, &hosting.Claim{Kind: hosting.SandboxKind, HostID: hostID, Capacity: 1},
	)
	require.NoError(t, err)
	_, err = s.sandboxNodes.Update(ctx, &hosting.Update{
		Kind: parent.Kind, HostID: hostID, JobID: parent.JobID, LeaseToken: parent.LeaseToken,
		Generation: parent.Generation, State: "ready", NodeID: node.NodeID,
	}, true)
	require.NoError(t, err)

	body := `{"kind":"sandbox_model","host_id":"aws-pilot","capacity":1,"supported_harnesses":["codex"]}`
	path := "/api/v1/hosting/jobs/claim"
	require.Equal(t, 401, hostingRequest(t, s, "POST", path, body, "", legacyKey).Code)
	require.Equal(t, 401, hostingRequest(t, s, "POST", path, body, "", "").Code)
	empty := hostingRequest(t, s, "POST", path, body, "", sandboxKey)
	require.Equal(t, 200, empty.Code)
	require.Nil(t, decodeData[struct{ Job *hosting.Job }](t, empty.Body.Bytes()).Job)
	require.NotContains(t, empty.Body.String(), `"queued_count"`)
	get := hostingRequest(t, s, "GET", "/api/v1/starter", "", email, "")
	status := decodeData[hosting.Status](t, get.Body.Bytes())
	require.Equal(t, "available", status.SandboxModelAccess.State)
	require.Equal(t, node.NodeID, status.SandboxModelAccess.NodeID)
	require.Equal(t, []string{"codex"}, status.SandboxModelAccess.Harnesses)
	require.True(t, status.PoolAvailable)
	require.True(t, status.UserBudgetAvailable)
	require.False(t, status.EnrolledNode)

	target, err := json.Marshal(hosting.WakeTarget{NodeID: node.NodeID, AgentID: agent.AgentID})
	require.NoError(t, err)
	wake := "/api/v1/starter/wake"
	foreign := hostingRequest(t, s, "POST", wake, string(target), "foreign@example.com", "")
	require.Equal(t, 401, foreign.Code)
	queued := hostingRequest(t, s, "POST", wake, string(target), email, "")
	require.Equal(t, 200, queued.Code)
	require.Equal(
		t,
		[]hosting.SandboxModelAgent{{AgentID: agent.AgentID, State: "queued"}},
		decodeData[hosting.Status](t, queued.Body.Bytes()).SandboxModelAccess.Agents,
	)
	claimed := hostingRequest(t, s, "POST", path, body, "", sandboxKey)
	require.Equal(t, 200, claimed.Code)
	job := decodeData[struct{ Job *hosting.Job }](t, claimed.Body.Bytes()).Job
	require.NotNil(t, job)
	require.Equal(t, agent.AgentID, job.AgentID)
	require.Equal(t, parent.EnvironmentID, job.SandboxEnvironmentID)
	require.Empty(t, job.RegistrationToken)
	require.NotContains(t, queued.Body.String(), job.ModelToken)
	require.NotContains(t, queued.Body.String(), job.LeaseToken)
	update, err := json.Marshal(hosting.Update{
		Kind: hosting.SandboxModelKind, HostID: hostID, JobID: job.JobID,
		LeaseToken: job.LeaseToken, Generation: job.Generation, State: "ready",
		NodeID: node.NodeID, AgentID: agent.AgentID,
	})
	require.NoError(t, err)
	complete := "/api/v1/hosting/jobs/complete"
	require.Equal(
		t,
		401,
		hostingRequest(t, s, "POST", complete, string(update), "", legacyKey).Code,
	)
	require.Equal(
		t,
		200,
		hostingRequest(t, s, "POST", complete, string(update), "", sandboxKey).Code,
	)
}
