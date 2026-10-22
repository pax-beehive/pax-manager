package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func registeredSessionMatches(session AgentSession, req *domain.RegisterNodeSessionRequest) bool {
	var metadata struct {
		Origin     domain.SessionOrigin `json:"origin"`
		InitialCWD string               `json:"initial_cwd"`
	}
	if json.Unmarshal(session.Metadata, &metadata) != nil {
		return false
	}
	return session.NativeID == req.NativeID && metadata.InitialCWD == req.CWD &&
		metadata.Origin == req.Origin
}

func nodeSessionMetadata(req *domain.RegisterNodeSessionRequest) json.RawMessage {
	data, _ := json.Marshal(
		map[string]any{
			"initial_cwd": req.CWD,
			"origin":      req.Origin,
			"pax_config":  SessionPaxConfig{CWD: req.CWD, ApprovalMode: "manual"},
		},
	)
	return data
}

func (s *MemoryStore) RegisterNodeSession(
	ctx context.Context,
	node Node,
	agentID string,
	req *domain.RegisterNodeSessionRequest,
) (AgentSession, error) {
	if err := ctx.Err(); err != nil {
		return AgentSession{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[agentID]
	if !ok || agent.NodeID != node.NodeID || agent.OwnerUserID != node.OwnerUserID {
		return AgentSession{}, ErrNotFound
	}
	for _, session := range s.sessions {
		if session.AgentID != agentID {
			continue
		}
		var metadata struct {
			Origin domain.SessionOrigin `json:"origin"`
		}
		_ = json.Unmarshal(session.Metadata, &metadata)
		if session.NativeID == req.NativeID ||
			metadata.Origin.RegistrationID == req.Origin.RegistrationID {
			if !registeredSessionMatches(session, req) {
				return AgentSession{}, ErrConflict
			}
			return session, nil
		}
	}
	id, err := newSecret("sess")
	if err != nil {
		return AgentSession{}, err
	}
	session := s.upsertSessionLocked(
		node.NodeID,
		agentID,
		SessionStatusInput{
			SessionID:      id,
			NativeID:       req.NativeID,
			SessionName:    req.Origin.Name,
			Source:         domain.MessageSourceACPTunnel,
			Status:         "idle",
			WorkspaceRoots: []string{req.CWD},
		},
		"",
		s.now().UTC(),
	)
	session.Transport = domain.SessionTransportE2EE
	session.Metadata = nodeSessionMetadata(req)
	session.PaxConfig = SessionPaxConfig{CWD: req.CWD, ApprovalMode: "manual"}
	s.sessions[sessionKey(agentID, id)] = session
	return session, nil
}

func (s *PostgresStore) RegisterNodeSession(
	ctx context.Context,
	node Node,
	agentID string,
	req *domain.RegisterNodeSessionRequest,
) (AgentSession, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentSession{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// Serialize registrations for this agent, including retries with different native IDs.
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT owner_user_id FROM agents WHERE agent_id=$1 AND node_id=$2 AND deleted_at IS NULL FOR UPDATE`, agentID, node.NodeID).
		Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != node.OwnerUserID) {
		return AgentSession{}, ErrNotFound
	}
	if err != nil {
		return AgentSession{}, err
	}
	existing, err := scanSession(
		tx.QueryRowContext(
			ctx,
			sessionSelectSQL+` WHERE agent_sessions.agent_id=$1 AND (native_id=$2 OR metadata->'origin'->>'registration_id'=$3) LIMIT 1`,
			agentID,
			req.NativeID,
			req.Origin.RegistrationID,
		),
	)
	if err == nil {
		if !registeredSessionMatches(existing, req) {
			return AgentSession{}, ErrConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
		return AgentSession{}, err
	}
	id, err := newSecret("sess")
	if err != nil {
		return AgentSession{}, err
	}
	input := SessionStatusInput{
		SessionID:      id,
		NativeID:       req.NativeID,
		SessionName:    req.Origin.Name,
		Source:         domain.MessageSourceACPTunnel,
		Status:         "idle",
		WorkspaceRoots: []string{req.CWD},
	}
	if err = upsertSessionTx(ctx, tx, node.NodeID, agentID, input, "", s.now().UTC()); err != nil {
		return AgentSession{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_sessions SET metadata=$3::jsonb, transport='e2ee' WHERE agent_id=$1 AND session_id=$2`, agentID, id, string(nodeSessionMetadata(req))); err != nil {
		return AgentSession{}, err
	}
	session, err := scanSession(
		tx.QueryRowContext(
			ctx,
			sessionSelectSQL+` WHERE agent_sessions.agent_id=$1 AND session_id=$2`,
			agentID,
			id,
		),
	)
	if err != nil {
		return AgentSession{}, err
	}
	return session, tx.Commit()
}
