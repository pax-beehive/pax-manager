package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const queuedTurnColumns = "agent_id, session_id, turn_id, command_id, owner_user_id, input, state, created_at, updated_at"

func scanQueuedTurn(row interface{ Scan(...any) error }) (domain.QueuedTurn, error) {
	var q domain.QueuedTurn
	err := row.Scan(
		&q.AgentID,
		&q.SessionID,
		&q.TurnID,
		&q.CommandID,
		&q.OwnerID,
		&q.Input,
		&q.State,
		&q.CreatedAt,
		&q.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return q, err
}

func (s *PostgresStore) PutQueuedTurn(
	ctx context.Context,
	q domain.QueuedTurn,
	patch bool,
) (domain.QueuedTurn, bool, error) {
	if len(q.Input) == 0 || len(q.Input) > domain.MaxQueuedTurnBytes {
		return domain.QueuedTurn{}, false, ErrConflict
	}
	now := s.now().UTC()
	var row *sql.Row
	if patch {
		row = s.db.QueryRowContext(
			ctx,
			`UPDATE session_turn_queue SET input=$3, command_id=$4, updated_at=$5
   WHERE agent_id=$1 AND session_id=$2 AND state='queued' RETURNING `+queuedTurnColumns,
			q.AgentID,
			q.SessionID,
			q.Input,
			q.CommandID,
			now,
		)
	} else {
		row = s.db.QueryRowContext(ctx, `INSERT INTO session_turn_queue
   (agent_id,session_id,turn_id,command_id,owner_user_id,input,state,created_at,updated_at)
   VALUES ($1,$2,$3,$4,$5,$6,'queued',$7,$7)
   ON CONFLICT (agent_id,session_id) DO UPDATE SET
    input=EXCLUDED.input, command_id=EXCLUDED.command_id,
    owner_user_id=EXCLUDED.owner_user_id, updated_at=EXCLUDED.updated_at
   WHERE session_turn_queue.state='queued' RETURNING `+queuedTurnColumns,
			q.AgentID, q.SessionID, q.TurnID, q.CommandID, q.OwnerID, q.Input, now)
	}
	result, err := scanQueuedTurn(row)
	if errors.Is(err, ErrNotFound) {
		err = ErrConflict
	}
	return result, patch || result.TurnID != q.TurnID, err
}

func (s *PostgresStore) GetQueuedTurn(
	ctx context.Context,
	agentID, sessionID string,
) (domain.QueuedTurn, error) {
	return scanQueuedTurn(s.db.QueryRowContext(ctx, `SELECT `+queuedTurnColumns+`
 FROM session_turn_queue WHERE agent_id=$1 AND session_id=$2`, agentID, sessionID))
}

func (s *PostgresStore) DeleteQueuedTurn(
	ctx context.Context,
	agentID, sessionID string,
) (domain.QueuedTurn, error) {
	q, err := scanQueuedTurn(s.db.QueryRowContext(ctx, `DELETE FROM session_turn_queue
 WHERE agent_id=$1 AND session_id=$2 AND state<>'sending' RETURNING `+queuedTurnColumns, agentID, sessionID))
	if errors.Is(err, ErrNotFound) {
		if _, getErr := s.GetQueuedTurn(ctx, agentID, sessionID); getErr == nil {
			return q, ErrConflict
		} else if !errors.Is(getErr, ErrNotFound) {
			return q, getErr
		}
	}
	return q, err
}

// The leading primary-key column bounds every snapshot read to one agent.
// Payload and session joins are only read when that agent has queue entries.
func (s *PostgresStore) ListQueuedTurns(
	ctx context.Context,
	nodeID, agentID string,
) ([]domain.QueuedTurn, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT q.agent_id,q.session_id,q.turn_id,q.command_id,
 q.owner_user_id,q.input,q.state,q.created_at,q.updated_at,
 a.node_id,COALESCE(s.native_id,'')
 FROM session_turn_queue q
 JOIN agents a ON a.agent_id=q.agent_id
 JOIN agent_sessions s ON s.agent_id=q.agent_id AND s.session_id=q.session_id
 WHERE q.agent_id=$1 AND a.node_id=$2 AND a.deleted_at IS NULL
 AND s.archived_at IS NULL AND q.state<>'uncertain'
 LIMIT 32`, agentID, nodeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []domain.QueuedTurn
	for rows.Next() {
		var q domain.QueuedTurn
		if err := rows.Scan(&q.AgentID, &q.SessionID, &q.TurnID, &q.CommandID, &q.OwnerID, &q.Input, &q.State, &q.CreatedAt, &q.UpdatedAt, &q.NodeID, &q.NativeID); err != nil {
			return nil, err
		}
		result = append(result, q)
	}
	return result, rows.Err()
}

// No network work or session-row locks occur inside this atomic claim.
func (s *PostgresStore) ClaimQueuedTurn(
	ctx context.Context,
	q domain.QueuedTurn,
	snapshot domain.AgentRuntimeSnapshot,
) (bool, error) {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE session_turn_queue q SET state='sending',updated_at=$6
 FROM agent_sessions s,agent_runtime_snapshot_heads h,node_runtime_fences f
 WHERE q.agent_id=$1 AND q.session_id=$2 AND q.turn_id=$3 AND q.updated_at=$4 AND q.state='queued' AND q.input=$9 AND q.command_id=$10
 AND s.agent_id=q.agent_id AND s.session_id=q.session_id
 AND s.node_id=$5 AND s.archived_at IS NULL AND COALESCE(s.native_id,'')<>''
 AND s.runtime_status='idle' AND h.agent_id=q.agent_id
 AND h.connection_fence=$7 AND h.last_sequence=$8
 AND f.node_id=s.node_id AND f.connection_fence=$7`,
		q.AgentID,
		q.SessionID,
		q.TurnID,
		q.UpdatedAt,
		q.NodeID,
		s.now().UTC(),
		snapshot.ConnectionFence,
		snapshot.Sequence,
		q.Input,
		q.CommandID,
	)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *PostgresStore) FinishQueuedTurn(
	ctx context.Context,
	q domain.QueuedTurn,
	accepted bool,
) error {
	query := `UPDATE session_turn_queue SET state='uncertain',updated_at=$4
 WHERE agent_id=$1 AND session_id=$2 AND turn_id=$3 AND state='sending'`
	args := []any{q.AgentID, q.SessionID, q.TurnID, s.now().UTC()}
	if accepted {
		query = `DELETE FROM session_turn_queue WHERE agent_id=$1 AND session_id=$2 AND turn_id=$3 AND state IN ('sending','uncertain')`
		args = args[:3]
	}
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}
