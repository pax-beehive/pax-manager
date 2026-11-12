package storage

import (
	"context"
	"errors"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func isWorkspaceSearchApproval(approval AgentApproval) bool {
	return approval.Domain == "workspace" && approval.Operation == "search" &&
		approval.ResourceType == "turn"
}

func workspaceApprovalCanDecide(
	approval AgentApproval,
	request ApprovalDecisionRequest,
	now time.Time,
) bool {
	return approval.Status == "pending" && approval.GrantRevokedAt == nil &&
		(approval.ExpiresAt == nil || approval.ExpiresAt.After(now)) &&
		(request.DecisionOption == "allow_once" || request.DecisionOption == "deny")
}

func (s *PostgresStore) decideWorkspaceApproval(
	ctx context.Context,
	principal UserPrincipal,
	approvalID string,
	request ApprovalDecisionRequest,
) (AgentApproval, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentApproval{}, err
	}
	defer func() { _ = tx.Rollback() }()
	approval, err := scanApproval(tx.QueryRowContext(ctx, approvalSelectSQL+`
		WHERE approval_id=$1 AND owner_user_id=$2 FOR UPDATE`, approvalID, principal.User.UserID))
	if err != nil {
		return AgentApproval{}, err
	}
	// Take the clock after the row lock: a concurrent turn expiry may have waited
	// after the caller's earlier read and must never be extended into a grant.
	now := s.now().UTC()
	if !isWorkspaceSearchApproval(approval) || !workspaceApprovalCanDecide(approval, request, now) {
		return AgentApproval{}, ErrConflict
	}
	decision, scope, nodeID, agentID, sessionID, err := approvalDecisionGrant(approval, request)
	if err != nil {
		return AgentApproval{}, err
	}
	expiresAt := approval.ExpiresAt
	if decision == "allow" {
		grantExpiry := now.Add(domain.WorkspaceSearchGrantLifetime)
		expiresAt = &grantExpiry
	}
	approval, err = scanApproval(tx.QueryRowContext(ctx, `
		UPDATE agent_approvals
		SET status='decided', decision=$2, decision_option=$3, decision_scope=$4,
			grant_node_id=$5, grant_agent_id=$6, grant_session_id=$7, grant_body=$8,
			decided_by_user_id=$9, decided_at=$10, expires_at=$11
		WHERE approval_id=$1 AND owner_user_id=$9
			AND domain='workspace' AND operation='search' AND resource_type='turn'
			AND status='pending' AND grant_revoked_at IS NULL
			AND (expires_at IS NULL OR expires_at>$10)
		RETURNING `+approvalReturningSQL,
		approvalID, decision, request.DecisionOption, scope, nodeID, agentID, sessionID,
		jsonDefault(request.GrantBody, "{}"), principal.User.UserID, now, expiresAt))
	if errors.Is(err, ErrNotFound) {
		return AgentApproval{}, ErrConflict
	}
	if err != nil {
		return AgentApproval{}, err
	}
	if err = tx.Commit(); err != nil {
		return AgentApproval{}, err
	}
	if err = upsertAuditEvents(ctx, s, auditEventsForApprovalDecided(approval)); err != nil {
		return AgentApproval{}, err
	}
	return approval, nil
}
