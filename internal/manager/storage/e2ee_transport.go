package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) CreateAgentCommand(
	_ context.Context,
	command AgentCommand,
) (AgentCommand, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.agentCommands[command.RecordID]; ok {
		if !sameAgentCommand(existing, command) {
			return AgentCommand{}, false, ErrConflict
		}
		return cloneAgentCommand(existing), false, nil
	}
	s.nextAgentCommandID++
	command.ID = s.nextAgentCommandID
	if command.CreatedAt.IsZero() {
		command.CreatedAt = s.now().UTC()
	}
	command.Nonce = append([]byte(nil), command.Nonce...)
	command.Ciphertext = append([]byte(nil), command.Ciphertext...)
	s.agentCommands[command.RecordID] = command
	return cloneAgentCommand(command), true, nil
}

func (s *MemoryStore) ListPendingAgentCommands(
	_ context.Context,
	agentID string,
	connectionEpoch int64,
	limit int,
) ([]AgentCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	now := s.now().UTC()
	commands := make([]AgentCommand, 0, limit)
	for id := int64(1); id <= s.nextAgentCommandID && len(commands) < limit; id++ {
		for _, command := range s.agentCommands {
			if command.ID != id || command.AgentID != agentID || command.AcknowledgedAt != nil ||
				(command.DeliveredEpoch != nil && *command.DeliveredEpoch == connectionEpoch) {
				continue
			}
			if command.ExpiresAt != nil && !command.ExpiresAt.After(now) {
				continue
			}
			commands = append(commands, cloneAgentCommand(command))
		}
	}
	return commands, nil
}

func (s *MemoryStore) MarkAgentCommandDelivered(
	_ context.Context,
	commandID string,
	connectionEpoch int64,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	command, ok := s.agentCommands[commandID]
	if !ok {
		return ErrNotFound
	}
	if s.agentConnectionEpochs[command.AgentID] != connectionEpoch {
		return ErrConflict
	}
	now := s.now().UTC()
	command.DeliveredAt = &now
	command.DeliveredEpoch = int64Ptr(connectionEpoch)
	s.agentCommands[commandID] = command
	return nil
}

func (s *MemoryStore) AcknowledgeAgentCommand(
	_ context.Context,
	commandID string,
	connectionEpoch int64,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	command, ok := s.agentCommands[commandID]
	if !ok {
		return ErrNotFound
	}
	if s.agentConnectionEpochs[command.AgentID] != connectionEpoch {
		return ErrConflict
	}
	now := s.now().UTC()
	command.AcknowledgedAt = &now
	command.AcknowledgedEpoch = int64Ptr(connectionEpoch)
	s.agentCommands[commandID] = command
	return nil
}

func (s *MemoryStore) InsertAgentEvent(
	_ context.Context,
	event AgentEvent,
) (AgentEvent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := event.AgentID + "\x00" + event.RecordID
	if existing, ok := s.agentEvents[key]; ok {
		if !sameAgentEvent(existing, event) {
			return AgentEvent{}, false, ErrConflict
		}
		return cloneAgentEvent(existing), false, nil
	}
	s.nextAgentEventCursor++
	event.Cursor = s.nextAgentEventCursor
	if event.CreatedAt.IsZero() {
		event.CreatedAt = s.now().UTC()
	}
	event.Nonce = append([]byte(nil), event.Nonce...)
	event.Ciphertext = append([]byte(nil), event.Ciphertext...)
	s.agentEvents[key] = event
	return cloneAgentEvent(event), true, nil
}

func (s *MemoryStore) ListAgentEvents(
	_ context.Context,
	ownerUserID string,
	sessionID string,
	afterCursor int64,
	limit int,
) ([]AgentEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	events := make([]AgentEvent, 0, limit)
	for cursor := afterCursor + 1; cursor <= s.nextAgentEventCursor && len(events) < limit; cursor++ {
		for _, event := range s.agentEvents {
			if event.Cursor == cursor && event.OwnerUserID == ownerUserID &&
				event.SessionID == sessionID {
				events = append(events, cloneAgentEvent(event))
			}
		}
	}
	return events, nil
}

func (s *MemoryStore) RegisterAgentConnection(_ context.Context, agentID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentConnectionEpochs[agentID]++
	return s.agentConnectionEpochs[agentID], nil
}

func (s *MemoryStore) CurrentAgentConnectionEpoch(
	_ context.Context,
	agentID string,
) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	epoch := s.agentConnectionEpochs[agentID]
	if epoch == 0 {
		return 0, ErrNotFound
	}
	return epoch, nil
}

func (s *PostgresStore) CreateAgentCommand(
	ctx context.Context,
	command AgentCommand,
) (AgentCommand, bool, error) {
	requested := cloneAgentCommand(command)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentCommand{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, `
		INSERT INTO agent_commands (
			command_id, owner_user_id, node_id, agent_id, session_id, kind,
			protocol_version, cipher_version, key_epoch, nonce, ciphertext,
			created_at, expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (command_id) DO NOTHING
		RETURNING id, created_at`,
		command.RecordID, command.OwnerUserID, command.NodeID, command.AgentID,
		command.SessionID, command.Kind, command.ProtocolVersion, command.CipherVersion,
		command.KeyEpoch, command.Nonce, command.Ciphertext, command.CreatedAt, command.ExpiresAt,
	)
	created := true
	scanErr := row.Scan(&command.ID, &command.CreatedAt)
	if errors.Is(scanErr, sql.ErrNoRows) {
		created = false
		command, scanErr = scanAgentCommand(
			tx.QueryRowContext(ctx, agentCommandSelect+` WHERE command_id = $1`, command.RecordID),
		)
	}
	if scanErr != nil {
		return AgentCommand{}, false, scanErr
	}
	if !created && !sameAgentCommand(command, requested) {
		return AgentCommand{}, false, ErrConflict
	}
	if created {
		if _, err := tx.ExecContext(ctx, `SELECT pg_notify('pax_agent_commands', $1)`, command.AgentID); err != nil {
			return AgentCommand{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return AgentCommand{}, false, err
	}
	return command, created, nil
}

func (s *PostgresStore) ListPendingAgentCommands(
	ctx context.Context,
	agentID string,
	connectionEpoch int64,
	limit int,
) ([]AgentCommand, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, agentCommandSelect+`
		WHERE agent_id = $1 AND acknowledged_at IS NULL
		  AND (expires_at IS NULL OR expires_at > NOW())
		  AND (delivered_epoch IS NULL OR delivered_epoch <> $2)
		ORDER BY id LIMIT $3`, agentID, connectionEpoch, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	commands := make([]AgentCommand, 0)
	for rows.Next() {
		command, err := scanAgentCommand(rows)
		if err != nil {
			return nil, err
		}
		commands = append(commands, command)
	}
	return commands, rows.Err()
}

func (s *PostgresStore) MarkAgentCommandDelivered(
	ctx context.Context,
	commandID string,
	connectionEpoch int64,
) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_commands AS command
		SET delivered_at = NOW(), delivered_epoch = $2
		FROM agent_connection_epochs AS connection
		WHERE command.command_id = $1
		  AND connection.agent_id = command.agent_id
		  AND connection.connection_epoch = $2`, commandID, connectionEpoch)
	return e2eeUpdateResult(result, err)
}

func (s *PostgresStore) AcknowledgeAgentCommand(
	ctx context.Context,
	commandID string,
	connectionEpoch int64,
) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_commands AS command
		SET acknowledged_at = NOW(), acknowledged_epoch = $2
		FROM agent_connection_epochs AS connection
		WHERE command.command_id = $1
		  AND connection.agent_id = command.agent_id
		  AND connection.connection_epoch = $2`, commandID, connectionEpoch)
	return e2eeUpdateResult(result, err)
}

func (s *PostgresStore) InsertAgentEvent(
	ctx context.Context,
	event AgentEvent,
) (AgentEvent, bool, error) {
	requested := cloneAgentEvent(event)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentEvent{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, `
		INSERT INTO agent_events (
			owner_user_id, agent_id, session_id, local_id, kind, protocol_version,
			cipher_version, key_epoch, nonce, ciphertext, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (agent_id, local_id) DO NOTHING
		RETURNING cursor, created_at`,
		event.OwnerUserID, event.AgentID, event.SessionID, event.RecordID, event.Kind,
		event.ProtocolVersion, event.CipherVersion, event.KeyEpoch, event.Nonce,
		event.Ciphertext, event.CreatedAt,
	)
	created := true
	scanErr := row.Scan(&event.Cursor, &event.CreatedAt)
	if errors.Is(scanErr, sql.ErrNoRows) {
		created = false
		event, scanErr = scanAgentEvent(
			tx.QueryRowContext(
				ctx,
				agentEventSelect+` WHERE agent_id = $1 AND local_id = $2`,
				event.AgentID,
				event.RecordID,
			),
		)
	}
	if scanErr != nil {
		return AgentEvent{}, false, scanErr
	}
	if !created && !sameAgentEvent(event, requested) {
		return AgentEvent{}, false, ErrConflict
	}
	if created {
		if _, err := tx.ExecContext(ctx, `SELECT pg_notify('pax_agent_events', $1)`, event.SessionID); err != nil {
			return AgentEvent{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return AgentEvent{}, false, err
	}
	return event, created, nil
}

func (s *PostgresStore) ListAgentEvents(
	ctx context.Context,
	ownerUserID string,
	sessionID string,
	afterCursor int64,
	limit int,
) ([]AgentEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, agentEventSelect+`
		WHERE owner_user_id = $1 AND session_id = $2 AND cursor > $3
		ORDER BY cursor LIMIT $4`, ownerUserID, sessionID, afterCursor, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	events := make([]AgentEvent, 0)
	for rows.Next() {
		event, err := scanAgentEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *PostgresStore) RegisterAgentConnection(
	ctx context.Context,
	agentID string,
) (int64, error) {
	var epoch int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO agent_connection_epochs (agent_id, connection_epoch, updated_at)
		VALUES ($1, 1, NOW())
		ON CONFLICT (agent_id) DO UPDATE SET
			connection_epoch = agent_connection_epochs.connection_epoch + 1,
			updated_at = NOW()
		RETURNING connection_epoch`, agentID).Scan(&epoch)
	return epoch, err
}

func (s *PostgresStore) CurrentAgentConnectionEpoch(
	ctx context.Context,
	agentID string,
) (int64, error) {
	var epoch int64
	err := s.db.QueryRowContext(ctx, `SELECT connection_epoch FROM agent_connection_epochs WHERE agent_id = $1`, agentID).
		Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return epoch, err
}

const agentCommandSelect = `SELECT id, command_id, owner_user_id, node_id, agent_id,
	session_id, kind, protocol_version, cipher_version, key_epoch, nonce, ciphertext,
	created_at, delivered_at, delivered_epoch, acknowledged_at, acknowledged_epoch, expires_at
	FROM agent_commands`

const agentEventSelect = `SELECT cursor, local_id, owner_user_id, agent_id, session_id,
	kind, protocol_version, cipher_version, key_epoch, nonce, ciphertext, created_at
	FROM agent_events`

func scanAgentCommand(row rowScanner) (AgentCommand, error) {
	var command AgentCommand
	err := row.Scan(&command.ID, &command.RecordID, &command.OwnerUserID, &command.NodeID,
		&command.AgentID, &command.SessionID, &command.Kind, &command.ProtocolVersion,
		&command.CipherVersion, &command.KeyEpoch, &command.Nonce, &command.Ciphertext,
		&command.CreatedAt, &command.DeliveredAt, &command.DeliveredEpoch,
		&command.AcknowledgedAt, &command.AcknowledgedEpoch, &command.ExpiresAt)
	return command, err
}

func scanAgentEvent(row rowScanner) (AgentEvent, error) {
	var event AgentEvent
	err := row.Scan(&event.Cursor, &event.RecordID, &event.OwnerUserID, &event.AgentID,
		&event.SessionID, &event.Kind, &event.ProtocolVersion, &event.CipherVersion,
		&event.KeyEpoch, &event.Nonce, &event.Ciphertext, &event.CreatedAt)
	return event, err
}

func e2eeUpdateResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrConflict
	}
	return nil
}

func sameAgentCommand(left AgentCommand, right AgentCommand) bool {
	return sameE2EERecord(left.E2EERecord, right.E2EERecord) &&
		timePointerEqual(left.ExpiresAt, right.ExpiresAt)
}

func sameAgentEvent(left AgentEvent, right AgentEvent) bool {
	return sameE2EERecord(left.E2EERecord, right.E2EERecord)
}

func sameE2EERecord(left E2EERecord, right E2EERecord) bool {
	return left.RecordID == right.RecordID &&
		left.OwnerUserID == right.OwnerUserID &&
		left.NodeID == right.NodeID &&
		left.AgentID == right.AgentID &&
		left.SessionID == right.SessionID &&
		left.Kind == right.Kind &&
		left.ProtocolVersion == right.ProtocolVersion &&
		left.CipherVersion == right.CipherVersion &&
		left.KeyEpoch == right.KeyEpoch &&
		bytes.Equal(left.Nonce, right.Nonce) &&
		bytes.Equal(left.Ciphertext, right.Ciphertext)
}

func timePointerEqual(left *time.Time, right *time.Time) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Equal(*right)
}

func cloneAgentCommand(command AgentCommand) AgentCommand {
	command.Nonce = append([]byte(nil), command.Nonce...)
	command.Ciphertext = append([]byte(nil), command.Ciphertext...)
	return command
}

func cloneAgentEvent(event AgentEvent) AgentEvent {
	event.Nonce = append([]byte(nil), event.Nonce...)
	event.Ciphertext = append([]byte(nil), event.Ciphertext...)
	return event
}

func int64Ptr(value int64) *int64 { return &value }

var _ domain.E2EETransportStore = (*MemoryStore)(nil)
