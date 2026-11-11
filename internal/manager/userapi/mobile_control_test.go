package userapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
	userapimocks "github.com/pax-beehive/pax-manager/internal/manager/userapi/mocks"
)

func TestMobileControlRequiresNodeOwnership(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "other owner denied", true: "owner forwarded"}[allowed],
			func(t *testing.T) {
				ctx := context.Background()
				principal := userPrincipal("user_mobile", false)
				store := userapimocks.NewMockStore(t)
				principals := userapimocks.NewMockPrincipalResolver(t)
				secrets := userapimocks.NewMockSecretIssuer(t)
				principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).
					Return(principal, nil).Once()
				client := &fakeNodeControlClient{
					result: json.RawMessage(`{"mobile_control":{"available":true,"sessions":[]}}`),
				}
				if allowed {
					store.EXPECT().GetNode(ctx, principal, "node_mobile").
						Return(domain.Node{NodeID: "node_mobile"}, nil).Once()
					secrets.EXPECT().New("ctlq").Return("ctlq_mobile", nil).Once()
				} else {
					store.EXPECT().GetNode(ctx, principal, "node_mobile").
						Return(domain.Node{}, errors.New("not owned")).Once()
				}
				svc := userapi.NewService(store, fixedUserClock, principals, secrets)
				svc.SetNodeControlClient(client)
				status, data, err := svc.NodeMobileControl(ctx, auth.RequestMetadata{},
					"node_mobile", "state", nil)
				if !allowed {
					require.Error(t, err)
					require.Zero(t, client.calls)
					return
				}
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, status)
				require.JSONEq(t, string(client.result), string(data.(json.RawMessage)))
				require.Equal(t, 1, client.calls)
				require.Zero(t, client.commandCalls)
				require.Equal(t, "node_mobile", client.nodeID)
				require.Equal(t, "ctlq_mobile", client.requestID)
				require.Equal(t, map[string]any{
					"type": "mobile.control", "mobile_control": map[string]any{
						"operation": "state", "payload": json.RawMessage(`{}`),
					},
				}, client.query)
			})
	}
}

func TestMobileControlRejectsNetworkCredentialAndSignalingOperations(t *testing.T) {
	svc := userapi.NewService(nil, fixedUserClock, nil, nil)
	for _, operation := range []string{
		"", "live", "live_open", "live_renew", "live_close", "webrtc", "rtc",
		"adb", "shell", "install_app", "login", "viewer-login", "credentials",
		"http://127.0.0.1:8554", "/v1/admin/sessions", "recording_read/../../secrets",
	} {
		_, _, err := svc.NodeMobileControl(context.Background(), auth.RequestMetadata{},
			"node_mobile", operation, json.RawMessage(`{}`))
		var apiErr apperr.Error
		require.ErrorAs(t, err, &apiErr)
		require.Equal(t, http.StatusBadRequest, apiErr.Status)
	}
}

func TestMobileControlRejectsUnboundedOrNonObjectPayload(t *testing.T) {
	svc := userapi.NewService(nil, fixedUserClock, nil, nil)
	for _, payload := range []string{
		`null`, `[]`, `"state"`, `1`, `{`, `{"extra":"` + strings.Repeat("a", 16384) + `"}`,
	} {
		_, _, err := svc.NodeMobileControl(context.Background(), auth.RequestMetadata{},
			"node_mobile", "state", json.RawMessage(payload))
		var apiErr apperr.Error
		require.ErrorAs(t, err, &apiErr)
		require.Equal(t, http.StatusBadRequest, apiErr.Status)
	}
}
