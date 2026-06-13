package storage

import (
	"database/sql"
	"encoding/json"
	"strconv"
)

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAgent(row rowScanner) (Agent, error) {
	var agent Agent
	var metadata []byte
	if err := row.Scan(
		&agent.AgentID,
		&agent.OwnerUserID,
		&agent.Name,
		&agent.Hostname,
		&agent.AgentType,
		&agent.MachineType,
		&agent.OS,
		&agent.HermesVersion,
		&agent.APIEndpoint,
		&agent.Status,
		&agent.LastHeartbeat,
		&agent.RegisteredAt,
		&metadata,
	); err != nil {
		return Agent{}, mapSQLError(err)
	}
	agent.Online = agent.Status == "online"
	agent.Metadata = json.RawMessage(metadata)
	return agent, nil
}

func scanAgents(rows *sql.Rows) ([]Agent, error) {
	out := make([]Agent, 0)
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, agent)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanSession(row rowScanner) (AgentSession, error) {
	var session AgentSession
	var roots []byte
	if err := row.Scan(
		&session.ID,
		&session.AgentID,
		&session.SessionID,
		&session.SessionName,
		&session.AgentType,
		&session.NativeID,
		&session.ProjectID,
		&session.Preview,
		&roots,
		&session.Source,
		&session.Status,
		&session.CurrentTask,
		&session.LastMessageAt,
		&session.MessageCount,
		&session.TokenInput,
		&session.TokenOutput,
		&session.TokenTotal,
		&session.Model,
		&session.RunID,
		&session.RunStatus,
		&session.CreatedAt,
		&session.UpdatedAt,
	); err != nil {
		return AgentSession{}, mapSQLError(err)
	}
	_ = json.Unmarshal(roots, &session.WorkspaceRoots)
	return session, nil
}

func scanSessions(rows *sql.Rows) ([]AgentSession, error) {
	out := make([]AgentSession, 0)
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, session)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanMailbox(row rowScanner) (MailboxMessage, error) {
	var msg MailboxMessage
	var payload []byte
	if err := row.Scan(
		&msg.ID,
		&msg.MessageID,
		&msg.UserID,
		&msg.OwnerUserID,
		&msg.AgentID,
		&msg.SessionID,
		&msg.Message,
		&msg.MessageType,
		&payload,
		&msg.Status,
		&msg.DeliveredAt,
		&msg.CompletedAt,
		&msg.Result,
		&msg.Error,
		&msg.CreatedAt,
		&msg.ExpiresAt,
	); err != nil {
		return MailboxMessage{}, mapSQLError(err)
	}
	msg.Payload = json.RawMessage(payload)
	return msg, nil
}

func scanUser(row rowScanner) (User, error) {
	var user User
	if err := row.Scan(
		&user.UserID,
		&user.Email,
		&user.DisplayName,
		&user.Role,
		&user.CreatedAt,
		&user.LastSeenAt,
	); err != nil {
		return User{}, mapSQLError(err)
	}
	return user, nil
}

func scanUserAPIKey(row rowScanner) (UserAPIKey, error) {
	var key UserAPIKey
	if err := row.Scan(
		&key.KeyID,
		&key.OwnerUserID,
		&key.Name,
		&key.Prefix,
		&key.CreatedAt,
		&key.LastUsedAt,
		&key.RevokedAt,
	); err != nil {
		return UserAPIKey{}, mapSQLError(err)
	}
	return key, nil
}

func scanUserAPIKeys(rows *sql.Rows) ([]UserAPIKey, error) {
	out := make([]UserAPIKey, 0)
	for rows.Next() {
		key, err := scanUserAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanMailboxRows(rows *sql.Rows) ([]MailboxMessage, error) {
	out := make([]MailboxMessage, 0)
	for rows.Next() {
		msg, err := scanMailbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func mapSQLError(err error) error {
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	return err
}

func nullRaw(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func strconvArg(i int) string {
	return strconv.Itoa(i)
}
