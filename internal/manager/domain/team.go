package domain

import (
	"encoding/json"
	"time"
)

const (
	TeamStatusActive   = "active"
	TeamStatusArchived = "archived"

	TeamRoleOwner    = "owner"
	TeamRoleOperator = "operator"
	TeamRoleMember   = "member"

	TeamAgentRoleGeneral = "general"

	TeamMemberStatusActive  = "active"
	TeamMemberStatusRemoved = "removed"

	TeamInviteStatusPending  = "pending"
	TeamInviteStatusAccepted = "accepted"
	TeamInviteStatusDeclined = "declined"
	TeamInviteStatusCanceled = "canceled"

	TeamAuditActionTeamCreated       = "team.created"
	TeamAuditActionTeamArchived      = "team.archived"
	TeamAuditActionInviteCreated     = "invite.created"
	TeamAuditActionInviteAccepted    = "invite.accepted"
	TeamAuditActionInviteDeclined    = "invite.declined"
	TeamAuditActionInviteCanceled    = "invite.canceled"
	TeamAuditActionMemberRoleUpdated = "member.role_updated"
	TeamAuditActionMemberRemoved     = "member.removed"
	TeamAuditActionAgentAdded        = "agent.added"
	TeamAuditActionAgentRemoved      = "agent.removed"
)

type Team struct {
	TeamID      string     `json:"team_id"`
	OwnerUserID string     `json:"owner_user_id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
}

type TeamSummary struct {
	Team
	MyRole      string `json:"my_role"`
	MemberCount int    `json:"member_count"`
	AgentCount  int    `json:"agent_count"`
}

type TeamMember struct {
	TeamID        string     `json:"team_id"`
	UserID        string     `json:"user_id"`
	Email         string     `json:"email,omitempty"`
	Role          string     `json:"role"`
	Status        string     `json:"status"`
	InvitedByUser string     `json:"invited_by_user_id,omitempty"`
	JoinedAt      time.Time  `json:"joined_at"`
	RemovedAt     *time.Time `json:"removed_at,omitempty"`
	RemovedByUser string     `json:"removed_by_user_id,omitempty"`
}

type TeamInvite struct {
	InviteID        string     `json:"invite_id"`
	TeamID          string     `json:"team_id"`
	Email           string     `json:"email"`
	RecipientUserID string     `json:"recipient_user_id,omitempty"`
	Role            string     `json:"role"`
	Status          string     `json:"status"`
	InvitedByUserID string     `json:"invited_by_user_id"`
	CreatedAt       time.Time  `json:"created_at"`
	AcceptedAt      *time.Time `json:"accepted_at,omitempty"`
	DeclinedAt      *time.Time `json:"declined_at,omitempty"`
	CanceledAt      *time.Time `json:"canceled_at,omitempty"`
}

type TeamAgent struct {
	TeamID           string          `json:"team_id"`
	AgentID          string          `json:"agent_id"`
	AgentOwnerUserID string          `json:"agent_owner_user_id"`
	AgentOwnerEmail  string          `json:"agent_owner_email,omitempty"`
	Identity         string          `json:"identity"`
	Role             string          `json:"role"`
	DisplayName      string          `json:"display_name,omitempty"`
	Description      string          `json:"description,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	AddedByUserID    string          `json:"added_by_user_id"`
	AddedAt          time.Time       `json:"added_at"`
	RemovedAt        *time.Time      `json:"removed_at,omitempty"`
	RemovedByUserID  string          `json:"removed_by_user_id,omitempty"`
	Agent            *Agent          `json:"agent,omitempty"`
}

type TeamAuditEvent struct {
	EventID        string          `json:"event_id"`
	TeamID         string          `json:"team_id"`
	ActorUserID    string          `json:"actor_user_id"`
	Action         string          `json:"action"`
	TargetUserID   string          `json:"target_user_id,omitempty"`
	TargetAgentID  string          `json:"target_agent_id,omitempty"`
	TargetInviteID string          `json:"target_invite_id,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

type CreateTeamRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type CreateTeamInviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role,omitempty"`
}

type AddTeamAgentRequest struct {
	AgentID     string          `json:"agent_id"`
	Identity    string          `json:"identity,omitempty"`
	Role        string          `json:"role,omitempty"`
	DisplayName string          `json:"display_name,omitempty"`
	Description string          `json:"description,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

type UpdateTeamMemberRoleRequest struct {
	Role string `json:"role"`
}
