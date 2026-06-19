package storage

func reportedAgentStatus(input AgentStatusInput) string {
	if input.Status != "" {
		return input.Status
	}
	if input.Online {
		return "online"
	}
	return "offline"
}
