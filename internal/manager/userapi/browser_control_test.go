package userapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
	userapimocks "github.com/pax-beehive/pax-manager/internal/manager/userapi/mocks"
)

func TestBrowserControlRequiresNodeOwnership(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "other owner denied", true: "owner forwarded"}[allowed],
			func(t *testing.T) {
				ctx := context.Background()
				principal := userPrincipal("user_browser", false)
				store := userapimocks.NewMockStore(t)
				principals := userapimocks.NewMockPrincipalResolver(t)
				secrets := userapimocks.NewMockSecretIssuer(t)
				principals.EXPECT().
					Principal(ctx, auth.RequestMetadata{}).
					Return(principal, nil).
					Once()
				client := &fakeNodeControlClient{
					result: json.RawMessage(`{"browser_control":{"policy":{"origins":[]}}}`),
				}
				if allowed {
					store.EXPECT().
						GetNode(ctx, principal, "node_browser").
						Return(domain.Node{NodeID: "node_browser"}, nil).
						Once()
					secrets.EXPECT().New("ctlq").Return("ctlq_browser", nil).Once()
				} else {
					store.EXPECT().GetNode(ctx, principal, "node_browser").Return(domain.Node{}, errors.New("not owned")).Once()
				}
				svc := userapi.NewService(store, fixedUserClock, principals, secrets)
				svc.SetNodeControlClient(client)
				_, _, err := svc.NodeBrowserControl(
					ctx,
					auth.RequestMetadata{},
					"node_browser",
					"state",
					json.RawMessage(`{}`),
				)
				if allowed {
					require.NoError(t, err)
					require.Equal(t, 1, client.calls)
					require.Equal(t, 0, client.commandCalls)
				} else {
					require.Error(t, err)
					require.Zero(t, client.calls)
				}
			},
		)
	}
}

func TestBrowserControlRejectsProxyAndCredentialRoutes(t *testing.T) {
	svc := userapi.NewService(nil, fixedUserClock, nil, nil)
	for _, operation := range []string{"pair", "login-code", "/runtime/consume-secret", "http://localhost:9222/json"} {
		_, _, err := svc.NodeBrowserControl(
			context.Background(),
			auth.RequestMetadata{},
			"node",
			operation,
			nil,
		)
		require.Error(t, err)
	}
}
