package storage

import (
	"context"
)

func (s *MemoryStore) ExpireWorkspaceTurnApprovals(
	_ context.Context, ownerID, nodeID, agentID, sessionID, turnID string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	for id, approval := range s.approvals {
		if approval.OwnerUserID != ownerID || approval.RequestNodeID != nodeID ||
			approval.RequestAgentID != agentID || approval.RequestSessionID != sessionID ||
			approval.Domain != "workspace" || approval.Operation != "search" ||
			approval.ResourceType != "turn" || approval.ResourceRef != turnID {
			continue
		}
		if approval.ExpiresAt == nil || approval.ExpiresAt.After(now) {
			approval.ExpiresAt = &now
			s.approvals[id] = approval
		}
	}
	return nil
}

func (s *PostgresStore) ExpireWorkspaceTurnApprovals(
	ctx context.Context, ownerID, nodeID, agentID, sessionID, turnID string,
) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE agent_approvals
		SET expires_at = $6
		WHERE owner_user_id = $1 AND request_node_id = $2
			AND request_agent_id = $3 AND request_session_id = $4
			AND domain = 'workspace' AND operation = 'search'
			AND resource_type = 'turn' AND resource_ref = $5
			AND (expires_at IS NULL OR expires_at > $6)
	`, ownerID, nodeID, agentID, sessionID, turnID, s.now().UTC())
	return err
}
