package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"time"
)

type PostgresStore struct {
	db  *sql.DB
	now func() time.Time
}

func NewPostgresStore(db *sql.DB, now func() time.Time) *PostgresStore {
	return &PostgresStore{db: db, now: now}
}

func (s *PostgresStore) EnsureSchema(ctx context.Context) error {
	initSQL, err := os.ReadFile("db/init.sql")
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, string(initSQL))
	return err
}

func (s *PostgresStore) EnsureUser(
	ctx context.Context,
	email string,
	displayName string,
	role string,
) (User, error) {
	email = normalizeEmail(email)
	if email == "" {
		return User{}, ErrUnauthorized
	}
	if role == "" {
		role = "user"
	}
	userID, err := newSecret("usr")
	if err != nil {
		return User{}, err
	}
	now := s.now().UTC()
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO users (user_id, email, display_name, role, created_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT (email) DO UPDATE SET
			display_name = COALESCE(NULLIF(EXCLUDED.display_name, ''), users.display_name),
			role = EXCLUDED.role,
			last_seen_at = EXCLUDED.last_seen_at
		RETURNING user_id, email, display_name, role, created_at, last_seen_at
	`, userID, email, displayName, role, now)
	return scanUser(row)
}

func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (User, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT user_id, email, display_name, role, created_at, last_seen_at
		FROM users
		WHERE email = $1
	`, normalizeEmail(email))
	return scanUser(row)
}

func (s *PostgresStore) GetUser(ctx context.Context, userID string) (User, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT user_id, email, display_name, role, created_at, last_seen_at
		FROM users
		WHERE user_id = $1
	`, userID)
	return scanUser(row)
}

func (s *PostgresStore) CreateRegistrationToken(
	ctx context.Context,
	ownerUserID string,
	tokenHash string,
	expiresAt *time.Time,
) error {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_registration_tokens (token_hash, owner_user_id, expires_at, created_at)
		VALUES ($1, $2, $3, $4)
	`, tokenHash, ownerUserID, expiresAt, s.now().UTC())
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ResolveRegistrationToken(
	ctx context.Context,
	tokenHash string,
) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var ownerUserID string
	var expiresAt *time.Time
	var usedAt *time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT owner_user_id, expires_at, used_at
		FROM agent_registration_tokens
		WHERE token_hash = $1
		FOR UPDATE
	`, tokenHash).Scan(&ownerUserID, &expiresAt, &usedAt)
	if err != nil {
		return User{}, mapSQLError(err)
	}
	now := s.now().UTC()
	if usedAt != nil || (expiresAt != nil && expiresAt.Before(now)) {
		return User{}, ErrUnauthorized
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_registration_tokens SET used_at = $2 WHERE token_hash = $1
	`, tokenHash, now); err != nil {
		return User{}, err
	}
	user, err := scanUser(tx.QueryRowContext(ctx, `
		SELECT user_id, email, display_name, role, created_at, last_seen_at
		FROM users
		WHERE user_id = $1
	`, ownerUserID))
	if err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *PostgresStore) CreateUserAPIKey(
	ctx context.Context,
	principal UserPrincipal,
	name string,
	keyHash string,
	prefix string,
) (UserAPIKey, error) {
	keyID, err := newSecret("key")
	if err != nil {
		return UserAPIKey{}, err
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO user_api_keys (key_id, owner_user_id, name, key_hash, prefix, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING key_id, owner_user_id, name, prefix, created_at, last_used_at, revoked_at
	`, keyID, principal.User.UserID, name, keyHash, prefix, s.now().UTC())
	return scanUserAPIKey(row)
}

func (s *PostgresStore) ListUserAPIKeys(
	ctx context.Context,
	principal UserPrincipal,
) ([]UserAPIKey, error) {
	query := `
		SELECT key_id, owner_user_id, name, prefix, created_at, last_used_at, revoked_at
		FROM user_api_keys
	`
	args := []any{}
	if !principal.IsAdmin {
		query += ` WHERE owner_user_id = $1`
		args = append(args, principal.User.UserID)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanUserAPIKeys(rows)
}

func (s *PostgresStore) RevokeUserAPIKey(
	ctx context.Context,
	principal UserPrincipal,
	keyID string,
) error {
	query := `UPDATE user_api_keys SET revoked_at = $2 WHERE key_id = $1`
	args := []any{keyID, s.now().UTC()}
	if !principal.IsAdmin {
		query += ` AND owner_user_id = $3`
		args = append(args, principal.User.UserID)
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) AuthenticateUserAPIKey(ctx context.Context, keyHash string) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var keyID string
	var ownerUserID string
	var revokedAt *time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT key_id, owner_user_id, revoked_at
		FROM user_api_keys
		WHERE key_hash = $1
		FOR UPDATE
	`, keyHash).Scan(&keyID, &ownerUserID, &revokedAt)
	if err != nil {
		return User{}, mapSQLError(err)
	}
	if revokedAt != nil {
		return User{}, ErrUnauthorized
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE user_api_keys SET last_used_at = $2 WHERE key_id = $1`, keyID, now); err != nil {
		return User{}, err
	}
	user, err := scanUser(tx.QueryRowContext(ctx, `
		SELECT user_id, email, display_name, role, created_at, last_seen_at
		FROM users
		WHERE user_id = $1
	`, ownerUserID))
	if err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *PostgresStore) RegisterAgent(
	ctx context.Context,
	owner User,
	req RegisterAgentRequest,
	apiKeyHash string,
) (Agent, error) {
	agentID, err := newSecret("agent")
	if err != nil {
		return Agent{}, err
	}
	now := s.now().UTC()
	metadata := nullRaw(req.Metadata)

	row := s.db.QueryRowContext(ctx, `
		INSERT INTO agents (
			agent_id, node_id, owner_user_id, name, hostname, agent_type, machine_type, os, hermes_version,
			api_endpoint, api_key_hash, status, last_heartbeat, registered_at, metadata
		)
		VALUES ($1,NULL,$2,$3,$4,$5,$6,$7,$8,$9,$10,'online',$11,$12,$13)
		RETURNING agent_id, COALESCE(node_id, ''), owner_user_id, name, hostname, agent_type, machine_type, os, hermes_version,
			api_endpoint, status, last_heartbeat, registered_at, COALESCE(metadata, '{}'::jsonb)
	`, agentID, owner.UserID, defaultAgentName(req), req.Hostname, defaultAgentType(req), req.MachineType, req.OS,
		req.HermesVersion, defaultAPIEndpoint(req.APIEndpoint), apiKeyHash, now, now, metadata)

	return scanAgent(row)
}

func (s *PostgresStore) RegisterNode(
	ctx context.Context,
	owner User,
	req RegisterNodeRequest,
	apiKeyHash string,
) (Node, error) {
	nodeID, err := newSecret("node")
	if err != nil {
		return Node{}, err
	}
	now := s.now().UTC()
	row := s.db.QueryRowContext(
		ctx,
		`
		INSERT INTO nodes (
			node_id, owner_user_id, name, hostname, machine_type, os, arch, paxd_version,
			api_endpoint, api_key_hash, status, last_heartbeat, registered_at, metadata
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'online',$11,$12,$13)
		RETURNING node_id, owner_user_id, name, hostname, machine_type, os, arch, paxd_version,
			api_endpoint, status, last_heartbeat, registered_at, COALESCE(metadata, '{}'::jsonb)
	`,
		nodeID,
		owner.UserID,
		defaultNodeName(req),
		req.Hostname,
		req.MachineType,
		defaultOS(req.OS),
		req.Arch,
		req.PaxdVersion,
		defaultAPIEndpoint(req.APIEndpoint),
		apiKeyHash,
		now,
		now,
		nullRaw(req.Metadata),
	)
	return scanNode(row)
}

func (s *PostgresStore) AuthenticateNode(ctx context.Context, apiKeyHash string) (Node, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT node_id, owner_user_id, name, hostname, machine_type, os, arch, paxd_version,
			api_endpoint, computed_status(last_heartbeat), last_heartbeat, registered_at,
			COALESCE(metadata, '{}'::jsonb)
		FROM nodes
		WHERE api_key_hash = $1
	`, apiKeyHash)
	return scanNode(row)
}

func (s *PostgresStore) AuthenticateAgent(ctx context.Context, apiKeyHash string) (Agent, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT agent_id, COALESCE(node_id, ''), owner_user_id, name, hostname, agent_type, machine_type, os, hermes_version,
			api_endpoint, status, last_heartbeat, registered_at, COALESCE(metadata, '{}'::jsonb)
		FROM agents
		WHERE api_key_hash = $1
	`, apiKeyHash)
	return scanAgent(row)
}

func (s *PostgresStore) UpsertNodeStatus(
	ctx context.Context,
	node Node,
	report NodeStatusReport,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE nodes
		SET status = 'online',
			last_heartbeat = $2,
			hostname = COALESCE(NULLIF($3, ''), hostname),
			metadata = COALESCE($4, metadata)
		WHERE node_id = $1
	`, node.NodeID, now, report.Hostname, nullRaw(report.Metadata)); err != nil {
		return err
	}
	for _, input := range report.Agents {
		agentID := input.AgentID
		if agentID == "" {
			agentID, err = newSecret("agent")
			if err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(
			ctx,
			`
			INSERT INTO agents (
				agent_id, node_id, owner_user_id, name, hostname, agent_type, machine_type, os,
				hermes_version, api_endpoint, api_key_hash, status, last_heartbeat, registered_at, metadata
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13,$14)
			ON CONFLICT (agent_id) DO UPDATE SET
				node_id = EXCLUDED.node_id,
				name = COALESCE(NULLIF(EXCLUDED.name, ''), agents.name),
				agent_type = COALESCE(NULLIF(EXCLUDED.agent_type, ''), agents.agent_type),
				status = EXCLUDED.status,
				last_heartbeat = EXCLUDED.last_heartbeat,
				metadata = COALESCE(EXCLUDED.metadata, agents.metadata)
		`,
			agentID,
			node.NodeID,
			node.OwnerUserID,
			firstNonEmpty(input.Name, "agent"),
			node.Hostname,
			firstNonEmpty(input.AgentType, "hermes"),
			node.MachineType,
			node.OS,
			node.PaxdVersion,
			node.APIEndpoint,
			"node:"+node.NodeID+":"+agentID,
			firstNonEmpty(input.Status, "online"),
			now,
			nullRaw(input.Metadata),
		)
		if err != nil {
			return err
		}
		for _, session := range input.Sessions {
			if err := upsertSessionTx(ctx, tx, node.NodeID, agentID, session, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *PostgresStore) UpsertAgentStatus(ctx context.Context, report AgentStatusReport) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `
		UPDATE agents
		SET status = 'online', last_heartbeat = $2, hostname = COALESCE(NULLIF($3, ''), hostname)
		WHERE agent_id = $1
	`, report.AgentID, now, report.Hostname)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}

	for _, input := range report.Sessions {
		if input.SessionID == "" {
			continue
		}
		if err := upsertSessionTx(ctx, tx, "", report.AgentID, input, now); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *PostgresStore) ListNodes(ctx context.Context, principal UserPrincipal) ([]Node, error) {
	query := `
		SELECT node_id, owner_user_id, name, hostname, machine_type, os, arch, paxd_version,
			api_endpoint, computed_status(last_heartbeat), last_heartbeat, registered_at,
			COALESCE(metadata, '{}'::jsonb)
		FROM nodes
	`
	args := []any{}
	if !principal.IsAdmin {
		query += ` WHERE owner_user_id = $1`
		args = append(args, principal.User.UserID)
	}
	query += ` ORDER BY registered_at ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanNodes(rows)
}

func (s *PostgresStore) GetNode(
	ctx context.Context,
	principal UserPrincipal,
	nodeID string,
) (Node, error) {
	query := `
		SELECT node_id, owner_user_id, name, hostname, machine_type, os, arch, paxd_version,
			api_endpoint, computed_status(last_heartbeat), last_heartbeat, registered_at,
			COALESCE(metadata, '{}'::jsonb)
		FROM nodes
		WHERE node_id = $1
	`
	args := []any{nodeID}
	if !principal.IsAdmin {
		query += ` AND owner_user_id = $2`
		args = append(args, principal.User.UserID)
	}
	return scanNode(s.db.QueryRowContext(ctx, query, args...))
}

func (s *PostgresStore) GetNodeAgent(ctx context.Context, nodeID string, agentID string) (Agent, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT agent_id, COALESCE(node_id, ''), owner_user_id, name, hostname, agent_type,
			machine_type, os, hermes_version, api_endpoint, computed_status(last_heartbeat),
			last_heartbeat, registered_at, COALESCE(metadata, '{}'::jsonb)
		FROM agents
		WHERE node_id = $1 AND agent_id = $2
	`, nodeID, agentID)
	return scanAgent(row)
}

func (s *PostgresStore) ListNodeAgents(
	ctx context.Context,
	principal UserPrincipal,
	nodeID string,
) ([]Agent, error) {
	if _, err := s.GetNode(ctx, principal, nodeID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT agent_id, COALESCE(node_id, ''), owner_user_id, name, hostname, agent_type,
			machine_type, os, hermes_version, api_endpoint, computed_status(last_heartbeat),
			last_heartbeat, registered_at, COALESCE(metadata, '{}'::jsonb)
		FROM agents
		WHERE node_id = $1
		ORDER BY registered_at ASC
	`, nodeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanAgents(rows)
}

func (s *PostgresStore) CreateNodeAgent(
	ctx context.Context,
	principal UserPrincipal,
	req CreateAgentRequest,
) (Agent, MailboxMessage, error) {
	node, err := s.GetNode(ctx, principal, req.NodeID)
	if err != nil {
		return Agent{}, MailboxMessage{}, err
	}
	agentID, err := newSecret("agent")
	if err != nil {
		return Agent{}, MailboxMessage{}, err
	}
	now := s.now().UTC()
	agent, err := scanAgent(s.db.QueryRowContext(ctx, `
		INSERT INTO agents (
			agent_id, node_id, owner_user_id, name, hostname, agent_type, machine_type, os,
			hermes_version, api_endpoint, api_key_hash, status, last_heartbeat, registered_at, metadata
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'pending',$12,$12,$13)
		RETURNING agent_id, COALESCE(node_id, ''), owner_user_id, name, hostname, agent_type,
			machine_type, os, hermes_version, api_endpoint, status, last_heartbeat, registered_at,
			COALESCE(metadata, '{}'::jsonb)
	`, agentID, node.NodeID, node.OwnerUserID, firstNonEmpty(req.Name, "agent"), node.Hostname,
		firstNonEmpty(req.AgentType, "hermes"), node.MachineType, node.OS, node.PaxdVersion,
		node.APIEndpoint, "node:"+node.NodeID+":"+agentID, now, nullRaw(req.Metadata)))
	if err != nil {
		return Agent{}, MailboxMessage{}, err
	}
	msg, err := s.insertMailbox(
		ctx,
		principal.User.UserID,
		node.OwnerUserID,
		node.NodeID,
		agent.AgentID,
		"",
		"bootstrap",
		"command",
		req.Metadata,
		"pending",
		"user_to_node",
		now,
	)
	if err != nil {
		return Agent{}, MailboxMessage{}, err
	}
	return agent, msg, nil
}

func (s *PostgresStore) CreateNodeAgentSession(
	ctx context.Context,
	principal UserPrincipal,
	req CreateSessionRequest,
) (AgentSession, error) {
	if req.SessionID == "" {
		generated, err := newSecret("sess")
		if err != nil {
			return AgentSession{}, err
		}
		req.SessionID = generated
	}
	if _, err := s.GetNode(ctx, principal, req.NodeID); err != nil {
		return AgentSession{}, err
	}
	var ownerUserID string
	if err := s.db.QueryRowContext(ctx, `
		SELECT owner_user_id FROM agents WHERE agent_id = $1 AND node_id = $2
	`, req.AgentID, req.NodeID).Scan(&ownerUserID); err != nil {
		return AgentSession{}, mapSQLError(err)
	}
	if !canAccessOwner(principal, ownerUserID) {
		return AgentSession{}, ErrNotFound
	}
	now := s.now().UTC()
	input := SessionStatusInput{
		SessionID:      req.SessionID,
		AgentType:      req.AgentType,
		NativeID:       req.NativeID,
		SessionName:    req.SessionName,
		ProjectID:      req.ProjectID,
		WorkspaceRoots: req.WorkspaceRoots,
		Source:         req.Source,
		Status:         "idle",
	}
	if err := upsertSessionTx(ctx, dbExecer{s.db}, req.NodeID, req.AgentID, input, now); err != nil {
		return AgentSession{}, err
	}
	return scanSession(s.db.QueryRowContext(ctx, sessionSelectSQL+`
		WHERE agent_sessions.agent_id = $1 AND agent_sessions.session_id = $2
	`, req.AgentID, req.SessionID))
}

func (s *PostgresStore) ListAgents(ctx context.Context, principal UserPrincipal) ([]Agent, error) {
	query := `
		SELECT agent_id, COALESCE(node_id, ''), owner_user_id, name, hostname, agent_type, machine_type, os, hermes_version,
			api_endpoint, computed_status(last_heartbeat), last_heartbeat, registered_at, COALESCE(metadata, '{}'::jsonb)
		FROM agents
	`
	args := []any{}
	if !principal.IsAdmin {
		query += ` WHERE owner_user_id = $1`
		args = append(args, principal.User.UserID)
	}
	query += ` ORDER BY registered_at ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanAgents(rows)
}

func (s *PostgresStore) GetAgent(
	ctx context.Context,
	principal UserPrincipal,
	agentID string,
) (Agent, error) {
	query := `
		SELECT agent_id, COALESCE(node_id, ''), owner_user_id, name, hostname, agent_type, machine_type, os, hermes_version,
			api_endpoint, computed_status(last_heartbeat), last_heartbeat, registered_at, COALESCE(metadata, '{}'::jsonb)
		FROM agents
		WHERE agent_id = $1
	`
	args := []any{agentID}
	if !principal.IsAdmin {
		query += ` AND owner_user_id = $2`
		args = append(args, principal.User.UserID)
	}
	row := s.db.QueryRowContext(ctx, query, args...)
	return scanAgent(row)
}

func (s *PostgresStore) ListAgentSessions(
	ctx context.Context,
	principal UserPrincipal,
	agentID string,
) ([]AgentSession, error) {
	query := sessionSelectSQL + ` JOIN agents a ON a.agent_id = agent_sessions.agent_id WHERE agent_sessions.agent_id = $1`
	args := []any{agentID}
	if !principal.IsAdmin {
		query += ` AND a.owner_user_id = $2`
		args = append(args, principal.User.UserID)
	}
	query += ` ORDER BY agent_sessions.updated_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanSessions(rows)
}

func (s *PostgresStore) GetSession(
	ctx context.Context,
	principal UserPrincipal,
	sessionID string,
) (AgentSession, error) {
	query := sessionSelectSQL + ` JOIN agents a ON a.agent_id = agent_sessions.agent_id WHERE agent_sessions.session_id = $1`
	args := []any{sessionID}
	if !principal.IsAdmin {
		query += ` AND a.owner_user_id = $2`
		args = append(args, principal.User.UserID)
	}
	query += ` ORDER BY agent_sessions.updated_at DESC LIMIT 1`
	row := s.db.QueryRowContext(ctx, query, args...)
	return scanSession(row)
}

func (s *PostgresStore) ListSessionMessages(
	ctx context.Context,
	principal UserPrincipal,
	sessionID string,
) ([]MailboxMessage, error) {
	query := mailboxSelectSQL + ` WHERE session_id = $1`
	args := []any{sessionID}
	if !principal.IsAdmin {
		query += ` AND owner_user_id = $2`
		args = append(args, principal.User.UserID)
	}
	query += ` ORDER BY id ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanMailboxRows(rows)
}

func (s *PostgresStore) CreateMailboxMessage(
	ctx context.Context,
	principal UserPrincipal,
	req CreateMailboxRequest,
) (MailboxMessage, error) {
	messageType := defaultMessageType(req.MessageType)
	if messageType == "" {
		return MailboxMessage{}, ErrConflict
	}
	payload, err := mailboxPayload(req)
	if err != nil {
		return MailboxMessage{}, ErrConflict
	}
	var ownerUserID string
	err = s.db.QueryRowContext(ctx, `
		SELECT owner_user_id FROM agents WHERE agent_id = $1
	`, req.AgentID).Scan(&ownerUserID)
	if err != nil {
		return MailboxMessage{}, mapSQLError(err)
	}
	if !canAccessOwner(principal, ownerUserID) {
		return MailboxMessage{}, ErrNotFound
	}
	messageID, err := newSecret("msg")
	if err != nil {
		return MailboxMessage{}, err
	}
	now := s.now().UTC()
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO mailbox (
			message_id, user_id, owner_user_id, node_id, agent_id, session_id, message, message_type,
			payload, status, direction, created_at, expires_at
		)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,NULLIF($6,''),$7,$8,$9,'pending','user_to_node',$10,$11)
		RETURNING `+mailboxReturningSQL+`
	`, messageID, principal.User.UserID, ownerUserID, req.NodeID, req.AgentID, req.SessionID, req.Message, messageType, payload, now, expiresAt(now, messageType))
	msg, err := scanMailbox(row)
	if err != nil {
		return MailboxMessage{}, err
	}
	if err := s.saveMailboxHistory(ctx, msg); err != nil {
		return MailboxMessage{}, err
	}
	return msg, nil
}

func (s *PostgresStore) ListMailbox(
	ctx context.Context,
	filter MailboxFilter,
) ([]MailboxMessage, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	clauses := []string{"1=1"}
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, clause+" $"+strconvArg(len(args)))
	}
	if filter.AgentID != "" {
		add("agent_id =", filter.AgentID)
	}
	if filter.NodeID != "" {
		add("node_id =", filter.NodeID)
	}
	if filter.SessionID != "" {
		add("session_id =", filter.SessionID)
	}
	if filter.Status != "" {
		add("status =", filter.Status)
	}
	if !filter.Principal.IsAdmin {
		add("owner_user_id =", filter.Principal.User.UserID)
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, mailboxSelectSQL+`
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY id DESC
		LIMIT $`+strconvArg(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanMailboxRows(rows)
}

func (s *PostgresStore) PullMailbox(
	ctx context.Context,
	agentID string,
	sessionID string,
	offset int64,
	limit int,
) (MailboxPull, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MailboxPull{}, err
	}
	defer func() { _ = tx.Rollback() }()

	now := s.now().UTC()
	rows, err := s.pullMailboxRows(ctx, tx, agentID, sessionID, offset, limit, now)
	if err != nil {
		return MailboxPull{}, err
	}
	messages, err := scanMailboxRows(rows)
	closeErr := rows.Close()
	if err != nil {
		return MailboxPull{}, err
	}
	if closeErr != nil {
		return MailboxPull{}, closeErr
	}

	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}

	maxOffset := offset
	for i := range messages {
		messages[i].Status = "delivered"
		messages[i].DeliveredAt = &now
		if messages[i].ID > maxOffset {
			maxOffset = messages[i].ID
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE mailbox
			SET status = 'delivered', delivered_at = $2
			WHERE id = $1
		`, messages[i].ID, now); err != nil {
			return MailboxPull{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return MailboxPull{}, err
	}
	return MailboxPull{Messages: messages, MaxOffset: maxOffset, HasMore: hasMore}, nil
}

func (s *PostgresStore) PullNodeMailbox(
	ctx context.Context,
	nodeID string,
	agentID string,
	sessionID string,
	offset int64,
	limit int,
) (MailboxPull, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MailboxPull{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UTC()
	clauses := []string{
		"node_id = $1",
		"id > $2",
		"status = 'pending'",
		"COALESCE(direction, 'user_to_node') <> 'node_to_user'",
	}
	args := []any{nodeID, offset}
	if agentID != "" {
		args = append(args, agentID)
		clauses = append(clauses, "agent_id = $"+strconvArg(len(args)))
	}
	if sessionID != "" {
		args = append(args, sessionID)
		clauses = append(clauses, "session_id = $"+strconvArg(len(args)))
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE mailbox
		SET status = 'expired'
		WHERE node_id = $1 AND status = 'pending' AND expires_at IS NOT NULL AND expires_at < $2
	`, nodeID, now); err != nil {
		return MailboxPull{}, err
	}
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, mailboxSelectSQL+`
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY id ASC
		LIMIT $`+strconvArg(len(args))+`
		FOR UPDATE SKIP LOCKED
	`, args...)
	if err != nil {
		return MailboxPull{}, err
	}
	messages, err := scanMailboxRows(rows)
	closeErr := rows.Close()
	if err != nil {
		return MailboxPull{}, err
	}
	if closeErr != nil {
		return MailboxPull{}, closeErr
	}
	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}
	maxOffset := offset
	for i := range messages {
		messages[i].Status = "delivered"
		messages[i].DeliveredAt = &now
		if messages[i].ID > maxOffset {
			maxOffset = messages[i].ID
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE mailbox
			SET status = 'delivered', delivered_at = $2
			WHERE id = $1
		`, messages[i].ID, now); err != nil {
			return MailboxPull{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return MailboxPull{}, err
	}
	return MailboxPull{Messages: messages, MaxOffset: maxOffset, HasMore: hasMore}, nil
}

func (s *PostgresStore) pullMailboxRows(
	ctx context.Context,
	tx *sql.Tx,
	agentID string,
	sessionID string,
	offset int64,
	limit int,
	now time.Time,
) (*sql.Rows, error) {
	if sessionID == "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE mailbox
			SET status = 'expired'
			WHERE agent_id = $1 AND status = 'pending' AND expires_at IS NOT NULL AND expires_at < $2
		`, agentID, now); err != nil {
			return nil, err
		}
		return tx.QueryContext(ctx, mailboxSelectSQL+`
			WHERE agent_id = $1 AND id > $2 AND status = 'pending'
			ORDER BY id ASC
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		`, agentID, offset, limit+1)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE mailbox
		SET status = 'expired'
		WHERE agent_id = $1 AND session_id = $2 AND status = 'pending'
			AND expires_at IS NOT NULL AND expires_at < $3
	`, agentID, sessionID, now); err != nil {
		return nil, err
	}
	return tx.QueryContext(ctx, mailboxSelectSQL+`
		WHERE agent_id = $1 AND session_id = $2 AND id > $3 AND status = 'pending'
		ORDER BY id ASC
		LIMIT $4
		FOR UPDATE SKIP LOCKED
	`, agentID, sessionID, offset, limit+1)
}

func (s *PostgresStore) MarkMessageResult(
	ctx context.Context,
	agentID string,
	messageID string,
	req MessageResultRequest,
) error {
	if req.Status != "completed" && req.Status != "failed" {
		return ErrConflict
	}
	completedAt := s.now().UTC()
	if req.CompletedAt != nil {
		completedAt = req.CompletedAt.UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE mailbox
		SET status = $3, result = $4, error = $5, completed_at = $6
		WHERE agent_id = $1 AND message_id = $2
	`, agentID, messageID, req.Status, req.Result, req.Error, completedAt)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) MarkNodeMessageResult(
	ctx context.Context,
	nodeID string,
	messageID string,
	req MessageResultRequest,
) error {
	if req.Status != "completed" && req.Status != "failed" {
		return ErrConflict
	}
	completedAt := s.now().UTC()
	if req.CompletedAt != nil {
		completedAt = req.CompletedAt.UTC()
	}
	result, err := s.db.ExecContext(
		ctx,
		`
		UPDATE mailbox
		SET status = $3, result = $4, error = $5, completed_at = $6, payload = COALESCE($7, payload),
			events = COALESCE($8, events), file_changes = COALESCE($9, file_changes), token_usage = COALESCE($10, token_usage)
		WHERE node_id = $1 AND message_id = $2
	`,
		nodeID,
		messageID,
		req.Status,
		firstNonEmpty(req.Result, req.Content, req.ResultMessageID),
		req.Error,
		completedAt,
		nullRaw(req.Payload),
		nullRaw(req.Events),
		jsonOrNil(req.FileChanges),
		jsonOrNil(req.TokenUsage),
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) MarkNodeMessageDelivered(
	ctx context.Context,
	nodeID string,
	req MarkDeliveredRequest,
) error {
	deliveredAt := s.now().UTC()
	if req.DeliveredAt != nil {
		deliveredAt = req.DeliveredAt.UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE mailbox
		SET status = CASE WHEN status = 'pending' THEN 'delivered' ELSE status END,
			delivered_at = $3
		WHERE node_id = $1 AND message_id = $2
	`, nodeID, req.MessageID, deliveredAt)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) CreateNodeOutboundMessage(
	ctx context.Context,
	node Node,
	req CreateOutboundMessageRequest,
) (MailboxMessage, error) {
	var ownerUserID string
	if err := s.db.QueryRowContext(ctx, `
		SELECT owner_user_id FROM agents WHERE agent_id = $1 AND node_id = $2
	`, req.AgentID, node.NodeID).Scan(&ownerUserID); err != nil {
		return MailboxMessage{}, mapSQLError(err)
	}
	createdAt := s.now().UTC()
	if req.CreatedAt != nil {
		createdAt = req.CreatedAt.UTC()
	}
	msg, err := s.insertMailbox(
		ctx,
		ownerUserID,
		ownerUserID,
		node.NodeID,
		req.AgentID,
		req.SessionID,
		req.Content,
		defaultOutboundMessageType(req.MessageType),
		req.Payload,
		defaultOutboundStatus(req.Status),
		"node_to_user",
		createdAt,
	)
	if err != nil {
		return MailboxMessage{}, err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE mailbox
		SET parent_message_id = $2, turn_id = $3, response_id = $4, events = $5,
			file_changes = COALESCE($6, '[]'::jsonb), token_usage = $7
		WHERE id = $1
	`, msg.ID, req.ParentMessageID, req.TurnID, req.ResponseID, nullRaw(req.Events),
		jsonOrNil(req.FileChanges), jsonOrNil(req.TokenUsage))
	if err != nil {
		return MailboxMessage{}, err
	}
	msg, err = scanMailbox(s.db.QueryRowContext(ctx, mailboxSelectSQL+` WHERE id = $1`, msg.ID))
	if err != nil {
		return MailboxMessage{}, err
	}
	if err := s.saveMailboxHistory(ctx, msg); err != nil {
		return MailboxMessage{}, err
	}
	return msg, nil
}

func (s *PostgresStore) UpdateOffset(ctx context.Context, agentID string, offset int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO message_offsets (agent_id, last_offset, updated_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (agent_id) DO UPDATE SET
			last_offset = GREATEST(message_offsets.last_offset, EXCLUDED.last_offset),
			updated_at = EXCLUDED.updated_at
	`, agentID, offset, s.now().UTC())
	return err
}

func (s *PostgresStore) UpdateNodeOffset(ctx context.Context, nodeID string, offset int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO node_message_offsets (node_id, last_offset, updated_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (node_id) DO UPDATE SET
			last_offset = GREATEST(node_message_offsets.last_offset, EXCLUDED.last_offset),
			updated_at = EXCLUDED.updated_at
	`, nodeID, offset, s.now().UTC())
	return err
}

const sessionSelectSQL = `
	SELECT agent_sessions.id, COALESCE(agent_sessions.node_id, ''), agent_sessions.agent_id, session_id,
		COALESCE(session_name, ''), COALESCE(agent_sessions.agent_type, ''),
		COALESCE(native_id, ''), COALESCE(project_id, ''), COALESCE(preview, ''),
		COALESCE(workspace_roots, '[]'::jsonb), COALESCE(source, ''), agent_sessions.status,
		COALESCE(current_task, ''), last_message_at, message_count, token_input,
		token_output, token_total, cache_read_tokens, cache_write_tokens, cache_creation_tokens,
		reasoning_tokens, estimated_cost_usd, actual_cost_usd, cost_usd, COALESCE(model, ''), COALESCE(run_id, ''),
		COALESCE(run_status, ''), agent_sessions.created_at, agent_sessions.updated_at
	FROM agent_sessions`

const mailboxSelectSQL = `
	SELECT ` + mailboxReturningSQL + `
	FROM mailbox`

const mailboxReturningSQL = `
	id, message_id, user_id, owner_user_id, COALESCE(node_id, ''), agent_id, COALESCE(session_id, ''), message, message_type,
		COALESCE(payload, '{}'::jsonb), status, delivered_at, completed_at,
		COALESCE(result, ''), COALESCE(error, ''), created_at, expires_at,
		COALESCE(direction, ''), COALESCE(parent_message_id, ''), COALESCE(turn_id, ''), COALESCE(response_id, ''),
		COALESCE(events, '{}'::jsonb), COALESCE(file_changes, '[]'::jsonb), COALESCE(token_usage, '{}'::jsonb)`

type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type dbExecer struct {
	db *sql.DB
}

func (e dbExecer) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return e.db.ExecContext(ctx, query, args...)
}

func upsertSessionTx(
	ctx context.Context,
	exec sqlExecer,
	nodeID string,
	agentID string,
	input SessionStatusInput,
	now time.Time,
) error {
	roots, err := json.Marshal(input.WorkspaceRoots)
	if err != nil {
		return err
	}
	_, err = exec.ExecContext(
		ctx,
		`
		INSERT INTO agent_sessions (
			node_id, agent_id, session_id, session_name, agent_type, native_id, project_id, preview,
			workspace_roots, source, status, current_task, last_message_at, message_count,
			token_input, token_output, token_total, cache_read_tokens, cache_write_tokens,
			cache_creation_tokens, reasoning_tokens, estimated_cost_usd, actual_cost_usd, cost_usd,
			model, run_id, run_status, created_at, updated_at
		)
		VALUES (NULLIF($1,''),$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$28)
		ON CONFLICT (agent_id, session_id) DO UPDATE SET
			node_id = COALESCE(EXCLUDED.node_id, agent_sessions.node_id),
			session_name = EXCLUDED.session_name,
			agent_type = EXCLUDED.agent_type,
			native_id = EXCLUDED.native_id,
			project_id = EXCLUDED.project_id,
			preview = EXCLUDED.preview,
			workspace_roots = EXCLUDED.workspace_roots,
			source = EXCLUDED.source,
			status = EXCLUDED.status,
			current_task = EXCLUDED.current_task,
			last_message_at = EXCLUDED.last_message_at,
			message_count = EXCLUDED.message_count,
			token_input = EXCLUDED.token_input,
			token_output = EXCLUDED.token_output,
			token_total = EXCLUDED.token_total,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			cache_write_tokens = EXCLUDED.cache_write_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			reasoning_tokens = EXCLUDED.reasoning_tokens,
			estimated_cost_usd = EXCLUDED.estimated_cost_usd,
			actual_cost_usd = EXCLUDED.actual_cost_usd,
			cost_usd = EXCLUDED.cost_usd,
			model = EXCLUDED.model,
			run_id = EXCLUDED.run_id,
			run_status = EXCLUDED.run_status,
			updated_at = EXCLUDED.updated_at
		`,
		nodeID,
		agentID,
		input.SessionID,
		input.SessionName,
		input.AgentType,
		input.NativeID,
		input.ProjectID,
		input.Preview,
		roots,
		input.Source,
		defaultSessionStatus(input.Status),
		input.CurrentTask,
		input.LastMessageAt,
		input.MessageCount,
		input.TokenUsage.Input,
		input.TokenUsage.Output,
		input.TokenUsage.Total,
		input.TokenUsage.CacheRead,
		input.TokenUsage.CacheWrite,
		input.TokenUsage.CacheCreation,
		input.TokenUsage.Reasoning,
		input.TokenUsage.EstimatedCostUSD,
		input.TokenUsage.ActualCostUSD,
		input.TokenUsage.CostUSD,
		input.Model,
		input.RunID,
		input.RunStatus,
		now,
	)
	return err
}

func (s *PostgresStore) insertMailbox(
	ctx context.Context,
	userID string,
	ownerUserID string,
	nodeID string,
	agentID string,
	sessionID string,
	message string,
	messageType string,
	payload []byte,
	status string,
	direction string,
	createdAt time.Time,
) (MailboxMessage, error) {
	messageID, err := newSecret("msg")
	if err != nil {
		return MailboxMessage{}, err
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO mailbox (
			message_id, user_id, owner_user_id, node_id, agent_id, session_id, message, message_type,
			payload, status, direction, created_at, expires_at
		)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,NULLIF($6,''),$7,$8,$9,$10,$11,$12,$13)
		RETURNING `+mailboxReturningSQL+`
	`, messageID, userID, ownerUserID, nodeID, agentID, sessionID, message, messageType, payload,
		status, direction, createdAt, expiresAt(createdAt, messageType))
	msg, err := scanMailbox(row)
	if err != nil {
		return MailboxMessage{}, err
	}
	if err := s.saveMailboxHistory(ctx, msg); err != nil {
		return MailboxMessage{}, err
	}
	return msg, nil
}

func jsonOrNil(v any) any {
	data, err := json.Marshal(v)
	if err != nil || string(data) == "null" || string(data) == "{}" {
		return nil
	}
	return data
}
