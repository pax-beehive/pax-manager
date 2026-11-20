package userapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
	userapimocks "github.com/pax-beehive/pax-manager/internal/manager/userapi/mocks"
)

type sentNodeCommand struct {
	nodeID    string
	commandID string
	command   string
}

// commandRecordingNodeControl records every command instead of only the last.
type commandRecordingNodeControl struct {
	fakeNodeControlClient
	sent        []sentNodeCommand
	hasDeadline bool
}

func (c *commandRecordingNodeControl) Command(
	ctx context.Context,
	nodeID string,
	commandID string,
	command any,
) (json.RawMessage, error) {
	raw, err := json.Marshal(command)
	if err != nil {
		return nil, err
	}
	_, c.hasDeadline = ctx.Deadline()
	c.sent = append(
		c.sent,
		sentNodeCommand{nodeID: nodeID, commandID: commandID, command: string(raw)},
	)
	return c.commandAck, c.commandErr
}

type connectionCleanupFixture struct {
	svc    *userapi.Service
	client *commandRecordingNodeControl
	now    *time.Time
}

func newConnectionCleanupFixture(
	t *testing.T,
	store *userapimocks.MockStore,
	principals *userapimocks.MockPrincipalResolver,
) connectionCleanupFixture {
	t.Helper()
	now := fixedUserClock()
	client := &commandRecordingNodeControl{}
	client.commandAck = json.RawMessage(`{"ok":true,"status":"received"}`)
	svc := userapi.NewServiceWithBackgroundRunner(
		store,
		func() time.Time { return now },
		principals,
		userapimocks.NewMockSecretIssuer(t),
		func(ctx context.Context, task func(context.Context)) { task(ctx) },
	)
	svc.SetNodeControlClient(client)
	return connectionCleanupFixture{svc: svc, client: client, now: &now}
}

func newDetachedConnectionCleanupFixture(t *testing.T) connectionCleanupFixture {
	t.Helper()
	return newConnectionCleanupFixture(
		t,
		userapimocks.NewMockStore(t),
		userapimocks.NewMockPrincipalResolver(t),
	)
}

func TestRemoveDeletedAgentConnectionsSendsOneDeleteCommandPerConnection(t *testing.T) {
	f := newDetachedConnectionCleanupFixture(t)
	connections := []domain.NodeAgentConnectionRef{
		{AgentID: "agent_1", ConnectionID: "conn_1"},
		{AgentID: "agent_2", ConnectionID: "conn_2"},
		{AgentID: "agent_3", ConnectionID: ""},
		{AgentID: "", ConnectionID: "conn_4"},
	}

	f.svc.RemoveDeletedAgentConnections(context.Background(), "node_1", connections)

	require.Len(t, f.client.sent, 2)
	require.True(t, f.client.hasDeadline)
	for i, connectionID := range []string{"conn_1", "conn_2"} {
		sent := f.client.sent[i]
		require.Equal(t, "node_1", sent.nodeID)
		require.Equal(t, domain.DeletedAgentConnectionCommandID(connections[i]), sent.commandID)
		require.JSONEq(t, `{
			"command_id":"`+sent.commandID+`",
			"type":"agent_connection.delete",
			"delete_agent_connection":{"connection_id":"`+connectionID+`"}
		}`, sent.command)
	}
	require.NotEqual(t, f.client.sent[0].commandID, f.client.sent[1].commandID)
}

func TestRemoveDeletedAgentConnectionsThrottlesAcknowledgedRemovals(t *testing.T) {
	tests := map[string]json.RawMessage{
		"accepted": json.RawMessage(`{"ok":true,"status":"received"}`),
		"rejected": json.RawMessage(
			`{"ok":false,"status":"rejected","error":{"code":"not_found","message":"gone"}}`,
		),
		"invalid ack": json.RawMessage(`not-json`),
	}
	for name, ack := range tests {
		t.Run(name, func(t *testing.T) {
			f := newDetachedConnectionCleanupFixture(t)
			f.client.commandAck = ack
			ctx := context.Background()
			connections := []domain.NodeAgentConnectionRef{
				{AgentID: "agent_1", ConnectionID: "conn_1"},
			}

			f.svc.RemoveDeletedAgentConnections(ctx, "node_1", connections)
			f.svc.RemoveDeletedAgentConnections(ctx, "node_1", connections)
			require.Len(t, f.client.sent, 1)

			// The same connection id on another node is a different connection.
			f.svc.RemoveDeletedAgentConnections(ctx, "node_2", connections)
			require.Len(t, f.client.sent, 2)

			*f.now = f.now.Add(59 * time.Second)
			f.svc.RemoveDeletedAgentConnections(ctx, "node_1", connections)
			require.Len(t, f.client.sent, 2)

			*f.now = f.now.Add(time.Second)
			f.svc.RemoveDeletedAgentConnections(ctx, "node_1", connections)
			require.Len(t, f.client.sent, 3)
			require.Equal(t, f.client.sent[0].commandID, f.client.sent[2].commandID)
		})
	}
}

func TestRemoveDeletedAgentConnectionsRetriesUndeliveredRemovalImmediately(t *testing.T) {
	f := newDetachedConnectionCleanupFixture(t)
	f.client.commandErr = errors.New("node control tunnel is not connected")
	ctx := context.Background()
	connections := []domain.NodeAgentConnectionRef{{AgentID: "agent_1", ConnectionID: "conn_1"}}

	f.svc.RemoveDeletedAgentConnections(ctx, "node_1", connections)
	f.svc.RemoveDeletedAgentConnections(ctx, "node_1", connections)

	require.Len(t, f.client.sent, 2)
}

func TestRemoveDeletedAgentConnectionsThrottlesTimedOutRemoval(t *testing.T) {
	f := newDetachedConnectionCleanupFixture(t)
	f.client.commandErr = fmt.Errorf("await ack: %w", context.DeadlineExceeded)
	ctx := context.Background()
	connections := []domain.NodeAgentConnectionRef{{AgentID: "agent_1", ConnectionID: "conn_1"}}

	f.svc.RemoveDeletedAgentConnections(ctx, "node_1", connections)
	f.svc.RemoveDeletedAgentConnections(ctx, "node_1", connections)
	require.Len(t, f.client.sent, 1)

	*f.now = f.now.Add(time.Minute)
	f.svc.RemoveDeletedAgentConnections(ctx, "node_1", connections)
	require.Len(t, f.client.sent, 2)
}

func TestRemoveDeletedAgentConnectionsIgnoresMissingNodeControl(t *testing.T) {
	f := newDetachedConnectionCleanupFixture(t)
	connections := []domain.NodeAgentConnectionRef{{AgentID: "agent_1", ConnectionID: "conn_1"}}

	f.svc.RemoveDeletedAgentConnections(context.Background(), " ", connections)
	f.svc.SetNodeControlClient(nil)
	f.svc.RemoveDeletedAgentConnections(context.Background(), "node_1", connections)

	require.Empty(t, f.client.sent)
}

func TestDeleteAgentRemovesItsReportedPaxdConnection(t *testing.T) {
	runtimeMetadata := json.RawMessage(`{"runtime":{"connection_id":"conn_1"}}`)
	tests := map[string]struct {
		agent        domain.Agent
		wantCommands int
	}{
		"node agent with a reported connection": {
			agent: domain.Agent{
				AgentID: "agent_1", NodeID: "node_1", Metadata: runtimeMetadata,
			},
			wantCommands: 1,
		},
		"node agent that paxd never reported": {
			agent: domain.Agent{AgentID: "agent_1", NodeID: "node_1"},
		},
		"node agent with unreadable metadata": {
			agent: domain.Agent{
				AgentID: "agent_1", NodeID: "node_1", Metadata: json.RawMessage(`[]`),
			},
		},
		"agent without a node": {
			agent: domain.Agent{AgentID: "agent_1", Metadata: runtimeMetadata},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().
				Principal(ctx, auth.RequestMetadata{}).
				Return(principal, nil).
				Twice()
			store.EXPECT().
				DeleteAgent(ctx, principal, domain.DeleteAgentRequest{AgentID: "agent_1"}).
				Return(tt.agent, nil).
				Once()
			nodeReq := domain.DeleteAgentRequest{NodeID: "node_1", AgentID: "agent_1"}
			store.EXPECT().DeleteNodeAgent(ctx, principal, nodeReq).Return(tt.agent, nil).Once()
			f := newConnectionCleanupFixture(t, store, principals)

			status, data, err := f.svc.DeleteAgent(ctx, auth.RequestMetadata{}, "agent_1")
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, tt.agent, data)
			require.Len(t, f.client.sent, tt.wantCommands)

			// The node-scoped delete targets the same connection, which was
			// just attempted, so it must not send a second command.
			status, data, err = f.svc.DeleteNodeAgent(ctx, auth.RequestMetadata{}, nodeReq)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, tt.agent, data)
			require.Len(t, f.client.sent, tt.wantCommands)

			if tt.wantCommands == 0 {
				return
			}
			sent := f.client.sent[0]
			require.Equal(t, "node_1", sent.nodeID)
			require.JSONEq(t, `{
				"command_id":"`+sent.commandID+`",
				"type":"agent_connection.delete",
				"delete_agent_connection":{"connection_id":"conn_1"}
			}`, sent.command)
		})
	}
}

func TestDeleteNodeAgentRemovesItsReportedPaxdConnection(t *testing.T) {
	ctx := context.Background()
	principal := userPrincipal("usr_self", false)
	agent := domain.Agent{
		AgentID:  "agent_1",
		NodeID:   "node_1",
		Metadata: json.RawMessage(`{"runtime":{"connection_id":"conn_1"}}`),
	}
	req := domain.DeleteAgentRequest{NodeID: "node_1", AgentID: "agent_1"}
	store := userapimocks.NewMockStore(t)
	principals := userapimocks.NewMockPrincipalResolver(t)
	principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
	store.EXPECT().DeleteNodeAgent(ctx, principal, req).Return(agent, nil).Once()
	f := newConnectionCleanupFixture(t, store, principals)

	status, data, err := f.svc.DeleteNodeAgent(ctx, auth.RequestMetadata{}, req)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, agent, data)
	require.Len(t, f.client.sent, 1)
	require.Equal(
		t,
		domain.DeletedAgentConnectionCommandID(
			domain.NodeAgentConnectionRef{AgentID: "agent_1", ConnectionID: "conn_1"},
		),
		f.client.sent[0].commandID,
	)
}

func TestDeleteAgentSendsNoRemovalWhenStorageFails(t *testing.T) {
	ctx := context.Background()
	principal := userPrincipal("usr_self", false)
	store := userapimocks.NewMockStore(t)
	principals := userapimocks.NewMockPrincipalResolver(t)
	principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
	store.EXPECT().
		DeleteAgent(ctx, principal, domain.DeleteAgentRequest{AgentID: "agent_1"}).
		Return(domain.Agent{}, domain.ErrNotFound).
		Once()
	f := newConnectionCleanupFixture(t, store, principals)

	_, _, err := f.svc.DeleteAgent(ctx, auth.RequestMetadata{}, "agent_1")

	require.ErrorIs(t, err, domain.ErrNotFound)
	require.Empty(t, f.client.sent)
}
