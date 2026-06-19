//go:build integration

package integration_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

type knowledgeCapsule struct {
	CapsuleID       string `json:"capsule_id"`
	OwnerUserID     string `json:"owner_user_id"`
	SourceSessionID string `json:"source_session_id"`
	SourceAgentID   string `json:"source_agent_id"`
	Keyword         string `json:"keyword"`
	Title           string `json:"title"`
	Summary         string `json:"summary"`
	Content         string `json:"content"`
	Status          string `json:"status"`
	Truncated       bool   `json:"truncated"`
}

type knowledgeCapsuleResponse struct {
	Capsule knowledgeCapsule `json:"capsule"`
}

type knowledgeCapsuleListResponse struct {
	Capsules []knowledgeCapsule `json:"capsules"`
}

type knowledgeInjection struct {
	InjectionID         string     `json:"injection_id"`
	CapsuleID           string     `json:"capsule_id"`
	TargetSessionID     string     `json:"target_session_id"`
	TargetAgentID       string     `json:"target_agent_id"`
	DeliveryMethod      string     `json:"delivery_method"`
	DeliveryMessageID   string     `json:"delivery_message_id"`
	DeliveryMessageType string     `json:"delivery_message_type"`
	Status              string     `json:"status"`
	DeliveredAt         *time.Time `json:"delivered_at"`
}

type knowledgeInjectionResponse struct {
	Injection knowledgeInjection `json:"injection"`
	Message   mailboxMessage     `json:"message"`
}

type knowledgeInjectionListResponse struct {
	Injections []knowledgeInjection `json:"injections"`
}

func TestKnowledgeCapsuleLifecycleAndTenantBoundaryIntegration(t *testing.T) {
	fixture := newIntegrationFixture(t)
	fixture.waitForHealth(t)

	ownerHeaders := fixture.userHeaders()
	ownerNode := createBoundaryNode(t, fixture, ownerHeaders, "knowledge-capsule-node")
	ownerAgent := createBoundaryAgent(
		t,
		fixture,
		ownerHeaders,
		ownerNode.NodeID,
		"knowledge-capsule-agent",
	)
	sourceSessionID := "sess-knowledge-source"
	targetSessionID := "sess-knowledge-target"
	createBoundarySession(
		t,
		fixture,
		ownerHeaders,
		ownerNode.NodeID,
		ownerAgent.Agent.AgentID,
		sourceSessionID,
	)
	createBoundarySession(
		t,
		fixture,
		ownerHeaders,
		ownerNode.NodeID,
		ownerAgent.Agent.AgentID,
		targetSessionID,
	)
	postJSON[mailboxMessage](
		t,
		fixture,
		nodeSessionMessagesPath(ownerNode.NodeID, ownerAgent.Agent.AgentID, sourceSessionID),
		map[string]any{
			"message":      "Knowledge capsule handoff should preserve capability injection context token=secret123.",
			"message_type": "chat",
		},
		ownerHeaders,
		http.StatusOK,
	)

	created := postJSON[knowledgeCapsuleResponse](
		t,
		fixture,
		"/api/v1/user/self/sessions/"+sourceSessionID+"/knowledge-capsules",
		map[string]any{"keyword": "capability injection"},
		ownerHeaders,
		http.StatusOK,
	).Capsule
	if created.CapsuleID == "" ||
		created.SourceSessionID != sourceSessionID ||
		created.SourceAgentID != ownerAgent.Agent.AgentID ||
		created.Keyword != "capability injection" ||
		created.Status != "active" {
		t.Fatalf("unexpected created capsule: %+v", created)
	}
	if !strings.Contains(created.Content, "capability injection") ||
		strings.Contains(created.Content, "secret123") {
		t.Fatalf("capsule content was not extracted/redacted correctly: %q", created.Content)
	}

	listed := getJSON[knowledgeCapsuleListResponse](
		t,
		fixture,
		"/api/v1/user/self/knowledge-capsules?keyword=capability%20injection",
		ownerHeaders,
		http.StatusOK,
	)
	assertHasCapsule(t, listed.Capsules, created.CapsuleID)

	got := getJSON[knowledgeCapsuleResponse](
		t,
		fixture,
		"/api/v1/user/self/knowledge-capsules/"+created.CapsuleID,
		ownerHeaders,
		http.StatusOK,
	).Capsule
	if got.CapsuleID != created.CapsuleID {
		t.Fatalf("get capsule = %+v, want %s", got, created.CapsuleID)
	}

	injected := postJSON[knowledgeInjectionResponse](
		t,
		fixture,
		"/api/v1/user/self/sessions/"+targetSessionID+"/knowledge-injections",
		map[string]any{"capsule_id": created.CapsuleID},
		ownerHeaders,
		http.StatusOK,
	)
	if injected.Injection.InjectionID == "" ||
		injected.Injection.CapsuleID != created.CapsuleID ||
		injected.Injection.TargetSessionID != targetSessionID ||
		injected.Injection.DeliveryMethod != "mailbox_steer" ||
		injected.Injection.DeliveryMessageType != "system_handoff" ||
		injected.Injection.Status != "delivered" ||
		injected.Injection.DeliveredAt == nil {
		t.Fatalf("unexpected injection: %+v", injected.Injection)
	}
	if injected.Message.MessageID == "" ||
		injected.Message.MessageType != "system_handoff" ||
		!strings.Contains(injected.Message.Message, "system_handoff") {
		t.Fatalf("unexpected handoff mailbox message: %+v", injected.Message)
	}

	injections := getJSON[knowledgeInjectionListResponse](
		t,
		fixture,
		"/api/v1/user/self/sessions/"+targetSessionID+"/knowledge-injections",
		ownerHeaders,
		http.StatusOK,
	)
	assertHasInjection(t, injections.Injections, injected.Injection.InjectionID)

	otherHeaders := map[string]string{"X-User-Email": "other-" + fixture.email}
	otherNode := createBoundaryNode(t, fixture, otherHeaders, "knowledge-capsule-other-node")
	otherAgent := createBoundaryAgent(
		t,
		fixture,
		otherHeaders,
		otherNode.NodeID,
		"knowledge-capsule-other-agent",
	)
	otherSessionID := "sess-knowledge-other"
	createBoundarySession(
		t,
		fixture,
		otherHeaders,
		otherNode.NodeID,
		otherAgent.Agent.AgentID,
		otherSessionID,
	)

	otherList := getJSON[knowledgeCapsuleListResponse](
		t,
		fixture,
		"/api/v1/user/self/knowledge-capsules",
		otherHeaders,
		http.StatusOK,
	)
	if len(otherList.Capsules) != 0 {
		t.Fatalf("other tenant saw capsules: %+v", otherList.Capsules)
	}
	fixture.getExpectError(
		t,
		"/api/v1/user/self/knowledge-capsules/"+created.CapsuleID,
		otherHeaders,
		http.StatusNotFound,
	)
	fixture.postExpectError(
		t,
		"/api/v1/user/self/sessions/"+otherSessionID+"/knowledge-injections",
		map[string]any{"capsule_id": created.CapsuleID},
		otherHeaders,
		http.StatusNotFound,
	)
}

func assertHasCapsule(t *testing.T, capsules []knowledgeCapsule, capsuleID string) {
	t.Helper()
	for _, capsule := range capsules {
		if capsule.CapsuleID == capsuleID {
			return
		}
	}
	t.Fatalf("capsule %s not found in %+v", capsuleID, capsules)
}

func assertHasInjection(t *testing.T, injections []knowledgeInjection, injectionID string) {
	t.Helper()
	for _, injection := range injections {
		if injection.InjectionID == injectionID {
			return
		}
	}
	t.Fatalf("injection %s not found in %+v", injectionID, injections)
}
