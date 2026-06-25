package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAgent(row rowScanner) (Agent, error) {
	var agent Agent
	var metadata []byte
	var liveness string
	if err := row.Scan(
		&agent.AgentID,
		&agent.NodeID,
		&agent.OwnerUserID,
		&agent.Name,
		&agent.Hostname,
		&agent.AgentType,
		&agent.MachineType,
		&agent.OS,
		&agent.HermesVersion,
		&agent.APIEndpoint,
		&agent.Status,
		&liveness,
		&agent.LastHeartbeat,
		&agent.RegisteredAt,
		&metadata,
	); err != nil {
		return Agent{}, mapSQLError(err)
	}
	agent.Status = effectiveAgentStatus(agent.Status, liveness)
	agent.Online = agent.Status == "online"
	agent.Metadata = json.RawMessage(metadata)
	return agent, nil
}

func effectiveAgentStatus(storedStatus string, heartbeatStatus string) string {
	stored := strings.ToLower(strings.TrimSpace(storedStatus))
	heartbeat := strings.ToLower(strings.TrimSpace(heartbeatStatus))
	switch stored {
	case "", "online", "pending":
		if heartbeat != "" {
			return heartbeat
		}
		if stored != "" {
			return stored
		}
		return "offline"
	case "offline", "stopped", "disabled", "failed", "error", "errored":
		return stored
	default:
		return stored
	}
}

func scanNode(row rowScanner) (Node, error) {
	var node Node
	var metadata []byte
	if err := row.Scan(
		&node.NodeID,
		&node.OwnerUserID,
		&node.Kind,
		&node.Name,
		&node.Hostname,
		&node.MachineType,
		&node.OS,
		&node.Arch,
		&node.PaxdVersion,
		&node.APIEndpoint,
		&node.Status,
		&node.LastHeartbeat,
		&node.RegisteredAt,
		&metadata,
	); err != nil {
		return Node{}, mapSQLError(err)
	}
	node.Online = node.Status == "online"
	node.Metadata = json.RawMessage(metadata)
	return node, nil
}

func scanNodeRegistrationSession(row rowScanner) (NodeRegistrationSession, error) {
	var session NodeRegistrationSession
	var metadata []byte
	if err := row.Scan(
		&session.RegistrationID,
		&session.PairCode,
		&session.PollTokenHash,
		&session.Status,
		&session.OwnerUserID,
		&session.NodeID,
		&session.Request.Name,
		&session.Request.Hostname,
		&session.Request.MachineType,
		&session.Request.OS,
		&session.Request.Arch,
		&session.Request.PaxdVersion,
		&session.Request.APIEndpoint,
		&metadata,
		&session.RequestIP,
		&session.RequestCity,
		&session.RequestCountry,
		&session.ExpiresAt,
		&session.CreatedAt,
		&session.ApprovedAt,
		&session.ConsumedAt,
	); err != nil {
		return NodeRegistrationSession{}, mapSQLError(err)
	}
	session.Request.Metadata = json.RawMessage(metadata)
	return session, nil
}

func scanPaxlDeviceLoginSession(row rowScanner) (PaxlDeviceLoginSession, error) {
	var session PaxlDeviceLoginSession
	if err := row.Scan(
		&session.LoginID,
		&session.UserCode,
		&session.PollTokenHash,
		&session.Status,
		&session.ClientName,
		&session.OwnerUserID,
		&session.UserAPIKeyID,
		&session.NodeID,
		&session.APIKey,
		&session.ExpiresAt,
		&session.CreatedAt,
		&session.ApprovedAt,
		&session.ConsumedAt,
	); err != nil {
		return PaxlDeviceLoginSession{}, mapSQLError(err)
	}
	return session, nil
}

func scanNodes(rows *sql.Rows) ([]Node, error) {
	out := make([]Node, 0)
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
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
	var metadata []byte
	if err := row.Scan(
		&session.ID,
		&session.NodeID,
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
		&session.TokenUsage.Input,
		&session.TokenUsage.Output,
		&session.TokenUsage.Total,
		&session.TokenUsage.CacheRead,
		&session.TokenUsage.CacheWrite,
		&session.TokenUsage.CacheCreation,
		&session.TokenUsage.Reasoning,
		&session.TokenUsage.EstimatedCostUSD,
		&session.TokenUsage.ActualCostUSD,
		&session.TokenUsage.CostUSD,
		&session.Model,
		&session.RunID,
		&session.RunStatus,
		&session.CreatedAt,
		&session.UpdatedAt,
		&metadata,
	); err != nil {
		return AgentSession{}, mapSQLError(err)
	}
	session.TokenInput = session.TokenUsage.Input
	session.TokenOutput = session.TokenUsage.Output
	session.TokenTotal = session.TokenUsage.Total
	_ = json.Unmarshal(roots, &session.WorkspaceRoots)
	session.Metadata = json.RawMessage(metadata)
	session.RuntimeState = runtimeStateFromMetadata(metadata)
	return session, nil
}

func runtimeStateFromMetadata(metadata []byte) *SessionRuntimeState {
	if len(metadata) == 0 || !json.Valid(metadata) {
		return nil
	}
	var object struct {
		RuntimeState *SessionRuntimeState `json:"runtime_state"`
	}
	if err := json.Unmarshal(metadata, &object); err != nil {
		return nil
	}
	return object.RuntimeState
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
	var events []byte
	var fileChanges []byte
	var tokenUsage []byte
	if err := row.Scan(
		&msg.ID,
		&msg.MessageID,
		&msg.UserID,
		&msg.OwnerUserID,
		&msg.NodeID,
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
		&msg.Direction,
		&msg.ParentMessageID,
		&msg.TurnID,
		&msg.ResponseID,
		&events,
		&fileChanges,
		&tokenUsage,
	); err != nil {
		return MailboxMessage{}, mapSQLError(err)
	}
	msg.Payload = json.RawMessage(payload)
	msg.Events = json.RawMessage(events)
	_ = json.Unmarshal(fileChanges, &msg.FileChanges)
	_ = json.Unmarshal(tokenUsage, &msg.TokenUsage)
	return msg, nil
}

func scanApproval(row rowScanner) (AgentApproval, error) {
	var approval AgentApproval
	var requestBody []byte
	var requestedEffects []byte
	var options []byte
	var grantBody []byte
	var rawPayload []byte
	if err := row.Scan(
		&approval.ApprovalID,
		&approval.OwnerUserID,
		&approval.RequestNodeID,
		&approval.RequestAgentID,
		&approval.RequestSessionID,
		&approval.SourceMessageID,
		&approval.GrantNodeID,
		&approval.GrantAgentID,
		&approval.GrantSessionID,
		&approval.Domain,
		&approval.Operation,
		&approval.ResourceType,
		&approval.ResourceRef,
		&approval.Title,
		&approval.Description,
		&approval.RiskLevel,
		&approval.ActionFingerprint,
		&requestBody,
		&requestedEffects,
		&options,
		&approval.Status,
		&approval.Decision,
		&approval.DecisionOption,
		&approval.DecisionScope,
		&grantBody,
		&approval.DecidedByUserID,
		&approval.GrantRevokedAt,
		&approval.GrantRevokedByUserID,
		&approval.GrantRevocationReason,
		&approval.CreatedAt,
		&approval.ExpiresAt,
		&approval.DecidedAt,
		&rawPayload,
	); err != nil {
		return AgentApproval{}, mapSQLError(err)
	}
	approval.RequestBody = json.RawMessage(requestBody)
	approval.RequestedEffects = json.RawMessage(requestedEffects)
	_ = json.Unmarshal(options, &approval.Options)
	approval.GrantBody = json.RawMessage(grantBody)
	approval.RawPayload = json.RawMessage(rawPayload)
	return approval, nil
}

func scanApprovals(rows *sql.Rows) ([]AgentApproval, error) {
	out := make([]AgentApproval, 0)
	for rows.Next() {
		approval, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, approval)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanSecret(row rowScanner) (Secret, error) {
	var secret Secret
	var metadata []byte
	if err := row.Scan(
		&secret.SecretID,
		&secret.OwnerUserID,
		&secret.Name,
		&secret.Kind,
		&secret.Description,
		&metadata,
		&secret.CurrentVersionID,
		&secret.CurrentVersion,
		&secret.CreatedAt,
		&secret.UpdatedAt,
		&secret.DeletedAt,
	); err != nil {
		return Secret{}, mapSQLError(err)
	}
	secret.Metadata = json.RawMessage(metadata)
	return secret, nil
}

func scanSecrets(rows *sql.Rows) ([]Secret, error) {
	out := make([]Secret, 0)
	for rows.Next() {
		secret, err := scanSecret(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, secret)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanSecretVersion(row rowScanner) (SecretVersion, error) {
	var version SecretVersion
	if err := row.Scan(
		&version.VersionID,
		&version.SecretID,
		&version.VersionNumber,
		&version.Ciphertext,
		&version.Nonce,
		&version.KeyID,
		&version.State,
		&version.CreatedAt,
		&version.CreatedByUserID,
		&version.CreatedByNodeID,
		&version.CreatedByAgentID,
		&version.IdempotencyKey,
	); err != nil {
		return SecretVersion{}, mapSQLError(err)
	}
	return version, nil
}

func scanPaxdArtifact(row rowScanner) (PaxdArtifact, error) {
	var artifact PaxdArtifact
	var tags []byte
	if err := row.Scan(
		&artifact.ArtifactID,
		&artifact.Product,
		&artifact.Platform,
		&tags,
		&artifact.Version,
		&artifact.BuildID,
		&artifact.Bucket,
		&artifact.Object,
		&artifact.Generation,
		&artifact.SHA256,
		&artifact.SizeBytes,
		&artifact.ContentType,
		&artifact.CreatedBy,
		&artifact.CreatedAt,
		&artifact.DeletedAt,
	); err != nil {
		return PaxdArtifact{}, mapSQLError(err)
	}
	if len(tags) > 0 {
		if err := json.Unmarshal(tags, &artifact.Tags); err != nil {
			return PaxdArtifact{}, err
		}
	}
	return artifact, nil
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
	if errors.Is(err, gorm.ErrRecordNotFound) {
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
