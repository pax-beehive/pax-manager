package manager

import (
	"encoding/json"
	"strings"
	"time"

	hzapi "github.com/pax-beehive/pax-manager/internal/transport/http/model/paxmanager/api"
)

func registerAgentRequest(req *hzapi.RegisterAgentRequest) RegisterAgentRequest {
	return RegisterAgentRequest{
		Name:          req.GetName(),
		AgentType:     req.GetAgentType(),
		Hostname:      req.GetHostname(),
		MachineType:   req.GetMachineType(),
		OS:            req.GetOs(),
		HermesVersion: req.GetHermesVersion(),
		APIEndpoint:   req.GetAPIEndpoint(),
		Metadata:      json.RawMessage(req.GetMetadata()),
	}
}

func agentStatusReport(req *hzapi.AgentStatusReportRequest) AgentStatusReport {
	report := AgentStatusReport{
		AgentID:  req.GetAgentID(),
		Hostname: req.GetHostname(),
		Sessions: sessionStatusInputs(req.GetSessions()),
		System:   json.RawMessage(req.GetSystem()),
	}
	if ts, ok := parseOptionalTime(req.GetTimestamp()); ok {
		report.Timestamp = ts
	}
	return report
}

func sessionStatusInputs(inputs []*hzapi.SessionStatusInput) []SessionStatusInput {
	if len(inputs) == 0 {
		return nil
	}
	out := make([]SessionStatusInput, 0, len(inputs))
	for _, input := range inputs {
		var lastMessageAt *time.Time
		if ts, ok := parseOptionalTime(input.GetLastMessageAt()); ok {
			lastMessageAt = &ts
		}
		out = append(out, SessionStatusInput{
			SessionID:      input.GetSessionID(),
			AgentType:      input.GetAgentType(),
			NativeID:       input.GetNativeID(),
			SessionName:    input.GetName(),
			ProjectID:      input.GetProjectID(),
			Preview:        input.GetPreview(),
			WorkspaceRoots: input.GetWorkspaceRoots(),
			Source:         input.GetSource(),
			Status:         input.GetStatus(),
			CurrentTask:    input.GetCurrentTask(),
			LastMessageAt:  lastMessageAt,
			MessageCount:   int(input.GetMessageCount()),
			TokenUsage:     tokenUsage(input.GetTokenUsage()),
			Model:          input.GetModel(),
			RunID:          input.GetRunID(),
			RunStatus:      input.GetRunStatus(),
		})
	}
	return out
}

func tokenUsage(input *hzapi.TokenUsage) TokenUsage {
	return TokenUsage{
		Input:  input.GetInputTokens(),
		Output: input.GetOutputTokens(),
		Total:  input.GetTotalTokens(),
	}
}

func messageResultRequest(req *hzapi.ReportMessageResultRequest) MessageResultRequest {
	var completedAt *time.Time
	if ts, ok := parseOptionalTime(req.GetCompletedAt()); ok {
		completedAt = &ts
	}
	return MessageResultRequest{
		MessageID:   req.GetMessageID(),
		Status:      req.GetStatus(),
		Result:      req.GetResult(),
		Error:       req.GetError(),
		CompletedAt: completedAt,
	}
}

func createMailboxRequest(req *hzapi.CreateMailboxRequest) CreateMailboxRequest {
	return CreateMailboxRequest{
		AgentID:     req.GetAgentID(),
		SessionID:   req.GetSessionID(),
		Message:     req.GetMessage(),
		MessageType: req.GetMessageType(),
		Payload:     json.RawMessage(req.GetPayload()),
	}
}

func createRegistrationTokenRequest(
	req *hzapi.CreateRegistrationTokenRequest,
) CreateRegistrationTokenRequest {
	return CreateRegistrationTokenRequest{
		OwnerUserID:      req.GetOwnerUserID(),
		OwnerEmail:       req.GetOwnerEmail(),
		ExpiresInSeconds: req.GetExpiresInSeconds(),
	}
}

func parseOptionalTime(value string) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}
