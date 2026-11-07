package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const secretarySourceQuietPeriod = 30 * time.Second

func secretarySourceSettled(
	message domain.Message,
	session domain.AgentSession,
	written, now time.Time,
) bool {
	if session.RuntimeStatus != domain.RuntimeStatusIdle {
		return false
	}
	state := session.RuntimeState
	if state == nil {
		state = runtimeStateFromMetadata(session.Metadata)
	}
	if state != nil && state.Lifecycle != "" && state.Lifecycle != domain.RuntimeLifecycleIdle &&
		state.Lifecycle != domain.RuntimeLifecycleErrored {
		return false
	}
	switch message.Status {
	case "streaming", "running", "pending", "in_progress":
		return false
	}
	for _, updated := range []time.Time{message.CreatedAt, message.UpdatedAt, written} {
		if updated.After(now.Add(-secretarySourceQuietPeriod)) {
			return false
		}
	}
	return !message.CreatedAt.IsZero() || !message.UpdatedAt.IsZero()
}

func secretarySourceComplete(message, terminal domain.Message) bool {
	if message.Status == "complete" {
		return true
	}
	if terminal.AgentID != message.AgentID || terminal.SessionID != message.SessionID ||
		terminal.SessionSeq < message.SessionSeq ||
		(message.TurnID != "" && terminal.TurnID != message.TurnID) {
		return false
	}
	if terminal.MessageType == "turn_done" && terminal.Status == "complete" {
		return true
	}
	if terminal.MessageType != "end_turn" || len(terminal.RawJSON) > 64*1024 {
		return false
	}
	var result struct {
		Result struct {
			StopReason string `json:"stopReason"`
		} `json:"result"`
	}
	return json.Unmarshal(terminal.RawJSON, &result) == nil &&
		result.Result.StopReason == "end_turn"
}

func (s *MemoryStore) secretarySourcesCompleteLocked(job domain.SecretaryJob) bool {
	for _, doc := range job.Messages {
		message, ok := s.messages[doc.SourceID]
		if !ok {
			return false
		}
		complete := secretarySourceComplete(message, domain.Message{})
		for _, terminal := range s.messages {
			if complete {
				break
			}
			complete = secretarySourceComplete(message, terminal)
		}
		if !complete {
			session, exists := s.sessions[sessionKey(message.AgentID, message.SessionID)]
			if !exists {
				return false
			}
			var written time.Time
			for key, part := range s.messageParts {
				if key.MessageID == message.MessageID && part.PartType == "text" {
					written = maxSecretarySourceWrite(written, part.CreatedAt, part.UpdatedAt)
				}
			}
			if !secretarySourceSettled(message, session, written, s.now()) {
				return false
			}
		}
	}
	return true
}

func maxSecretarySourceWrite(values ...time.Time) time.Time {
	var latest time.Time
	for _, value := range values {
		if value.After(latest) {
			latest = value
		}
	}
	return latest
}

func secretarySourcesCompleteTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
) (bool, error) {
	if len(job.Messages) == 0 {
		return true, nil
	}
	ids := make([]string, 0, len(job.Messages))
	for _, doc := range job.Messages {
		ids = append(ids, doc.SourceID)
	}
	rows, err := tx.QueryContext(
		ctx,
		`SELECT m.agent_id,m.session_id,m.turn_id,m.session_seq,m.status,
 COALESCE(t.message_type,''),COALESCE(t.status,''),COALESCE(t.turn_id,''),COALESCE(t.session_seq,0),
 CASE WHEN octet_length(t.raw_json::text)<=65536 THEN t.raw_json ELSE '{}'::jsonb END,
 m.created_at,m.updated_at,s.runtime_status,
 COALESCE(s.metadata#>>'{runtime_state,lifecycle}',''),
 COALESCE((SELECT max(GREATEST(p.created_at,p.updated_at)) FROM message_parts p
 WHERE p.message_id=m.message_id AND p.part_type='text'),m.updated_at),NOW()
 FROM messages m JOIN agent_sessions s ON s.agent_id=m.agent_id AND s.session_id=m.session_id
 LEFT JOIN LATERAL (
 SELECT message_type,status,turn_id,session_seq,raw_json FROM messages t
 WHERE t.agent_id=m.agent_id AND t.session_id=m.session_id AND t.session_seq>=m.session_seq
 AND (COALESCE(m.turn_id,'')='' OR t.turn_id=m.turn_id)
 AND ((t.message_type='turn_done' AND t.status='complete') OR (t.message_type='end_turn'
 AND octet_length(t.raw_json::text)<=65536 AND t.raw_json#>>'{result,stopReason}'='end_turn'))
 ORDER BY t.session_seq LIMIT 1) t ON m.status IS DISTINCT FROM 'complete'
 WHERE m.message_id=ANY($1) AND m.agent_id=$2 AND m.session_id=$3`,
		ids,
		job.SourceAgentID,
		job.SourceSessionID,
	)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		var message, terminal domain.Message
		var session domain.AgentSession
		var runtime domain.SessionRuntimeState
		var written, now time.Time
		var status, turn sql.NullString
		var raw []byte
		if err = rows.Scan(&message.AgentID, &message.SessionID, &turn, &message.SessionSeq, &status,
			&terminal.MessageType, &terminal.Status, &terminal.TurnID, &terminal.SessionSeq, &raw,
			&message.CreatedAt, &message.UpdatedAt, &session.RuntimeStatus, &runtime.Lifecycle,
			&written, &now); err != nil {
			return false, err
		}
		message.Status, message.TurnID = status.String, turn.String
		terminal.AgentID, terminal.SessionID, terminal.RawJSON = message.AgentID, message.SessionID, raw
		session.RuntimeState = &runtime
		if !secretarySourceComplete(message, terminal) &&
			!secretarySourceSettled(message, session, written, now) {
			return false, nil
		}
		count++
	}
	return count == len(ids), rows.Err()
}
