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
	agent             *ACPTunnelAgent
	direction         acpFrameDirection
	messageType       int
	payload           []byte
	frame             acpJSONRPCMessage
	transportMetadata map[string]string
	managerSessionID  string
	nativeSessionID   string
	requestKind       string
	legacyRaw         bool
	dropReason        string
	handled           bool
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

type acpSessionIDMiddleware struct {
	store Store
}

type acpSessionLifecycleMiddleware struct {
	store Store
}

func (m acpSessionLifecycleMiddleware) HandleACPFrame(
	ctx context.Context,
	frame *acpFrameContext,
	next acpFrameHandler,
) error {
	if frame.agent == nil {
		return next(ctx, frame)
	}
	requestID := acpJSONRPCID(frame.frame)
	if frame.direction == acpUserToAgent && frame.frame.Method == "session/new" {
		managerSessionID := frame.managerSessionID
		if managerSessionID == "" {
			var err error
			managerSessionID, err = frame.agent.ensureManagerSessionID()
			if err != nil {
				return err
			}
		}
		frame.managerSessionID = managerSessionID
		frame.agent.trackSessionNew(requestID, managerSessionID)
		return next(ctx, frame)
	}
	if frame.direction != acpAgentToUser || requestID == "" {
		return next(ctx, frame)
	}
	managerSessionID, ok := frame.agent.takeSessionNew(requestID)
	if !ok {
		return next(ctx, frame)
	}
	frame.managerSessionID = managerSessionID
	if len(frame.frame.Error) > 0 {
		return next(ctx, frame)
	}
	nativeSessionID := findStringFromRaw(frame.frame.Result, "sessionId", "session_id")
	if nativeSessionID == "" {
		return next(ctx, frame)
	}
	if err := m.bindNativeSessionID(ctx, frame.agent, managerSessionID, nativeSessionID); err != nil {
		return err
	}
	payload, changed, err := rewriteACPFrameSessionID(frame.payload, managerSessionID)
	if err != nil {
		return err
	}
	if changed {
		frame.payload = payload
		_ = json.Unmarshal(payload, &frame.frame)
	}
	return next(ctx, frame)
}

func (m acpSessionLifecycleMiddleware) bindNativeSessionID(
	ctx context.Context,
	agent *ACPTunnelAgent,
	managerSessionID string,
	nativeSessionID string,
) error {
	if m.store == nil || agent == nil || managerSessionID == "" || nativeSessionID == "" {
		return nil
	}
	req := domain.CreateSessionRequest{
		NodeID:    agent.nodeID,
		AgentID:   agent.agentID,
		SessionID: managerSessionID,
		NativeID:  nativeSessionID,
		Source:    domain.MessageSourceACPTunnel,
	}
	principal := domain.UserPrincipal{User: domain.User{UserID: agent.ownerUserID}}
	if agent.nodeID != "" {
		if _, err := m.store.CreateNodeAgentSession(ctx, principal, req); err == nil {
			return nil
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
	}
	return m.store.UpsertAgentStatus(ctx, domain.AgentStatusReport{
		AgentID: agent.agentID,
		Sessions: []domain.SessionStatusInput{{
			SessionID: managerSessionID,
			NativeID:  nativeSessionID,
			Source:    domain.MessageSourceACPTunnel,
			Status:    "idle",
		}},
	})
}

func acpJSONRPCID(frame acpJSONRPCMessage) string {
	if len(frame.ID) == 0 {
		return ""
	}
	return string(frame.ID)
}

func (m acpSessionIDMiddleware) HandleACPFrame(
	ctx context.Context,
	frame *acpFrameContext,
	next acpFrameHandler,
) error {
	if frame.agent == nil {
		return next(ctx, frame)
	}
	payloadSessionID := frameSessionID(frame.frame)
	targetSessionID := ""
	if frame.direction == acpUserToAgent {
		managerSessionID := firstNonEmpty(frame.managerSessionID, payloadSessionID)
		if managerSessionID == "" {
			return next(ctx, frame)
		}
		nativeID, err := m.nativeSessionID(ctx, frame.agent, managerSessionID)
		if err != nil {
			return err
		}
		frame.managerSessionID = managerSessionID
		frame.nativeSessionID = nativeID
		targetSessionID = nativeID
	} else {
		nativeSessionID := firstNonEmpty(payloadSessionID, frame.nativeSessionID)
		if nativeSessionID == "" {
			return next(ctx, frame)
		}
		managerID, err := m.managerSessionID(ctx, frame.agent, nativeSessionID)
		if err != nil {
			return err
		}
		frame.nativeSessionID = nativeSessionID
		frame.managerSessionID = managerID
		targetSessionID = managerID
	}
	payload, ok, err := rewriteACPFrameSessionID(frame.payload, targetSessionID)
	if err != nil {
		return err
	}
	if ok {
		frame.payload = payload
		_ = json.Unmarshal(payload, &frame.frame)
	}
	return next(ctx, frame)
}

func (m acpSessionIDMiddleware) managerSessionID(
	ctx context.Context,
	agent *ACPTunnelAgent,
	frameSessionID string,
) (string, error) {
	if m.store == nil {
		return frameSessionID, nil
	}
	sessions, err := m.store.ListAgentSessions(
		ctx,
		domain.UserPrincipal{User: domain.User{UserID: agent.ownerUserID}},
		agent.agentID,
	)
	if err != nil {
		return frameSessionID, nil
	}
	for _, session := range sessions {
		if frameSessionID == session.SessionID {
			return session.SessionID, nil
		}
		if frameSessionID == session.NativeID {
			return session.SessionID, nil
		}
	}
	return frameSessionID, nil
}

func (m acpSessionIDMiddleware) nativeSessionID(
	ctx context.Context,
	agent *ACPTunnelAgent,
	frameSessionID string,
) (string, error) {
	if m.store == nil {
		return frameSessionID, nil
	}
	sessions, err := m.store.ListAgentSessions(
		ctx,
		domain.UserPrincipal{User: domain.User{UserID: agent.ownerUserID}},
		agent.agentID,
	)
	if err != nil {
		return frameSessionID, nil
	}
	for _, session := range sessions {
		if frameSessionID == session.SessionID {
			return firstNonEmpty(session.NativeID, session.SessionID), nil
		}
		if frameSessionID == session.NativeID {
			return firstNonEmpty(session.NativeID, session.SessionID), nil
		}
	}
	return frameSessionID, nil
}

type acpSessionMuxMiddleware struct{}

func (acpSessionMuxMiddleware) HandleACPFrame(
	ctx context.Context,
	frame *acpFrameContext,
	next acpFrameHandler,
) error {
	if frame.agent == nil {
		return next(ctx, frame)
	}
	requestID := acpJSONRPCID(frame.frame)
	if frame.direction == acpUserToAgent {
		if !isACPJSONRPCResponse(frame.frame) {
			return next(ctx, frame)
		}
		request, ok := frame.agent.sessionRouter().workerRequestContext(
			requestID,
			frame.managerSessionID,
			frame.agent.currentUser() != nil,
		)
		if !ok {
			frame.dropReason = "unknown_worker_response"
			frame.handled = true
			return nil
		}
		frame.managerSessionID = request.managerSessionID
		frame.nativeSessionID = request.nativeSessionID
		frame.requestKind = request.requestKind
		err := next(ctx, frame)
		if err == nil {
			frame.agent.sessionRouter().completeWorkerRequest(requestID, request)
		}
		return err
	}
	if frame.managerSessionID == "" && isACPJSONRPCResponse(frame.frame) {
		managerSessionID, requestKind, ok := frame.agent.responseWaiterContext(
			requestID,
		)
		if ok {
			frame.managerSessionID = managerSessionID
			frame.requestKind = requestKind
			frame.legacyRaw = managerSessionID == ""
		}
	}
	if frame.managerSessionID == "" && !frame.legacyRaw {
		frame.dropReason = "unclassified_sessionless_frame"
		frame.handled = true
		return nil
	}
	workerRequest := acpWorkerRequest{
		managerSessionID: frame.managerSessionID,
		nativeSessionID:  frame.nativeSessionID,
		requestKind:      frame.frame.Method,
	}
	if frame.frame.Method != "" && requestID != "" {
		frame.agent.sessionRouter().trackWorkerRequest(
			requestID,
			workerRequest.managerSessionID,
			workerRequest.nativeSessionID,
			workerRequest.requestKind,
		)
	}
	err := next(ctx, frame)
	if err == nil && frame.handled && workerRequest.managerSessionID != "" {
		frame.agent.sessionRouter().completeWorkerRequest(requestID, workerRequest)
	}
	return err
}

func frameSessionID(frame acpJSONRPCMessage) string {
	for _, raw := range []json.RawMessage{frame.Params, frame.Result} {
		if sessionID := findStringFromRaw(raw, "sessionId", "session_id"); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

func findStringFromRaw(raw json.RawMessage, keys ...string) string {
	if len(raw) == 0 {
		return ""
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return findString(value, keys...)
}

func rewriteACPFrameSessionID(payload []byte, sessionID string) ([]byte, bool, error) {
	if sessionID == "" || !json.Valid(payload) {
		return payload, false, nil
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, false, err
	}
	changed := false
	// Only the ACP envelope owns the transport session ID. Nested session IDs
	// belong to application metadata such as Pax invocation endpoints.
	for _, field := range []string{"params", "result"} {
		container, ok := value[field].(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"sessionId", "session_id"} {
			current, ok := container[key]
			if !ok || current == sessionID {
				continue
			}
			container[key] = sessionID
			changed = true
		}
	}
	if !changed {
		return payload, false, nil
	}
	rewritten, err := json.Marshal(value)
	return rewritten, true, err
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
	handled, err := m.autoApproveSessionPolicy(ctx, frame, params, next)
	if handled || err != nil {
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
		RequestSessionID:  frame.managerSessionID,
		Domain:            domainName,
		Operation:         operation,
		ActionFingerprint: fingerprint,
	})
	if err == nil {
		return m.autoApproveReusableGrant(ctx, frame, params)
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

func (m acpApprovalMiddleware) autoApproveSessionPolicy(
	ctx context.Context,
	frame *acpFrameContext,
	params map[string]any,
	next acpFrameHandler,
) (bool, error) {
	sessionID, ok := m.autoApproveSessionID(ctx, frame)
	if !ok {
		return false, nil
	}
	fingerprint, err := acpPermissionFingerprint(params)
	if err != nil {
		return true, err
	}
	requestID := acpRequestID(frame.frame.ID)
	approval, err := m.store.CreateApproval(ctx, Node{
		NodeID:      frame.agent.nodeID,
		OwnerUserID: frame.agent.ownerUserID,
	}, CreateApprovalRequest{
		AgentID:      frame.agent.agentID,
		SessionID:    sessionID,
		NativeID:     requestID,
		Domain:       stringField(params, "domain", "agent_action"),
		Operation:    stringField(params, "operation", "session/request_permission"),
		ResourceType: "acp_permission",
		ResourceRef:  firstNonEmpty(acpToolCallID(params), requestID),
		Title:        conversationApprovalTitle(params),
		Description:  conversationApprovalDescription(params),
		RiskLevel: firstNonEmpty(
			stringField(params, "riskLevel", ""),
			stringField(params, "risk_level", ""),
			"unknown",
		),
		ActionFingerprint: fingerprint,
		RequestBody:       append(json.RawMessage(nil), frame.frame.Params...),
		RequestedEffects:  json.RawMessage(`[]`),
		Options:           conversationApprovalOptions(params),
		RawPayload:        append(json.RawMessage(nil), frame.payload...),
	})
	if err != nil {
		return true, err
	}
	forwarded, err := injectConversationApprovalID(frame.payload, approval.ApprovalID)
	if err != nil {
		return true, err
	}
	frame.payload = forwarded
	_ = json.Unmarshal(forwarded, &frame.frame)
	if err := next(ctx, frame); err != nil {
		return true, err
	}
	principal := domain.UserPrincipal{User: domain.User{UserID: frame.agent.ownerUserID}}
	decided, err := m.store.DecideApproval(
		ctx,
		principal,
		approval.ApprovalID,
		ApprovalDecisionRequest{
			DecisionOption: "allow_once",
			GrantBody:      json.RawMessage(`{"approval_mode":"auto_approve_all"}`),
		},
	)
	if err != nil {
		return true, err
	}
	response, err := acpPermissionResponseFromApproval(decided)
	if err != nil {
		return true, err
	}
	if err := frame.agent.writeWorkerResponse(
		ctx,
		frame.managerSessionID,
		frame.nativeSessionID,
		websocket.TextMessage,
		response,
	); err != nil {
		return true, err
	}
	recorded, err := m.store.RecordApprovalResponse(
		ctx,
		principal,
		decided.ApprovalID,
		response,
		"",
	)
	if err != nil {
		return true, err
	}
	eventFrame, err := conversationPermissionResponseEventFrame(response, recorded)
	if err != nil {
		return true, err
	}
	if err := projectACPUserPromptForSession(
		ctx,
		frame.agent,
		frame.managerSessionID,
		response,
	); err != nil {
		return true, err
	}
	if err := persistApprovalResponseHistory(
		ctx,
		m.store,
		frame.agent.agentID,
		sessionID,
		response,
		eventFrame,
	); err != nil {
		return true, err
	}
	if err := m.notifyAutoApprovedFrame(ctx, frame, eventFrame); err != nil {
		return true, err
	}
	frame.handled = true
	return true, nil
}

func (m acpApprovalMiddleware) autoApproveSessionID(
	ctx context.Context,
	frame *acpFrameContext,
) (string, bool) {
	if m.store == nil || frame == nil || frame.agent == nil {
		return "", false
	}
	sessionID := frame.managerSessionID
	if sessionID == "" {
		return "", false
	}
	session, err := m.store.GetSession(
		ctx,
		domain.UserPrincipal{User: domain.User{UserID: frame.agent.ownerUserID}},
		sessionID,
	)
	if err != nil {
		return "", false
	}
	if domain.NormalizeSessionApprovalMode(session.PaxConfig.ApprovalMode) !=
		domain.SessionApprovalModeAutoApproveAll {
		return "", false
	}
	return sessionID, true
}

func (m acpApprovalMiddleware) autoApproveReusableGrant(
	ctx context.Context,
	frame *acpFrameContext,
	params map[string]any,
) error {
	response, err := acpAllowOnceResponse(frame.frame, params)
	if err != nil {
		return err
	}
	if err := frame.agent.writeWorkerResponse(
		ctx,
		frame.managerSessionID,
		frame.nativeSessionID,
		websocket.TextMessage,
		response,
	); err != nil {
		return err
	}
	if err := m.notifyAutoApprovedFrame(ctx, frame, json.RawMessage(response)); err != nil {
		return err
	}
	if err := projectACPUserPromptForSession(
		ctx,
		frame.agent,
		frame.managerSessionID,
		response,
	); err != nil {
		return err
	}
	frame.handled = true
	return nil
}

func (m acpApprovalMiddleware) notifyAutoApprovedFrame(
	ctx context.Context,
	frame *acpFrameContext,
	payload json.RawMessage,
) error {
	if len(payload) == 0 || frame == nil || frame.agent == nil {
		return nil
	}
	frame.agent.publishSSE(frame.managerSessionID, payload)
	userWS := frame.agent.currentUserForSession(frame.managerSessionID)
	if userWS == nil {
		userWS = frame.agent.currentLegacyRawUser()
	}
	if userWS == nil {
		return nil
	}
	frame.agent.userWriteMu.Lock()
	defer frame.agent.userWriteMu.Unlock()
	return userWS.WriteMessage(websocket.TextMessage, payload)
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
	if frame.agent == nil || frame.agent.agentID == "" || frame.managerSessionID == "" {
		return nil
	}
	event, ok := p.eventFromFrame(frame)
	if !ok {
		return nil
	}
	state := p.apply(frame.agent, frame.managerSessionID, event)
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
	managerSessionID string,
	event acpRuntimeEvent,
) domain.SessionRuntimeState {
	key := acpSessionKey(agent.agentID, managerSessionID)
	p.mu.Lock()
	defer p.mu.Unlock()
	session := p.sessions[key]
	if session == nil {
		session = &acpRuntimeSession{
			state: domain.SessionRuntimeState{
				OwnerUserID: agent.ownerUserID,
				NodeID:      agent.nodeID,
				AgentID:     agent.agentID,
				SessionID:   managerSessionID,
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
	state.SessionID = managerSessionID
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
	toolCall, _ := mapField(params, "toolCall", "tool_call")
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
