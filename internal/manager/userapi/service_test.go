package userapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
	userapimocks "github.com/pax-beehive/pax-manager/internal/manager/userapi/mocks"
)

type fakeNodeControlClient struct {
	remoteID     string
	remoteIDErr  error
	result       json.RawMessage
	err          error
	nodeID       string
	requestID    string
	query        any
	calls        int
	commandID    string
	command      any
	commandAck   json.RawMessage
	commandErr   error
	commandCalls int
}

func (c *fakeNodeControlClient) RemoteID(_ string) (string, error) {
	return c.remoteID, c.remoteIDErr
}

func (c *fakeNodeControlClient) Query(
	_ context.Context,
	nodeID string,
	requestID string,
	query any,
) (json.RawMessage, error) {
	c.calls++
	c.nodeID = nodeID
	c.requestID = requestID
	c.query = query
	return c.result, c.err
}

func (c *fakeNodeControlClient) Command(
	_ context.Context,
	nodeID string,
	commandID string,
	command any,
) (json.RawMessage, error) {
	c.commandCalls++
	c.nodeID = nodeID
	c.commandID = commandID
	c.command = command
	return c.commandAck, c.commandErr
}

func TestNodeDaemonQueries(t *testing.T) {
	t.Run("authorized status query is forwarded unchanged", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self"}
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		secrets := userapimocks.NewMockSecretIssuer(t)
		client := &fakeNodeControlClient{
			result: json.RawMessage(`{"type":"status.get","status":{"phase":"running"}}`),
		}

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().GetNode(ctx, principal, "node_1").Return(node, nil).Once()
		secrets.EXPECT().New("ctlq").Return("ctlq_1", nil).Once()

		svc := userapi.NewService(store, fixedUserClock, principals, secrets)
		svc.SetNodeControlClient(client)
		status, data, err := svc.GetNodeDaemonStatus(ctx, auth.RequestMetadata{}, "node_1")

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.JSONEq(t, string(client.result), string(data.(json.RawMessage)))
		require.Equal(t, 1, client.calls)
		require.Equal(t, "node_1", client.nodeID)
		require.Equal(t, "ctlq_1", client.requestID)
		require.Equal(t, map[string]any{
			"type":       "status.get",
			"get_status": map[string]any{},
		}, client.query)
	})

	t.Run("list query options are forwarded", func(t *testing.T) {
		ctx := context.Background()
		queries := []struct {
			name string
			call func(*userapi.Service) (int, any, error)
			want map[string]any
		}{
			{
				name: "harnesses",
				call: func(svc *userapi.Service) (int, any, error) {
					return svc.ListNodeDaemonHarnesses(ctx, auth.RequestMetadata{}, "node_1", true)
				},
				want: map[string]any{
					"type":           "harnesses.list",
					"list_harnesses": map[string]any{"include_missing": true},
				},
			},
			{
				name: "agent connections",
				call: func(svc *userapi.Service) (int, any, error) {
					return svc.ListNodeDaemonAgentConnections(
						ctx,
						auth.RequestMetadata{},
						"node_1",
						true,
					)
				},
				want: map[string]any{
					"type": "agent_connections.list",
					"list_agent_connections": map[string]any{
						"remote_id":        "remote_prod",
						"include_disabled": true,
					},
				},
			},
		}

		for _, tt := range queries {
			t.Run(tt.name, func(t *testing.T) {
				principal := userPrincipal("usr_self", false)
				node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self"}
				store := userapimocks.NewMockStore(t)
				principals := userapimocks.NewMockPrincipalResolver(t)
				secrets := userapimocks.NewMockSecretIssuer(t)
				client := &fakeNodeControlClient{
					remoteID: "remote_prod",
					result:   json.RawMessage(`{"type":"ok"}`),
				}

				principals.EXPECT().
					Principal(ctx, auth.RequestMetadata{}).
					Return(principal, nil).
					Once()
				store.EXPECT().GetNode(ctx, principal, "node_1").Return(node, nil).Once()
				secrets.EXPECT().New("ctlq").Return("ctlq_1", nil).Once()

				svc := userapi.NewService(store, fixedUserClock, principals, secrets)
				svc.SetNodeControlClient(client)
				status, _, err := tt.call(svc)

				require.NoError(t, err)
				require.Equal(t, http.StatusOK, status)
				require.Equal(t, tt.want, client.query)
			})
		}
	})

	t.Run("node authorization happens before forwarding", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		client := &fakeNodeControlClient{}
		forbidden := apperr.Error{Status: http.StatusForbidden, Message: "forbidden"}

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().GetNode(ctx, principal, "node_other").Return(domain.Node{}, forbidden).Once()

		svc := userapi.NewService(
			store,
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		svc.SetNodeControlClient(client)
		_, _, err := svc.GetNodeDaemonStatus(ctx, auth.RequestMetadata{}, "node_other")

		require.ErrorIs(t, err, forbidden)
		require.Zero(t, client.calls)
	})

	t.Run("agent connection list includes only unbound and owner-owned agents", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self"}
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		secrets := userapimocks.NewMockSecretIssuer(t)
		client := &fakeNodeControlClient{
			remoteID: "remote_prod",
			result: json.RawMessage(`{
				"type":"agent_connections.list",
				"agent_connections":{"items":[
					{"id":"conn_unbound","remote_id":"remote_prod"},
					{"id":"conn_owned","remote_id":"remote_prod","cloud_agent_id":"agent_owned"},
					{"id":"conn_shared","remote_id":"remote_prod","cloud_agent_id":"agent_shared"},
					{"id":"conn_stale","remote_id":"remote_prod","cloud_agent_id":"agent_missing"}
				]}
			}`),
		}

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().GetNode(ctx, principal, "node_1").Return(node, nil).Once()
		store.EXPECT().ListNodeAgents(ctx, principal, "node_1").Return([]domain.Agent{
			{AgentID: "agent_owned", NodeID: "node_1", OwnerUserID: "usr_self"},
			{AgentID: "agent_shared", NodeID: "node_1", OwnerUserID: "usr_other"},
		}, nil).Once()
		secrets.EXPECT().New("ctlq").Return("ctlq_owned_1", nil).Once()

		svc := userapi.NewService(store, fixedUserClock, principals, secrets)
		svc.SetNodeControlClient(client)
		status, data, err := svc.ListNodeDaemonAgentConnections(
			ctx,
			auth.RequestMetadata{},
			"node_1",
			true,
		)

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.JSONEq(t, `{
			"type":"agent_connections.list",
			"agent_connections":{"items":[
				{"id":"conn_unbound","remote_id":"remote_prod"},
				{"id":"conn_owned","remote_id":"remote_prod","cloud_agent_id":"agent_owned"}
			]}
		}`, string(data.(json.RawMessage)))
	})

	t.Run("agent connection list rejects a missing node id", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		principals := userapimocks.NewMockPrincipalResolver(t)
		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

		svc := userapi.NewService(
			userapimocks.NewMockStore(t),
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		_, _, err := svc.ListNodeDaemonAgentConnections(
			ctx,
			auth.RequestMetadata{},
			"",
			true,
		)

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusBadRequest, appErr.Status)
	})

	t.Run("agent connection list requires the tunnel remote identity", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self"}
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		client := &fakeNodeControlClient{remoteIDErr: errors.New("remote identity unavailable")}

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().GetNode(ctx, principal, "node_1").Return(node, nil).Once()

		svc := userapi.NewService(
			store,
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		svc.SetNodeControlClient(client)
		_, _, err := svc.ListNodeDaemonAgentConnections(
			ctx,
			auth.RequestMetadata{},
			"node_1",
			true,
		)

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusServiceUnavailable, appErr.Status)
		require.Zero(t, client.calls)
	})

	t.Run("agent connection list requires a control tunnel", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self"}
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().GetNode(ctx, principal, "node_1").Return(node, nil).Once()

		svc := userapi.NewService(
			store,
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		_, _, err := svc.ListNodeDaemonAgentConnections(
			ctx,
			auth.RequestMetadata{},
			"node_1",
			true,
		)

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusServiceUnavailable, appErr.Status)
	})

	t.Run("missing control tunnel returns service unavailable", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self"}
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().GetNode(ctx, principal, "node_1").Return(node, nil).Once()

		svc := userapi.NewService(
			store,
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		_, _, err := svc.GetNodeDaemonStatus(ctx, auth.RequestMetadata{}, "node_1")

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusServiceUnavailable, appErr.Status)
	})
}

func TestResetSessionRuntimeDispatchesCompareAndResetCommand(t *testing.T) {
	ctx := context.Background()
	principal := userPrincipal("usr_self", false)
	store := userapimocks.NewMockStore(t)
	principals := userapimocks.NewMockPrincipalResolver(t)
	secrets := userapimocks.NewMockSecretIssuer(t)
	client := &fakeNodeControlClient{
		remoteID: "remote_prod",
		commandAck: json.RawMessage(`{
			"command_id":"ctlcmd_1","ok":true,"status":"applied",
			"result":{"session_runtime_reset":{"status":"suppressed","projection_revision":9}}
		}`),
	}
	principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
	store.EXPECT().GetSession(ctx, principal, "sess_1").Return(domain.AgentSession{
		AgentID: "agent_1", SessionID: "sess_1", NodeID: "node_1", NativeID: "native_1",
		RuntimeStatus: domain.RuntimeStatusRunning, RuntimeTurnInstanceID: "turn_1",
	}, nil).Once()
	store.EXPECT().GetAgent(ctx, principal, "agent_1").Return(domain.Agent{
		AgentID: "agent_1", NodeID: "node_1", OwnerUserID: "usr_self",
		Metadata: json.RawMessage(`{"runtime":{"connection_id":"conn_1"}}`),
	}, nil).Once()
	secrets.EXPECT().New("ctlcmd").Return("ctlcmd_1", nil).Once()

	svc := userapi.NewService(store, fixedUserClock, principals, secrets)
	svc.SetNodeControlClient(client)
	status, data, err := svc.ResetSessionRuntime(
		ctx, auth.RequestMetadata{}, "agent_1", "sess_1", "turn_1",
	)

	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)
	response := data.(map[string]any)
	require.Equal(t, "accepted_pending", response["status"])
	require.Equal(t, "suppressed", response["reset_status"])
	require.Equal(t, "node_1", client.nodeID)
	command := client.command.(map[string]any)
	require.Equal(t, "session_runtime.reset", command["type"])
	reset := command["reset_session_runtime"].(map[string]any)
	require.Equal(t, "turn_1", reset["expected_turn_instance_id"])
	require.Equal(t, "native_1", reset["native_session_id"])
}

func TestResetSessionRuntimeRejectsUnsafeTargets(t *testing.T) {
	t.Run("Given an incomplete compare request then it is rejected before session lookup", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		svc := userapi.NewService(store, fixedUserClock, principals, userapimocks.NewMockSecretIssuer(t))

		_, _, err := svc.ResetSessionRuntime(ctx, auth.RequestMetadata{}, "agent_1", "sess_1", " ")

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusBadRequest, appErr.Status)
	})

	t.Run("Given the turn instance changed then it returns conflict without dispatch", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().GetSession(ctx, principal, "sess_1").Return(domain.AgentSession{
			AgentID: "agent_1", SessionID: "sess_1", RuntimeTurnInstanceID: "turn_new",
		}, nil).Once()
		svc := userapi.NewService(store, fixedUserClock, principals, userapimocks.NewMockSecretIssuer(t))

		_, _, err := svc.ResetSessionRuntime(ctx, auth.RequestMetadata{}, "agent_1", "sess_1", "turn_old")

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusConflict, appErr.Status)
	})

	t.Run("Given the canonical session has no native identity then it returns conflict", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().GetSession(ctx, principal, "sess_1").Return(domain.AgentSession{
			AgentID: "agent_1", SessionID: "sess_1", RuntimeTurnInstanceID: "turn_1",
		}, nil).Once()
		svc := userapi.NewService(store, fixedUserClock, principals, userapimocks.NewMockSecretIssuer(t))

		_, _, err := svc.ResetSessionRuntime(ctx, auth.RequestMetadata{}, "agent_1", "sess_1", "turn_1")

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusConflict, appErr.Status)
	})

	t.Run("Given the agent has no runtime connection binding then it returns conflict", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().GetSession(ctx, principal, "sess_1").Return(domain.AgentSession{
			AgentID: "agent_1", SessionID: "sess_1", NodeID: "node_1", NativeID: "native_1",
			RuntimeTurnInstanceID: "turn_1",
		}, nil).Once()
		store.EXPECT().GetAgent(ctx, principal, "agent_1").Return(domain.Agent{
			AgentID: "agent_1", NodeID: "node_1", Metadata: json.RawMessage(`not-json`),
		}, nil).Once()
		svc := userapi.NewService(store, fixedUserClock, principals, userapimocks.NewMockSecretIssuer(t))

		_, _, err := svc.ResetSessionRuntime(ctx, auth.RequestMetadata{}, "agent_1", "sess_1", "turn_1")

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusConflict, appErr.Status)
	})

	t.Run("Given node control is unavailable then it returns service unavailable", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().GetSession(ctx, principal, "sess_1").Return(domain.AgentSession{
			AgentID: "agent_1", SessionID: "sess_1", NodeID: "node_1", NativeID: "native_1",
			RuntimeTurnInstanceID: "turn_1",
		}, nil).Once()
		store.EXPECT().GetAgent(ctx, principal, "agent_1").Return(domain.Agent{
			AgentID: "agent_1", NodeID: "node_1",
			Metadata: json.RawMessage(`{"runtime":{"connection_id":"conn_1"}}`),
		}, nil).Once()
		svc := userapi.NewService(store, fixedUserClock, principals, userapimocks.NewMockSecretIssuer(t))

		_, _, err := svc.ResetSessionRuntime(ctx, auth.RequestMetadata{}, "agent_1", "sess_1", "turn_1")

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusServiceUnavailable, appErr.Status)
	})

	t.Run("Given paxd returns an invalid acknowledgement then it returns bad gateway", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		secrets := userapimocks.NewMockSecretIssuer(t)
		client := &fakeNodeControlClient{remoteID: "remote_prod", commandAck: json.RawMessage(`not-json`)}
		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().GetSession(ctx, principal, "sess_1").Return(domain.AgentSession{
			AgentID: "agent_1", SessionID: "sess_1", NodeID: "node_1", NativeID: "native_1",
			RuntimeTurnInstanceID: "turn_1",
		}, nil).Once()
		store.EXPECT().GetAgent(ctx, principal, "agent_1").Return(domain.Agent{
			AgentID: "agent_1", NodeID: "node_1",
			Metadata: json.RawMessage(`{"runtime":{"connection_id":"conn_1"}}`),
		}, nil).Once()
		secrets.EXPECT().New("ctlcmd").Return("ctlcmd_1", nil).Once()
		svc := userapi.NewService(store, fixedUserClock, principals, secrets)
		svc.SetNodeControlClient(client)

		_, _, err := svc.ResetSessionRuntime(ctx, auth.RequestMetadata{}, "agent_1", "sess_1", "turn_1")

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusBadGateway, appErr.Status)
	})

	t.Run("Given paxd rejects reset then manager returns the safe rejection", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		secrets := userapimocks.NewMockSecretIssuer(t)
		client := &fakeNodeControlClient{
			remoteID:   "remote_prod",
			commandAck: json.RawMessage(`{"ok":false,"status":"rejected","error":{"message":"reset denied"}}`),
		}
		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().GetSession(ctx, principal, "sess_1").Return(domain.AgentSession{
			AgentID: "agent_1", SessionID: "sess_1", NodeID: "node_1", NativeID: "native_1",
			RuntimeTurnInstanceID: "turn_1",
		}, nil).Once()
		store.EXPECT().GetAgent(ctx, principal, "agent_1").Return(domain.Agent{
			AgentID: "agent_1", NodeID: "node_1",
			Metadata: json.RawMessage(`{"runtime":{"connection_id":"conn_1"}}`),
		}, nil).Once()
		secrets.EXPECT().New("ctlcmd").Return("ctlcmd_1", nil).Once()
		svc := userapi.NewService(store, fixedUserClock, principals, secrets)
		svc.SetNodeControlClient(client)

		_, _, err := svc.ResetSessionRuntime(ctx, auth.RequestMetadata{}, "agent_1", "sess_1", "turn_1")

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusConflict, appErr.Status)
		require.Equal(t, "reset denied", appErr.Message)
	})

	t.Run("Given paxd reports a compare conflict then manager preserves the conflict", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		secrets := userapimocks.NewMockSecretIssuer(t)
		client := &fakeNodeControlClient{
			remoteID: "remote_prod",
			commandAck: json.RawMessage(`{
				"command_id":"ctlcmd_1","ok":true,"status":"applied",
				"result":{"session_runtime_reset":{"status":"conflict","projection_revision":10}}
			}`),
		}
		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().GetSession(ctx, principal, "sess_1").Return(domain.AgentSession{
			AgentID: "agent_1", SessionID: "sess_1", NodeID: "node_1", NativeID: "native_1",
			RuntimeTurnInstanceID: "turn_1",
		}, nil).Once()
		store.EXPECT().GetAgent(ctx, principal, "agent_1").Return(domain.Agent{
			AgentID: "agent_1", NodeID: "node_1", Metadata: json.RawMessage(`{"runtime":{"connection_id":"conn_1"}}`),
		}, nil).Once()
		secrets.EXPECT().New("ctlcmd").Return("ctlcmd_1", nil).Once()
		svc := userapi.NewService(store, fixedUserClock, principals, secrets)
		svc.SetNodeControlClient(client)

		_, _, err := svc.ResetSessionRuntime(ctx, auth.RequestMetadata{}, "agent_1", "sess_1", "turn_1")

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusConflict, appErr.Status)
	})
}

func TestUpdateNodeAgentSessionName(t *testing.T) {
	t.Run("Given a valid name then it trims and updates the session", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		session := domain.AgentSession{
			NodeID:    "node_1",
			AgentID:   "agent_1",
			SessionID: "sess_1",
		}
		name := "  Release planning  "
		trimmedName := "Release planning"
		req := domain.UpdateSessionRequest{
			NodeID:      "node_1",
			AgentID:     "agent_1",
			SessionID:   "sess_1",
			SessionName: &name,
		}
		expectedReq := req
		expectedReq.SessionName = &trimmedName
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().
			GetAgent(ctx, principal, "agent_1").
			Return(domain.Agent{AgentID: "agent_1", NodeID: "node_1", OwnerUserID: "usr_self"}, nil).
			Once()
		store.EXPECT().GetSession(ctx, principal, "sess_1").Return(session, nil).Once()
		store.EXPECT().
			UpdateNodeAgentSession(ctx, principal, expectedReq).
			Return(domain.AgentSession{
				NodeID:              "node_1",
				AgentID:             "agent_1",
				SessionID:           "sess_1",
				SessionName:         trimmedName,
				ReportedSessionName: "Reported name",
				NameIsCustom:        true,
			}, nil).
			Once()

		svc := userapi.NewService(
			store,
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		status, data, err := svc.UpdateNodeAgentSession(
			ctx,
			auth.RequestMetadata{},
			req,
		)

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, trimmedName, data.(domain.AgentSession).SessionName)
	})

	for _, tc := range []struct {
		name string
		req  domain.UpdateSessionRequest
	}{
		{
			name: "empty update",
			req: domain.UpdateSessionRequest{
				NodeID: "node_1", AgentID: "agent_1", SessionID: "sess_1",
			},
		},
		{
			name: "empty name",
			req: func() domain.UpdateSessionRequest {
				name := "   "
				return domain.UpdateSessionRequest{
					NodeID: "node_1", AgentID: "agent_1", SessionID: "sess_1", SessionName: &name,
				}
			}(),
		},
		{
			name: "name and reset",
			req: func() domain.UpdateSessionRequest {
				name := "Custom"
				return domain.UpdateSessionRequest{
					NodeID:          "node_1",
					AgentID:         "agent_1",
					SessionID:       "sess_1",
					SessionName:     &name,
					UseReportedName: true,
				}
			}(),
		},
		{
			name: "name over 120 characters",
			req: func() domain.UpdateSessionRequest {
				name := strings.Repeat("a", 121)
				return domain.UpdateSessionRequest{
					NodeID: "node_1", AgentID: "agent_1", SessionID: "sess_1", SessionName: &name,
				}
			}(),
		},
	} {
		t.Run("Given "+tc.name+" then it rejects the request", func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)

			_, _, err := svc.UpdateNodeAgentSession(ctx, auth.RequestMetadata{}, tc.req)

			require.Error(t, err)
			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		})
	}
}

func TestGetNodeDaemonCommand(t *testing.T) {
	ctx := context.Background()
	principal := userPrincipal("usr_self", false)
	node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self"}
	store := userapimocks.NewMockStore(t)
	principals := userapimocks.NewMockPrincipalResolver(t)
	secrets := userapimocks.NewMockSecretIssuer(t)
	client := &fakeNodeControlClient{
		result: json.RawMessage(`{
			"type":"command.get",
			"command":{"command_id":"cmd_1","status":"received"}
		}`),
	}

	principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
	store.EXPECT().GetNode(ctx, principal, "node_1").Return(node, nil).Once()
	secrets.EXPECT().New("ctlq").Return("ctlq_command_1", nil).Once()

	svc := userapi.NewService(store, fixedUserClock, principals, secrets)
	svc.SetNodeControlClient(client)
	status, _, err := svc.GetNodeDaemonCommand(
		ctx,
		auth.RequestMetadata{},
		"node_1",
		" cmd_1 ",
	)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, map[string]any{
		"type": "command.get",
		"get_command": map[string]any{
			"command_id": "cmd_1",
		},
	}, client.query)
}

func TestCreateNodeDaemonAgentConnection(t *testing.T) {
	t.Run("creates cloud agent and dispatches resolved harness command", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self"}
		agent := domain.Agent{
			AgentID:     "agent_1",
			NodeID:      "node_1",
			OwnerUserID: "usr_self",
			Name:        "work",
			AgentType:   "codex",
		}
		bootstrap := domain.MailboxMessage{MessageID: "msg_bootstrap", AgentID: "agent_1"}
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		secrets := userapimocks.NewMockSecretIssuer(t)
		client := &fakeNodeControlClient{
			remoteID: "remote_prod",
			result: json.RawMessage(`{
				"type":"harnesses.list",
				"harnesses":{"items":[{
					"harness":"codex",
					"state":"available",
					"command":["codex","--acp"]
				}]}
			}`),
			commandAck: json.RawMessage(`{
				"command_id":"cmd_create_1",
				"ok":true,
				"status":"received",
				"target_type":"agent_connection",
				"target_id":"conn_1",
				"desired_generation":1
			}`),
		}

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().GetNode(ctx, principal, "node_1").Return(node, nil).Once()
		secrets.EXPECT().New("ctlq").Return("ctlq_harnesses_1", nil).Once()
		store.EXPECT().CreateNodeAgent(
			ctx,
			principal,
			mock.MatchedBy(func(req domain.CreateAgentRequest) bool {
				return req.NodeID == "node_1" && req.Name == "work" && req.AgentType == "codex"
			}),
		).Return(agent, bootstrap, nil).Once()

		svc := userapi.NewService(store, fixedUserClock, principals, secrets)
		svc.SetNodeControlClient(client)
		status, rawData, err := svc.CreateNodeDaemonAgentConnection(
			ctx,
			auth.RequestMetadata{},
			domain.CreateNodeDaemonAgentConnectionRequest{
				NodeID:     "node_1",
				CommandID:  "cmd_create_1",
				Name:       "work",
				AgentType:  "codex",
				Harness:    "codex",
				InstanceID: "work",
				WorkingDir: "/workspace",
			},
		)

		require.NoError(t, err)
		require.Equal(t, http.StatusAccepted, status)
		data := rawData.(map[string]any)
		require.Equal(t, "agent_1", data["agent_id"])
		require.Equal(t, "conn_1", data["connection_id"])
		require.Equal(t, "received", data["command_status"])
		require.Equal(t, "acknowledged", data["dispatch_status"])
		require.Equal(t, "cmd_create_1", client.commandID)
		require.Equal(t, map[string]any{
			"command_id": "cmd_create_1",
			"type":       "agent_connection.create",
			"create_agent_connection": map[string]any{
				"remote_id":      "remote_prod",
				"name":           "work",
				"cloud_agent_id": "agent_1",
				"instance_id":    "work",
				"agent_type":     "codex",
				"harness":        "codex",
				"command":        []string{"codex", "--acp"},
				"working_dir":    "/workspace",
				"desired_state":  "running",
				"desired_slots":  2,
			},
		}, client.command)
	})

	t.Run(
		"explicit command skips harness query and preserves uncertain dispatch",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self"}
			agent := domain.Agent{AgentID: "agent_1", NodeID: "node_1", OwnerUserID: "usr_self"}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			client := &fakeNodeControlClient{
				remoteID:   "remote_prod",
				commandErr: context.DeadlineExceeded,
			}

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().GetNode(ctx, principal, "node_1").Return(node, nil).Once()
			store.EXPECT().CreateNodeAgent(
				ctx,
				principal,
				domain.CreateAgentRequest{NodeID: "node_1", Name: "codex", AgentType: "codex"},
			).Return(agent, domain.MailboxMessage{}, nil).Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			svc.SetNodeControlClient(client)
			status, rawData, err := svc.CreateNodeDaemonAgentConnection(
				ctx,
				auth.RequestMetadata{},
				domain.CreateNodeDaemonAgentConnectionRequest{
					NodeID:    "node_1",
					CommandID: "cmd_create_1",
					Harness:   "codex",
					Command:   []string{" codex ", "", " --acp "},
				},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusAccepted, status)
			data := rawData.(map[string]any)
			require.Equal(t, "agent_1", data["agent_id"])
			require.Equal(t, "unknown", data["dispatch_status"])
			require.Contains(t, data["dispatch_error"], "deadline exceeded")
			require.Zero(t, client.calls)
			command := client.command.(map[string]any)["create_agent_connection"].(map[string]any)
			require.Equal(t, []string{"codex", "--acp"}, command["command"])
		},
	)

	t.Run("missing reported remote id fails before creating cloud agent", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self"}
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		client := &fakeNodeControlClient{remoteIDErr: errors.New("remote id unavailable")}

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().GetNode(ctx, principal, "node_1").Return(node, nil).Once()

		svc := userapi.NewService(
			store,
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		svc.SetNodeControlClient(client)
		_, _, err := svc.CreateNodeDaemonAgentConnection(
			ctx,
			auth.RequestMetadata{},
			domain.CreateNodeDaemonAgentConnectionRequest{
				NodeID:    "node_1",
				CommandID: "cmd_create_1",
				Harness:   "codex",
			},
		)

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusServiceUnavailable, appErr.Status)
		require.Zero(t, client.commandCalls)
	})
}

func TestUpdateNodeDaemonAgentConnection(t *testing.T) {
	t.Run("resolves changed harness and dispatches update", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self"}
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		secrets := userapimocks.NewMockSecretIssuer(t)
		client := &fakeNodeControlClient{
			remoteID: "remote_prod",
			result: json.RawMessage(`{
				"type":"harnesses.list",
				"harnesses":{"items":[{
					"harness":"claude",
					"state":"available",
					"command":["claude","--acp"]
				}]}
			}`),
			commandAck: json.RawMessage(`{
				"command_id":"cmd_update_1",
				"ok":true,
				"status":"received",
				"target_id":"conn_1",
				"desired_generation":2
			}`),
		}
		harness := " claude "
		workingDir := " /project "
		desiredSlots := 4
		desiredState := " running "

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().GetNode(ctx, principal, "node_1").Return(node, nil).Once()
		secrets.EXPECT().New("ctlq").Return("ctlq_harnesses_1", nil).Once()

		svc := userapi.NewService(store, fixedUserClock, principals, secrets)
		svc.SetNodeControlClient(client)
		status, rawData, err := svc.UpdateNodeDaemonAgentConnection(
			ctx,
			auth.RequestMetadata{},
			domain.UpdateNodeDaemonAgentConnectionRequest{
				NodeID:       "node_1",
				ConnectionID: "conn_1",
				CommandID:    "cmd_update_1",
				Harness:      &harness,
				WorkingDir:   &workingDir,
				DesiredSlots: &desiredSlots,
				DesiredState: &desiredState,
			},
		)

		require.NoError(t, err)
		require.Equal(t, http.StatusAccepted, status)
		data := rawData.(map[string]any)
		require.Equal(t, "received", data["command_status"])
		require.Equal(t, int64(2), data["desired_generation"])
		require.Equal(t, map[string]any{
			"command_id": "cmd_update_1",
			"type":       "agent_connection.update",
			"update_agent_connection": map[string]any{
				"connection_id": "conn_1",
				"harness":       "claude",
				"command":       []string{"claude", "--acp"},
				"working_dir":   "/project",
				"desired_slots": 4,
				"desired_state": "running",
			},
		}, client.command)
	})

	t.Run("rejects an update without fields before dispatch", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		principals := userapimocks.NewMockPrincipalResolver(t)
		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		client := &fakeNodeControlClient{remoteID: "remote_prod"}

		svc := userapi.NewService(
			userapimocks.NewMockStore(t),
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		svc.SetNodeControlClient(client)
		_, _, err := svc.UpdateNodeDaemonAgentConnection(
			ctx,
			auth.RequestMetadata{},
			domain.UpdateNodeDaemonAgentConnectionRequest{
				NodeID:       "node_1",
				ConnectionID: "conn_1",
				CommandID:    "cmd_update_1",
			},
		)

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusBadRequest, appErr.Status)
		require.Zero(t, client.commandCalls)
	})

	t.Run("rejects an invalid desired slot count before dispatch", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		principals := userapimocks.NewMockPrincipalResolver(t)
		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		client := &fakeNodeControlClient{remoteID: "remote_prod"}
		desiredSlots := 17

		svc := userapi.NewService(
			userapimocks.NewMockStore(t),
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		svc.SetNodeControlClient(client)
		_, _, err := svc.UpdateNodeDaemonAgentConnection(
			ctx,
			auth.RequestMetadata{},
			domain.UpdateNodeDaemonAgentConnectionRequest{
				NodeID:       "node_1",
				ConnectionID: "conn_1",
				CommandID:    "cmd_update_1",
				DesiredSlots: &desiredSlots,
			},
		)

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusBadRequest, appErr.Status)
		require.Zero(t, client.commandCalls)
	})

	t.Run("rejects an invalid desired state before dispatch", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		principals := userapimocks.NewMockPrincipalResolver(t)
		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		client := &fakeNodeControlClient{remoteID: "remote_prod"}
		desiredState := "deleted"

		svc := userapi.NewService(
			userapimocks.NewMockStore(t),
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		svc.SetNodeControlClient(client)
		_, _, err := svc.UpdateNodeDaemonAgentConnection(
			ctx,
			auth.RequestMetadata{},
			domain.UpdateNodeDaemonAgentConnectionRequest{
				NodeID:       "node_1",
				ConnectionID: "conn_1",
				CommandID:    "cmd_update_1",
				DesiredState: &desiredState,
			},
		)

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusBadRequest, appErr.Status)
		require.Zero(t, client.commandCalls)
	})
}

func TestStopNodeDaemonAgentConnection(t *testing.T) {
	ctx := context.Background()
	principal := userPrincipal("usr_self", false)
	node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self"}
	store := userapimocks.NewMockStore(t)
	principals := userapimocks.NewMockPrincipalResolver(t)
	client := &fakeNodeControlClient{
		remoteID: "remote_prod",
		commandAck: json.RawMessage(`{
			"command_id":"cmd_stop_1",
			"ok":true,
			"status":"received",
			"target_id":"conn_1",
			"desired_generation":3
		}`),
	}

	principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
	store.EXPECT().GetNode(ctx, principal, "node_1").Return(node, nil).Once()

	svc := userapi.NewService(
		store,
		fixedUserClock,
		principals,
		userapimocks.NewMockSecretIssuer(t),
	)
	svc.SetNodeControlClient(client)
	status, rawData, err := svc.StopNodeDaemonAgentConnection(
		ctx,
		auth.RequestMetadata{},
		domain.NodeDaemonAgentConnectionActionRequest{
			NodeID:       "node_1",
			ConnectionID: "conn_1",
			CommandID:    "cmd_stop_1",
		},
	)

	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)
	data := rawData.(map[string]any)
	require.Equal(t, "received", data["command_status"])
	require.Equal(t, int64(3), data["desired_generation"])
	require.Equal(t, map[string]any{
		"command_id": "cmd_stop_1",
		"type":       "agent_connection.update",
		"update_agent_connection": map[string]any{
			"connection_id": "conn_1",
			"desired_state": "stopped",
		},
	}, client.command)
}

func TestRestartNodeDaemon(t *testing.T) {
	ctx := context.Background()
	principal := userPrincipal("usr_self", false)
	node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self"}
	store := userapimocks.NewMockStore(t)
	principals := userapimocks.NewMockPrincipalResolver(t)
	grace := 12
	client := &fakeNodeControlClient{
		remoteID: "remote_prod",
		commandAck: json.RawMessage(`{
			"command_id":"cmd_restart_paxd_1",
			"ok":true,
			"status":"received",
			"target_type":"paxd"
		}`),
	}

	principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
	store.EXPECT().GetNode(ctx, principal, "node_1").Return(node, nil).Once()
	svc := userapi.NewService(
		store,
		fixedUserClock,
		principals,
		userapimocks.NewMockSecretIssuer(t),
	)
	svc.SetNodeControlClient(client)
	status, rawData, err := svc.RestartNodeDaemon(
		ctx,
		auth.RequestMetadata{},
		domain.RestartNodeDaemonRequest{
			NodeID: "node_1", CommandID: "cmd_restart_paxd_1",
			ShutdownGraceSeconds: &grace, Reason: " operator requested ",
		},
	)

	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)
	data := rawData.(map[string]any)
	require.Equal(t, "received", data["command_status"])
	require.Equal(t, "acknowledged", data["dispatch_status"])
	require.Equal(t, map[string]any{
		"command_id": "cmd_restart_paxd_1",
		"type":       "paxd.restart",
		"restart_paxd": map[string]any{
			"mode": "immediate", "shutdown_grace_seconds": 12, "reason": "operator requested",
		},
	}, client.command)
}

func TestRestartNodeDaemonRejectsUnsupportedPolicy(t *testing.T) {
	ctx := context.Background()
	principal := userPrincipal("usr_self", false)
	principals := userapimocks.NewMockPrincipalResolver(t)
	principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Times(2)
	svc := userapi.NewService(
		userapimocks.NewMockStore(t),
		fixedUserClock,
		principals,
		userapimocks.NewMockSecretIssuer(t),
	)
	grace := 61
	for _, req := range []domain.RestartNodeDaemonRequest{
		{NodeID: "node_1", CommandID: "cmd_bad_mode", Mode: "when_idle"},
		{NodeID: "node_1", CommandID: "cmd_bad_grace", ShutdownGraceSeconds: &grace},
	} {
		status, _, err := svc.RestartNodeDaemon(ctx, auth.RequestMetadata{}, req)
		require.Error(t, err)
		require.Zero(t, status)
		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusBadRequest, appErr.Status)
	}
}

func TestRestartNodeDaemonAgentConnection(t *testing.T) {
	ctx := context.Background()
	principal := userPrincipal("usr_self", false)
	node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self"}
	store := userapimocks.NewMockStore(t)
	principals := userapimocks.NewMockPrincipalResolver(t)
	client := &fakeNodeControlClient{
		remoteID: "remote_prod",
		commandAck: json.RawMessage(`{
			"command_id":"cmd_restart_1",
			"ok":true,
			"status":"received",
			"target_id":"conn_1",
			"desired_generation":3
		}`),
	}

	principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
	store.EXPECT().GetNode(ctx, principal, "node_1").Return(node, nil).Once()

	svc := userapi.NewService(
		store,
		fixedUserClock,
		principals,
		userapimocks.NewMockSecretIssuer(t),
	)
	svc.SetNodeControlClient(client)
	status, rawData, err := svc.RestartNodeDaemonAgentConnection(
		ctx,
		auth.RequestMetadata{},
		domain.NodeDaemonAgentConnectionActionRequest{
			NodeID:       "node_1",
			ConnectionID: "conn_1",
			CommandID:    "cmd_restart_1",
		},
	)

	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)
	data := rawData.(map[string]any)
	require.Equal(t, "received", data["command_status"])
	require.Equal(t, map[string]any{
		"command_id": "cmd_restart_1",
		"type":       "agent_connection.restart",
		"restart_agent_connection": map[string]any{
			"connection_id": "conn_1",
		},
	}, client.command)
}

func TestRemoveNodeDaemonAgentConnection(t *testing.T) {
	ctx := context.Background()
	principal := userPrincipal("usr_self", false)
	node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self"}
	store := userapimocks.NewMockStore(t)
	principals := userapimocks.NewMockPrincipalResolver(t)
	client := &fakeNodeControlClient{
		remoteID: "remote_prod",
		commandAck: json.RawMessage(`{
			"command_id":"cmd_remove_1",
			"ok":true,
			"status":"received",
			"target_id":"conn_1",
			"desired_generation":4
		}`),
	}

	principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
	store.EXPECT().GetNode(ctx, principal, "node_1").Return(node, nil).Once()

	svc := userapi.NewService(
		store,
		fixedUserClock,
		principals,
		userapimocks.NewMockSecretIssuer(t),
	)
	svc.SetNodeControlClient(client)
	status, rawData, err := svc.RemoveNodeDaemonAgentConnection(
		ctx,
		auth.RequestMetadata{},
		domain.NodeDaemonAgentConnectionActionRequest{
			NodeID:       "node_1",
			ConnectionID: "conn_1",
			CommandID:    "cmd_remove_1",
		},
	)

	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)
	data := rawData.(map[string]any)
	require.Equal(t, "received", data["command_status"])
	require.Equal(t, map[string]any{
		"command_id": "cmd_remove_1",
		"type":       "agent_connection.delete",
		"delete_agent_connection": map[string]any{
			"connection_id": "conn_1",
		},
	}, client.command)
}

func TestCreateRegistrationToken(t *testing.T) {
	t.Run(
		"Given a non-admin principal when minting for another user then it returns forbidden before loading that user",
		func(t *testing.T) {
			ctx := context.Background()
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().
				Principal(ctx, auth.RequestMetadata{}).
				Return(userPrincipal("usr_self", false), nil).
				Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.CreateRegistrationToken(
				ctx,
				auth.RequestMetadata{},
				domain.CreateRegistrationTokenRequest{OwnerEmail: "other@example.com"},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusForbidden, appErr.Status)
		},
	)

	t.Run(
		"Given an admin principal when minting for an owner email then it stores the hashed token with expiration",
		func(t *testing.T) {
			ctx := context.Background()
			meta := auth.NewRequestMetadata(map[string]string{"Cf-Access-Jwt-Assertion": "jwt"})
			principal := userPrincipal("usr_admin", true)
			owner := domain.User{UserID: "usr_owner", Email: "owner@example.com"}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			secrets := userapimocks.NewMockSecretIssuer(t)

			principals.EXPECT().Principal(ctx, meta).Return(principal, nil).Once()
			store.EXPECT().GetUserByEmail(ctx, "owner@example.com").Return(owner, nil).Once()
			secrets.EXPECT().New("reg").Return("reg_raw", nil).Once()
			secrets.EXPECT().Hash("reg_raw").Return("hashed_reg").Once()
			store.EXPECT().
				CreateRegistrationToken(ctx, "usr_owner", "hashed_reg", mock.MatchedBy(func(expiresAt *time.Time) bool {
					return expiresAt != nil && expiresAt.Equal(fixedUserNow().Add(2*time.Hour))
				})).
				Return(nil).
				Once()

			svc := userapi.NewService(store, fixedUserClock, principals, secrets)
			status, data, err := svc.CreateRegistrationToken(
				ctx,
				meta,
				domain.CreateRegistrationTokenRequest{
					OwnerEmail:       "owner@example.com",
					ExpiresInSeconds: 7200,
				},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			resp := data.(domain.CreateRegistrationTokenResponse)
			require.Equal(t, "reg_raw", resp.Token)
			require.Equal(t, "usr_owner", resp.OwnerUserID)
			require.NotNil(t, resp.ExpiresAt)
			require.True(t, resp.ExpiresAt.Equal(fixedUserNow().Add(2*time.Hour)))
		},
	)
}

func TestCreateUserAPIKey(t *testing.T) {
	t.Run(
		"Given a valid principal when creating an API key then it stores only hash and prefix and returns the raw key once",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			secrets := userapimocks.NewMockSecretIssuer(t)
			keyMeta := domain.UserAPIKey{
				KeyID:       "key_1",
				OwnerUserID: "usr_self",
				Name:        "laptop",
				Prefix:      "paxu_123",
			}

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			secrets.EXPECT().New("paxu").Return("paxu_1234567890", nil).Once()
			secrets.EXPECT().Hash("paxu_1234567890").Return("hashed_key").Once()
			secrets.EXPECT().Prefix("paxu_1234567890").Return("paxu_123").Once()
			store.EXPECT().
				CreateUserAPIKey(ctx, principal, "laptop", "hashed_key", "paxu_123").
				Return(keyMeta, nil).
				Once()

			svc := userapi.NewService(store, fixedUserClock, principals, secrets)
			status, data, err := svc.CreateUserAPIKey(
				ctx,
				auth.RequestMetadata{},
				domain.CreateUserAPIKeyRequest{Name: "laptop"},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(
				t,
				domain.CreateUserAPIKeyResponse{APIKey: keyMeta, Key: "paxu_1234567890"},
				data,
			)
		},
	)
}

func TestTeamService(t *testing.T) {
	t.Run(
		"Given a valid principal when creating a team then it stores the owner member",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_owner", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			secrets := userapimocks.NewMockSecretIssuer(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			secrets.EXPECT().New("team").Return("team_1", nil).Once()
			store.EXPECT().
				CreateTeam(
					ctx,
					mock.MatchedBy(func(team domain.Team) bool {
						return team.TeamID == "team_1" &&
							team.OwnerUserID == "usr_owner" &&
							team.Name == "Core" &&
							team.Description == "Build platform work" &&
							team.Status == domain.TeamStatusActive &&
							team.CreatedAt.Equal(fixedUserNow())
					}),
					mock.MatchedBy(func(member domain.TeamMember) bool {
						return member.TeamID == "team_1" &&
							member.UserID == "usr_owner" &&
							member.Role == domain.TeamRoleOwner &&
							member.Status == domain.TeamMemberStatusActive &&
							member.JoinedAt.Equal(fixedUserNow())
					}),
				).
				Return(domain.Team{
					TeamID:      "team_1",
					OwnerUserID: "usr_owner",
					Name:        "Core",
					Description: "Build platform work",
					Status:      domain.TeamStatusActive,
					CreatedAt:   fixedUserNow(),
				}, nil).
				Once()

			svc := userapi.NewService(store, fixedUserClock, principals, secrets)
			status, data, err := svc.CreateTeam(
				ctx,
				auth.RequestMetadata{},
				domain.CreateTeamRequest{
					Name:        " Core ",
					Description: " Build platform work ",
				},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			resp := data.(map[string]any)
			team := resp["team"].(domain.Team)
			require.Equal(t, "team_1", team.TeamID)
			require.Equal(t, "Build platform work", team.Description)
		},
	)

	t.Run(
		"Given a team ID when listing team sent invites then it returns that team's invites",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_owner", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			invites := []domain.TeamInvite{{
				InviteID: "tinv_1",
				TeamID:   "team_1",
				Email:    "operator@example.com",
				Role:     domain.TeamRoleOperator,
				Status:   domain.TeamInviteStatusPending,
			}}

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				ListTeamSentInvites(ctx, principal, "team_1").
				Return(invites, nil).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.ListTeamSentInvites(ctx, auth.RequestMetadata{}, "team_1")

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, invites, data.(map[string]any)["invites"])
		},
	)

	t.Run(
		"Given an owner invite role when creating a team invite then it returns bad request",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_owner", false)
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.CreateTeamInvite(
				ctx,
				auth.RequestMetadata{},
				"team_1",
				domain.CreateTeamInviteRequest{Email: "operator@example.com", Role: "owner"},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given an empty invite role when creating a team invite then it defaults to member",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_owner", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			secrets := userapimocks.NewMockSecretIssuer(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				GetUserByEmail(ctx, "operator@example.com").
				Return(domain.User{}, domain.ErrNotFound).
				Once()
			secrets.EXPECT().New("tinv").Return("tinv_1", nil).Once()
			store.EXPECT().
				CreateTeamInvite(
					ctx,
					principal,
					mock.MatchedBy(func(invite domain.TeamInvite) bool {
						return invite.InviteID == "tinv_1" &&
							invite.TeamID == "team_1" &&
							invite.Email == "operator@example.com" &&
							invite.Role == domain.TeamRoleMember &&
							invite.Status == domain.TeamInviteStatusPending &&
							invite.InvitedByUserID == principal.User.UserID &&
							invite.CreatedAt.Equal(fixedUserNow())
					}),
				).
				Return(domain.TeamInvite{
					InviteID: "tinv_1",
					TeamID:   "team_1",
					Email:    "operator@example.com",
					Role:     domain.TeamRoleMember,
					Status:   domain.TeamInviteStatusPending,
				}, nil).
				Once()

			svc := userapi.NewService(store, fixedUserClock, principals, secrets)
			status, data, err := svc.CreateTeamInvite(
				ctx,
				auth.RequestMetadata{},
				"team_1",
				domain.CreateTeamInviteRequest{Email: "operator@example.com"},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			invite := data.(map[string]any)["invite"].(domain.TeamInvite)
			require.Equal(t, domain.TeamRoleMember, invite.Role)
		},
	)

	t.Run(
		"Given an empty role when updating a team member then it returns bad request",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_owner", false)
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.UpdateTeamMemberRole(
				ctx,
				auth.RequestMetadata{},
				"team_1",
				"usr_member",
				domain.UpdateTeamMemberRoleRequest{},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given a too long team description when creating a team then it returns bad request before minting IDs",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_owner", false)
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.CreateTeam(ctx, auth.RequestMetadata{}, domain.CreateTeamRequest{
				Name:        "Core",
				Description: strings.Repeat("x", 1001),
			})

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given an invite for the current user's email then it rejects the self invite",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_owner", false)
			principal.User.Email = "owner@example.com"
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.CreateTeamInvite(
				ctx,
				auth.RequestMetadata{},
				"team_1",
				domain.CreateTeamInviteRequest{Email: " OWNER@example.com "},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given team agent fields with whitespace and case then it normalizes before storing",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_owner", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				AddTeamAgent(
					ctx,
					principal,
					"team_1",
					mock.MatchedBy(func(req domain.AddTeamAgentRequest) bool {
						return req.AgentID == "agent_1" &&
							req.Identity == "reviewer:primary" &&
							req.Role == "reviewer" &&
							req.DisplayName == "Review Bot" &&
							req.Description == "Reviews code."
					}),
					fixedUserNow(),
				).
				Return(domain.TeamAgent{
					TeamID:      "team_1",
					AgentID:     "agent_1",
					Identity:    "reviewer:primary",
					Role:        "reviewer",
					DisplayName: "Review Bot",
					Description: "Reviews code.",
					AddedAt:     fixedUserNow(),
				}, nil).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.AddTeamAgent(
				ctx,
				auth.RequestMetadata{},
				"team_1",
				domain.AddTeamAgentRequest{
					AgentID:     "agent_1",
					Identity:    " Reviewer:Primary ",
					Role:        " Reviewer ",
					DisplayName: " Review Bot ",
					Description: " Reviews code. ",
				},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			agent := data.(map[string]any)["agent"].(domain.TeamAgent)
			require.Equal(t, "reviewer:primary", agent.Identity)
		},
	)

	t.Run(
		"Given an invalid team agent identity then it returns bad request",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_owner", false)
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.AddTeamAgent(
				ctx,
				auth.RequestMetadata{},
				"team_1",
				domain.AddTeamAgentRequest{AgentID: "agent_1", Identity: "bad identity"},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given a member leaves a team then it removes that same principal from the team",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_member", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			member := domain.TeamMember{
				TeamID: "team_1",
				UserID: "usr_member",
				Role:   domain.TeamRoleMember,
				Status: domain.TeamMemberStatusRemoved,
			}

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				RemoveTeamMember(ctx, principal, "team_1", "usr_member", fixedUserNow()).
				Return(member, nil).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.LeaveTeam(ctx, auth.RequestMetadata{}, "team_1")

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, member, data.(map[string]any)["member"])
		},
	)

	t.Run(
		"Given a pending invite when accepting then it delegates with the fixed clock",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_member", false)
			invite := domain.TeamInvite{
				InviteID: "tinv_1",
				TeamID:   "team_1",
				Status:   domain.TeamInviteStatusAccepted,
			}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().AcceptTeamInvite(ctx, principal, "tinv_1", fixedUserNow()).
				Return(invite, nil).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.AcceptTeamInvite(ctx, auth.RequestMetadata{}, "tinv_1")

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, invite, data.(map[string]any)["invite"])
		},
	)

	t.Run(
		"Given a pending invite when declining then it delegates with the fixed clock",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_member", false)
			invite := domain.TeamInvite{
				InviteID: "tinv_1",
				TeamID:   "team_1",
				Status:   domain.TeamInviteStatusDeclined,
			}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().DeclineTeamInvite(ctx, principal, "tinv_1", fixedUserNow()).
				Return(invite, nil).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.DeclineTeamInvite(ctx, auth.RequestMetadata{}, "tinv_1")

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, invite, data.(map[string]any)["invite"])
		},
	)

	t.Run(
		"Given a team invite when canceling then it delegates with team and invite IDs",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_owner", false)
			invite := domain.TeamInvite{
				InviteID: "tinv_1",
				TeamID:   "team_1",
				Status:   domain.TeamInviteStatusCanceled,
			}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().CancelTeamInvite(ctx, principal, "team_1", "tinv_1", fixedUserNow()).
				Return(invite, nil).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.CancelTeamInvite(
				ctx,
				auth.RequestMetadata{},
				"team_1",
				"tinv_1",
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, invite, data.(map[string]any)["invite"])
		},
	)
}

func TestAgents(t *testing.T) {
	t.Run(
		"Given a valid principal when listing agents then it returns visible agents from the store",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			agents := []domain.Agent{{
				AgentID:     "agent_1",
				OwnerUserID: "usr_self",
				Name:        "codex",
			}}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().ListAgents(ctx, principal).Return(agents, nil).Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.ListAgents(ctx, auth.RequestMetadata{}, "")

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, agents, data.(map[string]any)["agents"])
		},
	)

	t.Run(
		"Given owned scope when listing agents then it excludes team-visible agents owned by others",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			agents := []domain.Agent{
				{AgentID: "agent_owned", OwnerUserID: "usr_self"},
				{AgentID: "agent_shared", OwnerUserID: "usr_other"},
			}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().ListAgents(ctx, principal).Return(agents, nil).Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.ListAgents(ctx, auth.RequestMetadata{}, "owned")

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, agents[:1], data.(map[string]any)["agents"])
		},
	)

	t.Run(
		"Given an invalid scope when listing agents then it returns bad request",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.ListAgents(ctx, auth.RequestMetadata{}, "all")

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given an empty agent ID when getting an agent then it returns bad request",
		func(t *testing.T) {
			ctx := context.Background()
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().
				Principal(ctx, auth.RequestMetadata{}).
				Return(userPrincipal("usr_self", false), nil).
				Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.GetAgent(ctx, auth.RequestMetadata{}, "")

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given a visible agent when getting an agent then it returns that agent",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			agent := domain.Agent{AgentID: "agent_1", OwnerUserID: "usr_self"}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().GetAgent(ctx, principal, "agent_1").Return(agent, nil).Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.GetAgent(ctx, auth.RequestMetadata{}, "agent_1")

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, agent, data)
		},
	)

	t.Run("Given a valid agent delete then it delegates to storage", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		agent := domain.Agent{AgentID: "agent_1", OwnerUserID: "usr_self"}
		req := domain.DeleteAgentRequest{AgentID: "agent_1"}
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().DeleteAgent(ctx, principal, req).Return(agent, nil).Once()

		svc := userapi.NewService(
			store,
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		status, data, err := svc.DeleteAgent(ctx, auth.RequestMetadata{}, "agent_1")

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, agent, data)
	})

	t.Run(
		"Given an empty agent ID when deleting an agent then it returns bad request",
		func(t *testing.T) {
			ctx := context.Background()
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().
				Principal(ctx, auth.RequestMetadata{}).
				Return(userPrincipal("usr_self", false), nil).
				Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.DeleteAgent(ctx, auth.RequestMetadata{}, "")

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given a node-scoped agent request with a different node then it returns not found",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				GetAgent(ctx, principal, "agent_1").
				Return(domain.Agent{AgentID: "agent_1", NodeID: "node_1", OwnerUserID: "usr_self"}, nil).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.GetNodeAgent(
				ctx,
				auth.RequestMetadata{},
				"node_2",
				"agent_1",
			)

			require.ErrorIs(t, err, domain.ErrNotFound)
		},
	)

	t.Run(
		"Given a visible session under an agent when getting that agent session then it returns the session",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			session := domain.AgentSession{AgentID: "agent_1", SessionID: "sess_1"}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().GetSession(ctx, principal, "sess_1").Return(session, nil).Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.GetAgentSession(
				ctx,
				auth.RequestMetadata{},
				"agent_1",
				"sess_1",
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, session, data)
		},
	)

	t.Run(
		"Given a session under another agent when getting that agent session then it returns not found",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				GetSession(ctx, principal, "sess_1").
				Return(domain.AgentSession{AgentID: "agent_2", SessionID: "sess_1"}, nil).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.GetAgentSession(
				ctx,
				auth.RequestMetadata{},
				"agent_1",
				"sess_1",
			)

			require.ErrorIs(t, err, domain.ErrNotFound)
		},
	)

	t.Run(
		"Given a legacy session without node ID under a matching node agent then it returns the session",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			session := domain.AgentSession{AgentID: "agent_1", SessionID: "sess_1"}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				GetAgent(ctx, principal, "agent_1").
				Return(domain.Agent{AgentID: "agent_1", NodeID: "node_1", OwnerUserID: "usr_self"}, nil).
				Once()
			store.EXPECT().GetSession(ctx, principal, "sess_1").Return(session, nil).Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.GetNodeAgentSession(
				ctx,
				auth.RequestMetadata{},
				"node_1",
				"agent_1",
				"sess_1",
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, session, data)
		},
	)

	t.Run(
		"Given a node-scoped session under a different node then it returns not found",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				GetAgent(ctx, principal, "agent_1").
				Return(domain.Agent{AgentID: "agent_1", NodeID: "node_1", OwnerUserID: "usr_self"}, nil).
				Once()
			store.EXPECT().
				GetSession(ctx, principal, "sess_1").
				Return(domain.AgentSession{
					NodeID:    "node_2",
					AgentID:   "agent_1",
					SessionID: "sess_1",
				}, nil).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.GetNodeAgentSession(
				ctx,
				auth.RequestMetadata{},
				"node_1",
				"agent_1",
				"sess_1",
			)

			require.ErrorIs(t, err, domain.ErrNotFound)
		},
	)

	t.Run(
		"Given node-scoped sessions then it filters out sessions from other nodes",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			sessions := []domain.AgentSession{
				{NodeID: "node_1", AgentID: "agent_1", SessionID: "sess_1"},
				{AgentID: "agent_1", SessionID: "sess_legacy"},
				{NodeID: "node_2", AgentID: "agent_1", SessionID: "sess_2"},
			}

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				GetAgent(ctx, principal, "agent_1").
				Return(domain.Agent{AgentID: "agent_1", NodeID: "node_1", OwnerUserID: "usr_self"}, nil).
				Once()
			store.EXPECT().ListAgentSessions(ctx, principal, "agent_1").Return(sessions, nil).Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.ListNodeAgentSessions(
				ctx,
				auth.RequestMetadata{},
				"node_1",
				"agent_1",
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(
				t,
				map[string]any{"sessions": []domain.AgentSession{sessions[0], sessions[1]}},
				data,
			)
		},
	)
}

func TestListSessions(t *testing.T) {
	t.Run(
		"Given owner sessions request when listing then it passes normalized pagination to store",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			expected := domain.ListSessionsResult{
				Sessions: []domain.AgentSession{{AgentID: "agent_1", SessionID: "sess_1"}},
				Pagination: domain.Pagination{
					PageNum:    2,
					PageSize:   25,
					Total:      51,
					TotalPages: 3,
				},
			}

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				ListSessions(
					ctx,
					principal,
					domain.ListSessionsFilter{
						OwnerUserID:      "usr_self",
						NodeIDs:          []string{"node_1", "node_2"},
						AgentIDs:         []string{"agent_1", "agent_2"},
						PrimaryProjectID: "proj_1",
						PageSize:         25,
						PageNum:          2,
					},
				).
				Return(expected, nil).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.ListSessions(
				ctx,
				auth.RequestMetadata{},
				"self",
				"node_1,node_2,node_1",
				"agent_1, agent_2",
				" proj_1 ",
				25,
				2,
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, expected, data)
		},
	)

	t.Run(
		"Given large page size when listing then it clamps the page size",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				ListSessions(
					ctx,
					principal,
					domain.ListSessionsFilter{
						OwnerUserID: "usr_self",
						PageSize:    200,
						PageNum:     1,
					},
				).
				Return(domain.ListSessionsResult{}, nil).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, _, err := svc.ListSessions(
				ctx,
				auth.RequestMetadata{},
				"usr_self",
				"",
				"",
				"",
				900,
				0,
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
		},
	)

	t.Run(
		"Given a different owner when listing then it returns not found before querying store",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.ListSessions(
				ctx,
				auth.RequestMetadata{},
				"usr_other",
				"",
				"",
				"",
				50,
				1,
			)

			require.ErrorIs(t, err, domain.ErrNotFound)
		},
	)
}

func TestCurrentUserAndAPIKeyListing(t *testing.T) {
	t.Run(
		"Given a valid principal when getting current user then it returns identity fields",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", true)
			principal.User.Email = "self@example.com"
			principal.User.DisplayName = "Self"
			principal.User.Role = "admin"
			principal.User.CreatedAt = fixedUserNow()
			lastSeenAt := fixedUserNow().Add(time.Minute)
			principal.User.LastSeenAt = &lastSeenAt
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.CurrentUser(ctx, auth.RequestMetadata{})

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			user := data.(map[string]any)["user"].(map[string]any)
			require.Equal(t, "usr_self", user["user_id"])
			require.Equal(t, "self@example.com", user["email"])
			require.Equal(t, true, user["is_admin"])
		},
	)

	t.Run(
		"Given a valid principal when listing API keys then it delegates to the store",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			keys := []domain.UserAPIKey{{
				KeyID:       "key_1",
				OwnerUserID: "usr_self",
				Name:        "laptop",
				Prefix:      "paxu_123",
			}}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().ListUserAPIKeys(ctx, principal).Return(keys, nil).Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.ListUserAPIKeys(ctx, auth.RequestMetadata{})

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, keys, data.(map[string]any)["api_keys"])
		},
	)

	t.Run(
		"Given an empty API key route when revoking then it returns not found",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.RevokeUserAPIKey(ctx, auth.RequestMetadata{}, "")

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusNotFound, appErr.Status)
		},
	)
}

func TestNodeService(t *testing.T) {
	t.Run(
		"Given a valid principal when listing nodes then it returns visible nodes",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			nodes := []domain.Node{{NodeID: "node_1", OwnerUserID: "usr_self", Name: "workstation"}}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().ListNodes(ctx, principal).Return(nodes, nil).Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.ListNodes(ctx, auth.RequestMetadata{})

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, nodes, data.(map[string]any)["nodes"])
		},
	)

	t.Run(
		"Given an empty node ID when getting node then it returns bad request",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.GetNode(ctx, auth.RequestMetadata{}, "")

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run("Given a valid node update then it delegates to storage", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		req := domain.UpdateNodeRequest{NodeID: "node_1", Name: "workstation"}
		node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self", Name: "workstation"}
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().UpdateNode(ctx, principal, req).Return(node, nil).Once()

		svc := userapi.NewService(
			store,
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		status, data, err := svc.UpdateNode(ctx, auth.RequestMetadata{}, req)

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, node, data)
	})

	t.Run("Given a valid node delete then it delegates to storage", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		req := domain.DeleteNodeRequest{NodeID: "node_1"}
		node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self", Name: "workstation"}
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().DeleteNode(ctx, principal, req).Return(node, nil).Once()

		svc := userapi.NewService(
			store,
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		status, data, err := svc.DeleteNode(ctx, auth.RequestMetadata{}, req)

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, node, data)
	})

	t.Run(
		"Given an empty node ID when deleting node then it returns bad request",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.DeleteNode(ctx, auth.RequestMetadata{}, domain.DeleteNodeRequest{})

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given a node agent create without node ID then it returns bad request",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.CreateNodeAgent(
				ctx,
				auth.RequestMetadata{},
				domain.CreateAgentRequest{},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given a valid node agent create then it returns agent and bootstrap message",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			req := domain.CreateAgentRequest{NodeID: "node_1", Name: "codex"}
			agent := domain.Agent{AgentID: "agent_1", NodeID: "node_1", OwnerUserID: "usr_self"}
			bootstrap := domain.MailboxMessage{MessageID: "msg_bootstrap", AgentID: "agent_1"}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().CreateNodeAgent(ctx, principal, req).Return(agent, bootstrap, nil).Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.CreateNodeAgent(ctx, auth.RequestMetadata{}, req)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			resp := data.(map[string]any)
			require.Equal(t, agent, resp["agent"])
			require.Equal(t, bootstrap, resp["bootstrap_message"])
		},
	)

	t.Run("Given a valid node agent delete then it delegates to storage", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_self", false)
		req := domain.DeleteAgentRequest{NodeID: "node_1", AgentID: "agent_1"}
		agent := domain.Agent{AgentID: "agent_1", NodeID: "node_1", OwnerUserID: "usr_self"}
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().DeleteNodeAgent(ctx, principal, req).Return(agent, nil).Once()

		svc := userapi.NewService(
			store,
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		status, data, err := svc.DeleteNodeAgent(ctx, auth.RequestMetadata{}, req)

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, agent, data)
	})

	t.Run(
		"Given a missing node agent delete target then it returns bad request",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.DeleteNodeAgent(ctx, auth.RequestMetadata{}, domain.DeleteAgentRequest{
				NodeID: "node_1",
			})

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)
}

func TestMailbox(t *testing.T) {
	t.Run(
		"Given missing agent ID when creating mailbox message then it returns bad request",
		func(t *testing.T) {
			ctx := context.Background()
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().
				Principal(ctx, auth.RequestMetadata{}).
				Return(userPrincipal("usr_self", false), nil).
				Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.CreateMailboxMessage(
				ctx,
				auth.RequestMetadata{},
				domain.CreateMailboxRequest{Message: "hello"},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given missing session ID when creating mailbox message then it returns bad request",
		func(t *testing.T) {
			ctx := context.Background()
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().
				Principal(ctx, auth.RequestMetadata{}).
				Return(userPrincipal("usr_self", false), nil).
				Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.CreateMailboxMessage(
				ctx,
				auth.RequestMetadata{},
				domain.CreateMailboxRequest{AgentID: "agent_1", Message: "hello"},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given no mailbox list limit when listing mailbox then it uses the default limit",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				ListMailbox(ctx, mock.MatchedBy(func(filter domain.MailboxFilter) bool {
					return filter.Principal == principal && filter.AgentID == "agent_1" &&
						filter.Limit == 50
				})).
				Return([]domain.MailboxMessage{{MessageID: "msg_1"}}, nil).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.ListMailbox(ctx, auth.RequestMetadata{}, "agent_1", "", "", 0)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(
				t,
				map[string]any{"messages": []domain.MailboxMessage{{MessageID: "msg_1"}}},
				data,
			)
		},
	)

	t.Run(
		"Given a session message for a matching agent session then it creates the mailbox message",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			req := domain.CreateMailboxRequest{
				AgentID:     "agent_1",
				SessionID:   "sess_1",
				Message:     "continue",
				MessageType: "chat",
			}
			expected := domain.MailboxMessage{
				MessageID: "msg_1",
				AgentID:   "agent_1",
				SessionID: "sess_1",
			}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				GetSession(ctx, principal, "sess_1").
				Return(domain.AgentSession{AgentID: "agent_1", SessionID: "sess_1"}, nil).
				Once()
			store.EXPECT().CreateMailboxMessage(ctx, principal, req).Return(expected, nil).Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.CreateSessionMessage(ctx, auth.RequestMetadata{}, req)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, expected, data)
		},
	)

	t.Run(
		"Given a node-scoped mailbox message for a different node agent then it returns not found",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			req := domain.CreateMailboxRequest{
				NodeID:      "node_2",
				AgentID:     "agent_1",
				SessionID:   "sess_1",
				Message:     "continue",
				MessageType: "chat",
			}

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				GetAgent(ctx, principal, "agent_1").
				Return(domain.Agent{AgentID: "agent_1", NodeID: "node_1", OwnerUserID: "usr_self"}, nil).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.CreateMailboxMessage(ctx, auth.RequestMetadata{}, req)

			require.ErrorIs(t, err, domain.ErrNotFound)
		},
	)

	t.Run(
		"Given a node-scoped mailbox message for a legacy session then it creates the message",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			req := domain.CreateMailboxRequest{
				NodeID:      "node_1",
				AgentID:     "agent_1",
				SessionID:   "sess_1",
				Message:     "continue",
				MessageType: "chat",
			}
			expected := domain.MailboxMessage{
				NodeID:    "node_1",
				MessageID: "msg_1",
				AgentID:   "agent_1",
				SessionID: "sess_1",
			}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				GetAgent(ctx, principal, "agent_1").
				Return(domain.Agent{AgentID: "agent_1", NodeID: "node_1", OwnerUserID: "usr_self"}, nil).
				Once()
			store.EXPECT().
				GetSession(ctx, principal, "sess_1").
				Return(domain.AgentSession{AgentID: "agent_1", SessionID: "sess_1"}, nil).
				Once()
			store.EXPECT().CreateMailboxMessage(ctx, principal, req).Return(expected, nil).Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.CreateSessionMessage(ctx, auth.RequestMetadata{}, req)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, expected, data)
		},
	)

	t.Run(
		"Given a session message for a different agent session then it returns not found",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				GetSession(ctx, principal, "sess_1").
				Return(domain.AgentSession{AgentID: "agent_2", SessionID: "sess_1"}, nil).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.CreateSessionMessage(
				ctx,
				auth.RequestMetadata{},
				domain.CreateMailboxRequest{
					AgentID:     "agent_1",
					SessionID:   "sess_1",
					Message:     "continue",
					MessageType: "chat",
				},
			)

			require.ErrorIs(t, err, domain.ErrNotFound)
		},
	)
}

func TestRevokeUserAPIKey(t *testing.T) {
	t.Run("Given an empty key ID when revoking then it returns not found", func(t *testing.T) {
		ctx := context.Background()
		principals := userapimocks.NewMockPrincipalResolver(t)
		principals.EXPECT().
			Principal(ctx, auth.RequestMetadata{}).
			Return(userPrincipal("usr_self", false), nil).
			Once()

		svc := userapi.NewService(
			userapimocks.NewMockStore(t),
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		_, _, err := svc.RevokeUserAPIKey(ctx, auth.RequestMetadata{}, "")

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusNotFound, appErr.Status)
	})
}

func TestKnowledgeCapsuleFlow(t *testing.T) {
	t.Run(
		"Given a keyword then it extracts matching session history into a capsule",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			source := domain.AgentSession{
				NodeID:    "node_1",
				AgentID:   "agt_1",
				SessionID: "sess_1",
			}
			message := domain.Message{
				MessageID: "msg_1",
				AgentID:   "agt_1",
				SessionID: "sess_1",
				Role:      "assistant",
				CreatedAt: fixedUserNow(),
			}
			part := domain.MessagePart{
				MessageID: "msg_1",
				PartType:  domain.MessagePartText,
				Text:      "Use capability injection after token=secret123 is configured.",
			}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			secrets := userapimocks.NewMockSecretIssuer(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().GetSession(ctx, principal, "sess_1").Return(source, nil).Once()
			store.EXPECT().ListMessages(ctx, "agt_1", "sess_1", 1000).
				Return([]domain.Message{message}, nil).
				Once()
			store.EXPECT().ListMessageParts(ctx, "msg_1").
				Return([]domain.MessagePart{part}, nil).
				Once()
			secrets.EXPECT().New("kcap").Return("kcap_1", nil).Once()
			store.EXPECT().CreateKnowledgeCapsule(
				ctx,
				mock.MatchedBy(func(capsule domain.KnowledgeCapsule) bool {
					require.Equal(t, "kcap_1", capsule.CapsuleID)
					require.Equal(t, "capability injection", capsule.Keyword)
					require.Contains(t, capsule.Content, "capability injection")
					require.NotContains(t, capsule.Content, "secret123")
					require.Equal(t, domain.KnowledgeCapsuleStatusActive, capsule.Status)
					return true
				}),
			).Return(domain.KnowledgeCapsule{CapsuleID: "kcap_1"}, nil).Once()

			svc := userapi.NewService(store, fixedUserClock, principals, secrets)
			status, data, err := svc.CreateKnowledgeCapsule(
				ctx,
				auth.RequestMetadata{},
				"sess_1",
				domain.CreateKnowledgeCapsuleRequest{Keyword: "capability injection"},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(
				t,
				"kcap_1",
				data.(map[string]any)["capsule"].(domain.KnowledgeCapsule).CapsuleID,
			)
		},
	)

	t.Run(
		"Given an active capsule then it injects a system handoff mailbox message",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			target := domain.AgentSession{
				NodeID:    "node_1",
				AgentID:   "agt_1",
				SessionID: "sess_2",
			}
			capsule := domain.KnowledgeCapsule{
				CapsuleID:       "kcap_1",
				OwnerUserID:     "usr_self",
				SourceSessionID: "sess_1",
				SourceAgentID:   "agt_1",
				Keyword:         "handoff",
				Title:           "Knowledge capsule: handoff",
				Summary:         "summary",
				Content:         "content",
				Status:          domain.KnowledgeCapsuleStatusActive,
			}
			mailbox := domain.MailboxMessage{MessageID: "msg_delivery"}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			secrets := userapimocks.NewMockSecretIssuer(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().GetSession(ctx, principal, "sess_2").Return(target, nil).Once()
			store.EXPECT().GetKnowledgeCapsule(ctx, principal, "kcap_1").Return(capsule, nil).Once()
			store.EXPECT().CreateMailboxMessage(
				ctx,
				principal,
				mock.MatchedBy(func(req domain.CreateMailboxRequest) bool {
					require.Equal(t, "node_1", req.NodeID)
					require.Equal(t, "agt_1", req.AgentID)
					require.Equal(t, "sess_2", req.SessionID)
					require.Equal(t, domain.MessageTypeSystemHandoff, req.MessageType)
					require.Contains(t, req.Message, "system_handoff")
					require.Contains(t, string(req.Payload), `"label":"system_handoff"`)
					return true
				}),
			).Return(mailbox, nil).Once()
			secrets.EXPECT().New("kinj").Return("kinj_1", nil).Once()
			store.EXPECT().CreateKnowledgeInjection(
				ctx,
				mock.MatchedBy(func(injection domain.SessionKnowledgeInjection) bool {
					require.Equal(t, "kinj_1", injection.InjectionID)
					require.Equal(t, "kcap_1", injection.CapsuleID)
					require.Equal(t, "sess_2", injection.TargetSessionID)
					require.Equal(t, "msg_delivery", injection.DeliveryMessageID)
					require.Equal(t, domain.KnowledgeInjectionStatusDelivered, injection.Status)
					require.Equal(t, domain.MessageTypeSystemHandoff, injection.DeliveryMessageType)
					return true
				}),
			).Return(domain.SessionKnowledgeInjection{InjectionID: "kinj_1"}, nil).Once()

			svc := userapi.NewService(store, fixedUserClock, principals, secrets)
			status, data, err := svc.InjectKnowledgeCapsule(
				ctx,
				auth.RequestMetadata{},
				"sess_2",
				domain.InjectKnowledgeCapsuleRequest{CapsuleID: "kcap_1"},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(
				t,
				"kinj_1",
				data.(map[string]any)["injection"].(domain.SessionKnowledgeInjection).InjectionID,
			)
		},
	)
}

func TestEnvelopeFlow(t *testing.T) {
	t.Run("Given a capsule payload then it creates a pending envelope", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_sender", false)
		friend := domain.Friend{
			FriendID:        "fr_1",
			RequesterUserID: "usr_sender",
			RequesterEmail:  "sender@example.com",
			RecipientUserID: "usr_recipient",
			RecipientEmail:  "recipient@example.com",
			Status:          domain.FriendStatusAccepted,
		}
		payload := json.RawMessage(`{"capsule":{"capsule_id":"kcap_1","title":"handoff"}}`)
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		secrets := userapimocks.NewMockSecretIssuer(t)

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().
			GetAcceptedFriendByEmail(ctx, principal, "recipient@example.com").
			Return(friend, nil).
			Once()
		secrets.EXPECT().New("env").Return("env_1", nil).Once()
		store.EXPECT().CreateEnvelope(
			ctx,
			mock.MatchedBy(func(envelope domain.Envelope) bool {
				require.Equal(t, "env_1", envelope.EnvelopeID)
				require.Equal(t, "usr_sender", envelope.SenderUserID)
				require.Equal(t, "usr_recipient", envelope.RecipientUserID)
				require.Equal(t, "recipient@example.com", envelope.RecipientEmail)
				require.Equal(t, domain.EnvelopePayloadKnowledgeCapsule, envelope.PayloadType)
				require.Equal(t, domain.EnvelopeStatusPending, envelope.Status)
				require.JSONEq(t, string(payload), string(envelope.PayloadJSON))
				return true
			}),
		).Return(domain.Envelope{EnvelopeID: "env_1"}, nil).Once()

		svc := userapi.NewService(store, fixedUserClock, principals, secrets)
		status, data, err := svc.CreateEnvelope(
			ctx,
			auth.RequestMetadata{},
			domain.CreateEnvelopeRequest{
				RecipientEmail: " Recipient@Example.com ",
				PayloadType:    domain.EnvelopePayloadKnowledgeCapsule,
				PayloadJSON:    payload,
				Message:        "please review",
			},
		)

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, "env_1", data.(map[string]any)["envelope"].(domain.Envelope).EnvelopeID)
	})

	t.Run(
		"Given from and to agent owners sharing a team then it creates an envelope",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_sender", false)
			recipient := domain.User{
				UserID: "usr_recipient",
				Email:  "recipient@example.com",
				Role:   "user",
			}
			payload := json.RawMessage(`{"capsule":{"capsule_id":"kcap_1","title":"team handoff"}}`)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			secrets := userapimocks.NewMockSecretIssuer(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				GetEnvelopeAgentRecipient(ctx, principal, "agent_from", "agent_to").
				Return(recipient, nil).
				Once()
			secrets.EXPECT().New("env").Return("env_1", nil).Once()
			store.EXPECT().CreateEnvelope(
				ctx,
				mock.MatchedBy(func(envelope domain.Envelope) bool {
					require.Equal(t, "env_1", envelope.EnvelopeID)
					require.Equal(t, "usr_sender", envelope.SenderUserID)
					require.Equal(t, "usr_recipient", envelope.RecipientUserID)
					require.Equal(t, "recipient@example.com", envelope.RecipientEmail)
					require.Equal(t, "agent_from", envelope.FromAgentID)
					require.Equal(t, "agent_to", envelope.ToAgentID)
					require.Equal(t, domain.EnvelopePayloadKnowledgeCapsule, envelope.PayloadType)
					require.Equal(t, domain.EnvelopeStatusPending, envelope.Status)
					require.JSONEq(t, string(payload), string(envelope.PayloadJSON))
					return true
				}),
			).Return(domain.Envelope{EnvelopeID: "env_1"}, nil).Once()

			svc := userapi.NewService(store, fixedUserClock, principals, secrets)
			status, data, err := svc.CreateEnvelope(
				ctx,
				auth.RequestMetadata{},
				domain.CreateEnvelopeRequest{
					FromAgentID: " agent_from ",
					ToAgentID:   " agent_to ",
					PayloadType: domain.EnvelopePayloadKnowledgeCapsule,
					PayloadJSON: payload,
				},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(
				t,
				"env_1",
				data.(map[string]any)["envelope"].(domain.Envelope).EnvelopeID,
			)
		},
	)

	t.Run("Given only one agent id then it rejects the envelope", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_sender", false)
		payload := json.RawMessage(`{"capsule":{"capsule_id":"kcap_1","title":"team handoff"}}`)
		principals := userapimocks.NewMockPrincipalResolver(t)

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

		svc := userapi.NewService(
			userapimocks.NewMockStore(t),
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		status, _, err := svc.CreateEnvelope(
			ctx,
			auth.RequestMetadata{},
			domain.CreateEnvelopeRequest{
				FromAgentID: "agent_from",
				PayloadType: domain.EnvelopePayloadKnowledgeCapsule,
				PayloadJSON: payload,
			},
		)

		require.Error(t, err)
		require.Zero(t, status)
		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusBadRequest, appErr.Status)
		require.Contains(t, err.Error(), "from_agent_id and to_agent_id must be provided together")
	})

	t.Run("Given users without a shared team then it rejects the envelope", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_sender", false)
		payload := json.RawMessage(`{"capsule":{"capsule_id":"kcap_1","title":"team handoff"}}`)
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().
			GetEnvelopeAgentRecipient(ctx, principal, "agent_from", "agent_to").
			Return(domain.User{}, domain.ErrNotFound).
			Once()

		svc := userapi.NewService(
			store,
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		status, _, err := svc.CreateEnvelope(
			ctx,
			auth.RequestMetadata{},
			domain.CreateEnvelopeRequest{
				FromAgentID: "agent_from",
				ToAgentID:   "agent_to",
				PayloadType: domain.EnvelopePayloadKnowledgeCapsule,
				PayloadJSON: payload,
			},
		)

		require.Error(t, err)
		require.Zero(t, status)
		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusForbidden, appErr.Status)
		require.Contains(t, err.Error(), "sender and recipient users must share an active team")
	})

	t.Run(
		"Given an agent recipient email mismatch then it rejects the envelope",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_sender", false)
			recipient := domain.User{
				UserID: "usr_recipient",
				Email:  "recipient@example.com",
				Role:   "user",
			}
			payload := json.RawMessage(`{"capsule":{"capsule_id":"kcap_1","title":"team handoff"}}`)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				GetEnvelopeAgentRecipient(ctx, principal, "agent_from", "agent_to").
				Return(recipient, nil).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, _, err := svc.CreateEnvelope(
				ctx,
				auth.RequestMetadata{},
				domain.CreateEnvelopeRequest{
					RecipientEmail: "other@example.com",
					FromAgentID:    "agent_from",
					ToAgentID:      "agent_to",
					PayloadType:    domain.EnvelopePayloadKnowledgeCapsule,
					PayloadJSON:    payload,
				},
			)

			require.Error(t, err)
			require.Zero(t, status)
			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
			require.Contains(t, err.Error(), "recipient_email must match target agent owner")
		},
	)

	t.Run(
		"Given a paxl capsule envelope then it creates without importing a recipient knowledge capsule",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_sender", false)
			friend := domain.Friend{
				FriendID:        "fr_1",
				RequesterUserID: "usr_sender",
				RequesterEmail:  "sender@example.com",
				RecipientUserID: "usr_recipient",
				RecipientEmail:  "recipient@example.com",
				Status:          domain.FriendStatusAccepted,
			}
			payload := json.RawMessage(`{
				"schema_version":"paxl.envelope_payload.knowledge_capsule.v1",
				"capsule":{
					"capsule_id":"kcap_local",
					"source_node_id":"local-node",
					"source_session_id":"codex-session",
					"source_agent":"codex",
					"keyword":"release",
					"title":"Release handoff",
					"summary":"Release summary",
					"content":"Release context",
					"status":"active",
					"truncated":true,
					"original_estimated_chars":42
				}
			}`)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			secrets := userapimocks.NewMockSecretIssuer(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				GetAcceptedFriendByEmail(ctx, principal, "recipient@example.com").
				Return(friend, nil).
				Once()
			secrets.EXPECT().New("env").Return("env_1", nil).Once()
			store.EXPECT().CreateEnvelope(
				ctx,
				mock.MatchedBy(func(envelope domain.Envelope) bool {
					require.Equal(t, "env_1", envelope.EnvelopeID)
					return true
				}),
			).RunAndReturn(func(_ context.Context, envelope domain.Envelope) (domain.Envelope, error) {
				return envelope, nil
			}).Once()

			svc := userapi.NewServiceWithBackgroundRunner(
				store,
				fixedUserClock,
				principals,
				secrets,
				func(taskCtx context.Context, task func(context.Context)) {
					task(taskCtx)
				},
			)
			status, data, err := svc.CreateEnvelope(
				ctx,
				auth.RequestMetadata{},
				domain.CreateEnvelopeRequest{
					RecipientEmail: " Recipient@Example.com ",
					PayloadType:    domain.EnvelopePayloadKnowledgeCapsule,
					PayloadJSON:    payload,
					Message:        "please review",
				},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(
				t,
				"env_1",
				data.(map[string]any)["envelope"].(domain.Envelope).EnvelopeID,
			)
		},
	)

	t.Run(
		"Given a routed paxl capsule envelope then it preserves route metadata",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_sender", false)
			friend := domain.Friend{
				FriendID:        "fr_1",
				RequesterUserID: "usr_sender",
				RequesterEmail:  "sender@example.com",
				RecipientUserID: "usr_recipient",
				RecipientEmail:  "recipient@example.com",
				Status:          domain.FriendStatusAccepted,
			}
			payload := json.RawMessage(`{
			"schema_version":"paxl.envelope_payload.knowledge_capsule.v2",
			"capsule":{
				"capsule_id":"kcap_local",
				"source_node_id":"local-node",
				"source_session_id":"codex-session",
				"source_agent":"codex",
				"keyword":"routing",
				"title":"Routing handoff",
				"summary":"Routing summary",
				"content":"Routing context",
				"status":"active"
			},
			"route":{
				"match_type":"project",
				"match_value":"pax-manager",
				"target_agent":"codex"
			}
		}`)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			secrets := userapimocks.NewMockSecretIssuer(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				GetAcceptedFriendByEmail(ctx, principal, "recipient@example.com").
				Return(friend, nil).
				Once()
			secrets.EXPECT().New("env").Return("env_1", nil).Once()
			store.EXPECT().CreateEnvelope(
				ctx,
				mock.MatchedBy(func(envelope domain.Envelope) bool {
					require.Equal(t, "env_1", envelope.EnvelopeID)
					require.JSONEq(t, string(payload), string(envelope.PayloadJSON))
					return true
				}),
			).Return(domain.Envelope{EnvelopeID: "env_1"}, nil).Once()

			svc := userapi.NewService(store, fixedUserClock, principals, secrets)
			status, data, err := svc.CreateEnvelope(
				ctx,
				auth.RequestMetadata{},
				domain.CreateEnvelopeRequest{
					RecipientEmail: "recipient@example.com",
					PayloadType:    domain.EnvelopePayloadKnowledgeCapsule,
					PayloadJSON:    payload,
				},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(
				t,
				"env_1",
				data.(map[string]any)["envelope"].(domain.Envelope).EnvelopeID,
			)
		},
	)

	t.Run(
		"Given a routed paxl capsule envelope with invalid match type then it rejects",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_sender", false)
			payload := json.RawMessage(`{
			"schema_version":"paxl.envelope_payload.knowledge_capsule.v2",
			"capsule":{"capsule_id":"kcap_local","title":"Routing handoff"},
			"route":{"match_type":"session","match_value":"local-session","target_agent":"codex"}
		}`)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, _, err := svc.CreateEnvelope(
				ctx,
				auth.RequestMetadata{},
				domain.CreateEnvelopeRequest{
					RecipientEmail: "recipient@example.com",
					PayloadType:    domain.EnvelopePayloadKnowledgeCapsule,
					PayloadJSON:    payload,
				},
			)

			require.Error(t, err)
			require.Zero(t, status)
			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
			require.Contains(t, err.Error(), "unsupported route match_type")
		},
	)

	t.Run(
		"Given a routed paxl capsule envelope without a capsule then it rejects",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_sender", false)
			payload := json.RawMessage(`{
			"schema_version":"paxl.envelope_payload.knowledge_capsule.v2",
			"route":{"match_type":"project","match_value":"pax-manager","target_agent":"codex"}
		}`)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, _, err := svc.CreateEnvelope(
				ctx,
				auth.RequestMetadata{},
				domain.CreateEnvelopeRequest{
					RecipientEmail: "recipient@example.com",
					PayloadType:    domain.EnvelopePayloadKnowledgeCapsule,
					PayloadJSON:    payload,
				},
			)

			require.Error(t, err)
			require.Zero(t, status)
			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
			require.Contains(t, err.Error(), "capsule is required")
		},
	)

	t.Run(
		"Given a routed paxl capsule envelope without required route value then it rejects",
		func(t *testing.T) {
			for _, matchType := range []string{"project", "keyword"} {
				t.Run(matchType, func(t *testing.T) {
					ctx := context.Background()
					principal := userPrincipal("usr_sender", false)
					payload := json.RawMessage(`{
					"schema_version":"paxl.envelope_payload.knowledge_capsule.v2",
					"capsule":{"capsule_id":"kcap_local","title":"Routing handoff"},
					"route":{"match_type":"` + matchType + `","target_agent":"codex"}
				}`)
					principals := userapimocks.NewMockPrincipalResolver(t)

					principals.EXPECT().
						Principal(ctx, auth.RequestMetadata{}).
						Return(principal, nil).
						Once()

					svc := userapi.NewService(
						userapimocks.NewMockStore(t),
						fixedUserClock,
						principals,
						userapimocks.NewMockSecretIssuer(t),
					)
					status, _, err := svc.CreateEnvelope(
						ctx,
						auth.RequestMetadata{},
						domain.CreateEnvelopeRequest{
							RecipientEmail: "recipient@example.com",
							PayloadType:    domain.EnvelopePayloadKnowledgeCapsule,
							PayloadJSON:    payload,
						},
					)

					require.Error(t, err)
					require.Zero(t, status)
					var appErr apperr.Error
					require.ErrorAs(t, err, &appErr)
					require.Equal(t, http.StatusBadRequest, appErr.Status)
					require.Contains(t, err.Error(), "route match_value is required")
				})
			}
		},
	)

	t.Run(
		"Given a paxl capsule envelope when accepted then it imports a recipient knowledge capsule",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_recipient", false)
			payload := json.RawMessage(`{
				"schema_version":"paxl.envelope_payload.knowledge_capsule.v1",
				"capsule":{
					"capsule_id":"kcap_local",
					"source_node_id":"local-node",
					"source_session_id":"codex-session",
					"source_agent":"codex",
					"keyword":"release",
					"title":"Release handoff",
					"summary":"Release summary",
					"content":"Release context",
					"status":"active",
					"truncated":true,
					"original_estimated_chars":42
				}
			}`)
			accepted := domain.Envelope{
				EnvelopeID:      "env_1",
				SenderUserID:    "usr_sender",
				RecipientUserID: "usr_recipient",
				PayloadType:     domain.EnvelopePayloadKnowledgeCapsule,
				PayloadJSON:     payload,
				Status:          domain.EnvelopeStatusAccepted,
			}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			secrets := userapimocks.NewMockSecretIssuer(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().AcceptEnvelope(ctx, principal, "env_1", fixedUserNow()).
				Return(accepted, nil).
				Once()
			secrets.EXPECT().New("kcap").Return("kcap_imported", nil).Once()
			store.EXPECT().CreateKnowledgeCapsule(
				mock.Anything,
				mock.MatchedBy(func(capsule domain.KnowledgeCapsule) bool {
					require.Equal(t, "kcap_imported", capsule.CapsuleID)
					require.Equal(t, "usr_recipient", capsule.OwnerUserID)
					require.Equal(t, "usr_sender", capsule.CreatedByUserID)
					require.Equal(t, "codex-session", capsule.SourceSessionID)
					require.Equal(t, "codex", capsule.SourceAgentID)
					require.Equal(t, "local-node", capsule.SourceNodeID)
					require.Equal(t, "release", capsule.Keyword)
					require.Equal(t, "Release handoff", capsule.Title)
					require.Equal(t, "Release summary", capsule.Summary)
					require.Equal(t, "Release context", capsule.Content)
					require.Equal(t, domain.KnowledgeCapsuleStatusActive, capsule.Status)
					require.True(t, capsule.Truncated)
					require.Equal(t, int64(42), capsule.OriginalEstimatedChars)
					return true
				}),
			).Return(domain.KnowledgeCapsule{CapsuleID: "kcap_imported"}, nil).Once()

			svc := userapi.NewServiceWithBackgroundRunner(
				store,
				fixedUserClock,
				principals,
				secrets,
				func(taskCtx context.Context, task func(context.Context)) {
					task(taskCtx)
				},
			)
			status, data, err := svc.AcceptEnvelope(ctx, auth.RequestMetadata{}, "env_1")

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(
				t,
				domain.EnvelopeStatusAccepted,
				data.(map[string]any)["envelope"].(domain.Envelope).Status,
			)
		},
	)

	t.Run(
		"Given a routed paxl capsule envelope when accepted then it imports route metadata",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_recipient", false)
			payload := json.RawMessage(`{
				"schema_version":"paxl.envelope_payload.knowledge_capsule.v2",
				"capsule":{
					"capsule_id":"kcap_local",
					"source_node_id":"local-node",
					"source_session_id":"codex-session",
					"source_agent":"codex",
					"keyword":"routing",
					"title":"Routing handoff",
					"summary":"Routing summary",
					"content":"Routing context",
					"status":"active"
				},
				"route":{
					"match_type":"project",
					"match_value":"pax-manager",
					"target_agent":"codex"
				}
			}`)
			accepted := domain.Envelope{
				EnvelopeID:      "env_1",
				SenderUserID:    "usr_sender",
				RecipientUserID: "usr_recipient",
				PayloadType:     domain.EnvelopePayloadKnowledgeCapsule,
				PayloadJSON:     payload,
				Status:          domain.EnvelopeStatusAccepted,
			}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			secrets := userapimocks.NewMockSecretIssuer(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().AcceptEnvelope(ctx, principal, "env_1", fixedUserNow()).
				Return(accepted, nil).
				Once()
			secrets.EXPECT().New("kcap").Return("kcap_imported", nil).Once()
			store.EXPECT().CreateKnowledgeCapsule(
				mock.Anything,
				mock.MatchedBy(func(capsule domain.KnowledgeCapsule) bool {
					require.Equal(t, "kcap_imported", capsule.CapsuleID)
					require.Equal(t, "usr_recipient", capsule.OwnerUserID)
					require.JSONEq(
						t,
						`[{
							"type":"paxl.envelope_route",
							"envelope_id":"env_1",
							"route_match_type":"project",
							"route_match_value":"pax-manager",
							"route_target_agent":"codex"
						}]`,
						string(capsule.References),
					)
					require.NotContains(t, string(capsule.References), "target_session_id")
					return true
				}),
			).Return(domain.KnowledgeCapsule{CapsuleID: "kcap_imported"}, nil).Once()

			svc := userapi.NewServiceWithBackgroundRunner(
				store,
				fixedUserClock,
				principals,
				secrets,
				func(taskCtx context.Context, task func(context.Context)) {
					task(taskCtx)
				},
			)
			status, _, err := svc.AcceptEnvelope(ctx, auth.RequestMetadata{}, "env_1")

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
		},
	)

	t.Run(
		"Given a recipient without an accepted friend then it rejects the envelope",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_sender", false)
			payload := json.RawMessage(`{"capsule":{"capsule_id":"kcap_1","title":"handoff"}}`)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				GetAcceptedFriendByEmail(ctx, principal, "recipient@example.com").
				Return(domain.Friend{}, domain.ErrNotFound).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, _, err := svc.CreateEnvelope(
				ctx,
				auth.RequestMetadata{},
				domain.CreateEnvelopeRequest{
					RecipientEmail: "recipient@example.com",
					PayloadType:    domain.EnvelopePayloadKnowledgeCapsule,
					PayloadJSON:    payload,
				},
			)

			require.Error(t, err)
			require.Zero(t, status)
			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusForbidden, appErr.Status)
			require.Contains(t, err.Error(), "recipient must be an accepted friend")
		},
	)

	t.Run("Given a recipient principal then it accepts the envelope", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_recipient", false)
		accepted := domain.Envelope{
			EnvelopeID:      "env_1",
			RecipientUserID: "usr_recipient",
			Status:          domain.EnvelopeStatusAccepted,
		}
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().AcceptEnvelope(ctx, principal, "env_1", fixedUserNow()).
			Return(accepted, nil).
			Once()

		svc := userapi.NewService(
			store,
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		status, data, err := svc.AcceptEnvelope(ctx, auth.RequestMetadata{}, "env_1")

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.Equal(
			t,
			domain.EnvelopeStatusAccepted,
			data.(map[string]any)["envelope"].(domain.Envelope).Status,
		)
	})
}

func TestFriendFlow(t *testing.T) {
	t.Run("Given a recipient email then it creates a pending friend request", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_sender", false)
		recipient := domain.User{UserID: "usr_recipient", Email: "recipient@example.com"}
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		secrets := userapimocks.NewMockSecretIssuer(t)

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().ListFriendsBetween(ctx, principal, "recipient@example.com").
			Return(nil, nil).
			Once()
		store.EXPECT().GetUserByEmail(ctx, "recipient@example.com").Return(recipient, nil).Once()
		secrets.EXPECT().New("fr").Return("fr_1", nil).Once()
		store.EXPECT().CreateFriend(
			ctx,
			mock.MatchedBy(func(friend domain.Friend) bool {
				require.Equal(t, "fr_1", friend.FriendID)
				require.Equal(t, "usr_sender", friend.RequesterUserID)
				require.Equal(t, "usr_recipient", friend.RecipientUserID)
				require.Equal(t, "recipient@example.com", friend.RecipientEmail)
				require.Equal(t, "buddy", friend.RequesterAlias)
				require.Equal(t, domain.FriendStatusPending, friend.Status)
				return true
			}),
		).Return(domain.Friend{FriendID: "fr_1"}, nil).Once()

		svc := userapi.NewService(store, fixedUserClock, principals, secrets)
		status, data, err := svc.CreateFriend(
			ctx,
			auth.RequestMetadata{},
			domain.CreateFriendRequest{Email: " Recipient@Example.com ", Alias: "@buddy"},
		)

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, "fr_1", data.(map[string]any)["friend"].(domain.Friend).FriendID)
	})

	t.Run("Given an active relationship then create returns conflict", func(t *testing.T) {
		for _, status := range []string{
			domain.FriendStatusPending,
			domain.FriendStatusAccepted,
		} {
			ctx := context.Background()
			principal := userPrincipal("usr_sender", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().ListFriendsBetween(ctx, principal, "recipient@example.com").
				Return([]domain.Friend{{FriendID: "fr_existing", Status: status}}, nil).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.CreateFriend(
				ctx,
				auth.RequestMetadata{},
				domain.CreateFriendRequest{Email: "recipient@example.com"},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr, "status %s should conflict", status)
			require.Equal(t, http.StatusConflict, appErr.Status)
		}
	})

	t.Run("Given a blocked relationship then create returns conflict", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_sender", false)
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().ListFriendsBetween(ctx, principal, "recipient@example.com").
			Return(
				[]domain.Friend{{FriendID: "fr_blocked", Status: domain.FriendStatusBlocked}},
				nil,
			).
			Once()

		svc := userapi.NewService(
			store,
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		_, _, err := svc.CreateFriend(
			ctx,
			auth.RequestMetadata{},
			domain.CreateFriendRequest{Email: "recipient@example.com"},
		)

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusConflict, appErr.Status)
	})

	t.Run(
		"Given a removed relationship then create clears it and issues a new request",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_sender", false)
			recipient := domain.User{UserID: "usr_recipient", Email: "recipient@example.com"}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			secrets := userapimocks.NewMockSecretIssuer(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().ListFriendsBetween(ctx, principal, "recipient@example.com").
				Return(
					[]domain.Friend{{FriendID: "fr_old", Status: domain.FriendStatusRemoved}},
					nil,
				).
				Once()
			store.EXPECT().DeleteRemovedFriendsBetween(ctx, principal, "recipient@example.com").
				Return(nil).
				Once()
			store.EXPECT().
				GetUserByEmail(ctx, "recipient@example.com").
				Return(recipient, nil).
				Once()
			secrets.EXPECT().New("fr").Return("fr_2", nil).Once()
			store.EXPECT().CreateFriend(
				ctx,
				mock.MatchedBy(func(friend domain.Friend) bool {
					require.Equal(t, "fr_2", friend.FriendID)
					require.Equal(t, domain.FriendStatusPending, friend.Status)
					return true
				}),
			).Return(domain.Friend{FriendID: "fr_2"}, nil).Once()

			svc := userapi.NewService(store, fixedUserClock, principals, secrets)
			status, data, err := svc.CreateFriend(
				ctx,
				auth.RequestMetadata{},
				domain.CreateFriendRequest{Email: "recipient@example.com"},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, "fr_2", data.(map[string]any)["friend"].(domain.Friend).FriendID)
		},
	)

	t.Run("Given a recipient principal then it accepts the friend request", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_recipient", false)
		accepted := domain.Friend{
			FriendID:        "fr_1",
			RecipientUserID: "usr_recipient",
			RecipientAlias:  "sender",
			Status:          domain.FriendStatusAccepted,
		}
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().AcceptFriend(ctx, principal, "fr_1", "sender", fixedUserNow()).
			Return(accepted, nil).
			Once()

		svc := userapi.NewService(
			store,
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		status, data, err := svc.AcceptFriend(
			ctx,
			auth.RequestMetadata{},
			"fr_1",
			domain.AcceptFriendRequest{Alias: "@sender"},
		)

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.Equal(
			t,
			domain.FriendStatusAccepted,
			data.(map[string]any)["friend"].(domain.Friend).Status,
		)
	})

	t.Run("Given a visible friend then it updates the caller alias", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_sender", false)
		updated := domain.Friend{
			FriendID:       "fr_1",
			RequesterAlias: "teammate",
			Status:         domain.FriendStatusAccepted,
		}
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
		store.EXPECT().UpdateFriendAlias(ctx, principal, "fr_1", "teammate").
			Return(updated, nil).
			Once()

		svc := userapi.NewService(
			store,
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		status, data, err := svc.UpdateFriendAlias(
			ctx,
			auth.RequestMetadata{},
			"fr_1",
			domain.UpdateFriendAliasRequest{Alias: "@TeamMate"},
		)

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, "teammate", data.(map[string]any)["friend"].(domain.Friend).RequesterAlias)
	})

	t.Run("Given an empty alias then it returns bad request", func(t *testing.T) {
		ctx := context.Background()
		principal := userPrincipal("usr_sender", false)
		principals := userapimocks.NewMockPrincipalResolver(t)

		principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()

		svc := userapi.NewService(
			userapimocks.NewMockStore(t),
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		_, _, err := svc.UpdateFriendAlias(
			ctx,
			auth.RequestMetadata{},
			"fr_1",
			domain.UpdateFriendAliasRequest{},
		)

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusBadRequest, appErr.Status)
	})
}

func userPrincipal(userID string, admin bool) domain.UserPrincipal {
	return domain.UserPrincipal{
		User:    domain.User{UserID: userID, Email: userID + "@example.com"},
		IsAdmin: admin,
	}
}

func fixedUserClock() time.Time {
	return fixedUserNow()
}

func fixedUserNow() time.Time {
	return time.Date(2026, 6, 12, 15, 0, 0, 0, time.UTC)
}
