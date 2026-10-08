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

func TestUpgradeNodeHarnessPreservesComponentAndTargetVersion(t *testing.T) {
	ctx := context.Background()
	principal := userPrincipal("user1", false)
	store := userapimocks.NewMockStore(t)
	principals := userapimocks.NewMockPrincipalResolver(t)
	principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil)
	store.EXPECT().GetNode(ctx, principal, "node1").Return(domain.Node{NodeID: "node1"}, nil)
	client := &fakeNodeControlClient{
		remoteID:   "remote1",
		commandAck: json.RawMessage(`{"command_id":"upgrade1","ok":true,"status":"received"}`),
	}
	svc := userapi.NewService(
		store,
		fixedUserClock,
		principals,
		userapimocks.NewMockSecretIssuer(t),
	)
	svc.SetNodeControlClient(client)
	status, data, err := svc.UpgradeNodeHarness(
		ctx,
		auth.RequestMetadata{},
		domain.UpgradeNodeHarnessRequest{
			NodeID:       "node1",
			CommandID:    "upgrade1",
			Version:      "1.2.3",
			Harness:      "codex",
			Component:    "acp",
			ConnectionID: "conn1",
		},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)
	require.Equal(t, "received", data.(map[string]any)["command_status"])
	require.NotContains(t, data.(map[string]any), "confirmation_status")
	require.Equal(
		t,
		map[string]any{
			"command_id": "upgrade1",
			"type":       "harness.upgrade",
			"upgrade_harness": map[string]any{
				"version":       "1.2.3",
				"harness":       "codex",
				"component":     "acp",
				"connection_id": "conn1",
			},
		},
		client.command,
	)
}

func TestUpgradeNodeHarnessRejectsUnknownComponent(t *testing.T) {
	for _, harness := range []string{"claude-code", "codex", "pi"} {
		t.Run(harness, func(t *testing.T) {
			ctx := context.Background()
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().
				Principal(ctx, auth.RequestMetadata{}).
				Return(userPrincipal("user1", false), nil)
			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.UpgradeNodeHarness(
				ctx,
				auth.RequestMetadata{},
				domain.UpgradeNodeHarnessRequest{
					NodeID:    "node1",
					CommandID: "native",
					Harness:   harness,
					Component: "unknown",
					Version:   "1.2.3",
				},
			)
			require.ErrorContains(t, err, "cli or acp")
		})
	}
}
