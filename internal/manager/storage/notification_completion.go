package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

type notificationCompletionRow struct {
	NotificationID string
	OwnerUserID    string
	NodeID         string
	AgentID        string
	SessionID      string
	TurnID         string
	CompletedAt    time.Time
}

func normalCompletionRPCID(msg Message) string {
	if msg.Source != domain.MessageSourceACPTunnel ||
		msg.Direction != domain.MessageDirectionAgentToUser ||
		msg.Role != "assistant" ||
		msg.MessageType != "end_turn" ||
		msg.TurnID == "" ||
		msg.OwnerUserID == "" ||
		msg.NodeID == "" ||
		msg.AgentID == "" ||
		msg.SessionID == "" {
		return ""
	}
	var rpc struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Error  json.RawMessage `json:"error"`
		Result struct {
			StopReason string `json:"stopReason"`
		} `json:"result"`
	}
	if json.Unmarshal(msg.RawJSON, &rpc) != nil || rpc.Method != "" ||
		rpc.Result.StopReason != "end_turn" ||
		(len(rpc.Error) > 0 && !bytes.Equal(rpc.Error, []byte("null"))) {
		return ""
	}
	id := bytes.TrimSpace(rpc.ID)
	if len(id) == 0 || bytes.Equal(id, []byte("null")) {
		return ""
	}
	return string(id)
}

func matchesCompletionPrompt(raw json.RawMessage, requestID string) bool {
	var rpc struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	return json.Unmarshal(raw, &rpc) == nil && rpc.Method == "session/prompt" &&
		string(bytes.TrimSpace(rpc.ID)) == requestID
}

func notificationForMessage(msg Message) (notifications.Notification, error) {
	return notifications.BuildTurnNotification(notifications.TurnCompletion{
		UserID: msg.OwnerUserID, NodeID: msg.NodeID, AgentID: msg.AgentID,
		SessionID: msg.SessionID, TurnID: msg.TurnID, CompletedAt: msg.CreatedAt,
	})
}

func completionDeliveryRows(
	notification notifications.Notification,
	records []notifications.RegistrationRecord,
	now time.Time,
) []notificationDeliveryRow {
	var rows []notificationDeliveryRow
	payload, _ := json.Marshal(notification.Payload)
	for _, record := range records {
		if record.RevokedAt != nil || record.InvalidatedAt != nil {
			continue
		}
		for _, delivery := range notifications.PlanDeliveries(notification, []notifications.Receiver{record.Receiver}, now) {
			rows = append(rows, notificationDeliveryRow{
				DeliveryID: delivery.ID, NotificationID: notification.ID, OwnerUserID: delivery.UserID,
				ReceiverID: delivery.ReceiverID, ReceiverRevision: delivery.ReceiverRevision,
				State: string(
					delivery.State,
				), NextAttemptAt: delivery.NextAttemptAt, ExpiresAt: delivery.ExpiresAt,
				CreatedAt: now.UTC(), UpdatedAt: now.UTC(), PayloadJSON: string(payload),
			})
		}
	}
	return rows
}

const notificationSessionSQL = `SELECT EXISTS (
	SELECT 1 FROM agents a JOIN nodes n ON n.node_id=a.node_id
	JOIN agent_sessions s ON s.agent_id=a.agent_id
	WHERE a.agent_id=$1 AND s.session_id=$2 AND a.node_id=$3
	AND a.owner_user_id=$4 AND n.owner_user_id=$4
	AND a.deleted_at IS NULL AND n.deleted_at IS NULL AND s.transport='manager'
)`

func (s *PostgresStore) enqueueCompletedMessage(
	ctx context.Context,
	tx *sql.Tx,
	msg Message,
) error {
	var allowed bool
	if err := tx.QueryRowContext(ctx, notificationSessionSQL, msg.AgentID, msg.SessionID, msg.NodeID, msg.OwnerUserID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	correlated, err := completionPromptCorrelated(ctx, tx, msg)
	if err != nil || !correlated {
		return err
	}
	notification, err := notificationForMessage(msg)
	if err != nil {
		return err
	}
	var owner string
	// Compatible with message FK key-share locks; still serializes registration
	// and fanout without two completions deadlocking while upgrading user locks.
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM users WHERE user_id=$1 FOR NO KEY UPDATE`, msg.OwnerUserID).Scan(&owner); err != nil {
		return err
	}
	result, err := tx.ExecContext(
		ctx,
		`INSERT INTO notification_completions(notification_id,owner_user_id,node_id,agent_id,session_id,turn_id,completed_at)
		VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`,
		notification.ID,
		msg.OwnerUserID,
		msg.NodeID,
		msg.AgentID,
		msg.SessionID,
		msg.TurnID,
		msg.CreatedAt,
	)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 0 {
		return err
	}
	records, err := completionReceivers(ctx, tx, msg)
	if err != nil {
		return err
	}
	for _, row := range completionDeliveryRows(notification, records, s.now()) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_deliveries(delivery_id,notification_id,owner_user_id,receiver_id,receiver_revision,state,next_attempt_at,expires_at,created_at,updated_at,payload_json)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`, row.DeliveryID, row.NotificationID, row.OwnerUserID, row.ReceiverID, row.ReceiverRevision, row.State, row.NextAttemptAt, row.ExpiresAt, row.CreatedAt, row.UpdatedAt, row.PayloadJSON); err != nil {
			return err
		}
	}
	return nil
}

func (s *MemoryStore) notificationSessionAllowed(row notificationCompletionRow) bool {
	agent, ok := s.agents[row.AgentID]
	node, nodeExists := s.nodes[row.NodeID]
	session, exists := s.sessions[sessionKey(row.AgentID, row.SessionID)]
	return ok && nodeExists && exists && agent.NodeID == row.NodeID &&
		agent.OwnerUserID == row.OwnerUserID &&
		node.OwnerUserID == row.OwnerUserID &&
		session.Transport != "e2ee"
}

func (s *MemoryStore) enqueueCompletedMessageLocked(msg Message) {
	requestID := normalCompletionRPCID(msg)
	if requestID == "" {
		return
	}
	row := notificationCompletionRow{
		OwnerUserID: msg.OwnerUserID,
		NodeID:      msg.NodeID,
		AgentID:     msg.AgentID,
		SessionID:   msg.SessionID,
		TurnID:      msg.TurnID,
		CompletedAt: msg.CreatedAt,
	}
	if !s.notificationSessionAllowed(row) {
		return
	}
	correlated := false
	for _, prompt := range s.messages {
		if prompt.AgentID == msg.AgentID && prompt.SessionID == msg.SessionID &&
			prompt.TurnID == msg.TurnID &&
			prompt.OwnerUserID == msg.OwnerUserID &&
			prompt.NodeID == msg.NodeID &&
			prompt.Source == msg.Source &&
			prompt.Role == "user" &&
			prompt.Direction == domain.MessageDirectionUserToAgent &&
			matchesCompletionPrompt(prompt.RawJSON, requestID) {
			correlated = true
			break
		}
	}
	if !correlated {
		return
	}
	notification, err := notificationForMessage(msg)
	if err != nil {
		return
	}
	if _, exists := s.notificationCompletions[notification.ID]; exists {
		return
	}
	if s.notificationCompletions == nil {
		s.notificationCompletions = map[string]notificationCompletionRow{}
	}
	row.NotificationID = notification.ID
	s.notificationCompletions[notification.ID] = row
	var records []notifications.RegistrationRecord
	for _, record := range s.notificationReceivers {
		if !record.CreatedAt.After(msg.CreatedAt) {
			records = append(records, record)
		}
	}
	if s.notificationDeliveries == nil {
		s.notificationDeliveries = map[string]notificationDeliveryRow{}
	}
	for _, delivery := range completionDeliveryRows(notification, records, s.now()) {
		s.notificationDeliveries[delivery.DeliveryID] = delivery
	}
}

func (s *MemoryStore) CanDeliverTurnNotification(
	_ context.Context,
	job notifications.LeasedDelivery,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.notificationCompletions[job.NotificationID]
	return ok && row.OwnerUserID == job.UserID && s.notificationSessionAllowed(row), nil
}

func (s *PostgresStore) CanDeliverTurnNotification(
	ctx context.Context,
	job notifications.LeasedDelivery,
) (bool, error) {
	var allowed bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM notification_completions c JOIN agents a ON a.agent_id=c.agent_id
		JOIN nodes n ON n.node_id=c.node_id JOIN agent_sessions s ON s.agent_id=c.agent_id AND s.session_id=c.session_id
		WHERE c.notification_id=$1 AND c.owner_user_id=$2 AND a.owner_user_id=c.owner_user_id
		AND n.owner_user_id=c.owner_user_id AND a.node_id=c.node_id
		AND a.deleted_at IS NULL AND n.deleted_at IS NULL AND s.transport='manager'
	)`, job.NotificationID, job.UserID).Scan(&allowed)
	return allowed, err
}

func completionPromptCorrelated(ctx context.Context, tx *sql.Tx, msg Message) (bool, error) {
	prompts, err := tx.QueryContext(
		ctx,
		`SELECT raw_json FROM messages WHERE agent_id=$1 AND session_id=$2 AND turn_id=$3
		AND owner_user_id=$4 AND node_id=$5 AND source=$6 AND direction=$7 AND role='user'`,
		msg.AgentID,
		msg.SessionID,
		msg.TurnID,
		msg.OwnerUserID,
		msg.NodeID,
		domain.MessageSourceACPTunnel,
		domain.MessageDirectionUserToAgent,
	)
	if err != nil {
		return false, err
	}
	correlated := false
	for prompts.Next() {
		var raw json.RawMessage
		if err := prompts.Scan(&raw); err != nil {
			_ = prompts.Close()
			return false, err
		}
		correlated = correlated || matchesCompletionPrompt(raw, normalCompletionRPCID(msg))
	}
	err = prompts.Err()
	_ = prompts.Close()
	return correlated, err

}

func completionReceivers(
	ctx context.Context,
	tx *sql.Tx,
	msg Message,
) ([]notifications.RegistrationRecord, error) {
	receivers, err := tx.QueryContext(
		ctx,
		`SELECT record_json FROM notification_receivers WHERE owner_user_id=$1 AND created_at <= $2`,
		msg.OwnerUserID,
		msg.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	var records []notifications.RegistrationRecord
	for receivers.Next() {
		var raw []byte
		var record notifications.RegistrationRecord
		if err := receivers.Scan(&raw); err != nil {
			_ = receivers.Close()
			return nil, err
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			_ = receivers.Close()
			return nil, err
		}
		records = append(records, record)
	}
	err = receivers.Err()
	_ = receivers.Close()
	if err != nil {
		return nil, err
	}
	return records, nil
}
