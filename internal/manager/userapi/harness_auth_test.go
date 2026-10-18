package userapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
	userapimocks "github.com/pax-beehive/pax-manager/internal/manager/userapi/mocks"
)

func TestHarnessAuthForwardsOnlyAfterNodePermissionAndBindsVerifiedActor(t *testing.T) {
	ctx := context.Background()
	principal := userPrincipal("verified-user", false)
	store := userapimocks.NewMockStore(t)
	resolver := userapimocks.NewMockPrincipalResolver(t)
	resolver.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Twice()
	store.EXPECT().
		GetNode(ctx, principal, "node1").
		Return(domain.Node{NodeID: "node1"}, nil).
		Twice()
	node := &fakeNodeControlClient{
		commandAck: json.RawMessage(
			`{"ok":true,"status":"applied","result":{"harness_auth":{"harness":"pi","session_id":"attempt1","state":"starting"}}}`,
		),
		result: json.RawMessage(
			`{"type":"harness_auth.status","harness_auth":{"harness":"pi","state":"awaiting_input"}}`,
		),
	}
	issuer := userapimocks.NewMockSecretIssuer(t)
	issuer.EXPECT().New("ctlq").Return("query-one", nil)
	service := userapi.NewService(store, fixedUserClock, resolver, issuer)
	service.SetNodeControlClient(node)
	status, _, err := service.NodeHarnessAuth(
		ctx,
		auth.RequestMetadata{},
		domain.NodeHarnessAuthRequest{
			NodeID:    "node1",
			CommandID: "submit1",
			Harness:   "pi",
			Operation: "submit",
			SessionID: "attempt1",
			PromptID:  "prompt1",
			Code:      "fixture-private-code",
		},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)
	payload := node.command.(map[string]any)["harness_auth_login"].(map[string]any)
	require.Equal(t, "verified-user", payload["actor_id"])
	require.Equal(t, "fixture-private-code", payload["code"])
	require.Equal(t, "prompt1", payload["prompt_id"])
	status, _, err = service.GetNodeHarnessAuth(
		ctx,
		auth.RequestMetadata{},
		domain.GetNodeHarnessAuthRequest{NodeID: "node1", Harness: "pi", CommandID: "start1"},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	query := node.query.(map[string]any)["harness_auth_status"].(map[string]any)
	require.Equal(t, "verified-user", query["actor_id"])
	require.Equal(t, "start1", query["command_id"])
	require.NotContains(t, query, "code")
}
func TestHarnessAuthDeniesUnownedNodeBeforeDispatch(t *testing.T) {
	ctx := context.Background()
	principal := userPrincipal("user1", false)
	store := userapimocks.NewMockStore(t)
	resolver := userapimocks.NewMockPrincipalResolver(t)
	resolver.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil)
	store.EXPECT().GetNode(ctx, principal, "other").Return(domain.Node{}, domain.ErrNotFound)
	client := &fakeNodeControlClient{}
	service := userapi.NewService(
		store,
		fixedUserClock,
		resolver,
		userapimocks.NewMockSecretIssuer(t),
	)
	service.SetNodeControlClient(client)
	_, _, err := service.NodeHarnessAuth(
		ctx,
		auth.RequestMetadata{},
		domain.NodeHarnessAuthRequest{
			NodeID:    "other",
			CommandID: "start1",
			Harness:   "codex",
			Operation: "start",
		},
	)
	require.Error(t, err)
	require.Zero(t, client.commandCalls)
}

func TestHarnessAuthRefusesLegacyExecutorsBeforeStartingLogin(t *testing.T) {
	ctx := context.Background()
	principal := userPrincipal("user1", false)
	store := userapimocks.NewMockStore(t)
	resolver := userapimocks.NewMockPrincipalResolver(t)
	issuer := userapimocks.NewMockSecretIssuer(t)
	resolver.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil)
	store.EXPECT().GetNode(ctx, principal, "node1").Return(domain.Node{NodeID: "node1"}, nil)
	issuer.EXPECT().New("ctlq").Return("cap1", nil)
	client := &fakeNodeControlClient{
		result: json.RawMessage(`{"harness_auth":{"harness":"codex","state":"logged_in"}}`),
	}
	service := userapi.NewService(store, fixedUserClock, resolver, issuer)
	service.SetNodeControlClient(client)
	_, _, err := service.NodeHarnessAuth(
		ctx,
		auth.RequestMetadata{},
		domain.NodeHarnessAuthRequest{
			NodeID:    "node1",
			Harness:   "codex",
			CommandID: "start1",
			Operation: "start",
		},
	)
	require.ErrorContains(t, err, "Update this node's paxd and paxl")
	require.Zero(t, client.commandCalls)
}
