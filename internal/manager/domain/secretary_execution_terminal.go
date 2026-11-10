package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// SecretaryExecutionCorrelation contains only Manager-owned dispatch identity.
type SecretaryExecutionCorrelation struct {
	Version   int                        `json:"version"`
	Method    string                     `json:"method"`
	RequestID json.RawMessage            `json:"request_id"`
	Failure   string                     `json:"failure,omitempty"`
	Origin    string                     `json:"origin,omitempty"`
	Binding   *SecretaryExecutionBinding `json:"binding,omitempty"`
}

type SecretaryExecutionBinding struct {
	SourceID         string `json:"source_id"`
	BatchID          string `json:"batch_id"`
	AttemptID        string `json:"attempt_id"`
	SettingsRevision int64  `json:"settings_revision"`
	RetryGeneration  int64  `json:"retry_generation"`
	RetryEpoch       int64  `json:"retry_epoch"`
}

func SecretaryExecutionRequestMessageID(job SecretaryJob) string {
	if job.OwnerUserID == "" || job.AgentID == "" || job.AttemptID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(job.OwnerUserID + "\x00" + job.AttemptID))
	suffix := hex.EncodeToString(sum[:16])
	return secretaryExecutionMessageID(secretaryExecutionMessageKey(job.OwnerUserID, job.AgentID,
		SecretaryInternalSessionPrefix+suffix, "turn_secretary_"+suffix))
}

func SecretaryExecutionTerminalMessageID(request Message) string {
	var correlation SecretaryExecutionCorrelation
	if json.Unmarshal(request.RawJSON, &correlation) != nil || correlation.Version != 1 ||
		correlation.Method != "session/prompt" || !validSecretaryExecutionRequestID(correlation.RequestID) {
		return ""
	}
	key := secretaryExecutionMessageKey(
		request.OwnerUserID,
		request.AgentID,
		request.SessionID,
		request.TurnID,
	)
	return secretaryExecutionMessageID(key + ":terminal:" + string(correlation.RequestID))
}

func SecretaryExecutionTerminalMatches(request, terminal Message, job SecretaryJob) bool {
	if !secretaryExecutionRequestMatches(request, job) ||
		!secretaryExecutionFailureMatches(request, terminal) {
		return false
	}
	var prepared, failed SecretaryExecutionCorrelation
	if json.Unmarshal(request.RawJSON, &prepared) != nil ||
		json.Unmarshal(terminal.RawJSON, &failed) != nil ||
		prepared.Version != 1 ||
		prepared.Method != "session/prompt" ||
		prepared.Failure != "" ||
		prepared.Origin != "" ||
		failed.Version != 1 ||
		failed.Method != "session/prompt" ||
		failed.Origin != "reliable_inbox" ||
		(failed.Failure != "cancelled" && failed.Failure != "rpc_error") ||
		string(failed.RequestID) != string(prepared.RequestID) {
		return false
	}
	want := SecretaryExecutionBinding{
		SourceID:         job.SourceID,
		BatchID:          job.BatchID,
		AttemptID:        job.AttemptID,
		SettingsRevision: job.SettingsRevision,
		RetryGeneration:  job.RetryGeneration,
		RetryEpoch:       job.RetryEpoch,
	}
	return prepared.Binding != nil && failed.Binding != nil && *prepared.Binding == want &&
		*failed.Binding == want
}

func secretaryExecutionRequestMatches(request Message, job SecretaryJob) bool {
	sum := sha256.Sum256([]byte(job.OwnerUserID + "\x00" + job.AttemptID))
	suffix := hex.EncodeToString(sum[:16])
	return job.SourceID != "" && job.BatchID != "" && job.AttemptID != "" &&
		request.SessionID == SecretaryInternalSessionPrefix+suffix && request.TurnID == "turn_secretary_"+suffix &&
		request.MessageID == SecretaryExecutionRequestMessageID(job) &&
		request.MessageType == "secretary_execution_request" && request.Status == "prepared" && request.Role == "system" &&
		request.Source == MessageSourceACPTunnel && request.Direction == MessageDirectionUserToAgent &&
		request.OwnerUserID == job.OwnerUserID && request.AgentID == job.AgentID
}

func secretaryExecutionFailureMatches(request, terminal Message) bool {
	return terminal.MessageID != "" &&
		terminal.MessageID == SecretaryExecutionTerminalMessageID(request) &&
		terminal.MessageType == "secretary_execution_failed" &&
		terminal.Status == "failed" &&
		terminal.Role == "system" &&
		terminal.Source == MessageSourceACPTunnel &&
		terminal.Direction == MessageDirectionAgentToUser &&
		terminal.OwnerUserID == request.OwnerUserID &&
		terminal.AgentID == request.AgentID &&
		terminal.SessionID == request.SessionID &&
		terminal.TurnID == request.TurnID
}

func validSecretaryExecutionRequestID(raw json.RawMessage) bool {
	if len(raw) == 0 || len(raw) > 128 || string(raw) == "null" {
		return false
	}
	var number int64
	if json.Unmarshal(raw, &number) == nil {
		return number > 0
	}
	var text string
	return json.Unmarshal(raw, &text) == nil && text != ""
}

func secretaryExecutionMessageKey(owner, agentID, sessionID, turnID string) string {
	return "secretary:request:" + owner + ":" + agentID + ":" + sessionID + ":" + turnID
}

func secretaryExecutionMessageID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "msg_" + hex.EncodeToString(sum[:24])
}
