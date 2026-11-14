package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

const defaultWorkspaceRequestTimeoutSeconds = 600

type workspaceToolRequest struct {
	SessionID     string `json:"session_id"`
	Tool          string `json:"tool"`
	ArgumentsJSON string `json:"arguments_json"`
}

type workspaceAccessRequest struct {
	Queries        []string `json:"queries,omitempty"`
	NodeIDs        []string `json:"node_ids,omitempty"`
	AgentIDs       []string `json:"agent_ids,omitempty"`
	SessionIDs     []string `json:"session_ids,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	TimeoutSeconds *int     `json:"timeout_seconds,omitempty"`
}

type workspaceSearchRequest struct {
	Query    string                       `json:"query"`
	Scope    *domain.WorkspaceSearchScope `json:"scope"`
	Kinds    []string                     `json:"kinds,omitempty"`
	Cursor   string                       `json:"cursor,omitempty"`
	Limit    int                          `json:"limit,omitempty"`
	GroupBy  string                       `json:"group_by,omitempty"`
	GroupKey string                       `json:"group_key,omitempty"`
}

type workspaceScopedSearchPage struct {
	userapi.WorkspaceSearchPage
	Scope domain.WorkspaceSearchScope `json:"scope"`
}

type workspaceAccessBody struct {
	TurnID           string                        `json:"turn_id"`
	Scope            *domain.WorkspaceSearchAccess `json:"scope"`
	Source           string                        `json:"source,omitempty"`
	TimeoutSeconds   *int                          `json:"timeout_seconds,omitempty"`
	RequestExpiresAt *time.Time                    `json:"request_expires_at,omitempty"`
}

type workspaceAccessStatus struct {
	Status      string                       `json:"status"`
	ApprovalID  string                       `json:"approval_id"`
	TurnID      string                       `json:"turn_id"`
	Scope       domain.WorkspaceSearchAccess `json:"scope"`
	ExpiresAt   *time.Time                   `json:"expires_at"`
	PollAfterMS int                          `json:"poll_after_ms,omitempty"`
}

func (s *Service) prepareTurnSearchAccess(
	ctx context.Context,
	nodeID, agentID, sessionID, turnID string,
	scope *domain.WorkspaceSearchAccess,
) error {
	if strings.HasPrefix(sessionID, domain.SecretaryInternalSessionPrefix) {
		return nil
	}
	session, agent, err := s.workspaceAccessSession(
		ctx,
		domain.Node{NodeID: nodeID},
		agentID,
		sessionID,
	)
	if err != nil {
		return err
	}
	normalized, err := domain.NormalizeWorkspaceSearchAccess(scope)
	if err != nil {
		return workspaceAccessError(http.StatusBadRequest, err.Error())
	}
	if turnID == "" {
		return workspaceAccessError(http.StatusBadRequest, "turn_id is required")
	}
	if normalized != nil {
		normalized.AgentIDs = domain.ResolveWorkspaceSearchAgents(normalized.AgentIDs, agentID)
		normalized.NodeIDs = domain.ResolveWorkspaceSearchNodes(normalized.NodeIDs, nodeID)
	}
	if err = s.registerWorkspaceAccessTurn(agentID, sessionID, turnID); err != nil {
		return err
	}
	if normalized == nil {
		return nil
	}
	_, err = s.ensureWorkspaceApproval(
		ctx,
		agent,
		session,
		turnID,
		*normalized,
		"user",
		"",
		defaultWorkspaceRequestTimeoutSeconds,
	)
	return err
}

func (s *Service) registerWorkspaceAccessTurn(agentID, sessionID, turnID string) error {
	s.workspaceAccessMu.Lock()
	defer s.workspaceAccessMu.Unlock()
	if s.workspaceTurnEnded(agentID, sessionID, turnID) {
		return workspaceAccessError(
			http.StatusForbidden,
			"workspace search turn is no longer active",
		)
	}
	s.workspaceAccessTurns.Store(acpSessionKey(agentID, sessionID), turnID)
	return nil
}

func (s *Service) callWorkspaceTool(
	ctx context.Context, node domain.Node, agentID string, request workspaceToolRequest,
) (any, error) {
	if request.SessionID == "" || !validWorkspaceToolOperation(request.Tool) {
		return nil, workspaceAccessError(http.StatusBadRequest, "invalid workspace tool request")
	}
	session, agent, err := s.workspaceAccessSession(ctx, node, agentID, request.SessionID)
	if err != nil {
		return nil, err
	}
	if request.Tool == "access_status" || request.Tool == "cancel_access" {
		return s.workspaceAccessRequestByID(
			ctx,
			agent,
			session,
			request.Tool,
			request.ArgumentsJSON,
		)
	}
	turnID, err := s.workspaceAccessActiveTurn(session)
	if err != nil {
		return nil, err
	}
	if request.Tool == "request_access" {
		return s.requestWorkspaceAccess(ctx, agent, session, turnID, request.ArgumentsJSON)
	}
	return s.searchWorkspaceWithAccess(ctx, node, agent, session, turnID, request.ArgumentsJSON)
}

func (s *Service) workspaceAccessSession(
	ctx context.Context, node domain.Node, agentID, sessionID string,
) (domain.AgentSession, domain.Agent, error) {
	if node.NodeID == "" {
		return domain.AgentSession{}, domain.Agent{}, domain.ErrUnauthorized
	}
	agent, err := s.store.GetNodeAgent(ctx, node.NodeID, agentID)
	if err != nil {
		return domain.AgentSession{}, domain.Agent{}, err
	}
	if agent.OwnerUserID == "" || agent.NodeID == "" ||
		(node.NodeID != "" && agent.NodeID != node.NodeID) ||
		(node.OwnerUserID != "" && agent.OwnerUserID != node.OwnerUserID) {
		return domain.AgentSession{}, domain.Agent{}, domain.ErrUnauthorized
	}
	session, err := s.store.GetSession(
		ctx,
		domain.UserPrincipal{User: domain.User{UserID: agent.OwnerUserID}},
		sessionID,
	)
	if err != nil {
		return domain.AgentSession{}, domain.Agent{}, err
	}
	if session.AgentID != agentID || session.NodeID != agent.NodeID ||
		session.ArchivedAt != nil || (session.Transport != "" && session.Transport != domain.SessionTransportManager) ||
		strings.HasPrefix(sessionID, domain.SecretaryInternalSessionPrefix) {
		return domain.AgentSession{}, domain.Agent{}, domain.ErrUnauthorized
	}
	if session.RuntimeState != nil && session.RuntimeState.OwnerUserID != agent.OwnerUserID {
		return domain.AgentSession{}, domain.Agent{}, domain.ErrUnauthorized
	}
	return session, agent, nil
}

func (s *Service) workspaceAccessActiveTurn(session domain.AgentSession) (string, error) {
	state := session.RuntimeState
	local := s.acpRuntime.CurrentCorrelation(session.AgentID, session.SessionID)
	if !local.UpdatedAt.IsZero() {
		if state != nil && state.ActiveTurnID != "" && local.ActiveTurnID != "" &&
			state.ActiveTurnID != local.ActiveTurnID {
			return "", workspaceAccessError(
				http.StatusForbidden,
				"workspace search turn state is not synchronized",
			)
		}
		// Local prompt completion must fence access before the next paxd snapshot.
		state = &local
	}
	if state == nil || state.ActiveTurnID == "" || state.ActivePromptRequestID == "" ||
		state.NodeID != session.NodeID || state.AgentID != session.AgentID || state.SessionID != session.SessionID {
		return "", workspaceAccessError(
			http.StatusForbidden,
			"workspace search requires an active turn",
		)
	}
	if state.Lifecycle != domain.RuntimeLifecycleRunning &&
		state.Lifecycle != domain.RuntimeLifecycleWaitingApproval {
		return "", workspaceAccessError(
			http.StatusForbidden,
			"workspace search requires an active turn",
		)
	}
	if s.workspaceTurnEnded(session.AgentID, session.SessionID, state.ActiveTurnID) {
		return "", workspaceAccessError(
			http.StatusForbidden,
			"workspace search turn is no longer active",
		)
	}
	registered, ok := s.workspaceAccessTurns.Load(acpSessionKey(session.AgentID, session.SessionID))
	if !ok || registered != state.ActiveTurnID {
		return "", workspaceAccessError(
			http.StatusForbidden,
			"workspace search requires a Manager-dispatched turn",
		)
	}
	return state.ActiveTurnID, nil
}

func (s *Service) workspaceTurnEnded(agentID, sessionID, turnID string) bool {
	ended, ok := s.workspaceAccessEnds.Load(acpSessionKey(agentID, sessionID))
	return ok && ended == turnID
}

func (s *Service) requestWorkspaceAccess(
	ctx context.Context, agent domain.Agent, session domain.AgentSession, turnID, arguments string,
) (workspaceAccessStatus, error) {
	var request workspaceAccessRequest
	if err := decodeSecretaryToolJSON(arguments, &request); err != nil {
		return workspaceAccessStatus{}, err
	}
	timeoutSeconds := defaultWorkspaceRequestTimeoutSeconds
	if request.TimeoutSeconds != nil {
		timeoutSeconds = *request.TimeoutSeconds
	}
	if timeoutSeconds < 0 || timeoutSeconds > 86400 {
		return workspaceAccessStatus{}, workspaceAccessError(
			http.StatusBadRequest,
			"timeout_seconds must be an integer between 0 and 86400",
		)
	}
	scope, err := domain.NormalizeWorkspaceSearchAccess(&domain.WorkspaceSearchAccess{
		Queries: request.Queries, NodeIDs: request.NodeIDs, AgentIDs: request.AgentIDs, SessionIDs: request.SessionIDs,
	})
	if err != nil || len(request.Reason) > 4096 {
		return workspaceAccessStatus{}, workspaceAccessError(
			http.StatusBadRequest,
			"invalid workspace search access scope",
		)
	}
	scope.AgentIDs = domain.ResolveWorkspaceSearchAgents(scope.AgentIDs, agent.AgentID)
	scope.NodeIDs = domain.ResolveWorkspaceSearchNodes(scope.NodeIDs, agent.NodeID)
	source := ""
	if workspaceAccessAutoApproved(session) {
		source = "pax_auto_approve"
	}
	approval, err := s.ensureWorkspaceApproval(
		ctx,
		agent,
		session,
		turnID,
		*scope,
		source,
		request.Reason,
		timeoutSeconds,
	)
	if err != nil {
		return workspaceAccessStatus{}, err
	}
	current, _, err := s.workspaceAccessSession(
		ctx,
		domain.Node{NodeID: agent.NodeID},
		agent.AgentID,
		session.SessionID,
	)
	if err != nil {
		return workspaceAccessStatus{}, err
	}
	currentTurn, err := s.workspaceAccessActiveTurn(current)
	if err != nil || currentTurn != turnID {
		return workspaceAccessStatus{}, workspaceAccessError(
			http.StatusForbidden,
			"workspace search turn is no longer active",
		)
	}
	return s.workspaceAccessStatusResult(approval, *scope), nil
}

func workspaceAccessAutoApproved(session domain.AgentSession) bool {
	return session.PaxConfig.ApprovalMode == domain.SessionApprovalModeAutoApproveAll &&
		session.PaxConfig.PermissionChoiceID == domain.PermissionChoicePAXAutoApprove
}

func (s *Service) ensureWorkspaceApproval(
	ctx context.Context, agent domain.Agent, session domain.AgentSession, turnID string,
	scope domain.WorkspaceSearchAccess, source, reason string, timeoutSeconds int,
) (domain.AgentApproval, error) {
	s.workspaceAccessMu.Lock()
	defer s.workspaceAccessMu.Unlock()
	if s.workspaceTurnEnded(agent.AgentID, session.SessionID, turnID) {
		return domain.AgentApproval{}, workspaceAccessError(
			http.StatusForbidden,
			"workspace search turn is no longer active",
		)
	}
	approvals, err := s.workspaceTurnApprovals(ctx, agent, session.SessionID, turnID)
	if err != nil {
		return domain.AgentApproval{}, err
	}
	body := workspaceAccessBody{TurnID: turnID, Scope: &scope, TimeoutSeconds: &timeoutSeconds}
	encoded, _ := json.Marshal(body)
	digest := sha256.Sum256(encoded)
	fingerprint := "workspace-search:" + hex.EncodeToString(digest[:])
	for _, approval := range approvals {
		status := s.workspaceApprovalStatus(approval)
		if approval.ActionFingerprint == fingerprint && status != "expired" &&
			status != "cancelled" {
			return s.approveWorkspaceAccess(ctx, agent, approval, source)
		}
	}
	if len(approvals) >= 32 {
		return domain.AgentApproval{}, workspaceAccessError(
			http.StatusTooManyRequests,
			"workspace search access request limit reached for this turn",
		)
	}
	var expiresAt *time.Time
	if timeoutSeconds > 0 {
		deadline := s.clock().Add(time.Duration(timeoutSeconds) * time.Second)
		expiresAt = &deadline
	}
	body.RequestExpiresAt = expiresAt
	encoded, _ = json.Marshal(body)
	approval, err := s.store.CreateApproval(
		ctx,
		domain.Node{NodeID: agent.NodeID, OwnerUserID: agent.OwnerUserID},
		domain.CreateApprovalRequest{
			AgentID: agent.AgentID, SessionID: session.SessionID, Domain: "workspace", Operation: "search",
			ResourceType: "turn", ResourceRef: turnID, Title: "Search workspace for this turn", Description: reason,
			RiskLevel: "read", ActionFingerprint: fingerprint, RequestBody: encoded, ExpiresAt: expiresAt,
			Options: []domain.ApprovalOption{
				{
					OptionID: "allow_once",
					Label:    "Allow for this turn",
					Decision: "allow",
					Scope:    "once",
				},
				{OptionID: "deny", Label: "Deny", Decision: "deny", Scope: "once"},
			},
		},
	)
	if err != nil {
		return domain.AgentApproval{}, err
	}
	return s.approveWorkspaceAccess(ctx, agent, approval, source)
}

func (s *Service) approveWorkspaceAccess(
	ctx context.Context, agent domain.Agent, approval domain.AgentApproval, source string,
) (domain.AgentApproval, error) {
	if source == "" || approval.Status != "pending" || approval.GrantRevokedAt != nil ||
		(approval.ExpiresAt != nil && !approval.ExpiresAt.After(s.clock())) {
		return approval, nil
	}
	body, _ := json.Marshal(map[string]string{"source": source})
	return s.store.DecideApproval(
		ctx,
		domain.UserPrincipal{User: domain.User{UserID: agent.OwnerUserID}},
		approval.ApprovalID,
		domain.ApprovalDecisionRequest{DecisionOption: "allow_once", GrantBody: body},
	)
}

func (s *Service) workspaceTurnApprovals(
	ctx context.Context, agent domain.Agent, sessionID, turnID string,
) ([]domain.AgentApproval, error) {
	return s.store.ListApprovals(ctx, domain.ApprovalFilter{
		Principal: domain.UserPrincipal{
			User: domain.User{UserID: agent.OwnerUserID},
		}, Domain: "workspace", Operation: "search",
		ResourceType: "turn", ResourceRef: turnID, RequestNodeID: agent.NodeID, RequestAgentID: agent.AgentID,
		RequestSessionID: sessionID, IncludeRevoked: true, Limit: 100,
	})
}

func (s *Service) workspaceApprovalStatus(approval domain.AgentApproval) string {
	if approval.GrantRevokedAt != nil {
		return "cancelled"
	}
	if approval.Status == "decided" && approval.Decision == "deny" {
		return "denied"
	}
	if approval.ExpiresAt != nil && !approval.ExpiresAt.After(s.clock()) {
		return "expired"
	}
	if approval.Status == "pending" {
		return "pending"
	}
	if approval.Status == "decided" && approval.Decision == "allow" &&
		approval.DecisionScope == "once" {
		return "approved"
	}
	return "denied"
}

func (s *Service) searchWorkspaceWithAccess(
	ctx context.Context,
	node domain.Node,
	agent domain.Agent,
	session domain.AgentSession,
	turnID, arguments string,
) (any, error) {
	var request workspaceSearchRequest
	if err := decodeSecretaryToolJSON(arguments, &request); err != nil {
		return nil, err
	}
	declared, err := domain.NormalizeWorkspaceSearchScope(
		request.Scope,
		agent.AgentID,
		agent.NodeID,
	)
	if err != nil {
		return nil, workspaceAccessError(http.StatusBadRequest, err.Error())
	}
	if err = s.validateWorkspaceSearchScope(ctx, agent, declared); err != nil {
		return nil, err
	}
	query := domain.SearchQuery{Query: request.Query, Kinds: request.Kinds, Cursor: request.Cursor,
		Limit: request.Limit, GroupBy: request.GroupBy, GroupKey: request.GroupKey,
		NodeIDs: workspaceScopeFilter(declared.NodeIDs),
		AgentIDs: workspaceScopeFilter(
			declared.AgentIDs,
		), SessionIDs: workspaceScopeFilter(declared.SessionIDs)}
	approvalID, err := s.authorizeWorkspaceSearchScope(
		ctx,
		agent,
		session.SessionID,
		turnID,
		request.Query,
		declared,
	)
	if err != nil {
		return nil, err
	}
	if s.workspaceSearch == nil {
		return nil, workspaceAccessError(
			http.StatusServiceUnavailable,
			"workspace search is unavailable",
		)
	}
	if err = s.recheckWorkspaceAccess(ctx, node, agent, session.SessionID, turnID, approvalID); err != nil {
		return nil, err
	}
	search := s.workspaceSearch.WithPrincipal(secretaryToolPrincipal{owner: agent.OwnerUserID})
	result, err := search.Search(ctx, auth.RequestMetadata{}, query)
	if err != nil {
		return nil, err
	}
	if err = s.recheckWorkspaceAccess(ctx, node, agent, session.SessionID, turnID, approvalID); err != nil {
		return nil, err
	}
	return workspaceScopedSearchPage{WorkspaceSearchPage: result, Scope: declared}, nil
}

func workspaceScopeFilter(values []string) []string {
	if len(values) == 1 && values[0] == "*" {
		return nil
	}
	return append([]string(nil), values...)
}

func (s *Service) validateWorkspaceSearchScope(
	ctx context.Context, caller domain.Agent, scope domain.WorkspaceSearchScope,
) error {
	principal := domain.UserPrincipal{User: domain.User{UserID: caller.OwnerUserID}}
	if err := s.validateWorkspaceSearchNodes(ctx, principal, caller.NodeID, scope.NodeIDs); err != nil {
		return err
	}
	known := map[string]bool{caller.AgentID: true}
	checkAgent := func(agentID string) bool {
		if known[agentID] {
			return true
		}
		agent, err := s.store.GetAgent(ctx, principal, agentID)
		if err != nil || agent.OwnerUserID != caller.OwnerUserID {
			return false
		}
		known[agentID] = true
		return true
	}
	for _, agentID := range scope.AgentIDs {
		if agentID != "*" && !checkAgent(agentID) {
			return workspaceScopeDenied()
		}
	}
	for _, sessionID := range workspaceScopeFilter(scope.SessionIDs) {
		session, err := s.store.GetSession(ctx, principal, sessionID)
		if err != nil || !checkAgent(session.AgentID) {
			return workspaceScopeDenied()
		}
		if !workspaceValueInScope(scope.AgentIDs, session.AgentID) ||
			!workspaceValueInScope(scope.NodeIDs, session.NodeID) {
			return workspaceScopeDenied()
		}
	}
	return nil
}

func workspaceValueInScope(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == "*" || candidate == value {
			return true
		}
	}
	return false
}

func (s *Service) validateWorkspaceSearchNodes(
	ctx context.Context, principal domain.UserPrincipal, callerNodeID string, nodeIDs []string,
) error {
	for _, nodeID := range workspaceScopeFilter(nodeIDs) {
		if nodeID == callerNodeID {
			continue
		}
		node, err := s.store.GetNode(ctx, principal, nodeID)
		if err != nil || node.OwnerUserID != principal.User.UserID {
			return workspaceScopeDenied()
		}
	}
	return nil
}

func workspaceScopeDenied() error {
	return workspaceAccessError(
		http.StatusForbidden,
		"requested scope is not accessible or contradicts the declared node/agent range; default access covers this node's sessions; declare node_ids [self] or use workspace_request_access for a wider scope",
	)
}

func (s *Service) authorizeWorkspaceSearchScope(
	ctx context.Context,
	agent domain.Agent,
	sessionID, turnID, query string,
	requested domain.WorkspaceSearchScope,
) (string, error) {
	if len(requested.NodeIDs) == 1 && requested.NodeIDs[0] == agent.NodeID {
		return "", nil
	}
	approvals, err := s.workspaceTurnApprovals(ctx, agent, sessionID, turnID)
	if err != nil {
		return "", err
	}
	approval, err := s.workspaceSearchApproval(
		approvals,
		query,
		requested,
		agent.AgentID,
		agent.NodeID,
	)
	return approval.ApprovalID, err
}

func (s *Service) workspaceSearchApproval(
	approvals []domain.AgentApproval,
	query string,
	requested domain.WorkspaceSearchScope,
	agentID, nodeID string,
) (domain.AgentApproval, error) {
	for _, approval := range approvals {
		if s.workspaceApprovalStatus(approval) != "approved" {
			continue
		}
		var body workspaceAccessBody
		if json.Unmarshal(approval.RequestBody, &body) != nil ||
			body.TurnID != approval.ResourceRef ||
			body.Scope == nil {
			continue
		}
		scope, err := domain.NormalizeWorkspaceSearchAccess(body.Scope)
		if err != nil || !scope.AllowsQuery(query) {
			continue
		}
		scope.AgentIDs = domain.ResolveWorkspaceSearchAgents(scope.AgentIDs, agentID)
		scope.NodeIDs = domain.ResolveWorkspaceSearchNodes(scope.NodeIDs, nodeID)
		if scope.Covers(requested) {
			return approval, nil
		}
	}
	return domain.AgentApproval{}, workspaceAccessError(
		http.StatusForbidden,
		"search scope or query exceeds current access; default access covers this node's sessions using node_ids [self]; use workspace_request_access for a wider scope for this turn",
	)
}

func (s *Service) recheckWorkspaceAccess(
	ctx context.Context, node domain.Node, agent domain.Agent, sessionID, turnID, approvalID string,
) error {
	session, _, err := s.workspaceAccessSession(ctx, node, agent.AgentID, sessionID)
	if err != nil {
		return err
	}
	currentTurn, err := s.workspaceAccessActiveTurn(session)
	if err != nil || currentTurn != turnID {
		return workspaceAccessError(
			http.StatusForbidden,
			"workspace search turn is no longer active",
		)
	}
	if approvalID == "" {
		return nil
	}
	approval, err := s.store.GetNodeApproval(
		ctx,
		domain.Node{NodeID: agent.NodeID, OwnerUserID: agent.OwnerUserID},
		agent.AgentID,
		approvalID,
	)
	if err != nil {
		return err
	}
	if s.workspaceApprovalStatus(approval) != "approved" {
		return workspaceAccessError(
			http.StatusForbidden,
			"workspace search authorization is no longer active",
		)
	}
	return nil
}

func workspaceAccessError(status int, message string) error {
	return apperr.Error{Status: status, Message: message}
}

type workspaceTurnApprovalExpirer interface {
	ExpireWorkspaceTurnApprovals(context.Context, string, string, string, string, string) error
}

func (s canonicalSessionStore) ExpireWorkspaceTurnApprovals(
	ctx context.Context, ownerID, nodeID, agentID, sessionID, turnID string,
) error {
	store, ok := s.Store.(workspaceTurnApprovalExpirer)
	if !ok {
		return domain.ErrConflict
	}
	return store.ExpireWorkspaceTurnApprovals(ctx, ownerID, nodeID, agentID, sessionID, turnID)
}

func (s *Service) expireTurnSearchAccess(
	ctx context.Context, nodeID, agentID, sessionID, turnID string,
) error {
	if turnID == "" || strings.HasPrefix(sessionID, domain.SecretaryInternalSessionPrefix) {
		return nil
	}
	key := acpSessionKey(agentID, sessionID)
	if s.workspaceAccessTurns.CompareAndDelete(key, turnID) {
		s.workspaceAccessEnds.Store(key, turnID)
	}
	s.workspaceAccessMu.Lock()
	defer s.workspaceAccessMu.Unlock()
	agent, err := s.store.GetNodeAgent(ctx, nodeID, agentID)
	if err != nil {
		return err
	}
	store, ok := s.store.(workspaceTurnApprovalExpirer)
	if !ok {
		return domain.ErrConflict
	}
	return store.ExpireWorkspaceTurnApprovals(
		ctx,
		agent.OwnerUserID,
		nodeID,
		agentID,
		sessionID,
		turnID,
	)
}
