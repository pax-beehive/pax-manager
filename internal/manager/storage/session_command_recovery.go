package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var (
	_ domain.SessionCommandJournalStore = (*MemoryStore)(nil)
	_ domain.SessionCommandJournalStore = (*PostgresStore)(nil)
)

type commandRecoveryPayload struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		SessionID       string `json:"sessionId"`
		LegacySessionID string `json:"session_id"`
		Update          struct {
			Kind string `json:"sessionUpdate"`
		} `json:"update"`
	} `json:"params"`
}

func (s *MemoryStore) LatestDeferredSessionCommands(
	ctx context.Context,
	ownerUserID, agentID, queueID, nativeSessionID string,
	beforeSeq int64,
) (domain.SessionCommandUpdate, error) {
	if err := ctx.Err(); err != nil {
		return domain.SessionCommandUpdate{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[agentID]
	if !ok || ownerUserID == "" || agent.OwnerUserID != ownerUserID || queueID == "" ||
		nativeSessionID == "" ||
		beforeSeq <= 0 {
		return domain.SessionCommandUpdate{}, ErrNotFound
	}
	var selected TransportFrame
	for _, frame := range s.transportJournal {
		if frame.AgentID != agentID || frame.QueueID != queueID || frame.Stream != "acp" ||
			frame.Direction != "inbound" || frame.Seq >= beforeSeq || frame.Seq <= selected.Seq {
			continue
		}
		if !isDeferredSessionCommandFrame(frame, nativeSessionID) {
			continue
		}
		selected = frame
	}
	if selected.Seq == 0 {
		return domain.SessionCommandUpdate{}, ErrNotFound
	}
	var payload struct {
		Params json.RawMessage `json:"params"`
	}
	_ = json.Unmarshal(selected.PayloadJSON, &payload)
	return domain.SessionCommandUpdate{
		Params:     append(json.RawMessage(nil), payload.Params...),
		ObservedAt: selected.CreatedAt,
	}, nil
}

func isDeferredSessionCommandFrame(frame TransportFrame, nativeSessionID string) bool {
	if frame.Metadata[domain.DeferredSessionCommandsMetadataKey] != "true" {
		return false
	}
	var payload commandRecoveryPayload
	return json.Unmarshal(frame.PayloadJSON, &payload) == nil &&
		payload.Method == "session/update" && len(payload.ID) == 0 &&
		payload.Params.Update.Kind == "available_commands_update" &&
		firstNonEmpty(
			frame.Metadata["native_session_id"],
			payload.Params.SessionID,
			payload.Params.LegacySessionID,
		) == nativeSessionID
}

func (s *MemoryStore) RecoverSessionCreation(
	ctx context.Context,
	ownerUserID, agentID, queueID, requestID string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[agentID]
	if !ok || ownerUserID == "" || agent.OwnerUserID != ownerUserID || queueID == "" ||
		!json.Valid([]byte(requestID)) {
		return "", ErrNotFound
	}
	var seq int64
	var sessionID string
	for _, frame := range s.transportJournal {
		if frame.AgentID != agentID || frame.QueueID != queueID || frame.Stream != "acp" ||
			frame.Direction != "outbound" ||
			frame.Seq <= seq {
			continue
		}
		var payload commandRecoveryPayload
		if json.Unmarshal(frame.PayloadJSON, &payload) != nil ||
			!commandRecoveryIDMatches(payload.ID, requestID) {
			continue
		}
		seq, sessionID = frame.Seq, ""
		if payload.Method == "session/new" {
			sessionID = frame.Metadata["manager_session_id"]
		}
	}
	if sessionID == "" {
		return "", ErrNotFound
	}
	return sessionID, nil
}

func commandRecoveryIDMatches(id json.RawMessage, requestID string) bool {
	if string(id) == requestID {
		return true
	}
	var stored, wanted string
	return json.Unmarshal(id, &stored) == nil &&
		json.Unmarshal([]byte(requestID), &wanted) == nil &&
		stored == wanted
}

func (s *PostgresStore) LatestDeferredSessionCommands(
	ctx context.Context,
	ownerUserID, agentID, queueID, nativeSessionID string,
	beforeSeq int64,
) (domain.SessionCommandUpdate, error) {
	if ownerUserID == "" || agentID == "" || queueID == "" || nativeSessionID == "" ||
		beforeSeq <= 0 {
		return domain.SessionCommandUpdate{}, ErrNotFound
	}
	var update domain.SessionCommandUpdate
	err := s.db.QueryRowContext(ctx, `
		SELECT j.payload_json->'params', j.created_at
		FROM transport_journal j
		JOIN agents a ON a.agent_id = j.agent_id
		WHERE a.owner_user_id=$1 AND a.agent_id=$2 AND a.deleted_at IS NULL
			AND j.queue_id=$3 AND j.stream='acp' AND j.direction='inbound' AND j.seq<$5
			AND j.metadata_json->>'deferred_session_commands'='true'
			AND j.payload_json->>'method'='session/update' AND NOT (j.payload_json ? 'id')
			AND j.payload_json->'params'->'update'->>'sessionUpdate'='available_commands_update'
			AND COALESCE(NULLIF(j.metadata_json->>'native_session_id',''),
				NULLIF(j.payload_json->'params'->>'sessionId',''),j.payload_json->'params'->>'session_id')=$4
		ORDER BY j.seq DESC LIMIT 1
	`, ownerUserID, agentID, queueID, nativeSessionID, beforeSeq).Scan(&update.Params, &update.ObservedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SessionCommandUpdate{}, ErrNotFound
	}
	update.ObservedAt = update.ObservedAt.UTC()
	return update, err
}

func (s *PostgresStore) RecoverSessionCreation(
	ctx context.Context,
	ownerUserID, agentID, queueID, requestID string,
) (string, error) {
	if ownerUserID == "" || agentID == "" || queueID == "" || !json.Valid([]byte(requestID)) {
		return "", ErrNotFound
	}
	var sessionID, method string
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(j.metadata_json->>'manager_session_id',''), COALESCE(j.payload_json->>'method','')
		FROM transport_journal j
		JOIN agents a ON a.agent_id=j.agent_id
		WHERE a.owner_user_id=$1 AND a.agent_id=$2 AND a.deleted_at IS NULL
			AND j.queue_id=$3 AND j.stream='acp' AND j.direction='outbound'
			AND j.payload_json->'id'=$4::jsonb
		ORDER BY j.seq DESC LIMIT 1
	`, ownerUserID, agentID, queueID, requestID).Scan(&sessionID, &method)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (method != "session/new" || sessionID == "") {
		return "", ErrNotFound
	}
	return sessionID, err
}
