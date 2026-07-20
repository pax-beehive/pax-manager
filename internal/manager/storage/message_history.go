package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *PostgresStore) UpsertMessage(ctx context.Context, msg *Message) error {
	if err := validateMessage(msg); err != nil {
		return err
	}
	if msg.ConversationID == "" && msg.AgentID != "" && msg.SessionID != "" {
		msg.ConversationID = s.messageConversationID(ctx, msg.AgentID, msg.SessionID)
	}
	now := s.now().UTC()
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = now
	}
	msg.UpdatedAt = now
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO messages (
			message_id, conversation_id, owner_user_id, node_id, agent_id, session_id, source, direction,
			role, status, message_type, parent_message_id, turn_id, response_id,
			logical_key, raw_json, created_at, updated_at
		)
		VALUES (
			$1,NULLIF($2,''),NULLIF($3,''),NULLIF($4,''),$5,NULLIF($6,''),$7,$8,
			NULLIF($9,''),NULLIF($10,''),NULLIF($11,''),NULLIF($12,''),NULLIF($13,''),
			NULLIF($14,''),NULLIF($15,''),$16,$17,$18
		)
		ON CONFLICT (message_id) DO UPDATE SET
			conversation_id = COALESCE(EXCLUDED.conversation_id, messages.conversation_id),
			owner_user_id = COALESCE(EXCLUDED.owner_user_id, messages.owner_user_id),
			node_id = COALESCE(EXCLUDED.node_id, messages.node_id),
			session_id = COALESCE(EXCLUDED.session_id, messages.session_id),
			role = COALESCE(EXCLUDED.role, messages.role),
			status = COALESCE(EXCLUDED.status, messages.status),
			message_type = COALESCE(EXCLUDED.message_type, messages.message_type),
			parent_message_id = COALESCE(EXCLUDED.parent_message_id, messages.parent_message_id),
			turn_id = COALESCE(EXCLUDED.turn_id, messages.turn_id),
			response_id = COALESCE(EXCLUDED.response_id, messages.response_id),
			raw_json = COALESCE(EXCLUDED.raw_json, messages.raw_json),
			updated_at = EXCLUDED.updated_at
		RETURNING `+messageReturningSQL+`
	`, msg.MessageID, msg.ConversationID, msg.OwnerUserID, msg.NodeID, msg.AgentID, msg.SessionID, msg.Source,
		msg.Direction, msg.Role, msg.Status, msg.MessageType, msg.ParentMessageID, msg.TurnID,
		msg.ResponseID, msg.LogicalKey, nullRaw(msg.RawJSON), msg.CreatedAt, msg.UpdatedAt)
	saved, err := scanMessage(row)
	if err != nil {
		return err
	}
	*msg = saved
	return nil
}

func (s *PostgresStore) messageConversationID(
	ctx context.Context,
	agentID string,
	sessionID string,
) string {
	var conversationID string
	_ = s.db.QueryRowContext(ctx, `
		SELECT COALESCE(conversation_id, '')
		FROM agent_sessions
		WHERE agent_id = $1 AND session_id = $2
		LIMIT 1
	`, agentID, sessionID).Scan(&conversationID)
	return conversationID
}

func (s *PostgresStore) UpsertMessagePart(ctx context.Context, part *MessagePart) error {
	if err := validateMessagePart(part); err != nil {
		return err
	}
	now := s.now().UTC()
	if part.CreatedAt.IsZero() {
		part.CreatedAt = now
	}
	part.UpdatedAt = now
	text, _ := postgresSafeText(part.Text)
	artifactURI, _ := postgresSafeText(part.ArtifactURI)
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO message_parts (
			message_id, part_index, part_type, text, payload_json, artifact_uri, created_at, updated_at
		)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,NULLIF($6,''),$7,$8)
		ON CONFLICT (message_id, part_index) DO UPDATE SET
			part_type = EXCLUDED.part_type,
			text = COALESCE(EXCLUDED.text, message_parts.text),
			payload_json = COALESCE(EXCLUDED.payload_json, message_parts.payload_json),
			artifact_uri = COALESCE(EXCLUDED.artifact_uri, message_parts.artifact_uri),
			updated_at = EXCLUDED.updated_at
		RETURNING `+messagePartReturningSQL+`
	`, part.MessageID, part.PartIndex, part.PartType, text, nullRaw(part.PayloadJSON),
		artifactURI, part.CreatedAt, part.UpdatedAt)
	saved, err := scanMessagePart(row)
	if err != nil {
		return err
	}
	*part = saved
	return nil
}

func (s *PostgresStore) AppendMessagePartText(
	ctx context.Context,
	messageID string,
	partIndex int,
	delta string,
	payloadJSON []byte,
) error {
	if messageID == "" {
		return fmt.Errorf("message_id is required")
	}
	delta, _ = postgresSafeText(delta)
	now := s.now().UTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO message_parts (
			message_id, part_index, part_type, text, payload_json, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$6)
		ON CONFLICT (message_id, part_index) DO UPDATE SET
			text = COALESCE(message_parts.text, '') || EXCLUDED.text,
			payload_json = COALESCE(EXCLUDED.payload_json, message_parts.payload_json),
			updated_at = EXCLUDED.updated_at
	`, messageID, partIndex, domain.MessagePartText, delta, nullRaw(json.RawMessage(payloadJSON)), now)
	return err
}

func (s *PostgresStore) ListMessages(
	ctx context.Context,
	agentID string,
	sessionID string,
	limit int,
) ([]Message, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	return s.listMessagesBefore(ctx, agentID, sessionID, 0, limit)
}

func (s *PostgresStore) ListMessageHistoryPage(
	ctx context.Context,
	agentID string,
	sessionID string,
	beforeID int64,
	limit int,
) (domain.MessageHistoryPage, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	messages, err := s.listMessagesBefore(ctx, agentID, sessionID, beforeID, limit+1)
	if err != nil {
		return domain.MessageHistoryPage{}, err
	}
	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[1:]
	}
	nextBeforeID := int64(0)
	if hasMore && len(messages) > 0 {
		nextBeforeID = messages[0].ID
	}
	return domain.MessageHistoryPage{
		Messages:     messages,
		NextBeforeID: nextBeforeID,
		HasMore:      hasMore,
	}, nil
}

func (s *PostgresStore) listMessagesBefore(
	ctx context.Context,
	agentID string,
	sessionID string,
	beforeID int64,
	limit int,
) ([]Message, error) {
	args := []any{agentID}
	filter := "agent_id = $1"
	if sessionID != "" {
		sessionIDs, err := s.messageHistorySessionIDs(ctx, agentID, sessionID)
		if err != nil {
			return nil, err
		}
		placeholders := make([]string, 0, len(sessionIDs))
		for _, id := range sessionIDs {
			args = append(args, id)
			placeholders = append(placeholders, "$"+strconvArg(len(args)))
		}
		filter += " AND session_id IN (" + strings.Join(placeholders, ",") + ")"
	}
	if beforeID > 0 {
		args = append(args, beforeID)
		filter += " AND id < $" + strconvArg(len(args))
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+messageReturningSQL+`
		FROM messages
		WHERE `+filter+`
		ORDER BY id DESC
		LIMIT $`+strconvArg(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	messages := make([]Message, 0)
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	reverseMessages(messages)
	return messages, nil
}

func (s *PostgresStore) ListConversationMessages(
	ctx context.Context,
	principal UserPrincipal,
	conversationID string,
	limit int,
) ([]domain.MessageWithParts, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if conversationID == "" {
		return nil, domain.ErrNotFound
	}
	var canRead bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM conversation_members
			WHERE conversation_id = $1 AND user_id = $2 AND left_at IS NULL
		)
	`, conversationID, principal.User.UserID).Scan(&canRead); err != nil {
		return nil, err
	}
	if !canRead {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+messageReturningSQL+`
		FROM messages
		WHERE conversation_id = $1
		ORDER BY created_at ASC, id ASC
		LIMIT $2
	`, conversationID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]domain.MessageWithParts, 0)
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		parts, err := s.ListMessageParts(ctx, msg.MessageID)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.MessageWithParts{Message: msg, Parts: parts})
	}
	return out, rows.Err()
}

func (s *PostgresStore) messageHistorySessionIDs(
	ctx context.Context,
	agentID string,
	sessionID string,
) ([]string, error) {
	managerID, err := s.virtualSessionID(ctx, s.db, agentID, sessionID)
	if err != nil {
		return nil, err
	}
	nativeID, err := s.nativeSessionID(ctx, s.db, agentID, managerID)
	if err != nil {
		return nil, err
	}
	return uniqueNonEmptyStrings(sessionID, managerID, nativeID), nil
}

func (s *PostgresStore) ListMessageParts(
	ctx context.Context,
	messageID string,
) ([]MessagePart, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+messagePartReturningSQL+`
		FROM message_parts
		WHERE message_id = $1
		ORDER BY part_index ASC
	`, messageID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	parts := make([]MessagePart, 0)
	for rows.Next() {
		part, err := scanMessagePart(rows)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, rows.Err()
}

func (s *PostgresStore) ListMessagePartsByMessageIDs(
	ctx context.Context,
	messageIDs []string,
) (map[string][]MessagePart, error) {
	partsByMessageID := make(map[string][]MessagePart, len(messageIDs))
	if len(messageIDs) == 0 {
		return partsByMessageID, nil
	}
	args := make([]any, 0, len(messageIDs))
	placeholders := make([]string, 0, len(messageIDs))
	for _, messageID := range messageIDs {
		partsByMessageID[messageID] = make([]MessagePart, 0)
		args = append(args, messageID)
		placeholders = append(placeholders, "$"+strconvArg(len(args)))
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+messagePartReturningSQL+`
		FROM message_parts
		WHERE message_id IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY message_id ASC, part_index ASC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		part, err := scanMessagePart(rows)
		if err != nil {
			return nil, err
		}
		partsByMessageID[part.MessageID] = append(partsByMessageID[part.MessageID], part)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return partsByMessageID, nil
}

func (s *MemoryStore) UpsertMessage(ctx context.Context, msg *Message) error {
	if err := validateMessage(msg); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg.ConversationID == "" && msg.AgentID != "" && msg.SessionID != "" {
		if session, ok := s.sessions[sessionKey(msg.AgentID, msg.SessionID)]; ok {
			msg.ConversationID = session.ConversationID
		}
	}
	s.prepareMessageLocked(msg)
	if msg.LogicalKey != "" {
		if existingID, ok := s.messageLogical[msg.LogicalKey]; ok && existingID != msg.MessageID {
			existing := s.messages[existingID]
			msg.MessageID = existing.MessageID
			msg.ID = existing.ID
			msg.CreatedAt = existing.CreatedAt
		}
		s.messageLogical[msg.LogicalKey] = msg.MessageID
	}
	if existing, ok := s.messages[msg.MessageID]; ok {
		msg.ID = existing.ID
		msg.CreatedAt = existing.CreatedAt
	}
	s.messages[msg.MessageID] = cloneMessage(*msg)
	return nil
}

func (s *MemoryStore) ListConversationMessages(
	ctx context.Context,
	principal UserPrincipal,
	conversationID string,
	limit int,
) ([]domain.MessageWithParts, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if conversationID == "" || !s.canReadConversationLocked(principal.User.UserID, conversationID) {
		return nil, ErrNotFound
	}
	messages := make([]Message, 0)
	for _, msg := range s.messages {
		if msg.ConversationID == conversationID {
			messages = append(messages, cloneMessage(msg))
		}
	}
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].ID < messages[j].ID
	})
	if len(messages) > limit {
		messages = messages[:limit]
	}
	out := make([]domain.MessageWithParts, 0, len(messages))
	for _, msg := range messages {
		parts := make([]MessagePart, 0)
		for key, part := range s.messageParts {
			if key.MessageID == msg.MessageID {
				parts = append(parts, cloneMessagePart(part))
			}
		}
		sort.Slice(parts, func(i, j int) bool {
			return parts[i].PartIndex < parts[j].PartIndex
		})
		out = append(out, domain.MessageWithParts{Message: msg, Parts: parts})
	}
	return out, nil
}

func (s *MemoryStore) ListMessages(
	ctx context.Context,
	agentID string,
	sessionID string,
	limit int,
) ([]Message, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	return s.listMessagesBefore(ctx, agentID, sessionID, 0, limit)
}

func (s *MemoryStore) ListMessageHistoryPage(
	ctx context.Context,
	agentID string,
	sessionID string,
	beforeID int64,
	limit int,
) (domain.MessageHistoryPage, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	messages, err := s.listMessagesBefore(ctx, agentID, sessionID, beforeID, limit+1)
	if err != nil {
		return domain.MessageHistoryPage{}, err
	}
	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[1:]
	}
	nextBeforeID := int64(0)
	if hasMore && len(messages) > 0 {
		nextBeforeID = messages[0].ID
	}
	return domain.MessageHistoryPage{
		Messages:     messages,
		NextBeforeID: nextBeforeID,
		HasMore:      hasMore,
	}, nil
}

func (s *MemoryStore) listMessagesBefore(
	ctx context.Context,
	agentID string,
	sessionID string,
	beforeID int64,
	limit int,
) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionIDs := map[string]struct{}{}
	if sessionID != "" {
		managerID := s.virtualSessionIDLocked(agentID, sessionID)
		nativeID := s.nativeSessionIDLocked(agentID, managerID)
		for _, id := range uniqueNonEmptyStrings(sessionID, managerID, nativeID) {
			sessionIDs[id] = struct{}{}
		}
	}
	messages := make([]Message, 0)
	for _, msg := range s.messages {
		if msg.AgentID != agentID {
			continue
		}
		if beforeID > 0 && msg.ID >= beforeID {
			continue
		}
		if len(sessionIDs) > 0 {
			if _, ok := sessionIDs[msg.SessionID]; !ok {
				continue
			}
		} else if sessionID != "" {
			continue
		}
		messages = append(messages, cloneMessage(msg))
	}
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].ID > messages[j].ID
	})
	if len(messages) > limit {
		messages = messages[:limit]
	}
	reverseMessages(messages)
	return messages, nil
}

func reverseMessages(messages []Message) {
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
}

func uniqueNonEmptyStrings(values ...string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (s *MemoryStore) ListMessageParts(
	ctx context.Context,
	messageID string,
) ([]MessagePart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts := make([]MessagePart, 0)
	for key, part := range s.messageParts {
		if key.MessageID == messageID {
			parts = append(parts, cloneMessagePart(part))
		}
	}
	sort.Slice(parts, func(i, j int) bool {
		return parts[i].PartIndex < parts[j].PartIndex
	})
	return parts, nil
}

func (s *MemoryStore) ListMessagePartsByMessageIDs(
	ctx context.Context,
	messageIDs []string,
) (map[string][]MessagePart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	partsByMessageID := make(map[string][]MessagePart, len(messageIDs))
	for _, messageID := range messageIDs {
		partsByMessageID[messageID] = make([]MessagePart, 0)
	}
	for key, part := range s.messageParts {
		if _, ok := partsByMessageID[key.MessageID]; !ok {
			continue
		}
		partsByMessageID[key.MessageID] = append(
			partsByMessageID[key.MessageID],
			cloneMessagePart(part),
		)
	}
	for messageID := range partsByMessageID {
		sort.Slice(partsByMessageID[messageID], func(i, j int) bool {
			return partsByMessageID[messageID][i].PartIndex <
				partsByMessageID[messageID][j].PartIndex
		})
	}
	return partsByMessageID, nil
}

func (s *MemoryStore) UpsertMessagePart(ctx context.Context, part *MessagePart) error {
	if err := validateMessagePart(part); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepareMessagePartLocked(part)
	key := messagePartKey{MessageID: part.MessageID, Index: part.PartIndex}
	if existing, ok := s.messageParts[key]; ok {
		part.ID = existing.ID
		part.CreatedAt = existing.CreatedAt
		if part.Text == "" {
			part.Text = existing.Text
		}
		if len(part.PayloadJSON) == 0 {
			part.PayloadJSON = existing.PayloadJSON
		}
		if part.ArtifactURI == "" {
			part.ArtifactURI = existing.ArtifactURI
		}
	}
	s.messageParts[key] = cloneMessagePart(*part)
	return nil
}

func (s *MemoryStore) AppendMessagePartText(
	ctx context.Context,
	messageID string,
	partIndex int,
	delta string,
	payloadJSON []byte,
) error {
	if messageID == "" {
		return fmt.Errorf("message_id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := messagePartKey{MessageID: messageID, Index: partIndex}
	part, ok := s.messageParts[key]
	if !ok {
		part = MessagePart{
			MessageID: messageID,
			PartIndex: partIndex,
			PartType:  domain.MessagePartText,
		}
		s.prepareMessagePartLocked(&part)
	}
	part.Text += delta
	if len(payloadJSON) > 0 {
		part.PayloadJSON = append(json.RawMessage(nil), payloadJSON...)
	}
	part.UpdatedAt = s.now().UTC()
	s.messageParts[key] = cloneMessagePart(part)
	return nil
}

const messageReturningSQL = `
	id, message_id, COALESCE(conversation_id, ''), COALESCE(owner_user_id, ''), COALESCE(node_id, ''), agent_id,
	COALESCE(session_id, ''), source, direction, COALESCE(role, ''), COALESCE(status, ''),
	COALESCE(message_type, ''), COALESCE(parent_message_id, ''), COALESCE(turn_id, ''),
	COALESCE(response_id, ''), COALESCE(logical_key, ''), COALESCE(raw_json, '{}'::jsonb),
	created_at, updated_at`

const messagePartReturningSQL = `
	id, message_id, part_index, part_type, COALESCE(text, ''), COALESCE(payload_json, '{}'::jsonb),
	COALESCE(artifact_uri, ''), created_at, updated_at`

func scanMessage(row interface{ Scan(dest ...any) error }) (Message, error) {
	var msg Message
	if err := row.Scan(
		&msg.ID,
		&msg.MessageID,
		&msg.ConversationID,
		&msg.OwnerUserID,
		&msg.NodeID,
		&msg.AgentID,
		&msg.SessionID,
		&msg.Source,
		&msg.Direction,
		&msg.Role,
		&msg.Status,
		&msg.MessageType,
		&msg.ParentMessageID,
		&msg.TurnID,
		&msg.ResponseID,
		&msg.LogicalKey,
		&msg.RawJSON,
		&msg.CreatedAt,
		&msg.UpdatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return Message{}, err
		}
		return Message{}, err
	}
	return msg, nil
}

func scanMessagePart(row interface{ Scan(dest ...any) error }) (MessagePart, error) {
	var part MessagePart
	if err := row.Scan(
		&part.ID,
		&part.MessageID,
		&part.PartIndex,
		&part.PartType,
		&part.Text,
		&part.PayloadJSON,
		&part.ArtifactURI,
		&part.CreatedAt,
		&part.UpdatedAt,
	); err != nil {
		return MessagePart{}, err
	}
	return part, nil
}

func validateMessage(msg *Message) error {
	if msg.MessageID == "" {
		return fmt.Errorf("message_id is required")
	}
	if msg.AgentID == "" {
		return fmt.Errorf("agent_id is required")
	}
	if msg.Source == "" {
		return fmt.Errorf("source is required")
	}
	if msg.Direction == "" {
		return fmt.Errorf("direction is required")
	}
	return nil
}

func validateMessagePart(part *MessagePart) error {
	if part.MessageID == "" {
		return fmt.Errorf("message_id is required")
	}
	if part.PartIndex < 0 {
		return fmt.Errorf("part_index must be non-negative")
	}
	if part.PartType == "" {
		return fmt.Errorf("part_type is required")
	}
	if len(part.PayloadJSON) > 0 && !json.Valid(part.PayloadJSON) {
		return fmt.Errorf("payload_json must be valid JSON")
	}
	return nil
}

func (s *MemoryStore) prepareMessageLocked(msg *Message) {
	now := s.now().UTC()
	if msg.ID == 0 {
		s.nextMessageID++
		msg.ID = s.nextMessageID
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = now
	}
	msg.UpdatedAt = now
}

func (s *MemoryStore) prepareMessagePartLocked(part *MessagePart) {
	now := s.now().UTC()
	if part.ID == 0 {
		s.nextPartID++
		part.ID = s.nextPartID
	}
	if part.CreatedAt.IsZero() {
		part.CreatedAt = now
	}
	part.UpdatedAt = now
}

func cloneMessage(msg Message) Message {
	msg.RawJSON = append(json.RawMessage(nil), msg.RawJSON...)
	return msg
}

func cloneMessagePart(part MessagePart) MessagePart {
	part.PayloadJSON = append(json.RawMessage(nil), part.PayloadJSON...)
	return part
}

func (s *PostgresStore) saveMailboxHistory(ctx context.Context, mailbox MailboxMessage) error {
	msg, part := historyFromMailbox(mailbox)
	if err := s.UpsertMessage(ctx, &msg); err != nil {
		return err
	}
	return s.UpsertMessagePart(ctx, &part)
}

func (s *MemoryStore) saveMailboxHistoryLocked(mailbox MailboxMessage) error {
	msg, part := historyFromMailbox(mailbox)
	s.prepareMessageLocked(&msg)
	if msg.LogicalKey != "" {
		s.messageLogical[msg.LogicalKey] = msg.MessageID
	}
	s.messages[msg.MessageID] = cloneMessage(msg)
	s.prepareMessagePartLocked(&part)
	s.messageParts[messagePartKey{MessageID: part.MessageID, Index: part.PartIndex}] = cloneMessagePart(
		part,
	)
	return nil
}

func historyFromMailbox(mailbox MailboxMessage) (Message, MessagePart) {
	direction := domain.MessageDirectionUserToAgent
	role := "user"
	if mailbox.Direction == "node_to_user" {
		direction = domain.MessageDirectionAgentToUser
		role = "assistant"
	}
	raw := mailbox.Payload
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	msg := Message{
		MessageID:       mailbox.MessageID,
		OwnerUserID:     mailbox.OwnerUserID,
		NodeID:          mailbox.NodeID,
		AgentID:         mailbox.AgentID,
		SessionID:       mailbox.SessionID,
		Source:          domain.MessageSourceMailbox,
		Direction:       direction,
		Role:            role,
		Status:          mailbox.Status,
		MessageType:     mailbox.MessageType,
		ParentMessageID: mailbox.ParentMessageID,
		TurnID:          mailbox.TurnID,
		ResponseID:      mailbox.ResponseID,
		LogicalKey:      "mailbox:" + mailbox.MessageID,
		RawJSON:         raw,
		CreatedAt:       mailbox.CreatedAt,
	}
	part := MessagePart{
		MessageID:   mailbox.MessageID,
		PartIndex:   0,
		PartType:    domain.MessagePartText,
		Text:        mailbox.Message,
		PayloadJSON: raw,
		CreatedAt:   mailbox.CreatedAt,
	}
	return msg, part
}
