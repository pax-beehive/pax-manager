package storage

import "testing"

func TestReportedAgentStatusUsesOnlineFlagWhenStatusIsEmpty(t *testing.T) {
	if got := reportedAgentStatus(AgentStatusInput{}); got != "offline" {
		t.Fatalf("empty status = %q, want offline", got)
	}
	if got := reportedAgentStatus(AgentStatusInput{Online: true}); got != "online" {
		t.Fatalf("online flag status = %q, want online", got)
	}
	if got := reportedAgentStatus(AgentStatusInput{Status: "degraded", Online: true}); got != "degraded" {
		t.Fatalf("explicit status = %q, want degraded", got)
	}
}
