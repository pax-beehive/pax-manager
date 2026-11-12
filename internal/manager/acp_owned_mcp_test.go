package manager

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pax-beehive/paxkit/reliablemq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func TestPaxConversationToolNameGivenKnownServerThenMatchesOnlyRegisteredTools(t *testing.T) {
	server := agentConversationMCPServerName("sess_owned")
	normalized := strings.ReplaceAll(server, "-", "_")
	for _, tool := range []string{"list_agents", "ask", "reply", "publish_artifact", "workspace_search", "workspace_read", "workspace_expand", "workspace_history", "workspace_summaries", "workspace_request_access", "workspace_access_status", "workspace_cancel_access"} {
		for _, name := range []string{"mcp__" + server + "__" + tool, "mcp__" + normalized + "__" + tool, "mcp_" + normalized + "_" + tool} {
			assert.True(t, paxConversationToolName(name, "sess_owned"), name)
		}
	}
	for _, name := range []string{"workspace_search", "mcp__pax-conversation__workspace_search", "mcp__" + agentConversationMCPServerName("sess_other") + "__workspace_search", "mcp__" + server + "__shell", "mcp__" + server + "__workspace_search_extra", "run mcp__" + server + "__workspace_search", "mcp__" + server + "__workspace_search ", "mcp__evil_" + server + "__workspace_search"} {
		assert.False(t, paxConversationToolName(name, "sess_owned"), name)
	}
}

type ownedMCPFixture struct {
	store      *storage.MemoryStore
	middleware acpApprovalMiddleware
	agent      *ACPTunnelAgent
	frame      *acpFrameContext
	params     map[string]any
	title      string
}

func newOwnedMCPFixture(t *testing.T) ownedMCPFixture {
	t.Helper()
	store := storage.NewMemoryStore(time.Now)
	user, err := store.EnsureUser(t.Context(), "owned@example.com", "Owner", "user")
	require.NoError(t, err)
	registered, err := store.RegisterAgent(
		t.Context(),
		user,
		domain.RegisterAgentRequest{Name: "agent", OS: "darwin"},
		"hash",
	)
	require.NoError(t, err)
	_, err = store.CreateNodeAgentSession(
		t.Context(),
		domain.UserPrincipal{User: user},
		domain.CreateSessionRequest{
			NodeID:    registered.NodeID,
			AgentID:   registered.AgentID,
			SessionID: "sess_owned",
			NativeID:  "native_owned",
		},
	)
	require.NoError(t, err)
	agent := &ACPTunnelAgent{
		agentID:      registered.AgentID,
		nodeID:       registered.NodeID,
		ownerUserID:  user.UserID,
		store:        store,
		connectionID: "queue_owned",
	}
	runtime := newACPRuntimeProjector(time.Now)
	runtime.apply(
		agent,
		"sess_owned",
		acpRuntimeEvent{kind: "prompt_started", requestID: "prompt_owned", turnID: "turn_owned"},
		"",
	)
	title := "mcp__" + agentConversationMCPServerName("sess_owned") + "__workspace_summaries"
	params := map[string]any{
		"sessionId": "native_owned",
		"toolCall":  map[string]any{"toolCallId": "call_owned", "title": title, "kind": "other"},
		"options":   []any{map[string]any{"optionId": "custom-native-allow", "kind": "allow_once"}},
	}
	frame := newACPFrameContext(
		agent,
		acpAgentToUser,
		websocket.TextMessage,
		mustMarshalRaw(
			map[string]any{
				"jsonrpc": "2.0",
				"id":      "permission_owned",
				"method":  "session/request_permission",
				"params":  params,
			},
		),
	)
	frame.managerSessionID = "sess_owned"
	frame.nativeSessionID = "native_owned"
	frame.transportMetadata = map[string]string{"turn_id": "turn_owned"}
	return ownedMCPFixture{
		store,
		acpApprovalMiddleware{store: store, runtime: runtime},
		agent,
		frame,
		params,
		title,
	}
}

func TestOwnedMCPToolCallGivenMatchingTurnThenUsesDirectOrCachedMetadata(t *testing.T) {
	t.Run("direct request", func(t *testing.T) {
		f := newOwnedMCPFixture(t)
		call, ok := f.middleware.ownedMCPToolCall(t.Context(), f.frame, f.params)
		require.True(t, ok)
		assert.Equal(t, f.title, call.Title)
	})
	t.Run("ID only request", func(t *testing.T) {
		f := newOwnedMCPFixture(t)
		f.middleware.runtime.apply(
			f.agent,
			"sess_owned",
			acpRuntimeEvent{
				kind: "tool_call_pending",
				toolCall: domain.RuntimeToolCall{
					ToolCallID: "call_owned",
					Title:      f.title,
					Kind:       "other",
					Status:     "pending",
					RawInput:   json.RawMessage(`{"limit":20}`),
				},
			},
			"turn_owned",
		)
		f.params["toolCall"] = map[string]any{"toolCallId": "call_owned"}
		call, ok := f.middleware.ownedMCPToolCall(t.Context(), f.frame, f.params)
		require.True(t, ok)
		assert.Equal(t, f.title, call.Title)
		assert.JSONEq(t, `{"limit":20}`, string(call.RawInput))
	})
}

func TestOwnedMCPToolCallGivenUntrustedContextThenRequiresManualApproval(t *testing.T) {
	cases := map[string]func(*ownedMCPFixture){
		"other owner":           func(f *ownedMCPFixture) { f.agent.ownerUserID = "other_owner" },
		"other agent":           func(f *ownedMCPFixture) { f.agent.agentID = "other_agent" },
		"other node":            func(f *ownedMCPFixture) { f.agent.nodeID = "other_node" },
		"other session":         func(f *ownedMCPFixture) { f.frame.managerSessionID = "other_session" },
		"stale turn":            func(f *ownedMCPFixture) { f.frame.transportMetadata["turn_id"] = "old_turn" },
		"missing turn":          func(f *ownedMCPFixture) { f.frame.transportMetadata = nil },
		"no runtime":            func(f *ownedMCPFixture) { f.middleware.runtime = nil },
		"no store":              func(f *ownedMCPFixture) { f.middleware.store = nil },
		"no agent":              func(f *ownedMCPFixture) { f.frame.agent = nil },
		"missing metadata":      func(f *ownedMCPFixture) { f.params["toolCall"] = map[string]any{"toolCallId": "call_owned"} },
		"shell deceptive title": func(f *ownedMCPFixture) { f.params["toolCall"].(map[string]any)["kind"] = "execute" },
		"missing kind":          func(f *ownedMCPFixture) { delete(f.params["toolCall"].(map[string]any), "kind") },
		"completed direct call": func(f *ownedMCPFixture) { f.params["toolCall"].(map[string]any)["status"] = "completed" },
		"other server": func(f *ownedMCPFixture) {
			f.params["toolCall"].(map[string]any)["title"] = "mcp__" + agentConversationMCPServerName(
				"other_session",
			) + "__workspace_summaries"
		},
		"raw input masquerade": func(f *ownedMCPFixture) {
			f.params["toolCall"] = map[string]any{
				"toolCallId": "call_owned",
				"kind":       "other",
				"title":      "shell",
				"rawInput":   map[string]any{"title": f.title},
			}
		},
		"cached completed": func(f *ownedMCPFixture) {
			f.middleware.runtime.apply(
				f.agent,
				"sess_owned",
				acpRuntimeEvent{
					kind: "tool_call_completed",
					toolCall: domain.RuntimeToolCall{
						ToolCallID: "call_owned",
						Title:      f.title,
						Kind:       "other",
						Status:     "completed",
					},
				},
				"turn_owned",
			)
		},
		"cached shell conflict": func(f *ownedMCPFixture) {
			f.middleware.runtime.apply(
				f.agent,
				"sess_owned",
				acpRuntimeEvent{
					kind: "tool_call_pending",
					toolCall: domain.RuntimeToolCall{
						ToolCallID: "call_owned",
						Title:      "shell",
						Kind:       "execute",
						Status:     "pending",
					},
				},
				"turn_owned",
			)
		},
		"previous turn call": func(f *ownedMCPFixture) {
			f.middleware.runtime.apply(
				f.agent,
				"sess_owned",
				acpRuntimeEvent{
					kind: "tool_call_pending",
					toolCall: domain.RuntimeToolCall{
						ToolCallID: "call_owned",
						Title:      f.title,
						Kind:       "other",
						Status:     "pending",
					},
				},
				"old_turn",
			)
			f.params["toolCall"] = map[string]any{"toolCallId": "call_owned"}
		},
		"completed prompt": func(f *ownedMCPFixture) {
			f.middleware.runtime.apply(
				f.agent,
				"sess_owned",
				acpRuntimeEvent{kind: "prompt_completed", requestID: "prompt_owned"},
				"turn_owned",
			)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newOwnedMCPFixture(t)
			change(&f)
			_, ok := f.middleware.ownedMCPToolCall(t.Context(), f.frame, f.params)
			assert.False(t, ok)
		})
	}
}

func TestOwnedMCPApprovalGivenManualSessionThenConsumesPermissionAndAuditsNativeAllowOnce(
	t *testing.T,
) {
	f := newOwnedMCPFixture(t)
	producer, err := reliablemq.NewProducer(
		t.Context(),
		reliablemq.ProducerConfig{QueueID: "queue_owned", Stream: reliablemq.StreamACP},
		f.store,
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, producer.Close(context.Background())) })
	f.agent.reliableEngine = reliablemq.NewEngine(reliablemq.Config{}, f.store, producer, nil)
	sub := f.agent.liveState().sessionMux.subscribe("sess_owned")
	defer f.agent.liveState().sessionMux.unsubscribe(sub)
	forwarded := false
	require.NoError(
		t,
		f.middleware.HandleACPFrame(
			t.Context(),
			f.frame,
			func(context.Context, *acpFrameContext) error { forwarded = true; return nil },
		),
	)
	assert.False(t, forwarded, "manual approval request must never reach the conversation runner")
	assert.True(t, f.frame.handled)
	_, err = producer.Checkpoint(t.Context())
	require.NoError(t, err)
	frames, err := f.store.ListOutboundReplay(t.Context(), "queue_owned", reliablemq.StreamACP, 10)
	require.NoError(t, err)
	require.Len(t, frames, 1)
	assert.Contains(t, string(frames[0].Payload), `"optionId":"custom-native-allow"`)
	assert.Equal(t, "turn_owned", frames[0].Metadata["turn_id"])
	select {
	case payload := <-sub.ch:
		assert.NotContains(t, string(payload), "session/request_permission")
		assert.Contains(t, string(payload), `"approval_policy":"pax_owned_mcp"`)
	default:
		t.Fatal("expected approved response notification")
	}
	approvals, err := f.store.ListApprovals(
		t.Context(),
		domain.ApprovalFilter{
			Principal:        domain.UserPrincipal{User: domain.User{UserID: f.agent.ownerUserID}},
			RequestSessionID: "sess_owned",
		},
	)
	require.NoError(t, err)
	require.Len(t, approvals, 1)
	assert.Equal(t, "decided", approvals[0].Status)
	assert.Equal(t, "allow_once", approvals[0].DecisionOption)
	require.NotNil(t, approvals[0].RespondedAt)
	assert.Contains(t, string(approvals[0].ResponseBody), `"custom-native-allow"`)
	state := f.middleware.runtime.CurrentCorrelation(f.agent.agentID, "sess_owned")
	assert.Equal(t, domain.RuntimeLifecycleRunning, state.Lifecycle)
	assert.Empty(t, state.PendingApprovalID)
}

func TestOwnedMCPToolCallGivenCodexExecuteMetadataThenRequiresExactStructuredIdentity(
	t *testing.T,
) {
	cases := map[string]struct {
		kind, title, raw string
		want             bool
	}{
		"exact Codex MCP call": {
			kind:  "execute",
			title: "list_agents",
			raw:   `{"server":"%s","tool":"list_agents","arguments":{"limit":1}}`,
			want:  true,
		},
		"wrong structured server": {
			kind:  "execute",
			title: "list_agents",
			raw:   `{"server":"other","tool":"list_agents"}`,
		},
		"wrong structured tool": {
			kind:  "execute",
			title: "list_agents",
			raw:   `{"server":"%s","tool":"workspace_read"}`,
		},
		"shell masquerading as MCP": {
			kind:  "execute",
			title: "list_agents",
			raw:   `{"command":"echo MCP"}`,
		},
		"unknown Pax tool": {
			kind:  "execute",
			title: "shell",
			raw:   `{"server":"%s","tool":"shell"}`,
		},
		"nested identity": {
			kind:  "execute",
			title: "list_agents",
			raw:   `{"arguments":{"server":"%s","tool":"list_agents"}}`,
		},
		"invalid identity types": {
			kind:  "execute",
			title: "list_agents",
			raw:   `{"server":[],"tool":"list_agents"}`,
		},
		"unexpected kind": {
			kind:  "delete",
			title: "list_agents",
			raw:   `{"server":"%s","tool":"list_agents"}`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newOwnedMCPFixture(t)
			server := agentConversationMCPServerName("sess_owned")
			f.params["toolCall"] = map[string]any{
				"toolCallId": "call_owned",
				"kind":       tc.kind,
				"title":      "mcp." + server + "." + tc.title,
				"rawInput":   json.RawMessage(strings.ReplaceAll(tc.raw, "%s", server)),
			}
			_, ok := f.middleware.ownedMCPToolCall(t.Context(), f.frame, f.params)
			assert.Equal(t, tc.want, ok)
		})
	}
}

func TestOwnedMCPApprovalGivenInvalidNativeOptionsThenDoesNotCreateOrSendAnApproval(t *testing.T) {
	for _, options := range []any{[]any{}, []any{map[string]any{"optionId": "allow_once", "kind": "reject_once"}}, []any{map[string]any{"kind": "allow_once"}}} {
		f := newOwnedMCPFixture(t)
		f.params["options"] = options
		f.frame.frame.Params = mustMarshalRaw(f.params)
		require.NoError(t, f.frame.replaceFrame(f.frame.frame))
		handled, err := f.middleware.autoApproveOwnedMCP(t.Context(), f.frame, f.params)
		assert.False(t, handled)
		assert.NoError(t, err)
		approvals, listErr := f.store.ListApprovals(
			t.Context(),
			domain.ApprovalFilter{
				Principal: domain.UserPrincipal{User: domain.User{UserID: f.agent.ownerUserID}},
			},
		)
		require.NoError(t, listErr)
		assert.Empty(t, approvals)
	}
	t.Run("missing RPC ID", func(t *testing.T) {
		f := newOwnedMCPFixture(t)
		f.frame.frame.ID = nil
		handled, err := f.middleware.autoApproveOwnedMCP(t.Context(), f.frame, f.params)
		assert.True(t, handled)
		assert.ErrorContains(t, err, "missing JSON-RPC id")
	})
}

func TestOwnedMCPApprovalGivenIDOnlyThenAuditsCachedMetadataAndPropagatesTransportFailure(
	t *testing.T,
) {
	f := newOwnedMCPFixture(t)
	f.middleware.runtime.apply(
		f.agent,
		"sess_owned",
		acpRuntimeEvent{
			kind: "tool_call_pending",
			toolCall: domain.RuntimeToolCall{
				ToolCallID: "call_owned",
				Title:      f.title,
				Kind:       "other",
				Status:     "pending",
				RawInput:   json.RawMessage(`{"limit":20}`),
			},
		},
		"turn_owned",
	)
	f.frame.transportMetadata = nil
	f.params["toolCall"] = map[string]any{"toolCallId": "call_owned"}
	f.frame.frame.Params = mustMarshalRaw(f.params)
	require.NoError(t, f.frame.replaceFrame(f.frame.frame))
	handled, err := f.middleware.autoApproveOwnedMCP(t.Context(), f.frame, f.params)
	assert.True(t, handled)
	require.ErrorIs(t, err, reliablemq.ErrProducerNotReady)
	assert.Equal(t, "turn_owned", f.frame.businessTurnID)
	approvals, listErr := f.store.ListApprovals(
		t.Context(),
		domain.ApprovalFilter{
			Principal: domain.UserPrincipal{User: domain.User{UserID: f.agent.ownerUserID}},
		},
	)
	require.NoError(t, listErr)
	require.Len(t, approvals, 1)
	assert.Equal(t, f.title, approvals[0].Title)
	assert.Contains(t, string(approvals[0].RequestBody), `"limit":20`)
	assert.Nil(t, approvals[0].RespondedAt)
}

func TestCurrentCorrelationGivenToolCallsThenReturnsAnOwnedCopy(t *testing.T) {
	f := newOwnedMCPFixture(t)
	f.middleware.runtime.apply(
		f.agent,
		"sess_owned",
		acpRuntimeEvent{
			kind: "tool_call_pending",
			toolCall: domain.RuntimeToolCall{
				ToolCallID: "call_owned",
				Title:      f.title,
				Kind:       "other",
				Status:     "pending",
				RawInput:   json.RawMessage(`{"limit":20}`),
			},
		},
		"turn_owned",
	)
	state := f.middleware.runtime.CurrentCorrelation(f.agent.agentID, "sess_owned")
	state.ActiveToolCalls[0].Title = "modified"
	state.ActiveToolCalls[0].RawInput[0] = '['
	unchanged := f.middleware.runtime.CurrentCorrelation(f.agent.agentID, "sess_owned")
	assert.Equal(t, f.title, unchanged.ActiveToolCalls[0].Title)
	assert.JSONEq(t, `{"limit":20}`, string(unchanged.ActiveToolCalls[0].RawInput))
}

type ownedMCPSessionStore struct {
	Store
	mutate func(*domain.AgentSession)
}

func (s ownedMCPSessionStore) GetSession(
	ctx context.Context,
	principal domain.UserPrincipal,
	sessionID string,
) (domain.AgentSession, error) {
	session, err := s.Store.GetSession(ctx, principal, sessionID)
	if err == nil {
		s.mutate(&session)
	}
	return session, err
}

func TestOwnedMCPToolCallGivenUnavailableInjectionThenRequiresManualApproval(t *testing.T) {
	for name, mutate := range map[string]func(*domain.AgentSession){
		"archived session":                         func(s *domain.AgentSession) { s.ArchivedAt = new(time.Time) },
		"OpenClaw does not inject per-session MCP": func(s *domain.AgentSession) { s.AgentType = "openclaw" },
		"E2EE transport":                           func(s *domain.AgentSession) { s.Transport = domain.SessionTransportE2EE },
	} {
		t.Run(name, func(t *testing.T) {
			f := newOwnedMCPFixture(t)
			f.middleware.store = ownedMCPSessionStore{Store: f.store, mutate: mutate}
			_, ok := f.middleware.ownedMCPToolCall(t.Context(), f.frame, f.params)
			assert.False(t, ok)
		})
	}
}

func TestOwnedMCPApprovalGivenUnsupportedNativeOptionThenPreservesManualApproval(t *testing.T) {
	f := newOwnedMCPFixture(t)
	f.params["options"] = []any{map[string]any{"optionId": "native-reject", "kind": "reject_once"}}
	f.frame.frame.Params = mustMarshalRaw(f.params)
	require.NoError(t, f.frame.replaceFrame(f.frame.frame))
	forwarded := false
	require.NoError(
		t,
		f.middleware.HandleACPFrame(
			t.Context(),
			f.frame,
			func(_ context.Context, frame *acpFrameContext) error {
				forwarded = true
				assert.NotEmpty(t, conversationPermissionRequestApprovalID(frame.payload))
				assert.Equal(t, "session/request_permission", frame.frame.Method)
				return nil
			},
		),
	)
	assert.True(t, forwarded)
	assert.False(t, f.frame.handled)
	approvals, err := f.store.ListApprovals(
		t.Context(),
		domain.ApprovalFilter{
			Principal: domain.UserPrincipal{User: domain.User{UserID: f.agent.ownerUserID}},
		},
	)
	require.NoError(t, err)
	require.Len(t, approvals, 1)
	assert.Equal(t, "pending", approvals[0].Status)
	assert.Empty(t, approvals[0].Decision)
}

func TestOwnedMCPToolCallGivenCachedCodexIdentityThenRejectsChangedServerOrTool(t *testing.T) {
	for name, tc := range map[string]struct {
		cachedServer, cachedTool, cachedArguments string
		want                                      bool
	}{
		"conflicting cached server":       {cachedServer: "other-server", cachedTool: "list_agents", cachedArguments: `{"limit":1}`},
		"conflicting cached tool":         {cachedServer: "%s", cachedTool: "workspace_read", cachedArguments: `{"limit":1}`},
		"changed arguments same identity": {cachedServer: "%s", cachedTool: "list_agents", cachedArguments: `{"limit":20}`, want: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOwnedMCPFixture(t)
			server := agentConversationMCPServerName("sess_owned")
			title := "mcp." + server + ".list_agents"
			cached := domain.RuntimeToolCall{
				ToolCallID: "call_owned",
				Kind:       "execute",
				Title:      title,
				Status:     "in_progress",
				RawInput: mustMarshalRaw(
					map[string]any{
						"server":    strings.ReplaceAll(tc.cachedServer, "%s", server),
						"tool":      tc.cachedTool,
						"arguments": json.RawMessage(tc.cachedArguments),
					},
				),
			}
			f.middleware.runtime.apply(
				f.agent,
				"sess_owned",
				acpRuntimeEvent{kind: "tool_call_started", toolCall: cached},
				"turn_owned",
			)
			f.params["toolCall"] = map[string]any{
				"toolCallId": "call_owned",
				"kind":       "execute",
				"title":      title,
				"rawInput": map[string]any{
					"server":    server,
					"tool":      "list_agents",
					"arguments": map[string]any{"limit": 1},
				},
			}
			_, ok := f.middleware.ownedMCPToolCall(t.Context(), f.frame, f.params)
			assert.Equal(t, tc.want, ok)
		})
	}
}

func TestCurrentTurnIDGivenInvalidOrMissingContextThenReturnsEmpty(t *testing.T) {
	runtime := newACPRuntimeProjector(time.Now)
	agent := &ACPTunnelAgent{agentID: "agent_turn"}
	for name, tc := range map[string]struct {
		runtime *acpRuntimeProjector
		agent   *ACPTunnelAgent
		session string
	}{
		"nil projector":   {nil, agent, "session_turn"},
		"nil agent":       {runtime, nil, "session_turn"},
		"empty agent":     {runtime, &ACPTunnelAgent{}, "session_turn"},
		"empty session":   {runtime, agent, ""},
		"missing session": {runtime, agent, "session_missing"},
	} {
		t.Run(
			name,
			func(t *testing.T) { assert.Empty(t, tc.runtime.CurrentTurnID(tc.agent, tc.session)) },
		)
	}
}

func TestCurrentTurnIDGivenActiveTurnThenDoesNotCopyToolArguments(t *testing.T) {
	runtime := newACPRuntimeProjector(time.Now)
	agent := &ACPTunnelAgent{agentID: "agent_turn"}
	runtime.apply(
		agent,
		"session_turn",
		acpRuntimeEvent{kind: "prompt_started", requestID: "prompt_turn", turnID: "turn_active"},
		"",
	)
	runtime.apply(
		agent,
		"session_turn",
		acpRuntimeEvent{
			kind: "tool_call_pending",
			toolCall: domain.RuntimeToolCall{
				ToolCallID: "tool_turn",
				RawInput:   json.RawMessage(strings.Repeat("x", 32*1024)),
			},
		},
		"turn_active",
	)
	assert.Equal(t, "turn_active", runtime.CurrentTurnID(agent, "session_turn"))
	assert.Zero(
		t,
		testing.AllocsPerRun(100, func() { runtime.CurrentTurnID(agent, "session_turn") }),
		"reading the turn identity on streaming frames must not copy tool arguments",
	)
}
