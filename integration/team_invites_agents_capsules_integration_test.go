//go:build integration

package integration_test

import (
	"net/http"
	"testing"
)

type team struct {
	TeamID      string `json:"team_id"`
	OwnerUserID string `json:"owner_user_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

type teamResponse struct {
	Team team `json:"team"`
}

type teamInvite struct {
	InviteID        string `json:"invite_id"`
	TeamID          string `json:"team_id"`
	Email           string `json:"email"`
	RecipientUserID string `json:"recipient_user_id"`
	Role            string `json:"role"`
	Status          string `json:"status"`
	InvitedByUserID string `json:"invited_by_user_id"`
}

type teamInviteResponse struct {
	Invite teamInvite `json:"invite"`
}

type teamInviteListResponse struct {
	Invites []teamInvite `json:"invites"`
}

type teamAgent struct {
	TeamID           string `json:"team_id"`
	AgentID          string `json:"agent_id"`
	AgentOwnerUserID string `json:"agent_owner_user_id"`
	AgentOwnerEmail  string `json:"agent_owner_email"`
	AddedByUserID    string `json:"added_by_user_id"`
}

type teamAgentResponse struct {
	Agent teamAgent `json:"agent"`
}

type teamAgentListResponse struct {
	Agents []teamAgent `json:"agents"`
}

func TestTeamInviteAgentSharingAndCapsulePermissionsIntegration(t *testing.T) {
	fixture := newIntegrationFixture(t)
	fixture.waitForHealth(t)

	ownerHeaders := fixture.userHeaders()
	operatorHeaders := map[string]string{"X-User-Email": "operator-" + fixture.email}

	ownerNode := createBoundaryNode(t, fixture, ownerHeaders, "team-owner-node")
	ownerAgent := createBoundaryAgent(
		t,
		fixture,
		ownerHeaders,
		ownerNode.NodeID,
		"team-owner-agent",
	)
	ownerSourceSession := "sess-team-owner-source"
	ownerTargetSession := "sess-team-owner-target"
	createBoundarySession(
		t,
		fixture,
		ownerHeaders,
		ownerNode.NodeID,
		ownerAgent.Agent.AgentID,
		ownerSourceSession,
	)
	createBoundarySession(
		t,
		fixture,
		ownerHeaders,
		ownerNode.NodeID,
		ownerAgent.Agent.AgentID,
		ownerTargetSession,
	)
	postJSON[mailboxMessage](
		t,
		fixture,
		nodeSessionMessagesPath(ownerNode.NodeID, ownerAgent.Agent.AgentID, ownerSourceSession),
		map[string]any{
			"message":      "Owner agent has launch checklist context for team capsule handoff.",
			"message_type": "chat",
		},
		ownerHeaders,
		http.StatusOK,
	)

	operatorNode := createBoundaryNode(t, fixture, operatorHeaders, "team-operator-node")
	operatorAgent := createBoundaryAgent(
		t,
		fixture,
		operatorHeaders,
		operatorNode.NodeID,
		"team-operator-agent",
	)
	operatorSourceSession := "sess-team-operator-source"
	operatorTargetSession := "sess-team-operator-target"
	createBoundarySession(
		t,
		fixture,
		operatorHeaders,
		operatorNode.NodeID,
		operatorAgent.Agent.AgentID,
		operatorSourceSession,
	)
	createBoundarySession(
		t,
		fixture,
		operatorHeaders,
		operatorNode.NodeID,
		operatorAgent.Agent.AgentID,
		operatorTargetSession,
	)
	postJSON[mailboxMessage](
		t,
		fixture,
		nodeSessionMessagesPath(
			operatorNode.NodeID,
			operatorAgent.Agent.AgentID,
			operatorSourceSession,
		),
		map[string]any{
			"message":      "Operator agent has deployment checklist context for team capsule handoff.",
			"message_type": "chat",
		},
		operatorHeaders,
		http.StatusOK,
	)

	team := postJSON[teamResponse](
		t,
		fixture,
		"/api/v1/user/self/teams",
		map[string]any{
			"name":        "Integration Team",
			"description": "  Team invite and agent sharing integration scope.  ",
		},
		ownerHeaders,
		http.StatusOK,
	).Team
	requireIntegrationTeam(t, team)

	invite := postJSON[teamInviteResponse](
		t,
		fixture,
		"/api/v1/user/self/teams/"+team.TeamID+"/invites",
		map[string]any{"email": operatorHeaders["X-User-Email"], "role": "operator"},
		ownerHeaders,
		http.StatusOK,
	).Invite
	requireCreatedTeamInvite(t, invite, team.TeamID, operatorHeaders["X-User-Email"])
	ownerSentInvites := getJSON[teamInviteListResponse](
		t,
		fixture,
		"/api/v1/user/self/teams/"+team.TeamID+"/invites",
		ownerHeaders,
		http.StatusOK,
	)
	assertHasTeamInvite(t, ownerSentInvites.Invites, invite.InviteID, "pending")

	operatorPendingInvites := getJSON[teamInviteListResponse](
		t,
		fixture,
		"/api/v1/user/self/team-invites",
		operatorHeaders,
		http.StatusOK,
	)
	assertHasTeamInvite(t, operatorPendingInvites.Invites, invite.InviteID, "pending")

	accepted := postJSON[teamInviteResponse](
		t,
		fixture,
		"/api/v1/user/self/team-invites/"+invite.InviteID+"/accept",
		nil,
		operatorHeaders,
		http.StatusOK,
	).Invite
	requireAcceptedTeamInvite(t, accepted)
	operatorSentInvites := getJSON[teamInviteListResponse](
		t,
		fixture,
		"/api/v1/user/self/teams/"+team.TeamID+"/invites",
		operatorHeaders,
		http.StatusOK,
	)
	assertHasTeamInvite(t, operatorSentInvites.Invites, invite.InviteID, "accepted")

	operatorCapsule := postJSON[knowledgeCapsuleResponse](
		t,
		fixture,
		"/api/v1/user/self/sessions/"+operatorSourceSession+"/knowledge-capsules",
		map[string]any{"keyword": "deployment checklist"},
		operatorHeaders,
		http.StatusOK,
	).Capsule
	fixture.postExpectError(
		t,
		"/api/v1/user/self/sessions/"+ownerTargetSession+"/knowledge-injections",
		map[string]any{"capsule_id": operatorCapsule.CapsuleID},
		operatorHeaders,
		http.StatusNotFound,
	)

	ownerSharedAgent := postJSON[teamAgentResponse](
		t,
		fixture,
		"/api/v1/user/self/teams/"+team.TeamID+"/agents",
		map[string]any{"agent_id": ownerAgent.Agent.AgentID},
		ownerHeaders,
		http.StatusOK,
	).Agent
	requireTeamAgent(t, ownerSharedAgent, team.TeamID, ownerAgent.Agent.AgentID)
	operatorInjection := postJSON[knowledgeInjectionResponse](
		t,
		fixture,
		"/api/v1/user/self/sessions/"+ownerTargetSession+"/knowledge-injections",
		map[string]any{"capsule_id": operatorCapsule.CapsuleID},
		operatorHeaders,
		http.StatusOK,
	)
	requireKnowledgeInjection(
		t,
		operatorInjection.Injection,
		operatorCapsule.CapsuleID,
		ownerTargetSession,
		ownerAgent.Agent.AgentID,
	)

	ownerCapsule := postJSON[knowledgeCapsuleResponse](
		t,
		fixture,
		"/api/v1/user/self/sessions/"+ownerSourceSession+"/knowledge-capsules",
		map[string]any{"keyword": "launch checklist"},
		ownerHeaders,
		http.StatusOK,
	).Capsule
	fixture.postExpectError(
		t,
		"/api/v1/user/self/sessions/"+operatorTargetSession+"/knowledge-injections",
		map[string]any{"capsule_id": ownerCapsule.CapsuleID},
		ownerHeaders,
		http.StatusNotFound,
	)

	operatorSharedAgent := postJSON[teamAgentResponse](
		t,
		fixture,
		"/api/v1/user/self/teams/"+team.TeamID+"/agents",
		map[string]any{"agent_id": operatorAgent.Agent.AgentID},
		operatorHeaders,
		http.StatusOK,
	).Agent
	requireTeamAgent(t, operatorSharedAgent, team.TeamID, operatorAgent.Agent.AgentID)

	teamAgents := getJSON[teamAgentListResponse](
		t,
		fixture,
		"/api/v1/user/self/teams/"+team.TeamID+"/agents",
		ownerHeaders,
		http.StatusOK,
	)
	assertHasTeamAgent(t, teamAgents.Agents, ownerAgent.Agent.AgentID)
	assertHasTeamAgent(t, teamAgents.Agents, operatorAgent.Agent.AgentID)

	ownerInjection := postJSON[knowledgeInjectionResponse](
		t,
		fixture,
		"/api/v1/user/self/sessions/"+operatorTargetSession+"/knowledge-injections",
		map[string]any{"capsule_id": ownerCapsule.CapsuleID},
		ownerHeaders,
		http.StatusOK,
	)
	requireKnowledgeInjection(
		t,
		ownerInjection.Injection,
		ownerCapsule.CapsuleID,
		operatorTargetSession,
		operatorAgent.Agent.AgentID,
	)

	fixture.delete(
		t,
		"/api/v1/user/self/teams/"+team.TeamID+"/agents/"+operatorAgent.Agent.AgentID,
		ownerHeaders,
		http.StatusOK,
	)
	afterRemovalAgents := getJSON[teamAgentListResponse](
		t,
		fixture,
		"/api/v1/user/self/teams/"+team.TeamID+"/agents",
		ownerHeaders,
		http.StatusOK,
	)
	assertHasTeamAgent(t, afterRemovalAgents.Agents, ownerAgent.Agent.AgentID)
	assertMissingTeamAgent(t, afterRemovalAgents.Agents, operatorAgent.Agent.AgentID)
	fixture.postExpectError(
		t,
		"/api/v1/user/self/sessions/"+operatorTargetSession+"/knowledge-injections",
		map[string]any{"capsule_id": ownerCapsule.CapsuleID},
		ownerHeaders,
		http.StatusNotFound,
	)
}

func requireIntegrationTeam(t *testing.T, team team) {
	t.Helper()
	if team.TeamID == "" ||
		team.Name != "Integration Team" ||
		team.Description != "Team invite and agent sharing integration scope." ||
		team.Status != "active" {
		t.Fatalf("unexpected team: %+v", team)
	}
}

func requireCreatedTeamInvite(
	t *testing.T,
	invite teamInvite,
	teamID string,
	email string,
) {
	t.Helper()
	if invite.InviteID == "" ||
		invite.TeamID != teamID ||
		invite.Email != email ||
		invite.Role != "operator" ||
		invite.Status != "pending" {
		t.Fatalf("unexpected invite: %+v", invite)
	}
}

func requireAcceptedTeamInvite(t *testing.T, invite teamInvite) {
	t.Helper()
	if invite.Status != "accepted" ||
		invite.RecipientUserID == "" ||
		invite.Role != "operator" {
		t.Fatalf("unexpected accepted invite: %+v", invite)
	}
}

func requireTeamAgent(t *testing.T, agent teamAgent, teamID string, agentID string) {
	t.Helper()
	if agent.AgentID != agentID || agent.TeamID != teamID {
		t.Fatalf("unexpected team agent: %+v", agent)
	}
}

func requireKnowledgeInjection(
	t *testing.T,
	injection knowledgeInjection,
	capsuleID string,
	targetSessionID string,
	targetAgentID string,
) {
	t.Helper()
	if injection.CapsuleID != capsuleID ||
		injection.TargetSessionID != targetSessionID ||
		injection.TargetAgentID != targetAgentID ||
		injection.Status != "delivered" {
		t.Fatalf("unexpected knowledge injection: %+v", injection)
	}
}

func assertHasTeamInvite(
	t *testing.T,
	invites []teamInvite,
	inviteID string,
	status string,
) {
	t.Helper()
	for _, invite := range invites {
		if invite.InviteID == inviteID {
			if invite.Status != status {
				t.Fatalf("invite %s status = %s, want %s", inviteID, invite.Status, status)
			}
			return
		}
	}
	t.Fatalf("invite %s not found in %+v", inviteID, invites)
}

func assertHasTeamAgent(t *testing.T, agents []teamAgent, agentID string) {
	t.Helper()
	for _, agent := range agents {
		if agent.AgentID == agentID {
			return
		}
	}
	t.Fatalf("team agent %s not found in %+v", agentID, agents)
}

func assertMissingTeamAgent(t *testing.T, agents []teamAgent, agentID string) {
	t.Helper()
	for _, agent := range agents {
		if agent.AgentID == agentID {
			t.Fatalf("team agent %s unexpectedly found in %+v", agentID, agents)
		}
	}
}
