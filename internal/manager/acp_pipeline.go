package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type acpFrameDirection string

const (
	acpAgentToUser acpFrameDirection = "agent_to_user"
	acpUserToAgent acpFrameDirection = "user_to_agent"
)

type acpFrameContext struct {
	agent       *ACPTunnelAgent
	direction   acpFrameDirection
	messageType int
	payload     []byte
	frame       acpJSONRPCMessage
	handled     bool
}

type acpFrameHandler func(context.Context, *acpFrameContext) error

type acpFrameMiddleware interface {
	HandleACPFrame(context.Context, *acpFrameContext, acpFrameHandler) error
}

type acpFramePipeline struct {
	middlewares []acpFrameMiddleware
}

func newACPFramePipeline(middlewares ...acpFrameMiddleware) acpFramePipeline {
	return acpFramePipeline{middlewares: middlewares}
}

func (p acpFramePipeline) Handle(
	ctx context.Context,
	frame *acpFrameContext,
	final acpFrameHandler,
) error {
	handler := final
	for i := len(p.middlewares) - 1; i >= 0; i-- {
		middleware := p.middlewares[i]
		next := handler
		handler = func(c context.Context, f *acpFrameContext) error {
			return middleware.HandleACPFrame(c, f, next)
		}
	}
	return handler(ctx, frame)
}

func newACPFrameContext(
	agent *ACPTunnelAgent,
	direction acpFrameDirection,
	messageType int,
	payload []byte,
) *acpFrameContext {
	frame := acpJSONRPCMessage{}
	if messageType == websocket.TextMessage || messageType == websocket.BinaryMessage {
		_ = json.Unmarshal(payload, &frame)
	}
	return &acpFrameContext{
		agent:       agent,
		direction:   direction,
		messageType: messageType,
		payload:     payload,
		frame:       frame,
	}
}

func (f *acpFrameContext) replaceFrame(frame acpJSONRPCMessage) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	f.frame = frame
	f.payload = payload
	f.messageType = websocket.TextMessage
	return nil
}

type acpApprovalMiddleware struct {
	store Store
}

func (m acpApprovalMiddleware) HandleACPFrame(
	ctx context.Context,
	frame *acpFrameContext,
	next acpFrameHandler,
) error {
	if frame.direction != acpAgentToUser || frame.frame.Method != "session/request_permission" {
		return next(ctx, frame)
	}
	params, err := decodeACPParams(frame.frame.Params)
	if err != nil {
		return err
	}
	fingerprint, err := acpPermissionFingerprint(params)
	if err != nil {
		return err
	}
	domainName := stringField(params, "domain", "agent_action")
	operation := stringField(params, "operation", "session/request_permission")
	_, err = m.store.FindReusableApprovalGrant(ctx, ApprovalGrantLookup{
		OwnerUserID:       frame.agent.ownerUserID,
		RequestNodeID:     frame.agent.nodeID,
		RequestAgentID:    frame.agent.agentID,
		RequestSessionID:  frame.agent.sessionID,
		Domain:            domainName,
		Operation:         operation,
		ActionFingerprint: fingerprint,
	})
	if err == nil {
		response, buildErr := acpAllowOnceResponse(frame.frame, params)
		if buildErr != nil {
			return buildErr
		}
		if writeErr := frame.agent.writeToAgent(websocket.TextMessage, response); writeErr != nil {
			return writeErr
		}
		frame.handled = true
		return nil
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	if appendACPAllowAlwaysOption(params) {
		frame.frame.Params = mustMarshalRaw(params)
		if err := frame.replaceFrame(frame.frame); err != nil {
			return err
		}
	}
	return next(ctx, frame)
}

type acpRuntimeStateMiddleware struct {
	projector *acpRuntimeProjector
}

func (m acpRuntimeStateMiddleware) HandleACPFrame(
	ctx context.Context,
	frame *acpFrameContext,
	next acpFrameHandler,
) error {
	if m.projector != nil {
		_ = m.projector.Observe(ctx, frame)
	}
	return next(ctx, frame)
}

type acpRuntimeProjector struct {
	store acpRuntimeStateStore
	clock func() time.Time

	mu       sync.Mutex
	sessions map[string]*acpRuntimeSession
}

type acpRuntimeStateStore interface {
	UpdateSessionRuntimeState(context.Context, domain.SessionRuntimeState) error
}

type acpRuntimeSession struct {
	state              domain.SessionRuntimeState
	promptRequests     map[string]string
	permissionRequests map[string]string
}

func newACPRuntimeProjector(
	store acpRuntimeStateStore,
	clock func() time.Time,
) *acpRuntimeProjector {
	return &acpRuntimeProjector{
		store:    store,
		clock:    clock,
		sessions: make(map[string]*acpRuntimeSession),
	}
}

func (p *acpRuntimeProjector) Observe(ctx context.Context, frame *acpFrameContext) error {
	if frame.agent == nil || frame.agent.agentID == "" || frame.agent.sessionID == "" {
		return nil
	}
	event, ok := p.eventFromFrame(frame)
	if !ok {
		return nil
	}
	state := p.apply(frame.agent, event)
	return p.store.UpdateSessionRuntimeState(ctx, state)
}

type acpRuntimeEvent struct {
	kind        string
	requestID   string
	turnID      string
	stopReason  string
	errorText   string
	toolCall    domain.RuntimeToolCall
	approvalRef string
}

func (p *acpRuntimeProjector) eventFromFrame(frame *acpFrameContext) (acpRuntimeEvent, bool) {
	switch frame.direction {
	case acpUserToAgent:
		if frame.frame.Method == "session/prompt" {
			return acpRuntimeEvent{
				kind:      "prompt_started",
				requestID: acpRequestID(frame.frame.ID),
			}, true
		}
		if requestID := acpRequestID(frame.frame.ID); requestID != "" {
			return acpRuntimeEvent{
				kind:      "permission_resolved",
				requestID: requestID,
			}, true
		}
	case acpAgentToUser:
		if frame.frame.Method == "session/request_permission" {
			params, _ := decodeACPParams(frame.frame.Params)
			return acpRuntimeEvent{
				kind:        "permission_requested",
				requestID:   acpRequestID(frame.frame.ID),
				approvalRef: acpToolCallID(params),
				toolCall:    runtimeToolCallFromPermission(params),
			}, true
		}
		if frame.frame.Method == "session/update" {
			return acpRuntimeEventFromUpdate(frame.frame.Params)
		}
		if requestID := acpRequestID(frame.frame.ID); requestID != "" &&
			(len(frame.frame.Result) > 0 || len(frame.frame.Error) > 0) {
			result := map[string]any{}
			_ = json.Unmarshal(frame.frame.Result, &result)
			errObject := map[string]any{}
			_ = json.Unmarshal(frame.frame.Error, &errObject)
			if len(errObject) > 0 {
				return acpRuntimeEvent{
					kind:      "prompt_failed",
					requestID: requestID,
					errorText: firstNonEmpty(
						stringField(errObject, "message", ""),
						fmt.Sprint(errObject),
					),
				}, true
			}
			return acpRuntimeEvent{
				kind:       "prompt_completed",
				requestID:  requestID,
				stopReason: stringField(result, "stopReason", ""),
			}, true
		}
	}
	return acpRuntimeEvent{}, false
}

func (p *acpRuntimeProjector) apply(
	agent *ACPTunnelAgent,
	event acpRuntimeEvent,
) domain.SessionRuntimeState {
	key := acpSessionKey(agent.agentID, agent.sessionID)
	p.mu.Lock()
	defer p.mu.Unlock()
	session := p.sessions[key]
	if session == nil {
		session = &acpRuntimeSession{
			state: domain.SessionRuntimeState{
				OwnerUserID: agent.ownerUserID,
				NodeID:      agent.nodeID,
				AgentID:     agent.agentID,
				SessionID:   agent.sessionID,
				Lifecycle:   domain.RuntimeLifecycleIdle,
			},
			promptRequests:     make(map[string]string),
			permissionRequests: make(map[string]string),
		}
		p.sessions[key] = session
	}
	state := session.state
	state.OwnerUserID = agent.ownerUserID
	state.NodeID = agent.nodeID
	state.AgentID = agent.agentID
	state.SessionID = agent.sessionID
	state.UpdatedAt = p.clock().UTC()

	switch event.kind {
	case "prompt_started":
		state.Lifecycle = domain.RuntimeLifecycleRunning
		state.ActivePromptRequestID = event.requestID
		state.ActiveTurnID = firstNonEmpty(event.turnID, event.requestID)
		state.BlockedReason = ""
		state.BlockedRef = ""
		state.PendingApprovalID = ""
		state.LastError = ""
		state.LastStopReason = ""
		state.ActiveToolCalls = nil
		if event.requestID != "" {
			session.promptRequests[event.requestID] = state.ActiveTurnID
		}
	case "permission_requested":
		state.Lifecycle = domain.RuntimeLifecycleWaitingApproval
		state.BlockedReason = domain.RuntimeBlockedReasonToolApproval
		state.BlockedRef = firstNonEmpty(event.approvalRef, event.requestID)
		state.PendingApprovalID = event.requestID
		state.ActiveToolCalls = upsertRuntimeToolCall(state.ActiveToolCalls, event.toolCall)
		if event.requestID != "" {
			session.permissionRequests[event.requestID] = state.BlockedRef
		}
	case "permission_resolved":
		if _, ok := session.permissionRequests[event.requestID]; ok {
			delete(session.permissionRequests, event.requestID)
			state.PendingApprovalID = ""
			state.BlockedReason = ""
			state.BlockedRef = ""
			state.Lifecycle = domain.RuntimeLifecycleRunning
		}
	case "tool_call_pending", "tool_call_started", "tool_call_completed":
		state.ActiveToolCalls = upsertRuntimeToolCall(state.ActiveToolCalls, event.toolCall)
		if state.Lifecycle == domain.RuntimeLifecycleIdle {
			state.Lifecycle = domain.RuntimeLifecycleRunning
		}
	case "prompt_completed":
		if _, ok := session.promptRequests[event.requestID]; ok {
			delete(session.promptRequests, event.requestID)
			state.Lifecycle = domain.RuntimeLifecycleIdle
			state.BlockedReason = ""
			state.BlockedRef = ""
			state.PendingApprovalID = ""
			state.ActivePromptRequestID = ""
			state.LastStopReason = event.stopReason
		}
	case "prompt_failed":
		if _, ok := session.promptRequests[event.requestID]; ok {
			delete(session.promptRequests, event.requestID)
			state.Lifecycle = domain.RuntimeLifecycleErrored
			state.BlockedReason = ""
			state.BlockedRef = ""
			state.PendingApprovalID = ""
			state.ActivePromptRequestID = ""
			state.LastError = event.errorText
		}
	}
	session.state = state
	return state
}

func acpRuntimeEventFromUpdate(raw json.RawMessage) (acpRuntimeEvent, bool) {
	params := map[string]any{}
	if err := json.Unmarshal(raw, &params); err != nil {
		return acpRuntimeEvent{}, false
	}
	update, ok := params["update"].(map[string]any)
	if !ok {
		return acpRuntimeEvent{}, false
	}
	updateType := stringField(update, "sessionUpdate", "")
	if updateType != "tool_call" && updateType != "tool_call_update" {
		return acpRuntimeEvent{}, false
	}
	toolCall := runtimeToolCallFromUpdate(update)
	switch toolCall.Status {
	case "pending":
		return acpRuntimeEvent{kind: "tool_call_pending", toolCall: toolCall}, true
	case "in_progress":
		return acpRuntimeEvent{kind: "tool_call_started", toolCall: toolCall}, true
	case "completed", "failed":
		return acpRuntimeEvent{kind: "tool_call_completed", toolCall: toolCall}, true
	default:
		if toolCall.ToolCallID != "" {
			return acpRuntimeEvent{kind: "tool_call_pending", toolCall: toolCall}, true
		}
		return acpRuntimeEvent{}, false
	}
}

func runtimeToolCallFromPermission(params map[string]any) domain.RuntimeToolCall {
	toolCall, _ := params["toolCall"].(map[string]any)
	return runtimeToolCallFromUpdate(toolCall)
}

func runtimeToolCallFromUpdate(update map[string]any) domain.RuntimeToolCall {
	rawInput, _ := json.Marshal(update["rawInput"])
	status := stringField(update, "status", "")
	return domain.RuntimeToolCall{
		ToolCallID: firstNonEmpty(
			stringField(update, "toolCallId", ""),
			stringField(update, "tool_call_id", ""),
		),
		Kind:     stringField(update, "kind", ""),
		Title:    stringField(update, "title", ""),
		Status:   firstNonEmpty(status, "pending"),
		RawInput: json.RawMessage(rawInput),
	}
}

func upsertRuntimeToolCall(
	calls []domain.RuntimeToolCall,
	next domain.RuntimeToolCall,
) []domain.RuntimeToolCall {
	if next.ToolCallID == "" {
		return calls
	}
	for i := range calls {
		if calls[i].ToolCallID == next.ToolCallID {
			calls[i] = mergeRuntimeToolCall(calls[i], next)
			return calls
		}
	}
	return append(calls, next)
}

func mergeRuntimeToolCall(
	current domain.RuntimeToolCall,
	next domain.RuntimeToolCall,
) domain.RuntimeToolCall {
	if next.Kind != "" {
		current.Kind = next.Kind
	}
	if next.Title != "" {
		current.Title = next.Title
	}
	if next.Status != "" {
		current.Status = next.Status
	}
	if len(next.RawInput) > 0 && string(next.RawInput) != "null" {
		current.RawInput = next.RawInput
	}
	return current
}

func acpToolCallID(params map[string]any) string {
	toolCall, _ := params["toolCall"].(map[string]any)
	return firstNonEmpty(
		stringField(toolCall, "toolCallId", ""),
		stringField(toolCall, "tool_call_id", ""),
	)
}

func acpRequestID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return fmt.Sprintf("%d", n)
	}
	return string(raw)
}

func acpSessionKey(agentID string, sessionID string) string {
	return agentID + "\x00" + sessionID
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
