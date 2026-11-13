package domain

import "encoding/json"

// SecretaryExecutionCompletionMatches recognizes a persisted native response
// only when it belongs to the exact Manager-prepared dispatch.
func SecretaryExecutionCompletionMatches(request, completion Message, job SecretaryJob) bool {
	if !secretaryExecutionRequestMatches(request, job) ||
		!secretaryExecutionCompletionMetadataMatches(request, completion) {
		return false
	}
	var prepared SecretaryExecutionCorrelation
	if json.Unmarshal(request.RawJSON, &prepared) != nil || prepared.Version != 1 ||
		prepared.Method != "session/prompt" || prepared.Failure != "" || prepared.Origin != "" ||
		!validSecretaryExecutionRequestID(prepared.RequestID) || prepared.Binding == nil {
		return false
	}
	want := SecretaryExecutionBinding{
		SourceID: job.SourceID, BatchID: job.BatchID, AttemptID: job.AttemptID,
		SettingsRevision: job.SettingsRevision, RetryGeneration: job.RetryGeneration,
		RetryEpoch: job.RetryEpoch,
	}
	return *prepared.Binding == want &&
		secretaryExecutionSuccessfulResponse(completion.RawJSON, prepared.RequestID)
}

func secretaryExecutionCompletionMetadataMatches(request, completion Message) bool {
	return completion.MessageID != "" && completion.MessageType == "end_turn" &&
		completion.Status == "received" && completion.Role == "assistant" &&
		completion.Source == MessageSourceACPTunnel &&
		completion.Direction == MessageDirectionAgentToUser &&
		completion.OwnerUserID == request.OwnerUserID && completion.AgentID == request.AgentID &&
		completion.SessionID == request.SessionID && completion.TurnID == request.TurnID
}

func secretaryExecutionSuccessfulResponse(raw, requestID json.RawMessage) bool {
	if len(raw) > 64*1024 {
		return false
	}
	var rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Error   json.RawMessage `json:"error"`
		Result  struct {
			StopReason string `json:"stopReason"`
		} `json:"result"`
	}
	return json.Unmarshal(raw, &rpc) == nil && rpc.JSONRPC == "2.0" && rpc.Method == "" &&
		(len(rpc.Error) == 0 || string(rpc.Error) == "null") &&
		string(rpc.ID) == string(requestID) && rpc.Result.StopReason == "end_turn"
}
