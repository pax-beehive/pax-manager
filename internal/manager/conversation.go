package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

const (
	conversationTunnelClaimTimeout = 2 * time.Second
	conversationTunnelClaimTick    = 50 * time.Millisecond
)

var conversationRequestIdleTimeout = 5 * time.Minute
var provisionalConversationCleanupTimeout = time.Second

type conversationRequest struct {
	SessionID          string                     `json:"session_id,omitempty"`
	Input              string                     `json:"input"`
	Content            []conversationContentBlock `json:"content,omitempty"`
	Resume             json.RawMessage            `json:"resume,omitempty"`
	CWD                string                     `json:"cwd,omitempty"`
	ApprovalMode       string                     `json:"approval_mode,omitempty"`
	PermissionChoiceID string                     `json:"permission_choice_id,omitempty"`
	PrimaryProjectID   string                     `json:"primary_project_id,omitempty"`
	ProjectTargetID    string                     `json:"project_target_id,omitempty"`
}

type conversationEvent struct {
	Type       string          `json:"type"`
	NodeID     string          `json:"node_id,omitempty"`
	AgentID    string          `json:"agent_id,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
	StatusCode int             `json:"status_code,omitempty"`
	TurnID     string          `json:"turn_id,omitempty"`
	MessageID  string          `json:"message_id,omitempty"`
	Status     string          `json:"status,omitempty"`
	Frame      json.RawMessage `json:"frame,omitempty"`
	ApprovalID string          `json:"approval_id,omitempty"`
	Approval   *AgentApproval  `json:"approval,omitempty"`
	Message    string          `json:"message,omitempty"`
	Reason     string          `json:"reason,omitempty"`
}

type conversationSession struct {
	managerID string
	nativeID  string
}

type conversationRunner struct {
	service          *Service
	agentConn        *ACPTunnelAgent
	managerSessionID string
	turnID           string
}

type conversationResponse struct {
	acpJSONRPCMessage
	raw []byte
}

type conversationACPError struct {
	message   string
	kind      string
	retryable bool
}

func (e conversationACPError) Error() string {
	return firstNonEmpty(e.message, "ACP request failed")
}

func (e conversationACPError) Unwrap() error {
	return apperr.Error{Status: http.StatusBadGateway, Message: e.Error()}
}

type conversationResumeRequest struct {
	Requested  bool
	ApprovalID string
}

func (s *Service) handleConversation(w http.ResponseWriter, r *http.Request) {
	ctx, logID := httpRequestLogContext(r.Context(), r)
	w.Header().Set(logging.HeaderLogID, logID)
	r = r.WithContext(ctx)
	if r.Method != http.MethodPost {
		writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	_, nodeID, agentID := conversationRouteIDs(r)
	if nodeID == "" || agentID == "" {
		writeHTTPError(w, http.StatusBadRequest, "node_id and agent_id are required")
		return
	}
	principal, agent, err := s.authorizeConversation(r, nodeID, agentID)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	req, resumeReq, ok := s.readConversationRequest(w, r)
	if !ok {
		return
	}
	req, err = s.resolveConversationProjectContext(
		r.Context(),
		principal,
		agentID,
		req,
	)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	var prompt []map[string]any
	if !resumeReq.Requested {
		prompt, err = s.resolveConversationPrompt(
			r.Context(),
			principal,
			nodeID,
			req,
		)
		if err != nil {
			writeHTTPEndpointError(w, err)
			return
		}
	}

	session, err := s.resolveConversationSession(
		r.Context(),
		principal,
		nodeID,
		agentID,
		req.SessionID,
	)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	if session.managerID == "" {
		session.managerID, err = newManagerSessionID()
		if err != nil {
			writeHTTPEndpointError(w, err)
			return
		}
	}
	claimSessionIDs := []string{""}
	if session.nativeID != "" {
		claimSessionIDs = []string{session.managerID, session.nativeID, ""}
	}
	agentConn, release, err := s.acpTunnels.claimStructuredAnyWait(
		r.Context(),
		conversationTunnelClaimTimeout,
		conversationTunnelClaimTick,
		agentID,
		session.managerID,
		claimSessionIDs...,
	)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	defer release()

	runner := conversationRunner{
		service:          s,
		agentConn:        agentConn,
		managerSessionID: session.managerID,
	}
	if session.nativeID == "" {
		session, err = s.createConversationSession(r.Context(), principal, &runner, req)
		if err != nil {
			writeHTTPEndpointError(w, err)
			return
		}
	}
	runner.managerSessionID = session.managerID

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeHTTPError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	if err := s.writeConversationEvent(w, flusher, conversationEvent{
		Type:      "session",
		NodeID:    agent.NodeID,
		AgentID:   agent.AgentID,
		SessionID: session.managerID,
	}); err != nil {
		return
	}
	if resumeReq.Requested {
		err = s.resumeConversation(r.Context(), w, flusher, &runner, session, principal, resumeReq)
	} else {
		err = s.promptConversation(r.Context(), w, flusher, &runner, session, prompt)
	}
	if err != nil {
		status, _ := endpointErrorStatus(err)
		_ = s.writeConversationEvent(w, flusher, conversationEvent{
			Type:       "error",
			NodeID:     agent.NodeID,
			AgentID:    agent.AgentID,
			SessionID:  session.managerID,
			TurnID:     runner.turnID,
			StatusCode: status,
			Message:    err.Error(),
		})
	}
}

func (s *Service) readConversationRequest(
	w http.ResponseWriter,
	r *http.Request,
) (conversationRequest, conversationResumeRequest, bool) {
	var req conversationRequest
	body := r.Body
	if s.maxBodyBytes > 0 {
		body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)
	}
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeHTTPError(w, http.StatusBadRequest, "invalid JSON body")
		return conversationRequest{}, conversationResumeRequest{}, false
	}
	req.Input = strings.TrimSpace(req.Input)
	if err := normalizeConversationContentBlocks(req.Content); err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return conversationRequest{}, conversationResumeRequest{}, false
	}
	resumeReq, err := parseConversationResume(req.Resume)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return conversationRequest{}, conversationResumeRequest{}, false
	}
	if err := validateConversationResumeContent(req, resumeReq); err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return conversationRequest{}, conversationResumeRequest{}, false
	}
	req.CWD = strings.TrimSpace(req.CWD)
	req.ApprovalMode = strings.TrimSpace(req.ApprovalMode)
	req.PermissionChoiceID = strings.TrimSpace(req.PermissionChoiceID)
	if err := validateConversationSessionOptions(req); err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return conversationRequest{}, conversationResumeRequest{}, false
	}
	if err := validateConversationPromptState(req, resumeReq); err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return conversationRequest{}, conversationResumeRequest{}, false
	}
	return req, resumeReq, true
}

func validateConversationResumeContent(
	req conversationRequest,
	resumeReq conversationResumeRequest,
) error {
	if resumeReq.Requested && (req.Input != "" || len(req.Content) > 0) {
		return errors.New("content and resume are mutually exclusive")
	}
	return nil
}

func validateConversationSessionOptions(req conversationRequest) error {
	if !isSupportedSessionWorkspace(req.CWD) {
		return errors.New("cwd must be an absolute path, ~, or start with ~/")
	}
	if req.SessionID != "" && req.CWD != "" {
		return errors.New("cwd can only be set when creating a session")
	}
	if req.SessionID != "" && req.ApprovalMode != "" {
		return errors.New(
			"approval_mode can only be set when creating a session; " +
				"use session PATCH to change it",
		)
	}
	if req.SessionID != "" && req.PermissionChoiceID != "" {
		return errors.New("permission_choice_id can only be set when creating a session")
	}
	if req.SessionID != "" &&
		(req.PrimaryProjectID != "" || req.ProjectTargetID != "") {
		return errors.New("project context can only be set when creating a session")
	}
	if req.ApprovalMode != "" && !domain.IsSessionApprovalMode(req.ApprovalMode) {
		return errors.New("approval_mode must be manual or auto_approve_all")
	}
	if req.PermissionChoiceID != "" &&
		req.PermissionChoiceID != domain.PermissionChoicePAXAutoApprove &&
		!strings.HasPrefix(req.PermissionChoiceID, "agent:") {
		return errors.New("permission_choice_id is invalid")
	}
	if req.PermissionChoiceID != "" && req.ApprovalMode != "" {
		expected := domain.SessionApprovalModeManual
		if req.PermissionChoiceID == domain.PermissionChoicePAXAutoApprove {
			expected = domain.SessionApprovalModeAutoApproveAll
		}
		if req.ApprovalMode != expected {
			return errors.New("approval_mode conflicts with permission_choice_id")
		}
	}
	return nil
}

func validateConversationPromptState(
	req conversationRequest,
	resumeReq conversationResumeRequest,
) error {
	if resumeReq.Requested && req.SessionID == "" {
		return errors.New("session_id is required for resume")
	}
	if !resumeReq.Requested && req.Input == "" && len(req.Content) == 0 {
		return errors.New("input or content is required")
	}
	return nil
}

func (s *Service) resolveConversationProjectContext(
	ctx context.Context,
	principal UserPrincipal,
	agentID string,
	req conversationRequest,
) (conversationRequest, error) {
	req.PrimaryProjectID = strings.TrimSpace(req.PrimaryProjectID)
	req.ProjectTargetID = strings.TrimSpace(req.ProjectTargetID)
	if req.PrimaryProjectID == "" {
		if req.ProjectTargetID != "" {
			return conversationRequest{}, apperr.Error{
				Status:  http.StatusBadRequest,
				Message: "primary_project_id is required with project_target_id",
			}
		}
		return req, nil
	}
	project, err := s.store.GetProject(ctx, principal, req.PrimaryProjectID)
	if err != nil {
		return conversationRequest{}, err
	}
	if project.ArchivedAt != nil {
		return conversationRequest{}, apperr.Error{
			Status:  http.StatusConflict,
			Message: "project is archived",
		}
	}
	if req.ProjectTargetID == "" {
		return req, nil
	}
	target, err := s.store.GetProjectTarget(
		ctx,
		principal,
		req.PrimaryProjectID,
		req.ProjectTargetID,
	)
	if err != nil {
		return conversationRequest{}, err
	}
	if !target.Enabled {
		return conversationRequest{}, apperr.Error{
			Status:  http.StatusConflict,
			Message: "project target is disabled",
		}
	}
	if target.AgentID != agentID {
		return conversationRequest{}, ErrNotFound
	}
	req.CWD = target.Cwd
	return req, nil
}

func isSupportedSessionWorkspace(value string) bool {
	return value == "" ||
		filepath.IsAbs(value) ||
		value == "~" ||
		strings.HasPrefix(value, "~/")
}

func (s *Service) authorizeConversation(
	r *http.Request,
	nodeID string,
	agentID string,
) (UserPrincipal, Agent, error) {
	principal, err := s.auth.Principal(r.Context(), httpRequestMetadata(r))
	if err != nil {
		return UserPrincipal{}, Agent{}, err
	}
	agent, err := s.store.GetAgent(r.Context(), principal, agentID)
	if err != nil {
		return UserPrincipal{}, Agent{}, err
	}
	if agent.NodeID != nodeID {
		return UserPrincipal{}, Agent{}, ErrNotFound
	}
	return principal, agent, nil
}

func (s *Service) resolveConversationSession(
	ctx context.Context,
	principal UserPrincipal,
	nodeID string,
	agentID string,
	sessionID string,
) (conversationSession, error) {
	if sessionID == "" {
		return conversationSession{}, nil
	}
	session, err := s.store.GetSession(ctx, principal, sessionID)
	if err != nil {
		return conversationSession{}, err
	}
	if session.AgentID != agentID || session.NodeID != nodeID {
		return conversationSession{}, ErrNotFound
	}
	if session.NativeID == "" {
		return conversationSession{}, apperr.Error{
			Status:  http.StatusConflict,
			Message: "session has no native ACP session id",
		}
	}
	return conversationSession{managerID: session.SessionID, nativeID: session.NativeID}, nil
}

func (s *Service) createConversationSession(
	ctx context.Context,
	principal UserPrincipal,
	runner *conversationRunner,
	req conversationRequest,
) (conversationSession, error) {
	managerSessionID := runner.managerSessionID
	if managerSessionID == "" {
		return conversationSession{}, errors.New("manager session id is required")
	}
	cwd := firstNonEmpty(req.CWD, "/tmp")
	approvalMode := domain.NormalizeSessionApprovalMode(req.ApprovalMode)
	runner.managerSessionID = managerSessionID
	resp, err := runner.request(
		ctx,
		"session/new",
		map[string]any{
			"cwd": cwd,
			"mcpServers": agentConversationMCPServers(domain.AgentSession{
				AgentID:   runner.agentConn.agentID,
				SessionID: managerSessionID,
			}),
		},
		nil,
	)
	if err != nil {
		return conversationSession{}, err
	}
	createdSessionID := findStringFromRaw(resp.Result, "sessionId", "session_id")
	if createdSessionID == "" {
		return conversationSession{}, apperr.Error{
			Status:  http.StatusBadGateway,
			Message: "ACP session/new did not return a sessionId",
		}
	}
	keepProvisionalSession := false
	defer func() {
		if !keepProvisionalSession {
			s.rollbackProvisionalConversationSession(
				ctx,
				runner,
				runner.agentConn.agentID,
				managerSessionID,
			)
		}
	}()
	if req.PermissionChoiceID != "" {
		resolved, err := s.resolveConversationPermissionChoice(
			ctx,
			principal,
			runner.agentConn.agentID,
			req.PermissionChoiceID,
			resp.Result,
		)
		if err != nil {
			return conversationSession{}, err
		}
		approvalMode = resolved.ApprovalMode
		if err := applyResolvedPermissionChoice(ctx, runner, managerSessionID, resolved); err != nil {
			return conversationSession{}, err
		}
	}
	if _, err := s.store.CreateNodeAgentSession(ctx, principal, domain.CreateSessionRequest{
		NodeID:           runner.agentConn.nodeID,
		AgentID:          runner.agentConn.agentID,
		SessionID:        managerSessionID,
		Source:           domain.MessageSourceACPTunnel,
		PrimaryProjectID: req.PrimaryProjectID,
		PaxConfig: domain.SessionPaxConfig{
			CWD:                cwd,
			ApprovalMode:       approvalMode,
			PermissionChoiceID: req.PermissionChoiceID,
		},
	}); err != nil {
		return conversationSession{}, err
	}
	keepProvisionalSession = true
	return conversationSession{managerID: managerSessionID}, nil
}

type provisionalConversationSessionCleaner interface {
	DeleteProvisionalAgentSession(context.Context, string, string) error
}

func (s *Service) rollbackProvisionalConversationSession(
	ctx context.Context,
	runner permissionConfigRequester,
	agentID string,
	sessionID string,
) {
	baseCtx := context.WithoutCancel(ctx)
	closeCtx, cancelClose := context.WithTimeout(baseCtx, provisionalConversationCleanupTimeout)
	_, _ = runner.request(closeCtx, "session/close", map[string]any{
		"sessionId": sessionID,
	}, nil)
	cancelClose()

	cleaner, ok := s.store.(provisionalConversationSessionCleaner)
	if !ok {
		return
	}
	deleteCtx, cancelDelete := context.WithTimeout(baseCtx, provisionalConversationCleanupTimeout)
	defer cancelDelete()
	if err := cleaner.DeleteProvisionalAgentSession(deleteCtx, agentID, sessionID); err != nil &&
		!errors.Is(err, domain.ErrNotFound) {
		logging.Warn(
			baseCtx,
			"provisional conversation session cleanup failed",
			logging.Err(err),
		)
	}
}

func (s *Service) promptConversation(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	runner *conversationRunner,
	session conversationSession,
	prompt []map[string]any,
) error {
	sub := runner.agentConn.subscribeSSE(session.managerID)
	defer runner.agentConn.unsubscribeSSE(sub)

	nextPrompt := prompt
	nextTurnID, err := newConversationTurnID()
	if err != nil {
		return err
	}
	for {
		completed, err := s.promptConversationOnce(
			ctx,
			w,
			flusher,
			runner,
			session,
			sub,
			nextTurnID,
			nextPrompt,
		)
		if err != nil {
			return err
		}
		if !completed {
			break
		}
		queued, ok := s.conversationTurns.take(runner.agentConn.agentID, session.managerID)
		if !ok {
			break
		}
		nextTurnID = queued.TurnID
		nextPrompt = []map[string]any{{
			"type": "text",
			"text": queued.Input,
		}}
	}
	return s.writeConversationEvent(w, flusher, conversationEvent{
		Type:      "done",
		NodeID:    runner.agentConn.nodeID,
		AgentID:   runner.agentConn.agentID,
		SessionID: session.managerID,
	})
}

func (s *Service) promptConversationOnce(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	runner *conversationRunner,
	session conversationSession,
	sub *acpSSESubscriber,
	turnID string,
	prompt []map[string]any,
) (bool, error) {
	runner.turnID = turnID
	if err := s.writeConversationEvent(w, flusher, conversationEvent{
		Type:      "turn_started",
		NodeID:    runner.agentConn.nodeID,
		AgentID:   runner.agentConn.agentID,
		SessionID: session.managerID,
		TurnID:    turnID,
		Status:    "running",
	}); err != nil {
		return false, err
	}
	params := map[string]any{
		"sessionId": session.managerID,
		"prompt":    prompt,
	}
	for attempt := 0; attempt < 2; attempt++ {
		requestID, waiter, cancel, err := runner.sendTurnPrompt(
			ctx,
			turnID,
			params,
		)
		if err != nil {
			return false, err
		}
		completed, streamErr := s.streamConversationUntilPromptDone(
			ctx,
			w,
			flusher,
			runner,
			session,
			sub,
			fmt.Sprint(requestID),
			waiter,
		)
		cancel()
		if !isConversationACPErrorKind(streamErr, "session_route_missing") || attempt > 0 {
			if streamErr != nil {
				return false, streamErr
			}
			if !completed {
				return false, nil
			}
			if err := s.writeConversationTurnDone(
				ctx,
				w,
				flusher,
				runner.agentConn,
				session,
				turnID,
			); err != nil {
				return false, err
			}
			return true, nil
		}
		if err := runner.resumeMissingRoute(ctx); err != nil {
			return false, err
		}
	}
	return false, errors.New("conversation prompt retry exhausted")
}

func (s *Service) resumeConversation(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	runner *conversationRunner,
	session conversationSession,
	principal UserPrincipal,
	req conversationResumeRequest,
) error {
	approval, promptRequestID, err := s.resolveConversationResumeApproval(
		ctx,
		principal,
		session,
		req,
	)
	if err != nil {
		return err
	}
	turnID, err := s.ensureConversationResumeTurnID(ctx, principal, session)
	if err != nil {
		return err
	}
	runner.turnID = turnID
	if err := s.writeConversationEvent(w, flusher, conversationEvent{
		Type:      "turn_started",
		NodeID:    runner.agentConn.nodeID,
		AgentID:   runner.agentConn.agentID,
		SessionID: session.managerID,
		TurnID:    turnID,
		Status:    "resumed",
	}); err != nil {
		return err
	}
	response, err := acpPermissionResponseFromApproval(approval)
	if err != nil {
		return err
	}
	sub := runner.agentConn.subscribeSSE(session.managerID)
	defer runner.agentConn.unsubscribeSSE(sub)
	waiter, cancel := runner.agentConn.addResponseWaiter(
		promptRequestID,
		session.managerID,
		"session/prompt",
	)
	defer cancel()
	if err := runner.sendWorkerResponseRaw(ctx, response); err != nil {
		return err
	}
	recorded, err := s.store.RecordApprovalResponse(
		ctx,
		principal,
		approval.ApprovalID,
		response,
		"",
	)
	if err != nil {
		return err
	}
	eventFrame, err := conversationPermissionResponseEventFrame(response, recorded)
	if err != nil {
		return err
	}
	if err := s.persistConversationApprovalResponseHistory(ctx, runner, session, response, eventFrame); err != nil {
		logging.Warn(
			ctx,
			"conversation approval response history update failed",
			logging.Err(err),
		)
	}
	if err := s.writeConversationEvent(w, flusher, conversationEvent{
		Type:      "acp",
		NodeID:    runner.agentConn.nodeID,
		AgentID:   runner.agentConn.agentID,
		SessionID: session.managerID,
		TurnID:    turnID,
		Frame:     eventFrame,
	}); err != nil {
		return err
	}
	completed, err := s.streamConversationUntilPromptDone(
		ctx,
		w,
		flusher,
		runner,
		session,
		sub,
		promptRequestID,
		waiter,
	)
	if err != nil {
		return err
	}
	if completed {
		if err := s.writeConversationTurnDone(
			ctx,
			w,
			flusher,
			runner.agentConn,
			session,
			turnID,
		); err != nil {
			return err
		}
	}
	return s.writeConversationEvent(w, flusher, conversationEvent{
		Type:      "done",
		NodeID:    runner.agentConn.nodeID,
		AgentID:   runner.agentConn.agentID,
		SessionID: session.managerID,
	})
}

func (s *Service) ensureConversationResumeTurnID(
	ctx context.Context,
	principal UserPrincipal,
	session conversationSession,
) (string, error) {
	stored, err := s.store.GetSession(ctx, principal, session.managerID)
	if err != nil {
		return "", err
	}
	if turnID := conversationActiveTurnID(stored); turnID != "" {
		return turnID, nil
	}
	turnID, err := newConversationTurnID()
	if err != nil {
		return "", err
	}
	state := domain.SessionRuntimeState{
		OwnerUserID: principal.User.UserID,
		NodeID:      stored.NodeID,
		AgentID:     stored.AgentID,
		SessionID:   stored.SessionID,
		Lifecycle:   domain.RuntimeLifecycleWaitingApproval,
		UpdatedAt:   s.clock().UTC(),
	}
	if stored.RuntimeState != nil {
		state = *stored.RuntimeState
		state.UpdatedAt = s.clock().UTC()
	}
	state.ActiveTurnID = turnID
	if err := s.store.UpdateSessionRuntimeState(ctx, state); err != nil {
		return "", err
	}
	return turnID, nil
}

func (r *conversationRunner) request(
	ctx context.Context,
	method string,
	params map[string]any,
	activity <-chan struct{},
) (conversationResponse, error) {
	for attempt := 0; attempt < 2; attempt++ {
		response, err := r.requestOnce(ctx, method, params, activity)
		if !isConversationACPErrorKind(err, "session_route_missing") || attempt > 0 ||
			method == "session/resume" {
			return response, err
		}
		if err := r.resumeMissingRoute(ctx); err != nil {
			return conversationResponse{}, err
		}
	}
	return conversationResponse{}, errors.New("ACP request retry exhausted")
}

func (r *conversationRunner) requestOnce(
	ctx context.Context,
	method string,
	params map[string]any,
	activity <-chan struct{},
) (conversationResponse, error) {
	requestID, err := r.service.store.NextAgentACPRequestID(ctx, r.agentConn.agentID)
	if err != nil {
		return conversationResponse{}, err
	}
	payload, err := conversationRequestPayload(requestID, method, params)
	if err != nil {
		return conversationResponse{}, err
	}
	waiter, cancel := r.agentConn.addResponseWaiter(
		fmt.Sprint(requestID),
		r.managerSessionID,
		method,
	)
	defer cancel()
	if err := r.sendRaw(ctx, payload); err != nil {
		return conversationResponse{}, err
	}
	timer := time.NewTimer(conversationRequestIdleTimeout)
	defer timer.Stop()
	for {
		select {
		case result := <-waiter:
			if result.err != nil {
				return conversationResponse{}, result.err
			}
			payload := result.payload
			var msg acpJSONRPCMessage
			_ = json.Unmarshal(payload, &msg)
			if len(msg.Error) > 0 {
				return conversationResponse{}, decodeConversationACPError(msg.Error)
			}
			return conversationResponse{acpJSONRPCMessage: msg, raw: payload}, nil
		case <-activity:
			resetConversationIdleTimer(timer)
		case <-timer.C:
			return conversationResponse{}, apperr.Error{
				Status:  http.StatusGatewayTimeout,
				Message: "ACP request idle timed out: " + method,
			}
		case <-ctx.Done():
			return conversationResponse{}, ctx.Err()
		}
	}
}

func (r *conversationRunner) resumeMissingRoute(ctx context.Context) error {
	principal := UserPrincipal{User: User{UserID: r.agentConn.ownerUserID}}
	session, err := r.service.store.GetSession(ctx, principal, r.managerSessionID)
	if err != nil {
		return err
	}
	params := map[string]any{
		"sessionId":  r.managerSessionID,
		"cwd":        firstNonEmpty(session.PaxConfig.CWD, "/tmp"),
		"mcpServers": agentConversationMCPServers(session),
	}
	if len(session.WorkspaceRoots) > 0 {
		params["additionalDirectories"] = append([]string(nil), session.WorkspaceRoots...)
	}
	_, err = r.requestOnce(ctx, "session/resume", params, nil)
	return err
}

func (r *conversationRunner) sendTurnPrompt(
	ctx context.Context,
	turnID string,
	params map[string]any,
) (int64, <-chan acpResponseWaiterResult, func(), error) {
	const method = "session/prompt"
	requestID, err := r.service.store.NextAgentACPRequestID(ctx, r.agentConn.agentID)
	if err != nil {
		return 0, nil, func() {}, err
	}
	payload, err := conversationRequestPayload(requestID, method, params)
	if err != nil {
		return 0, nil, func() {}, err
	}
	waiter, cancel := r.agentConn.addResponseWaiter(
		fmt.Sprint(requestID),
		r.managerSessionID,
		method,
	)
	if err := r.sendRawForTurn(ctx, payload, turnID); err != nil {
		cancel()
		return 0, nil, func() {}, err
	}
	return requestID, waiter, cancel, nil
}

func conversationRequestPayload(
	requestID int64,
	method string,
	params map[string]any,
) ([]byte, error) {
	return json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      requestID,
		"method":  method,
		"params":  params,
	})
}

func (r *conversationRunner) sendRaw(ctx context.Context, payload []byte) error {
	return r.sendRawForTurn(ctx, payload, "")
}

func (r *conversationRunner) sendRawForTurn(
	ctx context.Context,
	payload []byte,
	turnID string,
) error {
	frame := newACPFrameContext(r.agentConn, acpUserToAgent, websocket.TextMessage, payload)
	frame.managerSessionID = r.managerSessionID
	frame.businessTurnID = turnID
	return r.service.userACPFramePipeline().Handle(
		ctx,
		frame,
		func(_ context.Context, frame *acpFrameContext) error {
			if err := r.agentConn.writeToAgent(ctx, frame.messageType, frame.payload); err != nil {
				return err
			}
			return projectACPUserPromptForSessionTurn(
				ctx,
				r.agentConn,
				frame.managerSessionID,
				frame.businessTurnID,
				frame.payload,
			)
		},
	)
}

func (r *conversationRunner) sendWorkerResponseRaw(ctx context.Context, payload []byte) error {
	frame := newACPFrameContext(r.agentConn, acpUserToAgent, websocket.TextMessage, payload)
	frame.managerSessionID = r.managerSessionID
	frame.businessTurnID = r.turnID
	return r.service.userACPFramePipeline().Handle(
		ctx,
		frame,
		func(_ context.Context, frame *acpFrameContext) error {
			if err := r.agentConn.writeWorkerResponse(
				ctx,
				frame.managerSessionID,
				frame.nativeSessionID,
				frame.messageType,
				frame.payload,
			); err != nil {
				return err
			}
			return projectACPUserPromptForSessionTurn(
				ctx,
				r.agentConn,
				frame.managerSessionID,
				frame.businessTurnID,
				frame.payload,
			)
		},
	)
}

func (s *Service) streamConversationUntilPromptDone(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	runner *conversationRunner,
	session conversationSession,
	sub *acpSSESubscriber,
	promptRequestID string,
	waiter <-chan acpResponseWaiterResult,
) (bool, error) {
	timer := time.NewTimer(conversationRequestIdleTimeout)
	defer timer.Stop()
	for {
		select {
		case payload, ok := <-sub.ch:
			if !ok {
				select {
				case err := <-sub.terminal:
					return false, err
				default:
					return false, nil
				}
			}
			resetConversationIdleTimer(timer)
			interrupted, err := s.writeConversationACPEvent(
				ctx,
				w,
				flusher,
				runner,
				session,
				payload,
			)
			if err != nil || interrupted {
				return false, err
			}
		case result := <-waiter:
			if result.err != nil {
				return false, result.err
			}
			payload := result.payload
			s.drainConversationSSE(ctx, w, flusher, runner, session, sub)
			var msg acpJSONRPCMessage
			_ = json.Unmarshal(payload, &msg)
			if len(msg.Error) > 0 {
				return false, decodeConversationACPError(msg.Error)
			}
			return true, nil
		case err, ok := <-sub.terminal:
			if ok && err != nil {
				return false, err
			}
			return false, nil
		case <-timer.C:
			return false, apperr.Error{
				Status:  http.StatusGatewayTimeout,
				Message: "ACP request idle timed out: session/prompt",
			}
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

func (s *Service) drainConversationSSE(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	runner *conversationRunner,
	session conversationSession,
	sub *acpSSESubscriber,
) {
	for {
		select {
		case payload, ok := <-sub.ch:
			if !ok {
				return
			}
			interrupted, err := s.writeConversationACPEvent(
				ctx,
				w,
				flusher,
				runner,
				session,
				payload,
			)
			if interrupted || err != nil {
				return
			}
		default:
			return
		}
	}
}

func (s *Service) writeConversationACPEvent(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	runner *conversationRunner,
	session conversationSession,
	payload []byte,
) (bool, error) {
	if isConversationPermissionRequest(payload) {
		if conversationPermissionRequestApprovalID(payload) != "" {
			return false, s.writeConversationEvent(w, flusher, conversationEvent{
				Type:      "acp",
				NodeID:    runner.agentConn.nodeID,
				AgentID:   runner.agentConn.agentID,
				SessionID: session.managerID,
				TurnID:    runner.turnID,
				Frame:     append(json.RawMessage(nil), payload...),
			})
		}
		approval, frame, err := s.createConversationApproval(ctx, runner, session, payload)
		if err != nil {
			return false, err
		}
		if s.conversationApprovalMode(
			ctx,
			runner,
			session,
		) == domain.SessionApprovalModeAutoApproveAll {
			if err := s.writeConversationEvent(w, flusher, conversationEvent{
				Type:      "acp",
				NodeID:    runner.agentConn.nodeID,
				AgentID:   runner.agentConn.agentID,
				SessionID: session.managerID,
				TurnID:    runner.turnID,
				Frame:     frame,
			}); err != nil {
				return false, err
			}
			response, err := s.autoApproveConversationApproval(ctx, runner, session, approval)
			if err != nil {
				return false, err
			}
			return false, s.writeConversationEvent(w, flusher, conversationEvent{
				Type:      "acp",
				NodeID:    runner.agentConn.nodeID,
				AgentID:   runner.agentConn.agentID,
				SessionID: session.managerID,
				TurnID:    runner.turnID,
				Frame:     response,
			})
		}
		if err := s.writeConversationEvent(w, flusher, conversationEvent{
			Type:       "approval_required",
			NodeID:     runner.agentConn.nodeID,
			AgentID:    runner.agentConn.agentID,
			SessionID:  session.managerID,
			TurnID:     runner.turnID,
			ApprovalID: approval.ApprovalID,
			Approval:   &approval,
			Frame:      frame,
		}); err != nil {
			return false, err
		}
		return true, s.writeConversationEvent(w, flusher, conversationEvent{
			Type:       "interrupted",
			NodeID:     runner.agentConn.nodeID,
			AgentID:    runner.agentConn.agentID,
			SessionID:  session.managerID,
			TurnID:     runner.turnID,
			ApprovalID: approval.ApprovalID,
			Reason:     "permission_required",
		})
	}
	return false, s.writeConversationEvent(w, flusher, conversationEvent{
		Type:      "acp",
		NodeID:    runner.agentConn.nodeID,
		AgentID:   runner.agentConn.agentID,
		SessionID: session.managerID,
		TurnID:    runner.turnID,
		Frame:     append(json.RawMessage(nil), payload...),
	})
}

func (s *Service) conversationApprovalMode(
	ctx context.Context,
	runner *conversationRunner,
	session conversationSession,
) string {
	principal := UserPrincipal{User: User{UserID: runner.agentConn.ownerUserID}}
	stored, err := s.store.GetSession(ctx, principal, session.managerID)
	if err != nil {
		logging.Warn(
			ctx,
			"conversation session config lookup failed",
			logging.Err(err),
		)
		return domain.SessionApprovalModeManual
	}
	return domain.NormalizeSessionApprovalMode(stored.PaxConfig.ApprovalMode)
}

func (s *Service) autoApproveConversationApproval(
	ctx context.Context,
	runner *conversationRunner,
	session conversationSession,
	approval AgentApproval,
) (json.RawMessage, error) {
	principal := UserPrincipal{User: User{UserID: runner.agentConn.ownerUserID}}
	grantBody := json.RawMessage(`{"approval_mode":"auto_approve_all"}`)
	decided, err := s.store.DecideApproval(
		ctx,
		principal,
		approval.ApprovalID,
		ApprovalDecisionRequest{
			DecisionOption: "allow_once",
			GrantBody:      grantBody,
		},
	)
	if err != nil {
		return nil, err
	}
	response, err := acpPermissionResponseFromApproval(decided)
	if err != nil {
		return nil, err
	}
	if err := runner.sendWorkerResponseRaw(ctx, response); err != nil {
		return nil, err
	}
	recorded, err := s.store.RecordApprovalResponse(
		ctx,
		principal,
		decided.ApprovalID,
		response,
		"",
	)
	if err != nil {
		return nil, err
	}
	eventFrame, err := conversationPermissionResponseEventFrame(response, recorded)
	if err != nil {
		return nil, err
	}
	if err := s.persistConversationApprovalResponseHistory(ctx, runner, session, response, eventFrame); err != nil {
		logging.Warn(
			ctx,
			"conversation approval response history update failed",
			logging.Err(err),
		)
	}
	return eventFrame, nil
}

func (s *Service) createConversationApproval(
	ctx context.Context,
	runner *conversationRunner,
	session conversationSession,
	payload []byte,
) (AgentApproval, json.RawMessage, error) {
	var frame acpJSONRPCMessage
	if err := json.Unmarshal(payload, &frame); err != nil {
		return AgentApproval{}, nil, err
	}
	params, err := decodeACPParams(frame.Params)
	if err != nil {
		return AgentApproval{}, nil, err
	}
	fingerprint, err := acpPermissionFingerprint(params)
	if err != nil {
		return AgentApproval{}, nil, err
	}
	nativeID := acpRequestID(frame.ID)
	approval, err := s.store.CreateApproval(ctx, Node{
		NodeID:      runner.agentConn.nodeID,
		OwnerUserID: runner.agentConn.ownerUserID,
	}, CreateApprovalRequest{
		AgentID:      runner.agentConn.agentID,
		SessionID:    session.managerID,
		NativeID:     nativeID,
		Domain:       stringField(params, "domain", "agent_action"),
		Operation:    stringField(params, "operation", "session/request_permission"),
		ResourceType: "acp_permission",
		ResourceRef:  firstNonEmpty(acpToolCallID(params), nativeID),
		Title:        conversationApprovalTitle(params),
		Description:  conversationApprovalDescription(params),
		RiskLevel: firstNonEmpty(
			stringField(params, "riskLevel", ""),
			stringField(params, "risk_level", ""),
			"unknown",
		),
		ActionFingerprint: fingerprint,
		RequestBody:       frame.Params,
		RequestedEffects:  json.RawMessage(`[]`),
		Options:           conversationApprovalOptions(params),
		RawPayload:        append(json.RawMessage(nil), payload...),
	})
	if err != nil {
		return AgentApproval{}, nil, err
	}
	forwarded, err := injectConversationApprovalID(payload, approval.ApprovalID)
	if err != nil {
		return AgentApproval{}, nil, err
	}
	if err := s.persistConversationApprovalHistory(ctx, runner, session, frame, forwarded); err != nil {
		logging.Warn(
			ctx,
			"conversation approval history update failed",
			logging.Err(err),
		)
	}
	if err := s.markConversationWaitingApproval(ctx, runner, session.managerID, approval); err != nil {
		return AgentApproval{}, nil, err
	}
	return approval, forwarded, nil
}

func (s *Service) persistConversationApprovalHistory(
	ctx context.Context,
	runner *conversationRunner,
	session conversationSession,
	request acpJSONRPCMessage,
	forwarded json.RawMessage,
) error {
	if len(forwarded) == 0 {
		return nil
	}
	requestID := acpRequestID(request.ID)
	messages, err := s.store.ListMessages(ctx, runner.agentConn.agentID, session.managerID, 1000)
	if err != nil {
		return err
	}
	for _, msg := range messages {
		if msg.MessageType != "session/request_permission" {
			continue
		}
		if !conversationHistoryMessageMatchesPermission(msg, requestID) {
			continue
		}
		msg.RawJSON = append(json.RawMessage(nil), forwarded...)
		if err := s.store.UpsertMessage(ctx, &msg); err != nil {
			return err
		}
		return s.store.UpsertMessagePart(ctx, &domain.MessagePart{
			MessageID:   msg.MessageID,
			PartIndex:   0,
			PartType:    domain.MessagePartRawJSON,
			PayloadJSON: append(json.RawMessage(nil), forwarded...),
		})
	}
	return nil
}

func (s *Service) persistConversationApprovalResponseHistory(
	ctx context.Context,
	runner *conversationRunner,
	session conversationSession,
	response []byte,
	decorated json.RawMessage,
) error {
	return persistApprovalResponseHistory(
		ctx,
		s.store,
		runner.agentConn.agentID,
		session.managerID,
		response,
		decorated,
	)
}

func persistApprovalResponseHistory(
	ctx context.Context,
	store Store,
	agentID string,
	sessionID string,
	response []byte,
	decorated json.RawMessage,
) error {
	if len(decorated) == 0 {
		return nil
	}
	var frame acpJSONRPCMessage
	if err := json.Unmarshal(response, &frame); err != nil {
		return err
	}
	responseID := acpRequestID(frame.ID)
	messages, err := store.ListMessages(ctx, agentID, sessionID, 1000)
	if err != nil {
		return err
	}
	for _, msg := range messages {
		if msg.MessageType != "permission_response" {
			continue
		}
		if !conversationHistoryMessageMatchesPermissionResponse(msg, responseID) {
			continue
		}
		msg.RawJSON = append(json.RawMessage(nil), decorated...)
		if err := store.UpsertMessage(ctx, &msg); err != nil {
			return err
		}
		return store.UpsertMessagePart(ctx, &domain.MessagePart{
			MessageID:   msg.MessageID,
			PartIndex:   0,
			PartType:    domain.MessagePartRawJSON,
			PayloadJSON: append(json.RawMessage(nil), decorated...),
		})
	}
	return nil
}

func conversationHistoryMessageMatchesPermission(msg domain.Message, requestID string) bool {
	var frame acpJSONRPCMessage
	if len(msg.RawJSON) == 0 || json.Unmarshal(msg.RawJSON, &frame) != nil {
		return false
	}
	if frame.Method != "session/request_permission" {
		return false
	}
	if requestID == "" {
		return true
	}
	return acpRequestID(frame.ID) == requestID
}

func conversationHistoryMessageMatchesPermissionResponse(
	msg domain.Message,
	requestID string,
) bool {
	var frame acpJSONRPCMessage
	if len(msg.RawJSON) == 0 || json.Unmarshal(msg.RawJSON, &frame) != nil {
		return false
	}
	if requestID == "" {
		return true
	}
	return acpRequestID(frame.ID) == requestID
}

func (s *Service) markConversationWaitingApproval(
	ctx context.Context,
	runner *conversationRunner,
	sessionID string,
	approval AgentApproval,
) error {
	state := domain.SessionRuntimeState{
		OwnerUserID:       runner.agentConn.ownerUserID,
		NodeID:            runner.agentConn.nodeID,
		AgentID:           runner.agentConn.agentID,
		SessionID:         sessionID,
		Lifecycle:         domain.RuntimeLifecycleWaitingApproval,
		BlockedReason:     domain.RuntimeBlockedReasonToolApproval,
		BlockedRef:        firstNonEmpty(approval.ResourceRef, approval.ApprovalID),
		PendingApprovalID: approval.ApprovalID,
		UpdatedAt:         s.clock().UTC(),
	}
	session, err := s.store.GetSession(
		ctx,
		UserPrincipal{User: User{UserID: runner.agentConn.ownerUserID}},
		sessionID,
	)
	if err == nil && session.RuntimeState != nil {
		state = *session.RuntimeState
		state.PendingApprovalID = approval.ApprovalID
		state.BlockedRef = firstNonEmpty(approval.ResourceRef, approval.ApprovalID)
		state.BlockedReason = domain.RuntimeBlockedReasonToolApproval
		state.Lifecycle = domain.RuntimeLifecycleWaitingApproval
		state.UpdatedAt = s.clock().UTC()
	}
	return s.store.UpdateSessionRuntimeState(ctx, state)
}

func (s *Service) resolveConversationResumeApproval(
	ctx context.Context,
	principal UserPrincipal,
	session conversationSession,
	req conversationResumeRequest,
) (AgentApproval, string, error) {
	gotSession, err := s.store.GetSession(ctx, principal, session.managerID)
	if err != nil {
		return AgentApproval{}, "", err
	}
	approvalID := req.ApprovalID
	if approvalID == "" && gotSession.RuntimeState != nil {
		approvalID = gotSession.RuntimeState.PendingApprovalID
	}
	if approvalID == "" {
		return AgentApproval{}, "", apperr.Error{
			Status:  http.StatusConflict,
			Message: "session has no pending approval",
		}
	}
	approval, err := s.store.GetApproval(ctx, principal, approvalID)
	if err != nil {
		return AgentApproval{}, "", err
	}
	if approval.RequestSessionID != session.managerID &&
		approval.RequestSessionID != session.nativeID ||
		approval.RequestAgentID != gotSession.AgentID ||
		approval.RequestNodeID != gotSession.NodeID {
		return AgentApproval{}, "", ErrNotFound
	}
	if approval.Status != "decided" {
		return AgentApproval{}, "", apperr.Error{
			Status:  http.StatusConflict,
			Message: "approval is not decided",
		}
	}
	if approval.RespondedAt != nil {
		return AgentApproval{}, "", apperr.Error{
			Status:  http.StatusConflict,
			Message: "approval response already sent",
		}
	}
	promptRequestID := ""
	if gotSession.RuntimeState != nil {
		promptRequestID = gotSession.RuntimeState.ActivePromptRequestID
	}
	if promptRequestID == "" {
		return AgentApproval{}, "", apperr.Error{
			Status:  http.StatusConflict,
			Message: "session has no active prompt",
		}
	}
	return approval, promptRequestID, nil
}

func parseConversationResume(raw json.RawMessage) (conversationResumeRequest, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return conversationResumeRequest{}, nil
	}
	var asBool bool
	if err := json.Unmarshal(raw, &asBool); err == nil {
		return conversationResumeRequest{Requested: asBool}, nil
	}
	var asObject struct {
		ApprovalID string `json:"approval_id"`
	}
	if err := json.Unmarshal(raw, &asObject); err != nil {
		return conversationResumeRequest{}, fmt.Errorf("resume must be a boolean or object")
	}
	return conversationResumeRequest{
		Requested:  true,
		ApprovalID: strings.TrimSpace(asObject.ApprovalID),
	}, nil
}

func isConversationPermissionRequest(payload []byte) bool {
	var frame acpJSONRPCMessage
	if err := json.Unmarshal(payload, &frame); err != nil {
		return false
	}
	return frame.Method == "session/request_permission"
}

func conversationPermissionRequestApprovalID(payload []byte) string {
	var frame acpJSONRPCMessage
	if err := json.Unmarshal(payload, &frame); err != nil {
		return ""
	}
	if frame.Method != "session/request_permission" {
		return ""
	}
	params, err := decodeACPParams(frame.Params)
	if err != nil {
		return ""
	}
	return firstNonEmpty(
		stringField(params, "approval_id", ""),
		stringField(params, "approvalId", ""),
	)
}

func injectConversationApprovalID(payload []byte, approvalID string) (json.RawMessage, error) {
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, err
	}
	params, _ := value["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
		value["params"] = params
	}
	params["approvalId"] = approvalID
	params["approval_id"] = approvalID
	out, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func conversationApprovalTitle(params map[string]any) string {
	toolCall, _ := params["toolCall"].(map[string]any)
	return firstNonEmpty(
		stringField(toolCall, "title", ""),
		stringField(params, "operation", ""),
		"session/request_permission",
	)
}

func conversationApprovalDescription(params map[string]any) string {
	toolCall, _ := params["toolCall"].(map[string]any)
	rawInput, _ := toolCall["rawInput"].(map[string]any)
	return stringField(rawInput, "description", "")
}

func conversationApprovalOptions(params map[string]any) []ApprovalOption {
	return []ApprovalOption{
		{OptionID: "deny", Label: "Deny", Decision: "deny", Scope: "once"},
		{OptionID: "allow_once", Label: "Allow once", Decision: "allow", Scope: "once"},
		{
			OptionID: "allow_for_this_session",
			Label:    "Allow for this session",
			Decision: "allow",
			Scope:    "session",
		},
		{
			OptionID: "allow_for_this_agent",
			Label:    "Allow for this agent",
			Decision: "allow",
			Scope:    "agent",
		},
		{
			OptionID: "allow_always_on_this_node",
			Label:    "Allow always on this node",
			Decision: "allow",
			Scope:    "node",
		},
		{
			OptionID: "allow_always_on_all_agents",
			Label:    "Allow always on all agents",
			Decision: "allow",
			Scope:    "across_all_agents",
		},
	}
}

func acpPermissionResponseFromApproval(approval AgentApproval) ([]byte, error) {
	requestID, err := acpPermissionResponseRequestID(approval)
	if err != nil {
		return nil, err
	}
	optionID := acpPermissionDecisionOptionID(approval)
	return json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      requestID,
		"result": map[string]any{
			"outcome": map[string]any{
				"outcome":  "selected",
				"optionId": optionID,
			},
		},
	})
}

func conversationPermissionResponseEventFrame(
	response []byte,
	approval AgentApproval,
) (json.RawMessage, error) {
	var value map[string]any
	if err := json.Unmarshal(response, &value); err != nil {
		return nil, err
	}
	value["approval_id"] = approval.ApprovalID
	value["approvalId"] = approval.ApprovalID
	if approval.DecidedByUserID != "" {
		value["decided_by_user_id"] = approval.DecidedByUserID
		value["decidedByUserId"] = approval.DecidedByUserID
	}
	if len(approval.GrantBody) > 0 {
		var grantBody any
		if err := json.Unmarshal(approval.GrantBody, &grantBody); err == nil {
			value["grant_body"] = grantBody
			value["grantBody"] = grantBody
		}
	}
	out, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func acpPermissionResponseRequestID(approval AgentApproval) (json.RawMessage, error) {
	var frame struct {
		ID json.RawMessage `json:"id"`
	}
	if len(approval.RawPayload) > 0 {
		if err := json.Unmarshal(approval.RawPayload, &frame); err != nil {
			return nil, err
		}
		if len(frame.ID) > 0 && string(frame.ID) != "null" {
			return append(json.RawMessage(nil), frame.ID...), nil
		}
	}
	requestID := firstNonEmpty(approval.SourceMessageID, approval.NativeID)
	if requestID == "" {
		return nil, apperr.Error{
			Status:  http.StatusConflict,
			Message: "approval has no ACP request id",
		}
	}
	encoded, err := json.Marshal(requestID)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func acpPermissionDecisionOptionID(approval AgentApproval) string {
	wantKind := "allow_once"
	if approval.Decision == "deny" || approval.DecisionOption == "deny" {
		wantKind = "reject_once"
	}
	if optionID := acpPermissionOptionIDByKind(approval.RawPayload, wantKind); optionID != "" {
		return optionID
	}
	if wantKind == "reject_once" {
		return "reject"
	}
	return "allow"
}

func acpPermissionOptionIDByKind(raw json.RawMessage, kind string) string {
	var frame struct {
		Params struct {
			Options []struct {
				OptionID string `json:"optionId"`
				Kind     string `json:"kind"`
			} `json:"options"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		return ""
	}
	for _, option := range frame.Params.Options {
		if option.Kind == kind && option.OptionID != "" {
			return option.OptionID
		}
	}
	return ""
}

func resetConversationIdleTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(conversationRequestIdleTimeout)
}

func (s *Service) writeConversationTurnDone(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	agent *ACPTunnelAgent,
	session conversationSession,
	turnID string,
) error {
	if err := persistConversationTurnDone(ctx, agent, session.managerID, turnID); err != nil {
		return err
	}
	return s.writeConversationEvent(w, flusher, conversationEvent{
		Type:      "turn_done",
		NodeID:    agent.nodeID,
		AgentID:   agent.agentID,
		SessionID: session.managerID,
		TurnID:    turnID,
		Status:    "complete",
	})
}

func persistConversationTurnDone(
	ctx context.Context,
	agent *ACPTunnelAgent,
	sessionID string,
	turnID string,
) error {
	if agent == nil || agent.store == nil || sessionID == "" || turnID == "" {
		return errors.New("conversation turn completion requires agent, session, and turn IDs")
	}
	if err := agent.flushHistoryText(ctx); err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]string{
		"type":        "turn_done",
		"turn_id":     turnID,
		"turn_status": "complete",
	})
	if err != nil {
		return err
	}
	logicalKey := "conversation:" + agent.agentID + ":" + sessionID + ":" + turnID + ":done"
	msg := domain.Message{
		MessageID:   acpHistoryMessageID(logicalKey),
		OwnerUserID: agent.ownerUserID,
		NodeID:      agent.nodeID,
		AgentID:     agent.agentID,
		SessionID:   sessionID,
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionAgentToUser,
		Role:        "assistant",
		Status:      "complete",
		MessageType: "turn_done",
		TurnID:      turnID,
		LogicalKey:  logicalKey,
		RawJSON:     raw,
	}
	if err := agent.store.UpsertMessage(ctx, &msg); err != nil {
		return err
	}
	return agent.store.UpsertMessagePart(ctx, &domain.MessagePart{
		MessageID:   msg.MessageID,
		PartIndex:   0,
		PartType:    domain.MessagePartRawJSON,
		PayloadJSON: append(json.RawMessage(nil), raw...),
	})
}

func (s *Service) writeConversationEvent(
	w http.ResponseWriter,
	flusher http.Flusher,
	event conversationEvent,
) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func conversationRouteIDs(r *http.Request) (string, string, string) {
	userID := r.PathValue("userID")
	if userID == "" {
		userID = r.PathValue("user_id")
	}
	nodeID := r.PathValue("nodeID")
	if nodeID == "" {
		nodeID = r.PathValue("node_id")
	}
	agentID := r.PathValue("agentID")
	if agentID == "" {
		agentID = r.PathValue("agent_id")
	}
	if userID != "" || nodeID != "" || agentID != "" {
		return userID, nodeID, agentID
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/user/")
	if !ok {
		return "", "", ""
	}
	userID, rest, ok = strings.Cut(rest, "/nodes/")
	if !ok {
		return userID, "", ""
	}
	nodeID, rest, ok = strings.Cut(rest, "/agents/")
	if !ok {
		return userID, nodeID, ""
	}
	agentID, suffix, ok := strings.Cut(rest, "/")
	if !ok || suffix != "conversation" {
		return userID, nodeID, ""
	}
	return userID, nodeID, agentID
}

func acpErrorMessage(raw json.RawMessage) string {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return stringField(value, "message", "")
}

func decodeConversationACPError(raw json.RawMessage) error {
	var value struct {
		Message string `json:"message"`
		Data    struct {
			Kind      string `json:"kind"`
			Retryable bool   `json:"retryable"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return apperr.Error{Status: http.StatusBadGateway, Message: "ACP request failed"}
	}
	return conversationACPError{
		message:   value.Message,
		kind:      value.Data.Kind,
		retryable: value.Data.Retryable,
	}
}

func isConversationACPErrorKind(err error, kind string) bool {
	var acpErr conversationACPError
	return errors.As(err, &acpErr) && acpErr.kind == kind
}

func isConversationACPRetryable(err error) bool {
	var acpErr conversationACPError
	return errors.As(err, &acpErr) && acpErr.retryable
}
