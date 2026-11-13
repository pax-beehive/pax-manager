package manager

import (
	"context"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryKnownExecutionCompletion(
	ctx context.Context,
	store domain.Store,
	messages []domain.Message,
	agentID, sessionID, turnID string,
) (string, error) {
	if !secretaryExecutionScope(sessionID, turnID) {
		return "", nil
	}
	for _, message := range messages {
		if message.MessageType != "end_turn" || message.AgentID != agentID ||
			message.SessionID != sessionID || message.TurnID != turnID {
			continue
		}
		correlation, prepared, err := secretaryPromptCorrelation(
			ctx, store, message.OwnerUserID, agentID, sessionID, turnID,
		)
		if err != nil {
			return "", err
		}
		if correlation == nil || correlation.Binding == nil {
			continue
		}
		job := secretaryCorrelationJob(*correlation, message.OwnerUserID, agentID)
		if domain.SecretaryExecutionCompletionMatches(*prepared, message, job) {
			return message.MessageID, nil
		}
	}
	return "", nil
}
