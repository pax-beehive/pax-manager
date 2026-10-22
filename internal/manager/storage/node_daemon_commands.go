package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	defaultNodeDaemonCommandLimit = 20
	maxNodeDaemonCommandLimit     = 100
)

const nodeDaemonCommandColumns = `
	node_id, command_id, owner_user_id, requested_by_user_id, command_type, target,
	target_version, state, status, phase, error_code, error_message, result,
	maintenance_confirmation, created_at, updated_at, observed_at, completed_at`

func (s *PostgresStore) CreateNodeDaemonCommand(
	ctx context.Context,
	command domain.NodeDaemonCommand,
) (domain.NodeDaemonCommand, bool, error) {
	command = normalizeNodeDaemonCommand(command, s.now().UTC())
	confirmation, err := nullMaintenanceConfirmation(command.MaintenanceConfirmation)
	if err != nil {
		return domain.NodeDaemonCommand{}, false, err
	}
	created, err := scanNodeDaemonCommand(s.db.QueryRowContext(ctx, `
		INSERT INTO node_daemon_commands (`+nodeDaemonCommandColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (node_id, command_id) DO NOTHING
		RETURNING `+nodeDaemonCommandColumns,
		command.NodeID, command.CommandID, command.OwnerUserID, command.RequestedByUserID,
		command.Type, jsonDefault(command.Target, "{}"), command.TargetVersion, command.State,
		command.Status, command.Phase, command.ErrorCode, command.ErrorMessage,
		nullRaw(command.Result), confirmation, command.CreatedAt, command.UpdatedAt,
		command.ObservedAt, command.CompletedAt,
	))
	if err == nil {
		return created, true, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return domain.NodeDaemonCommand{}, false, err
	}
	existing, err := s.GetNodeDaemonCommand(ctx, command.NodeID, command.CommandID)
	return existing, false, err
}

func (s *PostgresStore) SaveNodeDaemonCommand(
	ctx context.Context,
	command domain.NodeDaemonCommand,
) error {
	confirmation, err := nullMaintenanceConfirmation(command.MaintenanceConfirmation)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE node_daemon_commands
		SET state = $3,
			status = $4,
			phase = $5,
			error_code = $6,
			error_message = $7,
			result = $8,
			maintenance_confirmation = $9,
			updated_at = $10,
			observed_at = $11,
			completed_at = $12
		WHERE node_id = $1 AND command_id = $2
	`, command.NodeID, command.CommandID, command.State, command.Status, command.Phase,
		command.ErrorCode, command.ErrorMessage, nullRaw(command.Result), confirmation,
		s.now().UTC(), command.ObservedAt, command.CompletedAt)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) GetNodeDaemonCommand(
	ctx context.Context,
	nodeID string,
	commandID string,
) (domain.NodeDaemonCommand, error) {
	return scanNodeDaemonCommand(s.db.QueryRowContext(ctx, `
		SELECT `+nodeDaemonCommandColumns+`
		FROM node_daemon_commands
		WHERE node_id = $1 AND command_id = $2
	`, nodeID, commandID))
}

func (s *PostgresStore) ListNodeDaemonCommands(
	ctx context.Context,
	nodeID string,
	filter domain.NodeDaemonCommandFilter,
) ([]domain.NodeDaemonCommand, error) {
	clauses := []string{"node_id = $1"}
	args := []any{nodeID}
	if filter.Type != "" {
		args = append(args, filter.Type)
		clauses = append(clauses, "command_type = $"+strconvArg(len(args)))
	}
	if filter.State != "" {
		args = append(args, filter.State)
		clauses = append(clauses, "state = $"+strconvArg(len(args)))
	}
	args = append(args, nodeDaemonCommandLimit(filter.Limit))
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+nodeDaemonCommandColumns+`
		FROM node_daemon_commands
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY created_at DESC, command_id DESC
		LIMIT $`+strconvArg(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanNodeDaemonCommands(rows)
}

func (s *PostgresStore) LatestNodeDaemonCommands(
	ctx context.Context,
	nodeIDs []string,
) (map[string][]domain.NodeDaemonCommand, error) {
	out := make(map[string][]domain.NodeDaemonCommand, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+nodeDaemonCommandColumns+`
		FROM (
			SELECT DISTINCT ON (node_id, command_type) *
			FROM node_daemon_commands
			WHERE node_id = ANY($1)
			ORDER BY node_id, command_type, created_at DESC, command_id DESC
		) latest
		ORDER BY node_id, created_at DESC
	`, nodeIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	commands, err := scanNodeDaemonCommands(rows)
	if err != nil {
		return nil, err
	}
	for _, command := range commands {
		out[command.NodeID] = append(out[command.NodeID], command)
	}
	return out, nil
}

func scanNodeDaemonCommand(row rowScanner) (domain.NodeDaemonCommand, error) {
	var command domain.NodeDaemonCommand
	var target, result, confirmation []byte
	var observedAt, completedAt sql.NullTime
	if err := row.Scan(
		&command.NodeID,
		&command.CommandID,
		&command.OwnerUserID,
		&command.RequestedByUserID,
		&command.Type,
		&target,
		&command.TargetVersion,
		&command.State,
		&command.Status,
		&command.Phase,
		&command.ErrorCode,
		&command.ErrorMessage,
		&result,
		&confirmation,
		&command.CreatedAt,
		&command.UpdatedAt,
		&observedAt,
		&completedAt,
	); err != nil {
		return domain.NodeDaemonCommand{}, mapSQLError(err)
	}
	if len(target) > 0 && string(target) != "{}" {
		command.Target = json.RawMessage(target)
	}
	if len(result) > 0 {
		command.Result = json.RawMessage(result)
	}
	if len(confirmation) > 0 {
		var decoded domain.NodeDaemonMaintenanceConfirmation
		if err := json.Unmarshal(confirmation, &decoded); err == nil {
			command.MaintenanceConfirmation = &decoded
		}
	}
	if observedAt.Valid {
		value := observedAt.Time
		command.ObservedAt = &value
	}
	if completedAt.Valid {
		value := completedAt.Time
		command.CompletedAt = &value
	}
	return command, nil
}

func scanNodeDaemonCommands(rows *sql.Rows) ([]domain.NodeDaemonCommand, error) {
	out := make([]domain.NodeDaemonCommand, 0)
	for rows.Next() {
		command, err := scanNodeDaemonCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, command)
	}
	return out, rows.Err()
}

func nullMaintenanceConfirmation(
	confirmation *domain.NodeDaemonMaintenanceConfirmation,
) (any, error) {
	if confirmation == nil {
		return nil, nil
	}
	raw, err := json.Marshal(confirmation)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *MemoryStore) CreateNodeDaemonCommand(
	ctx context.Context,
	command domain.NodeDaemonCommand,
) (domain.NodeDaemonCommand, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nodeDaemonCommands == nil {
		s.nodeDaemonCommands = make(map[string]domain.NodeDaemonCommand)
	}
	key := nodeDaemonCommandKey(command.NodeID, command.CommandID)
	if existing, ok := s.nodeDaemonCommands[key]; ok {
		return cloneNodeDaemonCommand(existing), false, nil
	}
	command = normalizeNodeDaemonCommand(command, s.now().UTC())
	s.nodeDaemonCommands[key] = cloneNodeDaemonCommand(command)
	return command, true, nil
}

func (s *MemoryStore) SaveNodeDaemonCommand(
	ctx context.Context,
	command domain.NodeDaemonCommand,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nodeDaemonCommandKey(command.NodeID, command.CommandID)
	existing, ok := s.nodeDaemonCommands[key]
	if !ok {
		return ErrNotFound
	}
	existing.State = command.State
	existing.Status = command.Status
	existing.Phase = command.Phase
	existing.ErrorCode = command.ErrorCode
	existing.ErrorMessage = command.ErrorMessage
	existing.Result = command.Result
	existing.MaintenanceConfirmation = command.MaintenanceConfirmation
	existing.UpdatedAt = s.now().UTC()
	existing.ObservedAt = command.ObservedAt
	existing.CompletedAt = command.CompletedAt
	s.nodeDaemonCommands[key] = cloneNodeDaemonCommand(existing)
	return nil
}

func (s *MemoryStore) GetNodeDaemonCommand(
	ctx context.Context,
	nodeID string,
	commandID string,
) (domain.NodeDaemonCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	command, ok := s.nodeDaemonCommands[nodeDaemonCommandKey(nodeID, commandID)]
	if !ok {
		return domain.NodeDaemonCommand{}, ErrNotFound
	}
	return cloneNodeDaemonCommand(command), nil
}

func (s *MemoryStore) ListNodeDaemonCommands(
	ctx context.Context,
	nodeID string,
	filter domain.NodeDaemonCommandFilter,
) ([]domain.NodeDaemonCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.NodeDaemonCommand, 0)
	for _, command := range s.nodeDaemonCommands {
		if command.NodeID != nodeID ||
			(filter.Type != "" && command.Type != filter.Type) ||
			(filter.State != "" && command.State != filter.State) {
			continue
		}
		out = append(out, cloneNodeDaemonCommand(command))
	}
	sortNodeDaemonCommandsNewestFirst(out)
	if limit := nodeDaemonCommandLimit(filter.Limit); len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) LatestNodeDaemonCommands(
	ctx context.Context,
	nodeIDs []string,
) (map[string][]domain.NodeDaemonCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wanted := make(map[string]struct{}, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		wanted[nodeID] = struct{}{}
	}
	latest := make(map[string]domain.NodeDaemonCommand)
	for _, command := range s.nodeDaemonCommands {
		if _, ok := wanted[command.NodeID]; !ok {
			continue
		}
		key := command.NodeID + "\x00" + command.Type
		current, ok := latest[key]
		if !ok || nodeDaemonCommandNewer(command, current) {
			latest[key] = command
		}
	}
	out := make(map[string][]domain.NodeDaemonCommand, len(nodeIDs))
	for _, command := range latest {
		out[command.NodeID] = append(out[command.NodeID], cloneNodeDaemonCommand(command))
	}
	for nodeID := range out {
		sortNodeDaemonCommandsNewestFirst(out[nodeID])
	}
	return out, nil
}

func normalizeNodeDaemonCommand(
	command domain.NodeDaemonCommand,
	now time.Time,
) domain.NodeDaemonCommand {
	if command.Status == "" {
		command.Status = domain.NodeDaemonCommandStatusDispatching
	}
	if command.State == "" {
		command.State = domain.NodeDaemonCommandState(command)
	}
	if command.CreatedAt.IsZero() {
		command.CreatedAt = now
	}
	if command.UpdatedAt.IsZero() {
		command.UpdatedAt = command.CreatedAt
	}
	return command
}

func nodeDaemonCommandLimit(limit int) int {
	if limit <= 0 {
		return defaultNodeDaemonCommandLimit
	}
	if limit > maxNodeDaemonCommandLimit {
		return maxNodeDaemonCommandLimit
	}
	return limit
}

func nodeDaemonCommandKey(nodeID, commandID string) string {
	return nodeID + "\x00" + commandID
}

func nodeDaemonCommandNewer(a, b domain.NodeDaemonCommand) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.CommandID > b.CommandID
}

func sortNodeDaemonCommandsNewestFirst(commands []domain.NodeDaemonCommand) {
	sort.Slice(commands, func(i, j int) bool {
		return nodeDaemonCommandNewer(commands[i], commands[j])
	})
}

func cloneNodeDaemonCommand(command domain.NodeDaemonCommand) domain.NodeDaemonCommand {
	command.Target = cloneRaw(command.Target)
	command.Result = cloneRaw(command.Result)
	if command.MaintenanceConfirmation != nil {
		confirmation := *command.MaintenanceConfirmation
		command.MaintenanceConfirmation = &confirmation
	}
	if command.ObservedAt != nil {
		value := *command.ObservedAt
		command.ObservedAt = &value
	}
	if command.CompletedAt != nil {
		value := *command.CompletedAt
		command.CompletedAt = &value
	}
	return command
}
