package manager

import (
	"context"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

func secretaryConversationMCPServers(session domain.AgentSession, capability string) []any {
	servers := agentConversationMCPServers(session)
	if capability == "" {
		return servers
	}
	for _, entry := range servers {
		server := entry.(map[string]any)
		env := server["env"].([]map[string]string)
		server["env"] = append(
			env,
			map[string]string{"name": "PAX_SECRETARY_CAPABILITY", "value": capability},
		)
	}
	return servers
}

// finishSecretaryExecution also validates receipts when recovering a completed turn.
func (s *Service) finishSecretaryExecution(
	ctx context.Context, request secretary.ExecutionRequest, result secretary.ExecutionResult,
) (secretary.ExecutionResult, error) {
	if request.Job.SourceID == "" {
		return result, nil
	}
	refs, err := secretary.RequestedRelatedSources(result.Text)
	if err != nil {
		return result, err
	}
	result.Reads.Messages = nil
	for _, ref := range refs {
		read, readErr := s.workspaceSearch.VerifyReadReceipt(
			ctx,
			request.OwnerID,
			ref,
			ref.ReadReceipt,
		)
		if readErr != nil {
			return result, readErr
		}
		result.Reads.Messages = append(result.Reads.Messages, domain.SecretaryReadMessage{
			Ref: ref, Text: read.Text, View: read.View, ReadReceipt: read.ReadReceipt,
		})
	}
	return result, nil
}
