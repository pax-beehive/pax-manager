package manager

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestStarterHostWorkerContract(t *testing.T) {
	driver := os.Getenv("PAX_HOST_CONTRACT_DRIVER")
	if driver == "" {
		t.Skip("set PAX_HOST_CONTRACT_DRIVER to the reviewed pax-host contract driver")
	}
	require.True(t, filepath.IsAbs(driver))
	s := hostingTestServer(t, "https://model.example.com/v1")
	user, err := s.store.EnsureUser(t.Context(), "alice@example.com", "Alice", "user")
	require.NoError(t, err)
	node, err := s.store.RegisterNode(
		t.Context(),
		user,
		domain.RegisterNodeRequest{Name: "Starter", Hostname: "starter"},
		"fixture-key-hash",
	)
	require.NoError(t, err)
	agent, _, err := s.store.CreateNodeAgent(
		t.Context(),
		domain.UserPrincipal{User: user},
		domain.CreateAgentRequest{NodeID: node.NodeID, Name: "Starter", AgentType: "pi"},
	)
	require.NoError(t, err)
	_, err = s.hosting.Wake(t.Context(), user.UserID)
	require.NoError(t, err)
	server := httptest.NewServer(s.routes())
	defer server.Close()
	payload, err := json.Marshal(
		map[string]string{
			"origin":            server.URL,
			"host_key":          s.cfg.HostingHostKey,
			"model_gateway_url": s.cfg.HostingGatewayURL,
			"owner_user_id":     user.UserID,
			"owner_email":       user.Email,
			"node_id":           node.NodeID,
			"agent_id":          agent.AgentID,
		},
	)
	require.NoError(t, err)
	command := exec.CommandContext(t.Context(), "python3", driver)
	command.Stdin = bytes.NewReader(payload)
	output, err := command.Output()
	if err != nil {
		t.Fatal("Manager/host contract driver failed; credentials and response output withheld")
	}
	var checks map[string]bool
	require.NoError(t, json.Unmarshal(output, &checks))
	for _, key := range []string{"claimed", "renewed", "ready", "sleep_wake", "identity_preserved", "generation_rotated"} {
		require.True(t, checks[key], key)
	}
}
