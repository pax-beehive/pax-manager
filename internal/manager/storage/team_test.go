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

	t.Run("Given team sent invites then owner and operator can list them", func(t *testing.T) {
		ctx := context.Background()
		now := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
		store := NewMemoryStore(func() time.Time { return now })
		owner, operator, member := seedTeamUsers(t, ctx, store)
		teamID := "team_1"
		seedTeam(t, ctx, store, teamID, owner, map[string]User{
			domain.TeamRoleOperator: operator,
			domain.TeamRoleMember:   member,
		}, now)

		_, err := store.CreateTeamInvite(ctx, UserPrincipal{User: owner}, TeamInvite{
			InviteID:        "tinv_pending",
			TeamID:          teamID,
			Email:           "pending@example.com",
			Role:            domain.TeamRoleMember,
			Status:          domain.TeamInviteStatusPending,
			InvitedByUserID: owner.UserID,
			CreatedAt:       now.Add(time.Minute),
		})
		require.NoError(t, err)
		_, err = store.CreateTeamInvite(ctx, UserPrincipal{User: owner}, TeamInvite{
			InviteID:        "tinv_canceled",
			TeamID:          teamID,
			Email:           "canceled@example.com",
			Role:            domain.TeamRoleMember,
			Status:          domain.TeamInviteStatusPending,
			InvitedByUserID: owner.UserID,
			CreatedAt:       now.Add(2 * time.Minute),
		})
		require.NoError(t, err)
		_, err = store.CancelTeamInvite(
			ctx,
			UserPrincipal{User: owner},
			teamID,
			"tinv_canceled",
			now.Add(3*time.Minute),
		)
		require.NoError(t, err)

		ownerInvites, err := store.ListTeamSentInvites(ctx, UserPrincipal{User: owner}, teamID)
		require.NoError(t, err)
		require.Len(t, ownerInvites, 2)
		require.Equal(t, "tinv_canceled", ownerInvites[0].InviteID)
		require.Equal(t, domain.TeamInviteStatusCanceled, ownerInvites[0].Status)
		require.Equal(t, "tinv_pending", ownerInvites[1].InviteID)

		operatorInvites, err := store.ListTeamSentInvites(
			ctx,
			UserPrincipal{User: operator},
			teamID,
		)
		require.NoError(t, err)
		require.Len(t, operatorInvites, 2)

		_, err = store.ListTeamSentInvites(ctx, UserPrincipal{User: member}, teamID)
		require.ErrorIs(t, err, ErrUnauthorized)
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

	t.Run("Given a team agent then active members can access user agent paths", func(t *testing.T) {
		ctx := context.Background()
		now := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
		store := NewMemoryStore(func() time.Time { return now })
		owner, _, member := seedTeamUsers(t, ctx, store)
		teamID := "team_1"
		seedTeam(t, ctx, store, teamID, owner, map[string]User{
			domain.TeamRoleMember: member,
		}, now)
		store.nodes["node_1"] = Node{
			NodeID:      "node_1",
			OwnerUserID: owner.UserID,
			Status:      "online",
		}
		store.agents["agent_owner"] = Agent{
			AgentID:      "agent_owner",
			NodeID:       "node_1",
			OwnerUserID:  owner.UserID,
			Status:       "online",
			RegisteredAt: now,
		}
		session, err := store.CreateNodeAgentSession(
			ctx,
			UserPrincipal{User: owner},
			CreateSessionRequest{
				NodeID:    "node_1",
				AgentID:   "agent_owner",
				SessionID: "sess_1",
			},
		)
		require.NoError(t, err)
		_, err = store.CreateMailboxMessage(ctx, UserPrincipal{User: owner}, CreateMailboxRequest{
			NodeID:    "node_1",
			AgentID:   "agent_owner",
			SessionID: "sess_1",
			Message:   "hello",
		})
		require.NoError(t, err)

		_, err = store.GetAgent(ctx, UserPrincipal{User: member}, "agent_owner")
		require.ErrorIs(t, err, ErrNotFound)
		_, err = store.ListNodeAgents(ctx, UserPrincipal{User: member}, "node_1")
		require.ErrorIs(t, err, ErrNotFound)

		_, err = store.AddTeamAgent(ctx, UserPrincipal{User: owner}, teamID, "agent_owner", now)
		require.NoError(t, err)

		agents, err := store.ListAgents(ctx, UserPrincipal{User: member})
		require.NoError(t, err)
		require.Len(t, agents, 1)
		require.Equal(t, "agent_owner", agents[0].AgentID)
		nodeAgents, err := store.ListNodeAgents(ctx, UserPrincipal{User: member}, "node_1")
		require.NoError(t, err)
		require.Len(t, nodeAgents, 1)
		require.Equal(t, "agent_owner", nodeAgents[0].AgentID)
		agent, err := store.GetAgent(ctx, UserPrincipal{User: member}, "agent_owner")
		require.NoError(t, err)
		require.Equal(t, owner.UserID, agent.OwnerUserID)
		sessions, err := store.ListAgentSessions(ctx, UserPrincipal{User: member}, "agent_owner")
		require.NoError(t, err)
		require.Len(t, sessions, 1)
		require.Equal(t, session.SessionID, sessions[0].SessionID)
		visibleSession, err := store.GetSession(ctx, UserPrincipal{User: member}, "sess_1")
		require.NoError(t, err)
		require.Equal(t, session.SessionID, visibleSession.SessionID)
		messages, err := store.ListSessionMessages(ctx, UserPrincipal{User: member}, "sess_1")
		require.NoError(t, err)
		require.Len(t, messages, 1)
		require.Equal(t, "hello", messages[0].Message)
		sharedSession, err := store.CreateNodeAgentSession(
			ctx,
			UserPrincipal{User: member},
			CreateSessionRequest{
				NodeID:    "node_1",
				AgentID:   "agent_owner",
				SessionID: "sess_shared",
			},
		)
		require.NoError(t, err)
		require.Equal(t, "sess_shared", sharedSession.SessionID)
		sharedMessage, err := store.CreateMailboxMessage(
			ctx,
			UserPrincipal{User: member},
			CreateMailboxRequest{
				NodeID:    "node_1",
				AgentID:   "agent_owner",
				SessionID: "sess_shared",
				Message:   "from member",
			},
		)
		require.NoError(t, err)
		require.Equal(t, member.UserID, sharedMessage.UserID)
		require.Equal(t, owner.UserID, sharedMessage.OwnerUserID)

		_, err = store.RemoveTeamMember(
			ctx,
			UserPrincipal{User: owner},
			teamID,
			member.UserID,
			now.Add(time.Minute),
		)
		require.NoError(t, err)
		agents, err = store.ListAgents(ctx, UserPrincipal{User: member})
		require.NoError(t, err)
		require.Empty(t, agents)
		_, err = store.ListNodeAgents(ctx, UserPrincipal{User: member}, "node_1")
		require.ErrorIs(t, err, ErrNotFound)
		_, err = store.GetAgent(ctx, UserPrincipal{User: member}, "agent_owner")
		require.ErrorIs(t, err, ErrNotFound)
		_, err = store.ListAgentSessions(ctx, UserPrincipal{User: member}, "agent_owner")
		require.ErrorIs(t, err, ErrNotFound)
		_, err = store.GetSession(ctx, UserPrincipal{User: member}, "sess_1")
		require.ErrorIs(t, err, ErrNotFound)
		messages, err = store.ListSessionMessages(ctx, UserPrincipal{User: member}, "sess_1")
		require.NoError(t, err)
		require.Empty(t, messages)
		_, err = store.CreateNodeAgentSession(
			ctx,
			UserPrincipal{User: member},
			CreateSessionRequest{
				NodeID:    "node_1",
				AgentID:   "agent_owner",
				SessionID: "sess_denied",
			},
		)
		require.ErrorIs(t, err, ErrNotFound)
		_, err = store.CreateMailboxMessage(ctx, UserPrincipal{User: member}, CreateMailboxRequest{
			NodeID:    "node_1",
			AgentID:   "agent_owner",
			SessionID: "sess_1",
			Message:   "denied",
		})
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("Given team management changes then audit events are recorded", func(t *testing.T) {
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

		invite, err := store.CreateTeamInvite(ctx, UserPrincipal{User: owner}, TeamInvite{
			InviteID:        "tinv_cancel",
			TeamID:          teamID,
			Email:           "new@example.com",
			Role:            domain.TeamRoleMember,
			Status:          domain.TeamInviteStatusPending,
			InvitedByUserID: owner.UserID,
			CreatedAt:       now.Add(time.Minute),
		})
		require.NoError(t, err)
		require.Equal(t, domain.TeamInviteStatusPending, invite.Status)

		canceled, err := store.CancelTeamInvite(
			ctx,
			UserPrincipal{User: owner},
			teamID,
			"tinv_cancel",
			now.Add(2*time.Minute),
		)
		require.NoError(t, err)
		require.Equal(t, domain.TeamInviteStatusCanceled, canceled.Status)
		require.NotNil(t, canceled.CanceledAt)

		updated, err := store.UpdateTeamMemberRole(
			ctx,
			UserPrincipal{User: owner},
			teamID,
			member.UserID,
			domain.TeamRoleOperator,
			now.Add(3*time.Minute),
		)
		require.NoError(t, err)
		require.Equal(t, domain.TeamRoleOperator, updated.Role)

		teamAgent, err := store.AddTeamAgent(
			ctx,
			UserPrincipal{User: operator},
			teamID,
			"agent_operator",
			now.Add(4*time.Minute),
		)
		require.NoError(t, err)
		require.Equal(t, operator.Email, teamAgent.AgentOwnerEmail)
		agents, err := store.ListTeamAgents(ctx, UserPrincipal{User: owner}, teamID)
		require.NoError(t, err)
		require.Len(t, agents, 1)
		require.Equal(t, operator.Email, agents[0].AgentOwnerEmail)

		events, err := store.ListTeamAuditEvents(ctx, UserPrincipal{User: owner}, teamID, 20)
		require.NoError(t, err)
		requireAuditActions(t, events,
			domain.TeamAuditActionAgentAdded,
			domain.TeamAuditActionMemberRoleUpdated,
			domain.TeamAuditActionInviteCanceled,
			domain.TeamAuditActionInviteCreated,
			domain.TeamAuditActionTeamCreated,
		)

		archived, err := store.ArchiveTeam(
			ctx,
			UserPrincipal{User: owner},
			teamID,
			now.Add(5*time.Minute),
		)
		require.NoError(t, err)
		require.Equal(t, domain.TeamStatusArchived, archived.Status)
		require.NotNil(t, archived.ArchivedAt)

		_, err = store.GetTeam(ctx, UserPrincipal{User: owner}, teamID)
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func requireAuditActions(t *testing.T, events []TeamAuditEvent, actions ...string) {
	t.Helper()
	require.GreaterOrEqual(t, len(events), len(actions))
	for i, action := range actions {
		require.Equal(t, action, events[i].Action)
	}
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
