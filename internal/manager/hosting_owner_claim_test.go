package manager

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

func TestStarterEnrollmentDerivesOwnerFromBothHostAndNodeCredentials(t *testing.T) {
	s := hostingTestServer(t, "https://gateway.example.com/v1")
	ctx := t.Context()
	const ownerEmail = "enrolled@example.com"
	const nodeKey = "fixture-node-key"
	owner, err := s.store.EnsureUser(ctx, ownerEmail, "Enrolled", "user")
	require.NoError(t, err)
	node, err := s.store.RegisterNode(ctx, owner, domain.RegisterNodeRequest{
		Name: "Sandbox", Hostname: "sandbox",
	}, s.secrets.Hash(nodeKey))
	require.NoError(t, err)
	agent, _, err := s.store.CreateNodeAgent(
		ctx,
		domain.UserPrincipal{User: owner},
		domain.CreateAgentRequest{NodeID: node.NodeID, Name: "Pi", AgentType: "pi"},
	)
	require.NoError(t, err)

	enroll := func(hostID, nodeID, bearer, key string) *httptest.ResponseRecorder {
		raw, err := json.Marshal(map[string]string{"host_id": hostID, "node_id": nodeID})
		require.NoError(t, err)
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/hosting/jobs/enroll",
			bytes.NewReader(raw),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("X-Pax-Key", key)
		rec := httptest.NewRecorder()
		s.routes().ServeHTTP(rec, req)
		return rec
	}
	require.Equal(t, 401, enroll("host-a", node.NodeID, "", nodeKey).Code)
	require.Equal(t, 401, enroll("host-a", node.NodeID, s.cfg.HostingHostKey, "").Code)
	require.Equal(t, 401, enroll("host-a", "wrong-node", s.cfg.HostingHostKey, nodeKey).Code)
	receipt := enroll("host-a", node.NodeID, s.cfg.HostingHostKey, nodeKey)
	require.Equal(t, 200, receipt.Code)
	require.Equal(t, "no-store", receipt.Header().Get("Cache-Control"))
	status := decodeData[hosting.Status](t, receipt.Body.Bytes())
	require.Equal(t, owner.UserID, status.Starter.OwnerUserID)
	require.Equal(t, node.NodeID, status.Starter.NodeID)
	require.Equal(t, "sleeping", status.Starter.State)
	require.NotContains(t, receipt.Body.String(), nodeKey)
	require.NotContains(t, receipt.Body.String(), "model_token")
	require.Equal(t, 409, enroll("wrong-host", node.NodeID, s.cfg.HostingHostKey, nodeKey).Code)
	require.Equal(t, 200, enroll("host-a", node.NodeID, s.cfg.HostingHostKey, nodeKey).Code)

	body, err := json.Marshal(hosting.WakeTarget{NodeID: node.NodeID, AgentID: agent.AgentID})
	require.NoError(t, err)
	wake := func(email string) *httptest.ResponseRecorder {
		return hostingRequest(t, s, "POST", "/api/v1/starter/wake", string(body), email, "")
	}
	require.Equal(t, 401, wake("foreign@example.com").Code)
	require.Equal(t, 200, wake(ownerEmail).Code)
	malformed := hostingRequest(t, s, "POST", "/api/v1/hosting/jobs/claim",
		`{"host_id":"host-a","capacity":1,"owner_user_id":null,"environment_id":null}`,
		"", s.cfg.HostingHostKey)
	require.Equal(t, 409, malformed.Code)

	rawClaim, err := json.Marshal(hosting.Claim{
		HostID: "host-a", Capacity: 1,
		OwnerUserID: &owner.UserID, EnvironmentID: &status.Starter.EnvironmentID,
	})
	require.NoError(t, err)
	claim := hostingRequest(t, s, "POST", "/api/v1/hosting/jobs/claim",
		string(rawClaim), "", s.cfg.HostingHostKey)
	require.Equal(t, 200, claim.Code)
	job := decodeData[struct{ Job *hosting.Job }](t, claim.Body.Bytes()).Job
	require.NotNil(t, job)
	require.Equal(t, node.NodeID, job.NodeID)
	require.Equal(t, agent.AgentID, job.AgentID)
	require.Empty(t, job.RegistrationToken)
	require.NotEmpty(t, job.ModelToken)

	const replacementKey = "fixture-replacement-node-key"
	replacement, err := s.store.RegisterNode(ctx, owner, domain.RegisterNodeRequest{
		Name: "Fresh sandbox", Hostname: "sandbox",
	}, s.secrets.Hash(replacementKey))
	require.NoError(t, err)
	update := &hosting.Update{
		HostID: "host-a", JobID: job.JobID, Generation: job.Generation, LeaseToken: job.LeaseToken,
		State: "ready", NodeID: node.NodeID, AgentID: agent.AgentID,
	}
	_, err = s.hosting.Update(ctx, update, true)
	require.NoError(t, err)
	reservation, err := s.hosting.Reserve(ctx, job.ModelToken, "old-sandbox-use", 100)
	require.NoError(t, err)
	require.NoError(t, s.hosting.Settle(ctx, reservation.ID, 12, 8, nil, "fixture-usage", true))
	spent, err := s.hosting.Status(ctx, owner.UserID, false)
	require.NoError(t, err)
	require.Positive(t, spent.Starter.Quota.SpentMicrousd)

	_, err = s.store.DeleteNode(
		ctx,
		domain.UserPrincipal{User: owner},
		domain.DeleteNodeRequest{NodeID: node.NodeID},
	)
	require.NoError(t, err)
	require.Equal(t, 401, enroll("host-a", node.NodeID, s.cfg.HostingHostKey, nodeKey).Code)
	require.Equal(
		t,
		409,
		enroll("host-b", replacement.NodeID, s.cfg.HostingHostKey, replacementKey).Code,
	)
	update.State = "sleeping"
	_, err = s.hosting.Update(ctx, update, true)
	require.NoError(t, err)
	replacementReceipt := enroll("host-b", replacement.NodeID, s.cfg.HostingHostKey, replacementKey)
	require.Equal(t, 200, replacementReceipt.Code)
	rebound := decodeData[hosting.Status](t, replacementReceipt.Body.Bytes())
	require.True(t, rebound.EnrolledNode)
	require.Equal(t, status.Starter.EnvironmentID, rebound.Starter.EnvironmentID)
	require.Equal(t, spent.Starter.Quota, rebound.Starter.Quota)
	require.Equal(t, "sleeping", rebound.Starter.State)
	require.Equal(t, replacement.NodeID, rebound.Starter.NodeID)
	require.Empty(t, rebound.Starter.AgentID)
	require.ErrorIs(t, s.hosting.Authorize(ctx, job.ModelToken), hosting.ErrUnauthorized)
}
