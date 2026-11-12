package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type workspaceApprovalCanceller interface {
	CancelWorkspaceApproval(
		context.Context,
		string,
		string,
		string,
		string,
		string,
		string,
	) (domain.AgentApproval, error)
}

func (s *Service) decideUserApproval(
	ctx context.Context,
	principal domain.UserPrincipal,
	approvalID string,
	request domain.ApprovalDecisionRequest,
) (domain.AgentApproval, error) {
	approval, err := s.store.GetApproval(ctx, principal, approvalID)
	if err != nil {
		return domain.AgentApproval{}, err
	}
	if approval.Domain == "workspace" && approval.Operation == "search" &&
		approval.ResourceType == "turn" {
		if err = s.validateWorkspaceApprovalTurn(ctx, approval); err != nil {
			return domain.AgentApproval{}, err
		}
	}
	return s.store.DecideApproval(ctx, principal, approvalID, request)
}

func (s *Service) validateWorkspaceApprovalTurn(
	ctx context.Context,
	approval domain.AgentApproval,
) error {
	agent := domain.Agent{
		AgentID:     approval.RequestAgentID,
		NodeID:      approval.RequestNodeID,
		OwnerUserID: approval.OwnerUserID,
	}
	approvals, err := s.workspaceTurnApprovals(ctx, agent, "", approval.ResourceRef)
	if err != nil {
		return err
	}
	for _, candidate := range approvals {
		if candidate.ApprovalID != approval.ApprovalID {
			continue
		}
		session, _, sessionErr := s.workspaceAccessSession(
			ctx,
			domain.Node{NodeID: agent.NodeID, OwnerUserID: agent.OwnerUserID},
			agent.AgentID,
			candidate.RequestSessionID,
		)
		if sessionErr != nil {
			return sessionErr
		}
		turnID, activeErr := s.workspaceAccessActiveTurn(session)
		if activeErr != nil || turnID != approval.ResourceRef {
			return domain.ErrConflict
		}
		return nil
	}
	return domain.ErrNotFound
}

func (s canonicalSessionStore) CancelWorkspaceApproval(
	ctx context.Context, ownerID, nodeID, agentID, sessionID, turnID, approvalID string,
) (domain.AgentApproval, error) {
	store, ok := s.Store.(workspaceApprovalCanceller)
	if !ok {
		return domain.AgentApproval{}, domain.ErrConflict
	}
	return store.CancelWorkspaceApproval(
		ctx,
		ownerID,
		nodeID,
		agentID,
		sessionID,
		turnID,
		approvalID,
	)
}

func validWorkspaceToolOperation(operation string) bool {
	switch operation {
	case "search", "request_access", "access_status", "cancel_access":
		return true
	default:
		return false
	}
}

func (s *Service) workspaceAccessStatusResult(
	approval domain.AgentApproval, scope domain.WorkspaceSearchAccess,
) workspaceAccessStatus {
	result := workspaceAccessStatus{
		Status:     s.workspaceApprovalStatus(approval),
		ApprovalID: approval.ApprovalID,
		TurnID:     approval.ResourceRef,
		Scope:      scope,
		ExpiresAt:  approval.ExpiresAt,
	}
	if result.Status == "pending" {
		result.PollAfterMS = 2000
	}
	return result
}

func (s *Service) workspaceAccessRequestByID(
	ctx context.Context,
	agent domain.Agent,
	session domain.AgentSession,
	operation, arguments string,
) (workspaceAccessStatus, error) {
	var request struct {
		ApprovalID string `json:"approval_id"`
	}
	if err := decodeSecretaryToolJSON(arguments, &request); err != nil {
		return workspaceAccessStatus{}, err
	}
	if strings.TrimSpace(request.ApprovalID) == "" || len(request.ApprovalID) > 4096 {
		return workspaceAccessStatus{}, workspaceAccessError(
			http.StatusBadRequest,
			"approval_id is required",
		)
	}
	approval, err := s.workspaceAccessApprovalForSession(
		ctx,
		agent,
		session.SessionID,
		request.ApprovalID,
	)
	if err != nil {
		return workspaceAccessStatus{}, err
	}
	var body workspaceAccessBody
	if json.Unmarshal(approval.RequestBody, &body) != nil || body.TurnID != approval.ResourceRef ||
		body.Scope == nil {
		return workspaceAccessStatus{}, domain.ErrNotFound
	}
	scope, err := domain.NormalizeWorkspaceSearchAccess(body.Scope)
	if err != nil {
		return workspaceAccessStatus{}, err
	}
	result := s.workspaceAccessStatusResult(approval, *scope)
	// Status remains readable after completion, without reviving the old turn.
	if result.Status == "pending" || result.Status == "approved" {
		turnID, activeErr := s.workspaceAccessActiveTurn(session)
		if activeErr != nil || turnID != approval.ResourceRef {
			result.Status = "expired"
			result.PollAfterMS = 0
		}
	}
	if operation != "cancel_access" || (result.Status != "pending" && result.Status != "approved") {
		return result, nil
	}
	store, ok := s.store.(workspaceApprovalCanceller)
	if !ok {
		return workspaceAccessStatus{}, domain.ErrConflict
	}
	approval, err = store.CancelWorkspaceApproval(
		ctx,
		agent.OwnerUserID,
		agent.NodeID,
		agent.AgentID,
		session.SessionID,
		approval.ResourceRef,
		approval.ApprovalID,
	)
	if err != nil {
		return workspaceAccessStatus{}, err
	}
	return s.workspaceAccessStatusResult(approval, *scope), nil
}

func (s *Service) workspaceAccessApprovalForSession(
	ctx context.Context, agent domain.Agent, sessionID, approvalID string,
) (domain.AgentApproval, error) {
	approval, err := s.store.GetNodeApproval(
		ctx,
		domain.Node{
			NodeID:      agent.NodeID,
			OwnerUserID: agent.OwnerUserID,
		},
		agent.AgentID,
		approvalID,
	)
	if err != nil {
		return domain.AgentApproval{}, err
	}
	if approval.OwnerUserID != agent.OwnerUserID || approval.RequestNodeID != agent.NodeID ||
		approval.RequestAgentID != agent.AgentID || approval.Domain != "workspace" ||
		approval.Operation != "search" || approval.ResourceType != "turn" {
		return domain.AgentApproval{}, domain.ErrNotFound
	}
	// Node approval reads translate session IDs to native aliases. Re-read through
	// the canonical session filter rather than comparing those different IDs.
	approvals, err := s.workspaceTurnApprovals(ctx, agent, sessionID, approval.ResourceRef)
	if err != nil {
		return domain.AgentApproval{}, err
	}
	for _, candidate := range approvals {
		if candidate.ApprovalID == approvalID {
			return candidate, nil
		}
	}
	return domain.AgentApproval{}, domain.ErrNotFound
}
