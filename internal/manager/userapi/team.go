package userapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	teamNameLimit = 100
)

func (s *Service) CreateTeam(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.CreateTeamRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	name, err := normalizeTeamName(req.Name)
	if err != nil {
		return 0, nil, err
	}
	teamID, err := s.secrets.New("team")
	if err != nil {
		return 0, nil, err
	}
	now := s.clock().UTC()
	team, err := s.store.CreateTeam(c, domain.Team{
		TeamID:      teamID,
		OwnerUserID: principal.User.UserID,
		Name:        name,
		Status:      domain.TeamStatusActive,
		CreatedAt:   now,
	}, domain.TeamMember{
		TeamID:        teamID,
		UserID:        principal.User.UserID,
		Email:         principal.User.Email,
		Role:          domain.TeamRoleOwner,
		Status:        domain.TeamMemberStatusActive,
		InvitedByUser: principal.User.UserID,
		JoinedAt:      now,
	})
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"team": team}, nil
}

func (s *Service) ListTeams(
	c context.Context,
	meta auth.RequestMetadata,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	teams, err := s.store.ListTeams(c, principal)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"teams": teams}, nil
}

func (s *Service) GetTeam(
	c context.Context,
	meta auth.RequestMetadata,
	teamID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if teamID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "team_id is required"}
	}
	team, err := s.store.GetTeam(c, principal, teamID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"team": team}, nil
}

func (s *Service) ListTeamMembers(
	c context.Context,
	meta auth.RequestMetadata,
	teamID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if teamID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "team_id is required"}
	}
	members, err := s.store.ListTeamMembers(c, principal, teamID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"members": members}, nil
}

func (s *Service) ListTeamAgents(
	c context.Context,
	meta auth.RequestMetadata,
	teamID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if teamID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "team_id is required"}
	}
	agents, err := s.store.ListTeamAgents(c, principal, teamID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"agents": agents}, nil
}

func (s *Service) CreateTeamInvite(
	c context.Context,
	meta auth.RequestMetadata,
	teamID string,
	req domain.CreateTeamInviteRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if teamID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "team_id is required"}
	}
	email := domain.NormalizeEmail(req.Email)
	if email == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "email is required"}
	}
	if email == domain.NormalizeEmail(principal.User.Email) {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "cannot invite yourself",
		}
	}
	role, err := normalizeTeamInviteRole(req.Role)
	if err != nil {
		return 0, nil, err
	}
	inviteID, err := s.secrets.New("tinv")
	if err != nil {
		return 0, nil, err
	}
	recipientUserID := ""
	recipient, err := s.store.GetUserByEmail(c, email)
	if err == nil {
		recipientUserID = recipient.UserID
	} else if !errors.Is(err, domain.ErrNotFound) {
		return 0, nil, err
	}
	invite, err := s.store.CreateTeamInvite(c, principal, domain.TeamInvite{
		InviteID:        inviteID,
		TeamID:          teamID,
		Email:           email,
		RecipientUserID: recipientUserID,
		Role:            role,
		Status:          domain.TeamInviteStatusPending,
		InvitedByUserID: principal.User.UserID,
		CreatedAt:       s.clock().UTC(),
	})
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"invite": invite}, nil
}

func (s *Service) ListTeamInvites(
	c context.Context,
	meta auth.RequestMetadata,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	invites, err := s.store.ListTeamInvites(c, principal)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"invites": invites}, nil
}

func (s *Service) AcceptTeamInvite(
	c context.Context,
	meta auth.RequestMetadata,
	inviteID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if inviteID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "invite_id is required"}
	}
	invite, err := s.store.AcceptTeamInvite(c, principal, inviteID, s.clock().UTC())
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"invite": invite}, nil
}

func (s *Service) DeclineTeamInvite(
	c context.Context,
	meta auth.RequestMetadata,
	inviteID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if inviteID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "invite_id is required"}
	}
	invite, err := s.store.DeclineTeamInvite(c, principal, inviteID, s.clock().UTC())
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"invite": invite}, nil
}

func (s *Service) AddTeamAgent(
	c context.Context,
	meta auth.RequestMetadata,
	teamID string,
	req domain.AddTeamAgentRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if teamID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "team_id is required"}
	}
	if req.AgentID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "agent_id is required"}
	}
	agent, err := s.store.AddTeamAgent(c, principal, teamID, req.AgentID, s.clock().UTC())
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"agent": agent}, nil
}

func (s *Service) RemoveTeamAgent(
	c context.Context,
	meta auth.RequestMetadata,
	teamID string,
	agentID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if teamID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "team_id is required"}
	}
	if agentID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "agent_id is required"}
	}
	agent, err := s.store.RemoveTeamAgent(c, principal, teamID, agentID, s.clock().UTC())
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"agent": agent}, nil
}

func (s *Service) RemoveTeamMember(
	c context.Context,
	meta auth.RequestMetadata,
	teamID string,
	userID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if teamID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "team_id is required"}
	}
	if userID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "user_id is required"}
	}
	member, err := s.store.RemoveTeamMember(c, principal, teamID, userID, s.clock().UTC())
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"member": member}, nil
}

func (s *Service) LeaveTeam(
	c context.Context,
	meta auth.RequestMetadata,
	teamID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if teamID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "team_id is required"}
	}
	member, err := s.store.RemoveTeamMember(
		c,
		principal,
		teamID,
		principal.User.UserID,
		s.clock().UTC(),
	)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"member": member}, nil
}

func normalizeTeamName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", apperr.Error{Status: http.StatusBadRequest, Message: "name is required"}
	}
	if len(name) > teamNameLimit {
		return "", apperr.Error{Status: http.StatusBadRequest, Message: "name is too long"}
	}
	return name, nil
}

func normalizeTeamInviteRole(role string) (string, error) {
	role = strings.TrimSpace(strings.ToLower(role))
	if role == "" {
		return domain.TeamRoleMember, nil
	}
	switch role {
	case domain.TeamRoleOperator, domain.TeamRoleMember:
		return role, nil
	default:
		return "", apperr.Error{Status: http.StatusBadRequest, Message: "role is invalid"}
	}
}
