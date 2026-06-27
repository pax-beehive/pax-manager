package storage

import (
	"context"
	"sort"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) CreateTeam(ctx context.Context, team Team, owner TeamMember) (Team, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[team.OwnerUserID]; !ok {
		return Team{}, ErrNotFound
	}
	if _, ok := s.teams[team.TeamID]; ok {
		return Team{}, ErrConflict
	}
	if team.CreatedAt.IsZero() {
		team.CreatedAt = s.now().UTC()
	}
	if team.Status == "" {
		team.Status = domain.TeamStatusActive
	}
	owner.TeamID = team.TeamID
	owner.UserID = team.OwnerUserID
	owner.Role = domain.TeamRoleOwner
	owner.Status = domain.TeamMemberStatusActive
	if owner.JoinedAt.IsZero() {
		owner.JoinedAt = team.CreatedAt
	}
	s.teams[team.TeamID] = team
	s.teamMembers[teamMemberKey{TeamID: team.TeamID, UserID: team.OwnerUserID}] = owner
	return team, nil
}

func (s *MemoryStore) ListTeams(
	ctx context.Context,
	principal UserPrincipal,
) ([]TeamSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TeamSummary, 0)
	for _, member := range s.teamMembers {
		if member.UserID != principal.User.UserID ||
			member.Status != domain.TeamMemberStatusActive {
			continue
		}
		team, ok := s.teams[member.TeamID]
		if !ok || team.Status != domain.TeamStatusActive {
			continue
		}
		out = append(out, TeamSummary{
			Team:        team,
			MyRole:      member.Role,
			MemberCount: s.activeTeamMemberCountLocked(team.TeamID),
			AgentCount:  s.activeTeamAgentCountLocked(team.TeamID),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *MemoryStore) GetTeam(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
) (Team, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.activeTeamMemberLocked(teamID, principal.User.UserID); !ok {
		return Team{}, ErrNotFound
	}
	team, ok := s.teams[teamID]
	if !ok || team.Status != domain.TeamStatusActive {
		return Team{}, ErrNotFound
	}
	return team, nil
}

func (s *MemoryStore) ListTeamMembers(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
) ([]TeamMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.activeTeamMemberLocked(teamID, principal.User.UserID); !ok {
		return nil, ErrNotFound
	}
	out := make([]TeamMember, 0)
	for _, member := range s.teamMembers {
		if member.TeamID != teamID || member.Status != domain.TeamMemberStatusActive {
			continue
		}
		if user, ok := s.users[member.UserID]; ok {
			member.Email = user.Email
		}
		out = append(out, member)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].JoinedAt.Before(out[j].JoinedAt)
	})
	return out, nil
}

func (s *MemoryStore) ListTeamAgents(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
) ([]TeamAgent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.activeTeamMemberLocked(teamID, principal.User.UserID); !ok {
		return nil, ErrNotFound
	}
	out := make([]TeamAgent, 0)
	for _, teamAgent := range s.teamAgents {
		if teamAgent.TeamID != teamID || teamAgent.RemovedAt != nil {
			continue
		}
		if agent, ok := s.agents[teamAgent.AgentID]; ok {
			agentCopy := agent
			teamAgent.Agent = &agentCopy
		}
		out = append(out, teamAgent)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].AddedAt.Before(out[j].AddedAt)
	})
	return out, nil
}

func (s *MemoryStore) CreateTeamInvite(
	ctx context.Context,
	principal UserPrincipal,
	invite TeamInvite,
) (TeamInvite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	member, ok := s.activeTeamMemberLocked(invite.TeamID, principal.User.UserID)
	if !ok || member.Role != domain.TeamRoleOwner {
		return TeamInvite{}, ErrUnauthorized
	}
	if invite.Role == domain.TeamRoleOwner || invite.Role == "" {
		return TeamInvite{}, ErrUnauthorized
	}
	recipientEmail := normalizeEmail(invite.Email)
	for _, existing := range s.teamInvites {
		if existing.TeamID == invite.TeamID &&
			existing.Status == domain.TeamInviteStatusPending &&
			normalizeEmail(existing.Email) == recipientEmail {
			return TeamInvite{}, ErrConflict
		}
	}
	if invite.RecipientUserID != "" {
		if active, ok := s.activeTeamMemberLocked(invite.TeamID, invite.RecipientUserID); ok &&
			active.UserID != "" {
			return TeamInvite{}, ErrConflict
		}
	}
	if invite.CreatedAt.IsZero() {
		invite.CreatedAt = s.now().UTC()
	}
	invite.Email = recipientEmail
	invite.Status = domain.TeamInviteStatusPending
	invite.InvitedByUserID = principal.User.UserID
	s.teamInvites[invite.InviteID] = invite
	return invite, nil
}

func (s *MemoryStore) ListTeamInvites(
	ctx context.Context,
	principal UserPrincipal,
) ([]TeamInvite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	email := normalizeEmail(principal.User.Email)
	out := make([]TeamInvite, 0)
	for _, invite := range s.teamInvites {
		if invite.Status != domain.TeamInviteStatusPending {
			continue
		}
		if invite.RecipientUserID == principal.User.UserID ||
			(invite.RecipientUserID == "" && normalizeEmail(invite.Email) == email) {
			out = append(out, invite)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *MemoryStore) AcceptTeamInvite(
	ctx context.Context,
	principal UserPrincipal,
	inviteID string,
	acceptedAt time.Time,
) (TeamInvite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	invite, ok := s.teamInvites[inviteID]
	if !ok || !teamInviteVisibleToPrincipal(invite, principal) ||
		invite.Status != domain.TeamInviteStatusPending {
		return TeamInvite{}, ErrNotFound
	}
	if _, ok := s.activeTeamMemberLocked(invite.TeamID, principal.User.UserID); ok {
		return TeamInvite{}, ErrConflict
	}
	invite.Status = domain.TeamInviteStatusAccepted
	invite.RecipientUserID = principal.User.UserID
	invite.AcceptedAt = &acceptedAt
	s.teamInvites[inviteID] = invite
	s.teamMembers[teamMemberKey{TeamID: invite.TeamID, UserID: principal.User.UserID}] = TeamMember{
		TeamID:        invite.TeamID,
		UserID:        principal.User.UserID,
		Email:         principal.User.Email,
		Role:          invite.Role,
		Status:        domain.TeamMemberStatusActive,
		InvitedByUser: invite.InvitedByUserID,
		JoinedAt:      acceptedAt,
	}
	return invite, nil
}

func (s *MemoryStore) DeclineTeamInvite(
	ctx context.Context,
	principal UserPrincipal,
	inviteID string,
	declinedAt time.Time,
) (TeamInvite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	invite, ok := s.teamInvites[inviteID]
	if !ok || !teamInviteVisibleToPrincipal(invite, principal) ||
		invite.Status != domain.TeamInviteStatusPending {
		return TeamInvite{}, ErrNotFound
	}
	invite.Status = domain.TeamInviteStatusDeclined
	invite.DeclinedAt = &declinedAt
	s.teamInvites[inviteID] = invite
	return invite, nil
}

func (s *MemoryStore) AddTeamAgent(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
	agentID string,
	addedAt time.Time,
) (TeamAgent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	member, ok := s.activeTeamMemberLocked(teamID, principal.User.UserID)
	if !ok || !teamRoleCanManageOwnAgents(member.Role) {
		return TeamAgent{}, ErrUnauthorized
	}
	agent, ok := s.agents[agentID]
	if !ok || agent.OwnerUserID != principal.User.UserID {
		return TeamAgent{}, ErrNotFound
	}
	key := teamAgentKey{TeamID: teamID, AgentID: agentID}
	if existing, ok := s.teamAgents[key]; ok && existing.RemovedAt == nil {
		return TeamAgent{}, ErrConflict
	}
	agentCopy := agent
	teamAgent := TeamAgent{
		TeamID:           teamID,
		AgentID:          agentID,
		AgentOwnerUserID: agent.OwnerUserID,
		AddedByUserID:    principal.User.UserID,
		AddedAt:          addedAt,
		Agent:            &agentCopy,
	}
	s.teamAgents[key] = teamAgent
	return teamAgent, nil
}

func (s *MemoryStore) RemoveTeamAgent(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
	agentID string,
	removedAt time.Time,
) (TeamAgent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	member, ok := s.activeTeamMemberLocked(teamID, principal.User.UserID)
	if !ok {
		return TeamAgent{}, ErrNotFound
	}
	key := teamAgentKey{TeamID: teamID, AgentID: agentID}
	teamAgent, ok := s.teamAgents[key]
	if !ok || teamAgent.RemovedAt != nil {
		return TeamAgent{}, ErrNotFound
	}
	if member.Role != domain.TeamRoleOwner &&
		(member.Role != domain.TeamRoleOperator ||
			teamAgent.AgentOwnerUserID != principal.User.UserID) {
		return TeamAgent{}, ErrUnauthorized
	}
	teamAgent.RemovedAt = &removedAt
	teamAgent.RemovedByUserID = principal.User.UserID
	s.teamAgents[key] = teamAgent
	return teamAgent, nil
}

func (s *MemoryStore) RemoveTeamMember(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
	userID string,
	removedAt time.Time,
) (TeamMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	requester, ok := s.activeTeamMemberLocked(teamID, principal.User.UserID)
	if !ok {
		return TeamMember{}, ErrNotFound
	}
	targetKey := teamMemberKey{TeamID: teamID, UserID: userID}
	target, ok := s.teamMembers[targetKey]
	if !ok || target.Status != domain.TeamMemberStatusActive {
		return TeamMember{}, ErrNotFound
	}
	if target.Role == domain.TeamRoleOwner {
		return TeamMember{}, ErrConflict
	}
	if target.UserID != principal.User.UserID && requester.Role != domain.TeamRoleOwner {
		return TeamMember{}, ErrUnauthorized
	}
	target.Status = domain.TeamMemberStatusRemoved
	target.RemovedAt = &removedAt
	target.RemovedByUser = principal.User.UserID
	s.teamMembers[targetKey] = target
	for key, teamAgent := range s.teamAgents {
		if teamAgent.TeamID == teamID && teamAgent.AgentOwnerUserID == userID &&
			teamAgent.RemovedAt == nil {
			teamAgent.RemovedAt = &removedAt
			teamAgent.RemovedByUserID = principal.User.UserID
			s.teamAgents[key] = teamAgent
		}
	}
	return target, nil
}

func (s *MemoryStore) activeTeamMemberLocked(teamID string, userID string) (TeamMember, bool) {
	member, ok := s.teamMembers[teamMemberKey{TeamID: teamID, UserID: userID}]
	if !ok || member.Status != domain.TeamMemberStatusActive {
		return TeamMember{}, false
	}
	team, ok := s.teams[teamID]
	if !ok || team.Status != domain.TeamStatusActive {
		return TeamMember{}, false
	}
	return member, true
}

func (s *MemoryStore) activeTeamMemberCountLocked(teamID string) int {
	count := 0
	for _, member := range s.teamMembers {
		if member.TeamID == teamID && member.Status == domain.TeamMemberStatusActive {
			count++
		}
	}
	return count
}

func (s *MemoryStore) activeTeamAgentCountLocked(teamID string) int {
	count := 0
	for _, agent := range s.teamAgents {
		if agent.TeamID == teamID && agent.RemovedAt == nil {
			count++
		}
	}
	return count
}

func teamInviteVisibleToPrincipal(invite TeamInvite, principal UserPrincipal) bool {
	return invite.RecipientUserID == principal.User.UserID ||
		(invite.RecipientUserID == "" &&
			normalizeEmail(invite.Email) == normalizeEmail(principal.User.Email))
}

func teamRoleCanManageOwnAgents(role string) bool {
	return role == domain.TeamRoleOwner || role == domain.TeamRoleOperator
}
