package manager

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestPendingApprovalIsPersistedWithoutConversationSubscriber(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	fixture := testNodeAgent(t, srv, "todd@example.com")
	createConversationTestSession(t, srv, fixture, "sess-existing", "native-existing")
	ctx := context.Background()
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	agent := &ACPTunnelAgent{
		agentID: fixture.agentID, nodeID: fixture.nodeID, ownerUserID: principal.User.UserID,
	}
	frame := newACPFrameContext(agent, acpAgentToUser, websocket.TextMessage, []byte(`{
		"jsonrpc":"2.0","id":"perm-offline","method":"session/request_permission",
		"params":{"sessionId":"sess-existing","toolCall":{"toolCallId":"call-1","title":"Run command"},
		"options":[{"optionId":"allow","kind":"allow_once","name":"Allow"}]}
	}`))
	frame.managerSessionID = "sess-existing"
	frame.nativeSessionID = "native-existing"
	oldPayload := json.RawMessage(
		`{"jsonrpc":"2.0","id":"perm-offline","method":"session/request_permission","params":{"approval_id":"appr-previous"}}`,
	)
	oldMessage := domain.Message{
		MessageID: "old-permission", AgentID: fixture.agentID, SessionID: "sess-existing",
		Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionAgentToUser,
		Role: "assistant", Status: "received", MessageType: "session/request_permission",
		LogicalKey: "old-permission", RawJSON: oldPayload,
	}
	require.NoError(t, srv.store.UpsertMessage(ctx, &oldMessage))
	pipeline := newACPFramePipeline(
		acpApprovalMiddleware{store: srv.store, runtime: srv.acpRuntime},
		acpRuntimeStateMiddleware{projector: srv.acpRuntime},
	)
	var approvalID string
	forward := func(ctx context.Context, frame *acpFrameContext) error {
		approvalID = conversationPermissionRequestApprovalID(frame.payload)
		require.NotEmpty(t, approvalID, "observers must receive an actionable approval ID")
		approval, err := srv.store.GetApproval(
			ctx,
			UserPrincipal{User: User{UserID: principal.User.UserID}},
			approvalID,
		)
		require.NoError(t, err)
		assert.Equal(t, "pending", approval.Status)
		assert.Equal(t, "perm-offline", approval.NativeID)
		return nil
	}
	require.NoError(t, pipeline.Handle(ctx, frame, forward))
	originalID := approvalID
	require.NoError(t, pipeline.Handle(ctx, frame, forward))
	assert.Equal(t, originalID, approvalID)
	session, err := srv.store.GetSession(
		ctx,
		UserPrincipal{User: User{UserID: principal.User.UserID}},
		"sess-existing",
	)
	require.NoError(t, err)
	require.NotNil(t, session.RuntimeState)
	assert.Equal(t, domain.RuntimeLifecycleWaitingApproval, session.RuntimeState.Lifecycle)
	assert.Equal(t, approvalID, session.RuntimeState.PendingApprovalID)
	var params map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(frame.frame.Params, &params))
	assert.Contains(t, params, "approval_id")
	messages, err := srv.store.ListMessages(ctx, fixture.agentID, "sess-existing", 100)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.JSONEq(
		t,
		string(oldPayload),
		string(messages[0].RawJSON),
		"a reused request ID must not rewrite old history",
	)
}
