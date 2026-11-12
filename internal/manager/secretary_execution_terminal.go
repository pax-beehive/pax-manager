package manager

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/pax-beehive/paxkit/reliablemq"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	secretaryExecutionRequestType = "secretary_execution_request"
	secretaryExecutionFailedType  = "secretary_execution_failed"
)

type secretaryExecutionCorrelation = domain.SecretaryExecutionCorrelation
type secretaryExecutionBinding = domain.SecretaryExecutionBinding

func secretaryExecutionScope(sessionID, turnID string) bool {
	suffix, ok := strings.CutPrefix(sessionID, "sess_secretary_")
	if !ok || len(suffix) != 32 || turnID != "turn_secretary_"+suffix {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}

func secretaryExecutionRequestKey(owner, agentID, sessionID, turnID string) string {
	return "secretary:request:" + owner + ":" + agentID + ":" + sessionID + ":" + turnID
}

// A prepared association proves request identity, not that dispatch succeeded.
func persistSecretaryPromptCorrelation(
	ctx context.Context,
	agent *ACPTunnelAgent,
	sessionID, turnID string,
	requestID json.RawMessage,
	job *domain.SecretaryJob,
) error {
	if !secretaryExecutionScope(sessionID, turnID) {
		return nil
	}
	if agent == nil || agent.store == nil || agent.ownerUserID == "" || agent.agentID == "" ||
		!validSecretaryPromptRequestID(requestID) {
		return domain.ErrConflict
	}
	key := secretaryExecutionRequestKey(agent.ownerUserID, agent.agentID, sessionID, turnID)
	correlation := secretaryExecutionCorrelation{
		Version:   1,
		Method:    "session/prompt",
		RequestID: requestID,
	}
	if job != nil && job.BatchID != "" {
		if job.OwnerUserID != agent.ownerUserID || job.AgentID != agent.agentID ||
			job.SourceID == "" ||
			job.AttemptID == "" ||
			job.RunID != job.AttemptID ||
			domain.SecretaryExecutionRequestMessageID(*job) != acpHistoryMessageID(key) {
			return domain.ErrSecretaryFenced
		}
		correlation.Binding = &secretaryExecutionBinding{
			SourceID:         job.SourceID,
			BatchID:          job.BatchID,
			AttemptID:        job.AttemptID,
			SettingsRevision: job.SettingsRevision,
			RetryGeneration:  job.RetryGeneration,
			RetryEpoch:       job.RetryEpoch,
		}
	}
	raw, _ := json.Marshal(correlation)
	message := domain.Message{MessageID: acpHistoryMessageID(key), LogicalKey: key,
		OwnerUserID: agent.ownerUserID, NodeID: agent.nodeID, AgentID: agent.agentID,
		SessionID: sessionID, TurnID: turnID, Source: domain.MessageSourceACPTunnel,
		Direction: domain.MessageDirectionUserToAgent, Role: "system", Status: "prepared",
		MessageType: secretaryExecutionRequestType, RawJSON: raw}
	return agent.store.UpsertMessage(ctx, &message)
}

func validSecretaryPromptRequestID(raw json.RawMessage) bool {
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

// Only the reliable inbox calls this helper. Session and turn must come from
// its persisted envelope, never a snapshot or the current in-memory waiter.
func persistSecretaryExecutionTerminal(
	ctx context.Context, agent *ACPTunnelAgent, frame reliablemq.Frame,
) error {
	sessionID, turnID := frame.Metadata["manager_session_id"], frame.Metadata["turn_id"]
	if !secretaryReliableTerminalScope(agent, frame) {
		return nil
	}
	var rpc acpJSONRPCMessage
	if json.Unmarshal(frame.Payload, &rpc) != nil || rpc.JSONRPC != "2.0" || rpc.Method != "" ||
		!validSecretaryPromptRequestID(rpc.ID) {
		return nil
	}
	failure := secretaryPromptFailure(rpc)
	if failure == "" {
		return nil
	}
	principal := domain.UserPrincipal{User: domain.User{UserID: agent.ownerUserID}}
	session, err := agent.store.GetSession(ctx, principal, sessionID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if session.AgentID != agent.agentID || session.ArchivedAt != nil ||
		session.Transport == domain.SessionTransportE2EE {
		return nil
	}
	correlation, prepared, err := secretaryPromptCorrelation(
		ctx,
		agent.store,
		agent.ownerUserID,
		agent.agentID,
		sessionID,
		turnID,
	)
	if err != nil || correlation == nil || string(correlation.RequestID) != string(rpc.ID) {
		return err
	}
	terminal := *correlation
	terminal.Failure = failure
	terminal.Origin = "reliable_inbox"
	raw, _ := json.Marshal(terminal)
	key := secretaryExecutionRequestKey(
		agent.ownerUserID,
		agent.agentID,
		sessionID,
		turnID,
	) + ":terminal:" + string(
		rpc.ID,
	)
	message := domain.Message{MessageID: acpHistoryMessageID(key), LogicalKey: key,
		OwnerUserID: agent.ownerUserID, NodeID: agent.nodeID, AgentID: agent.agentID,
		SessionID: sessionID, TurnID: turnID, Source: domain.MessageSourceACPTunnel,
		Direction: domain.MessageDirectionAgentToUser, Role: "system", Status: "failed",
		MessageType: secretaryExecutionFailedType, RawJSON: raw, CreatedAt: frame.CreatedAt}
	job := secretaryCorrelationJob(*correlation, agent.ownerUserID, agent.agentID)
	if correlation.Binding != nil &&
		!domain.SecretaryExecutionTerminalMatches(*prepared, message, job) {
		return nil
	}
	if err := agent.store.UpsertMessage(ctx, &message); err != nil {
		return err
	}
	if terminal.Binding != nil {
		if wake, ok := agent.store.(domain.SecretaryExecutionRecoveryWakeStore); ok {
			return wake.WakeSecretaryExecutionRecovery(ctx, job)
		}
	}
	return nil
}

func secretaryReliableTerminalScope(agent *ACPTunnelAgent, frame reliablemq.Frame) bool {
	return agent != nil && agent.store != nil && agent.ownerUserID != "" && agent.agentID != "" &&
		secretaryExecutionScope(frame.Metadata["manager_session_id"], frame.Metadata["turn_id"]) &&
		frame.Metadata["agent_id"] == agent.agentID && frame.Key.Direction == reliablemq.DirectionInbound &&
		frame.Key.Stream == reliablemq.StreamACP && frame.Key.QueueID == agent.queueID() &&
		frame.Key.Seq > 0 && frame.Kind == reliablemq.FrameKindData
}

func secretaryCorrelationJob(
	correlation secretaryExecutionCorrelation,
	owner, agentID string,
) domain.SecretaryJob {
	job := domain.SecretaryJob{OwnerUserID: owner, AgentID: agentID}
	if binding := correlation.Binding; binding != nil {
		job.SourceID, job.BatchID, job.AttemptID, job.RunID = binding.SourceID, binding.BatchID, binding.AttemptID, binding.AttemptID
		job.SettingsRevision, job.RetryGeneration, job.RetryEpoch = binding.SettingsRevision, binding.RetryGeneration, binding.RetryEpoch
	}
	return job
}

func secretaryPromptFailure(rpc acpJSONRPCMessage) string {
	hasError := len(rpc.Error) > 0 && string(rpc.Error) != "null"
	hasResult := len(rpc.Result) > 0 && string(rpc.Result) != "null"
	if hasError == hasResult {
		return ""
	}
	if hasError {
		var rpcError struct {
			Code    *int    `json:"code"`
			Message *string `json:"message"`
		}
		if json.Unmarshal(rpc.Error, &rpcError) == nil && rpcError.Code != nil &&
			rpcError.Message != nil {
			return "rpc_error"
		}
		return ""
	}
	var result struct {
		StopReason string `json:"stopReason"`
	}
	if json.Unmarshal(rpc.Result, &result) == nil && result.StopReason == "cancelled" {
		return "cancelled"
	}
	return ""
}

func secretaryPromptCorrelation(
	ctx context.Context, store domain.Store, owner, agentID, sessionID, turnID string,
) (*secretaryExecutionCorrelation, *domain.Message, error) {
	key := secretaryExecutionRequestKey(owner, agentID, sessionID, turnID)
	messageID := acpHistoryMessageID(key)
	var before int64
	for {
		page, err := store.ListMessageHistoryPage(ctx, agentID, sessionID, before, 1000)
		if err != nil {
			return nil, nil, err
		}
		for _, message := range page.Messages {
			if message.MessageID != messageID ||
				!secretaryPreparedMessageMatches(message, owner, agentID, sessionID, turnID) {
				continue
			}
			var correlation secretaryExecutionCorrelation
			if json.Unmarshal(message.RawJSON, &correlation) == nil && correlation.Version == 1 &&
				correlation.Method == "session/prompt" && correlation.Failure == "" && validSecretaryPromptRequestID(correlation.RequestID) {
				return &correlation, &message, nil
			}
			return nil, nil, nil
		}
		if !page.HasMore || page.NextBeforeID <= 0 || before > 0 && page.NextBeforeID >= before {
			return nil, nil, nil
		}
		before = page.NextBeforeID
	}
}

func secretaryPreparedMessageMatches(
	message domain.Message,
	owner, agentID, sessionID, turnID string,
) bool {
	return message.OwnerUserID == owner && message.AgentID == agentID &&
		message.SessionID == sessionID &&
		message.TurnID == turnID &&
		message.MessageType == secretaryExecutionRequestType &&
		message.Status == "prepared" &&
		message.Role == "system" &&
		message.Source == domain.MessageSourceACPTunnel &&
		message.Direction == domain.MessageDirectionUserToAgent
}

func secretaryKnownExecutionFailure(
	ctx context.Context,
	store domain.Store,
	messages []domain.Message,
	agentID, sessionID, turnID string,
) error {
	if !secretaryExecutionScope(sessionID, turnID) {
		return nil
	}
	for _, message := range messages {
		if !secretaryFailedMessageMatches(message, agentID, sessionID, turnID) {
			continue
		}
		var terminal secretaryExecutionCorrelation
		if json.Unmarshal(message.RawJSON, &terminal) != nil || terminal.Version != 1 ||
			terminal.Method != "session/prompt" ||
			terminal.Origin != "reliable_inbox" ||
			(terminal.Failure != "rpc_error" && terminal.Failure != "cancelled") {
			continue
		}
		correlation, prepared, err := secretaryPromptCorrelation(
			ctx,
			store,
			message.OwnerUserID,
			agentID,
			sessionID,
			turnID,
		)
		if err != nil {
			return err
		}
		key := secretaryExecutionRequestKey(
			message.OwnerUserID,
			agentID,
			sessionID,
			turnID,
		) + ":terminal:" + string(
			terminal.RequestID,
		)
		if correlation != nil && string(correlation.RequestID) == string(terminal.RequestID) &&
			message.MessageID == acpHistoryMessageID(key) {
			if correlation.Binding != nil &&
				!domain.SecretaryExecutionTerminalMatches(*prepared, message,
					secretaryCorrelationJob(*correlation, message.OwnerUserID, agentID)) {
				continue
			}
			if correlation.Binding == nil && terminal.Binding != nil {
				continue
			}
			return domain.ErrSecretaryExecutionFailed
		}
	}
	return nil
}

func secretaryFailedMessageMatches(message domain.Message, agentID, sessionID, turnID string) bool {
	return message.MessageType == secretaryExecutionFailedType && message.Status == "failed" &&
		message.Role == "system" &&
		message.TurnID == turnID &&
		message.SessionID == sessionID &&
		message.AgentID == agentID &&
		message.OwnerUserID != "" &&
		message.Source == domain.MessageSourceACPTunnel &&
		message.Direction == domain.MessageDirectionAgentToUser
}
