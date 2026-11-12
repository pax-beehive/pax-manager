package storage

import (
	"context"
	"errors"
	"time"
)

func workspaceApprovalCanCancel(approval AgentApproval, now time.Time) bool {
	return approval.GrantRevokedAt == nil &&
		(approval.ExpiresAt == nil || approval.ExpiresAt.After(now)) &&
		(approval.Status == "pending" ||
			(approval.Status == "decided" && approval.Decision == "allow" && approval.DecisionScope == "once"))
}

func (s *MemoryStore) CancelWorkspaceApproval(
	_ context.Context, ownerID, nodeID, agentID, sessionID, turnID, approvalID string,
) (AgentApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	approval, ok := s.approvals[approvalID]
	if !ok || !isWorkspaceSearchApproval(approval) || approval.OwnerUserID != ownerID ||
		approval.RequestNodeID != nodeID || approval.RequestAgentID != agentID ||
		approval.RequestSessionID != sessionID || approval.ResourceRef != turnID {
		return AgentApproval{}, ErrNotFound
	}
	now := s.now().UTC()
	if !workspaceApprovalCanCancel(approval, now) {
		return approval, nil
	}
	approval.GrantRevokedAt = &now
	approval.GrantRevokedByUserID = ownerID
	approval.GrantRevocationReason = "workspace_request_cancelled"
	s.approvals[approvalID] = approval
	for _, event := range auditEventsForApprovalRevoked(approval) {
		s.upsertAuditEventLocked(event)
	}
	return approval, nil
}

func (s *PostgresStore) CancelWorkspaceApproval(
	ctx context.Context, ownerID, nodeID, agentID, sessionID, turnID, approvalID string,
) (AgentApproval, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentApproval{}, err
	}
	defer func() { _ = tx.Rollback() }()
	approval, err := scanApproval(tx.QueryRowContext(ctx, approvalSelectSQL+`
		WHERE approval_id=$1 AND owner_user_id=$2 AND request_node_id=$3 AND request_agent_id=$4
			AND request_session_id=$5 AND resource_ref=$6
			AND domain='workspace' AND operation='search' AND resource_type='turn'
		FOR UPDATE`, approvalID, ownerID, nodeID, agentID, sessionID, turnID))
	if err != nil {
		return AgentApproval{}, err
	}
	now := s.now().UTC()
	if !workspaceApprovalCanCancel(approval, now) {
		return approval, nil
	}
	approval, err = scanApproval(tx.QueryRowContext(ctx, `
		UPDATE agent_approvals
		SET grant_revoked_at=$7, grant_revoked_by_user_id=$2,
			grant_revocation_reason='workspace_request_cancelled'
		WHERE approval_id=$1 AND owner_user_id=$2 AND request_node_id=$3 AND request_agent_id=$4
			AND request_session_id=$5 AND resource_ref=$6
			AND domain='workspace' AND operation='search' AND resource_type='turn'
			AND grant_revoked_at IS NULL AND (expires_at IS NULL OR expires_at>$7)
			AND (status='pending' OR (status='decided' AND decision='allow' AND decision_scope='once'))
		RETURNING `+approvalReturningSQL, approvalID, ownerID, nodeID, agentID, sessionID, turnID, now))
	if errors.Is(err, ErrNotFound) {
		return AgentApproval{}, ErrConflict
	}
	if err != nil {
		return AgentApproval{}, err
	}
	if err = tx.Commit(); err != nil {
		return AgentApproval{}, err
	}
	if err = upsertAuditEvents(ctx, s, auditEventsForApprovalRevoked(approval)); err != nil {
		return AgentApproval{}, err
	}
	return approval, nil
}
