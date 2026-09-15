package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type postgresRuntimeSnapshotHead struct {
	connectionFence sql.NullString
	lastSequence    sql.NullInt64
	authority       string
}

type postgresRuntimeSession struct {
	sessionID       string
	nativeSessionID string
	runtimeStatus   string
	turnInstanceID  string
}

func (s *PostgresStore) ActivateNodeRuntimeFence(
	ctx context.Context,
	node Node,
	fence string,
) error {
	if strings.TrimSpace(fence) == "" {
		return ErrConflict
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO node_runtime_fences (node_id, connection_fence, updated_at)
		SELECT node_id, $3, $4
		FROM nodes
		WHERE node_id = $1 AND owner_user_id = $2 AND deleted_at IS NULL
		ON CONFLICT (node_id) DO UPDATE SET
			connection_fence = EXCLUDED.connection_fence,
			updated_at = EXCLUDED.updated_at
	`, node.NodeID, node.OwnerUserID, fence, s.now().UTC())
	if err != nil {
		return err
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr == nil && affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ReplaceAgentActiveTurns(
	ctx context.Context,
	node Node,
	snapshot domain.AgentRuntimeSnapshot,
) (domain.ReplaceAgentActiveTurnsResult, error) {
	if err := snapshot.Validate(); err != nil {
		return domain.ReplaceAgentActiveTurnsResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ReplaceAgentActiveTurnsResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var currentFence string
	err = tx.QueryRowContext(ctx, `
		SELECT connection_fence
		FROM node_runtime_fences
		WHERE node_id = $1
		FOR UPDATE
	`, node.NodeID).Scan(&currentFence)
	if errors.Is(err, sql.ErrNoRows) || err == nil && currentFence != snapshot.ConnectionFence {
		return domain.ReplaceAgentActiveTurnsResult{Status: domain.RuntimeSnapshotFenced}, nil
	}
	if err != nil {
		return domain.ReplaceAgentActiveTurnsResult{}, err
	}

	// Serialize snapshot application without blocking messages.agent_id foreign key checks.
	var authorized bool
	err = tx.QueryRowContext(ctx, `
		SELECT true
		FROM agents
		WHERE agent_id = $1 AND node_id = $2 AND owner_user_id = $3 AND deleted_at IS NULL
		FOR NO KEY UPDATE
	`, snapshot.AgentID, node.NodeID, node.OwnerUserID).Scan(&authorized)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReplaceAgentActiveTurnsResult{}, ErrNotFound
	}
	if err != nil {
		return domain.ReplaceAgentActiveTurnsResult{}, err
	}

	head, err := lockPostgresRuntimeSnapshotHead(ctx, tx, snapshot.AgentID)
	if err != nil {
		return domain.ReplaceAgentActiveTurnsResult{}, err
	}
	if head.connectionFence.Valid && head.connectionFence.String == snapshot.ConnectionFence &&
		head.lastSequence.Valid {
		switch {
		case snapshot.Sequence == head.lastSequence.Int64:
			return domain.ReplaceAgentActiveTurnsResult{
				Status: domain.RuntimeSnapshotDuplicate,
			}, nil
		case snapshot.Sequence < head.lastSequence.Int64:
			return domain.ReplaceAgentActiveTurnsResult{Status: domain.RuntimeSnapshotStale}, nil
		}
	}

	resolved := make(map[string]string, len(snapshot.ActiveTurns))
	for _, turn := range snapshot.ActiveTurns {
		sessionID, found, resolveErr := s.resolvePostgresRuntimeSession(
			ctx,
			tx,
			snapshot.AgentID,
			turn.NativeSessionID,
		)
		if resolveErr != nil {
			return domain.ReplaceAgentActiveTurnsResult{}, resolveErr
		}
		if !found {
			continue
		}
		resolved[turn.NativeSessionID] = sessionID
	}

	previous, err := loadPostgresRuntimeSessions(ctx, tx, snapshot.AgentID)
	if err != nil {
		return domain.ReplaceAgentActiveTurnsResult{}, err
	}
	now := s.now().UTC()
	changes := make([]domain.RuntimeStatusChange, 0, len(snapshot.ActiveTurns)+len(previous))
	currentSessions := make(map[string]struct{}, len(snapshot.ActiveTurns))
	for _, turn := range snapshot.ActiveTurns {
		sessionID, reportable := resolved[turn.NativeSessionID]
		if !reportable {
			continue
		}
		state := runtimeStateFromTurn(node, snapshot.AgentID, sessionID, turn, now)
		stateJSON, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return domain.ReplaceAgentActiveTurnsResult{}, marshalErr
		}
		result, execErr := tx.ExecContext(ctx, `
			UPDATE agent_sessions
			SET runtime_status = $3,
				runtime_turn_instance_id = $4,
				run_status = $3,
				metadata = jsonb_set(
					COALESCE(metadata, '{}'::jsonb), '{runtime_state}', $5::jsonb, true
				),
				updated_at = $6
			WHERE agent_id = $1 AND session_id = $2
		`, snapshot.AgentID, sessionID, turn.RuntimeStatus, turn.TurnInstanceID, stateJSON, now)
		if execErr != nil {
			return domain.ReplaceAgentActiveTurnsResult{}, execErr
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr == nil && affected == 0 {
			return domain.ReplaceAgentActiveTurnsResult{}, ErrNotFound
		}
		currentSessions[sessionID] = struct{}{}
		old := previous[sessionID]
		if old.runtimeStatus != turn.RuntimeStatus || old.turnInstanceID != turn.TurnInstanceID {
			changes = append(changes, domain.RuntimeStatusChange{
				SessionID: sessionID, NativeSessionID: turn.NativeSessionID,
				RuntimeStatus: turn.RuntimeStatus, TurnInstanceID: turn.TurnInstanceID,
			})
		}
	}
	for sessionID, old := range previous {
		if _, current := currentSessions[sessionID]; current ||
			old.runtimeStatus != domain.RuntimeStatusRunning &&
				old.runtimeStatus != domain.RuntimeStatusWaitingApproval &&
				old.runtimeStatus != domain.RuntimeStatusUnknown {
			continue
		}
		state := idleRuntimeState(node, AgentSession{
			AgentID: snapshot.AgentID, SessionID: sessionID,
		}, now)
		stateJSON, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return domain.ReplaceAgentActiveTurnsResult{}, marshalErr
		}
		if _, execErr := tx.ExecContext(ctx, `
			UPDATE agent_sessions
			SET runtime_status = 'idle',
				runtime_turn_instance_id = NULL,
				run_status = 'idle',
				metadata = jsonb_set(
					COALESCE(metadata, '{}'::jsonb), '{runtime_state}', $3::jsonb, true
				),
				updated_at = $4
			WHERE agent_id = $1 AND session_id = $2
		`, snapshot.AgentID, sessionID, stateJSON, now); execErr != nil {
			return domain.ReplaceAgentActiveTurnsResult{}, execErr
		}
		changes = append(changes, domain.RuntimeStatusChange{
			SessionID: sessionID, NativeSessionID: old.nativeSessionID,
			RuntimeStatus: domain.RuntimeStatusIdle,
		})
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_runtime_snapshot_heads
		SET connection_fence = $2,
			last_sequence = $3,
			runtime_authority = 'snapshot',
			updated_at = $4
		WHERE agent_id = $1
	`, snapshot.AgentID, snapshot.ConnectionFence, snapshot.Sequence, now); err != nil {
		return domain.ReplaceAgentActiveTurnsResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ReplaceAgentActiveTurnsResult{}, err
	}
	return domain.ReplaceAgentActiveTurnsResult{
		Status:           domain.RuntimeSnapshotApplied,
		AuthorityChanged: head.authority != domain.RuntimeAuthoritySnapshot,
		Changes:          changes,
	}, nil
}

func lockPostgresRuntimeSnapshotHead(
	ctx context.Context,
	tx *sql.Tx,
	agentID string,
) (postgresRuntimeSnapshotHead, error) {
	var head postgresRuntimeSnapshotHead
	err := tx.QueryRowContext(ctx, `
		SELECT connection_fence, last_sequence, runtime_authority
		FROM agent_runtime_snapshot_heads
		WHERE agent_id = $1
		FOR UPDATE
	`, agentID).Scan(&head.connectionFence, &head.lastSequence, &head.authority)
	if err == nil {
		return head, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return postgresRuntimeSnapshotHead{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_runtime_snapshot_heads (
			agent_id, connection_fence, last_sequence, runtime_authority, updated_at
		) VALUES ($1, NULL, NULL, 'frames', NOW())
	`, agentID); err != nil {
		return postgresRuntimeSnapshotHead{}, err
	}
	head.authority = domain.RuntimeAuthorityFrames
	return head, nil
}

func (s *PostgresStore) resolvePostgresRuntimeSession(
	ctx context.Context,
	tx *sql.Tx,
	agentID string,
	nativeSessionID string,
) (string, bool, error) {
	var sessionID string
	err := tx.QueryRowContext(ctx, `
		SELECT session_id
		FROM agent_native_session_bindings
		WHERE agent_id = $1 AND native_session_id = $2
	`, agentID, nativeSessionID).Scan(&sessionID)
	if err == nil {
		return sessionID, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT session_id
		FROM agent_sessions
		WHERE agent_id = $1 AND native_id = $2
		ORDER BY updated_at DESC
		FOR UPDATE
	`, agentID, nativeSessionID)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = rows.Close() }()
	var matches []string
	for rows.Next() {
		var match string
		if err := rows.Scan(&match); err != nil {
			return "", false, err
		}
		matches = append(matches, match)
		if len(matches) > 1 {
			return "", false, ErrConflict
		}
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	if len(matches) == 1 {
		sessionID = matches[0]
	} else {
		return "", false, nil
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_native_session_bindings (
			agent_id, native_session_id, session_id, created_at
		) VALUES ($1, $2, $3, $4)
		ON CONFLICT (agent_id, native_session_id) DO UPDATE SET
			session_id = EXCLUDED.session_id
		WHERE agent_native_session_bindings.session_id = EXCLUDED.session_id
	`, agentID, nativeSessionID, sessionID, s.now().UTC())
	if err != nil {
		return "", false, err
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr == nil && affected == 0 {
		return "", false, ErrConflict
	}
	return sessionID, true, nil
}

func loadPostgresRuntimeSessions(
	ctx context.Context,
	tx *sql.Tx,
	agentID string,
) (map[string]postgresRuntimeSession, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT session_id, COALESCE(native_id, ''), runtime_status,
			COALESCE(runtime_turn_instance_id, '')
		FROM agent_sessions
		WHERE agent_id = $1
		FOR UPDATE
	`, agentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make(map[string]postgresRuntimeSession)
	for rows.Next() {
		var session postgresRuntimeSession
		if err := rows.Scan(
			&session.sessionID,
			&session.nativeSessionID,
			&session.runtimeStatus,
			&session.turnInstanceID,
		); err != nil {
			return nil, err
		}
		result[session.sessionID] = session
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
