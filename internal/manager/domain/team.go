package domain

import "time"

const (
	TeamStatusActive   = "active"
	TeamStatusArchived = "archived"

	TeamRoleOwner    = "owner"
	TeamRoleOperator = "operator"
	TeamRoleMember   = "member"

	TeamMemberStatusActive  = "active"
	TeamMemberStatusRemoved = "removed"

	TeamInviteStatusPending  = "pending"
	TeamInviteStatusAccepted = "accepted"
	TeamInviteStatusDeclined = "declined"
)

type Team struct {
	TeamID      string     `json:"team_id"`
	OwnerUserID string     `json:"owner_user_id"`
	Name        string     `json:"name"`
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
}

type TeamAgent struct {
	TeamID           string     `json:"team_id"`
	AgentID          string     `json:"agent_id"`
	AgentOwnerUserID string     `json:"agent_owner_user_id"`
	AddedByUserID    string     `json:"added_by_user_id"`
	AddedAt          time.Time  `json:"added_at"`
	RemovedAt        *time.Time `json:"removed_at,omitempty"`
	RemovedByUserID  string     `json:"removed_by_user_id,omitempty"`
	Agent            *Agent     `json:"agent,omitempty"`
}

type CreateTeamRequest struct {
	Name string `json:"name"`
}

type CreateTeamInviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role,omitempty"`
}

type AddTeamAgentRequest struct {
	AgentID string `json:"agent_id"`
}
