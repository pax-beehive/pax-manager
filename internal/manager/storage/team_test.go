package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestMemoryTeamStore(t *testing.T) {
	t.Run(
		"Given a team invite when accepted then the member can list the team",
		func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
			store := NewMemoryStore(func() time.Time { return now })
			owner, err := store.EnsureUser(ctx, "owner@example.com", "", "user")
			require.NoError(t, err)
			operator, err := store.EnsureUser(ctx, "operator@example.com", "", "user")
			require.NoError(t, err)
			_, err = store.CreateTeam(ctx, Team{
				TeamID:      "team_1",
				OwnerUserID: owner.UserID,
				Name:        "Core",
				Status:      domain.TeamStatusActive,
				CreatedAt:   now,
			}, TeamMember{
				TeamID:        "team_1",
				UserID:        owner.UserID,
				Email:         owner.Email,
				Role:          domain.TeamRoleOwner,
				Status:        domain.TeamMemberStatusActive,
				InvitedByUser: owner.UserID,
				JoinedAt:      now,
			})
			require.NoError(t, err)
			_, err = store.CreateTeamInvite(ctx, UserPrincipal{User: owner}, TeamInvite{
				InviteID:        "tinv_1",
				TeamID:          "team_1",
				Email:           operator.Email,
				RecipientUserID: operator.UserID,
				Role:            domain.TeamRoleOperator,
				Status:          domain.TeamInviteStatusPending,
				InvitedByUserID: owner.UserID,
				CreatedAt:       now,
			})
			require.NoError(t, err)

			invite, err := store.AcceptTeamInvite(
				ctx,
				UserPrincipal{User: operator},
				"tinv_1",
				now.Add(time.Minute),
			)
			require.NoError(t, err)
			require.Equal(t, domain.TeamInviteStatusAccepted, invite.Status)

			teams, err := store.ListTeams(ctx, UserPrincipal{User: operator})
			require.NoError(t, err)
			require.Len(t, teams, 1)
			require.Equal(t, domain.TeamRoleOperator, teams[0].MyRole)
			require.Equal(t, 2, teams[0].MemberCount)
		},
	)

	t.Run("Given team agents then role rules enforce ownership", func(t *testing.T) {
		ctx := context.Background()
		now := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
		store := NewMemoryStore(func() time.Time { return now })
		owner, operator, member := seedTeamUsers(t, ctx, store)
		teamID := "team_1"
		seedTeam(t, ctx, store, teamID, owner, map[string]User{
			domain.TeamRoleOperator: operator,
			domain.TeamRoleMember:   member,
		}, now)
		store.agents["agent_owner"] = Agent{
			AgentID:     "agent_owner",
			OwnerUserID: owner.UserID,
			Status:      "online",
		}
		store.agents["agent_operator"] = Agent{
			AgentID:     "agent_operator",
			OwnerUserID: operator.UserID,
			Status:      "online",
		}

		_, err := store.AddTeamAgent(
			ctx,
			UserPrincipal{User: member},
			teamID,
			"agent_owner",
			now,
		)
		require.ErrorIs(t, err, ErrUnauthorized)

		_, err = store.AddTeamAgent(
			ctx,
			UserPrincipal{User: operator},
			teamID,
			"agent_owner",
			now,
		)
		require.ErrorIs(t, err, ErrNotFound)

		operatorAgent, err := store.AddTeamAgent(
			ctx,
			UserPrincipal{User: operator},
			teamID,
			"agent_operator",
			now,
		)
		require.NoError(t, err)
		require.Equal(t, operator.UserID, operatorAgent.AgentOwnerUserID)

		_, err = store.AddTeamAgent(ctx, UserPrincipal{User: owner}, teamID, "agent_owner", now)
		require.NoError(t, err)

		_, err = store.RemoveTeamAgent(
			ctx,
			UserPrincipal{User: operator},
			teamID,
			"agent_owner",
			now.Add(time.Minute),
		)
		require.ErrorIs(t, err, ErrUnauthorized)

		removed, err := store.RemoveTeamAgent(
			ctx,
			UserPrincipal{User: owner},
			teamID,
			"agent_operator",
			now.Add(time.Minute),
		)
		require.NoError(t, err)
		require.NotNil(t, removed.RemovedAt)
	})

	t.Run(
		"Given an owner removes a member then that member's team agents are removed",
		func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
			store := NewMemoryStore(func() time.Time { return now })
			owner, operator, member := seedTeamUsers(t, ctx, store)
			teamID := "team_1"
			seedTeam(t, ctx, store, teamID, owner, map[string]User{
				domain.TeamRoleOperator: operator,
				domain.TeamRoleMember:   member,
			}, now)
			store.agents["agent_operator"] = Agent{
				AgentID:     "agent_operator",
				OwnerUserID: operator.UserID,
				Status:      "online",
			}
			_, err := store.AddTeamAgent(
				ctx,
				UserPrincipal{User: operator},
				teamID,
				"agent_operator",
				now,
			)
			require.NoError(t, err)

			removed, err := store.RemoveTeamMember(
				ctx,
				UserPrincipal{User: owner},
				teamID,
				operator.UserID,
				now.Add(time.Minute),
			)
			require.NoError(t, err)
			require.Equal(t, domain.TeamMemberStatusRemoved, removed.Status)

			agents, err := store.ListTeamAgents(ctx, UserPrincipal{User: owner}, teamID)
			require.NoError(t, err)
			require.Empty(t, agents)
			_, err = store.AddTeamAgent(
				ctx,
				UserPrincipal{User: operator},
				teamID,
				"agent_operator",
				now.Add(2*time.Minute),
			)
			require.ErrorIs(t, err, ErrUnauthorized)
		},
	)
}

func seedTeamUsers(t *testing.T, ctx context.Context, store *MemoryStore) (User, User, User) {
	t.Helper()
	owner, err := store.EnsureUser(ctx, "owner@example.com", "", "user")
	require.NoError(t, err)
	operator, err := store.EnsureUser(ctx, "operator@example.com", "", "user")
	require.NoError(t, err)
	member, err := store.EnsureUser(ctx, "member@example.com", "", "user")
	require.NoError(t, err)
	return owner, operator, member
}

func seedTeam(
	t *testing.T,
	ctx context.Context,
	store *MemoryStore,
	teamID string,
	owner User,
	members map[string]User,
	now time.Time,
) {
	t.Helper()
	_, err := store.CreateTeam(ctx, Team{
		TeamID:      teamID,
		OwnerUserID: owner.UserID,
		Name:        "Core",
		Status:      domain.TeamStatusActive,
		CreatedAt:   now,
	}, TeamMember{
		TeamID:        teamID,
		UserID:        owner.UserID,
		Email:         owner.Email,
		Role:          domain.TeamRoleOwner,
		Status:        domain.TeamMemberStatusActive,
		InvitedByUser: owner.UserID,
		JoinedAt:      now,
	})
	require.NoError(t, err)
	for role, user := range members {
		store.teamMembers[teamMemberKey{TeamID: teamID, UserID: user.UserID}] = TeamMember{
			TeamID:        teamID,
			UserID:        user.UserID,
			Email:         user.Email,
			Role:          role,
			Status:        domain.TeamMemberStatusActive,
			InvitedByUser: owner.UserID,
			JoinedAt:      now,
		}
	}
}
