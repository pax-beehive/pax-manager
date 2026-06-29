package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestTeamPostgresModelConversions(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	archivedAt := now.Add(time.Hour)
	removedAt := now.Add(2 * time.Hour)
	acceptedAt := now.Add(3 * time.Hour)
	declinedAt := now.Add(4 * time.Hour)
	canceledAt := now.Add(5 * time.Hour)

	team := Team{
		TeamID:      "team_1",
		OwnerUserID: "usr_owner",
		Name:        "Core",
		Description: "Build platform work",
		Status:      domain.TeamStatusArchived,
		CreatedAt:   now,
		ArchivedAt:  &archivedAt,
	}
	require.Equal(t, team, teamFromModel(teamModel(team)))
	require.Equal(t, Team{}, teamFromModel(nil))

	member := TeamMember{
		TeamID:        "team_1",
		UserID:        "usr_member",
		Role:          domain.TeamRoleOperator,
		Status:        domain.TeamMemberStatusRemoved,
		InvitedByUser: "usr_owner",
		JoinedAt:      now,
		RemovedAt:     &removedAt,
		RemovedByUser: "usr_owner",
	}
	require.Equal(t, member, teamMemberFromModel(teamMemberModel(member)))
	require.Nil(t, teamMemberModel(TeamMember{}).InvitedByUserID)
	require.Equal(t, TeamMember{}, teamMemberFromModel(nil))

	invite := TeamInvite{
		InviteID:        "tinv_1",
		TeamID:          "team_1",
		Email:           "operator@example.com",
		RecipientUserID: "usr_operator",
		Role:            domain.TeamRoleOperator,
		Status:          domain.TeamInviteStatusCanceled,
		InvitedByUserID: "usr_owner",
		CreatedAt:       now,
		AcceptedAt:      &acceptedAt,
		DeclinedAt:      &declinedAt,
		CanceledAt:      &canceledAt,
	}
	require.Equal(t, invite, teamInviteFromModel(teamInviteModel(invite)))
	require.Nil(t, teamInviteModel(TeamInvite{}).RecipientUserID)
	require.Equal(t, TeamInvite{}, teamInviteFromModel(nil))

	removedBy := "usr_owner"
	agent := teamAgentFromModel(&teamAgentRow{
		TeamID:           "team_1",
		AgentID:          "agent_1",
		AgentOwnerUserID: "usr_operator",
		Identity:         "",
		Role:             "",
		DisplayName:      "Review Bot",
		Description:      "Reviews code",
		Metadata:         nil,
		AddedByUserID:    "usr_operator",
		AddedAt:          now,
		RemovedAt:        &removedAt,
		RemovedByUserID:  &removedBy,
	})
	require.Equal(t, "agent_1", agent.Identity)
	require.Equal(t, domain.TeamAgentRoleGeneral, agent.Role)
	require.Equal(t, json.RawMessage(`{}`), agent.Metadata)
	require.Equal(t, "usr_owner", agent.RemovedByUserID)
	require.Equal(t, TeamAgent{}, teamAgentFromModel(nil))

	event := TeamAuditEvent{
		EventID:        "taud_1",
		TeamID:         "team_1",
		ActorUserID:    "usr_owner",
		Action:         domain.TeamAuditActionAgentAdded,
		TargetUserID:   "usr_operator",
		TargetAgentID:  "agent_1",
		TargetInviteID: "tinv_1",
		Metadata:       json.RawMessage(`{"agent_id":"agent_1"}`),
		CreatedAt:      now,
	}
	require.Equal(t, event, teamAuditEventFromModel(teamAuditEventModel(event)))
	defaultEvent := teamAuditEventFromModel(teamAuditEventModel(TeamAuditEvent{}))
	require.Equal(t, json.RawMessage(`{}`), defaultEvent.Metadata)
	require.Equal(t, TeamAuditEvent{}, teamAuditEventFromModel(nil))

	completedAt := now.Add(6 * time.Hour)
	run := TeamMemexRun{
		RunID:             "tmrun_1",
		TeamID:            "team_1",
		RequestedByUserID: "usr_owner",
		ExecutorType:      domain.TeamMemexRunExecutorDryRun,
		Status:            domain.TeamMemexRunStatusValidationFailed,
		Partial:           true,
		Constraints:       domain.DefaultTeamMemexRunConstraints(),
		IndexMD:           "# Team LLM Wiki\n",
		ValidationReport: &domain.TeamMemexValidationReport{
			Retryable: true,
			Errors: []domain.TeamMemexValidationError{{
				Code:    "DOC_BODY_TOO_LARGE",
				Path:    "runtime/sessions.md",
				Message: "body_md is too large",
			}},
			Constraints: domain.DefaultTeamMemexRunConstraints(),
		},
		Error:       "validation failed",
		StartedAt:   now,
		CompletedAt: &completedAt,
	}
	row, err := teamMemexRunModel(run)
	require.NoError(t, err)
	convertedRun, err := teamMemexRunFromModel(row)
	require.NoError(t, err)
	require.Equal(t, run, convertedRun)
	emptyRun, err := teamMemexRunFromModel(nil)
	require.NoError(t, err)
	require.Equal(t, TeamMemexRun{}, emptyRun)
}

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
		store.agents["agent_operator_2"] = Agent{
			AgentID:     "agent_operator_2",
			OwnerUserID: operator.UserID,
			Status:      "online",
		}

		_, err := store.AddTeamAgent(
			ctx,
			UserPrincipal{User: member},
			teamID,
			AddTeamAgentRequest{AgentID: "agent_owner"},
			now,
		)
		require.ErrorIs(t, err, ErrUnauthorized)

		_, err = store.AddTeamAgent(
			ctx,
			UserPrincipal{User: operator},
			teamID,
			AddTeamAgentRequest{AgentID: "agent_owner"},
			now,
		)
		require.ErrorIs(t, err, ErrNotFound)

		operatorAgent, err := store.AddTeamAgent(
			ctx,
			UserPrincipal{User: operator},
			teamID,
			AddTeamAgentRequest{
				AgentID:     "agent_operator",
				Identity:    "reviewer",
				Role:        "reviewer",
				DisplayName: "Review Bot",
				Description: "Reviews team tasks before handoff.",
			},
			now,
		)
		require.NoError(t, err)
		require.Equal(t, operator.UserID, operatorAgent.AgentOwnerUserID)
		require.Equal(t, "reviewer", operatorAgent.Identity)
		require.Equal(t, "reviewer", operatorAgent.Role)
		require.Equal(t, "Review Bot", operatorAgent.DisplayName)
		require.Equal(t, "Reviews team tasks before handoff.", operatorAgent.Description)

		secondReviewer, err := store.AddTeamAgent(
			ctx,
			UserPrincipal{User: operator},
			teamID,
			AddTeamAgentRequest{AgentID: "agent_operator_2", Identity: "reviewer"},
			now,
		)
		require.NoError(t, err)
		require.Equal(t, "reviewer", secondReviewer.Identity)

		ownerAgent, err := store.AddTeamAgent(
			ctx,
			UserPrincipal{User: owner},
			teamID,
			AddTeamAgentRequest{AgentID: "agent_owner"},
			now,
		)
		require.NoError(t, err)
		require.Equal(t, "agent_owner", ownerAgent.Identity)
		require.Equal(t, domain.TeamAgentRoleGeneral, ownerAgent.Role)

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
		"Given team memex documents then active members can read active docs only",
		func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
			store := NewMemoryStore(func() time.Time { return now })
			owner, operator, member := seedTeamUsers(t, ctx, store)
			teamID := "team_1"
			seedTeam(t, ctx, store, teamID, owner, map[string]User{
				domain.TeamRoleOperator: operator,
			}, now)
			store.teamMemexDocuments[teamMemexDocumentKey{TeamID: teamID, Path: "runtime/sessions.md"}] = TeamMemexDocument{
				DocumentID: "memex_doc_1",
				TeamID:     teamID,
				Path:       "runtime/sessions.md",
				Title:      "Sessions",
				Summary:    "How sessions are stored",
				Tags:       json.RawMessage(`["runtime"]`),
				BodyMD:     "# Sessions\n",
				Status:     domain.TeamMemexDocumentStatusActive,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			store.teamMemexDocuments[teamMemexDocumentKey{TeamID: teamID, Path: "old/archived.md"}] = TeamMemexDocument{
				DocumentID: "memex_doc_2",
				TeamID:     teamID,
				Path:       "old/archived.md",
				Title:      "Archived",
				Status:     domain.TeamMemexDocumentStatusArchived,
				CreatedAt:  now,
				UpdatedAt:  now,
			}

			documents, err := store.ListTeamMemexDocuments(
				ctx,
				UserPrincipal{User: operator},
				teamID,
			)
			require.NoError(t, err)
			require.Len(t, documents, 1)
			require.Equal(t, "runtime/sessions.md", documents[0].Path)
			documents[0].Tags[0] = 'x'

			paths, err := store.ListTeamMemexDocumentPaths(
				ctx,
				UserPrincipal{User: operator},
				teamID,
			)
			require.NoError(t, err)
			require.Equal(t, []string{"old/archived.md", "runtime/sessions.md"}, paths)

			document, err := store.GetTeamMemexDocument(
				ctx,
				UserPrincipal{User: owner},
				teamID,
				"runtime/sessions.md",
			)
			require.NoError(t, err)
			require.Equal(t, json.RawMessage(`["runtime"]`), document.Tags)
			require.Equal(t, "# Sessions\n", document.BodyMD)

			_, err = store.GetTeamMemexDocument(
				ctx,
				UserPrincipal{User: owner},
				teamID,
				"old/archived.md",
			)
			require.ErrorIs(t, err, ErrNotFound)

			_, err = store.ListTeamMemexDocuments(ctx, UserPrincipal{User: member}, teamID)
			require.ErrorIs(t, err, ErrNotFound)
		},
	)

	t.Run(
		"Given team memex run records then only owner and operator can create",
		func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
			store := NewMemoryStore(func() time.Time { return now })
			owner, operator, member := seedTeamUsers(t, ctx, store)
			teamID := "team_1"
			seedTeam(t, ctx, store, teamID, owner, map[string]User{
				domain.TeamRoleOperator: operator,
				domain.TeamRoleMember:   member,
			}, now)

			run := TeamMemexRun{
				RunID:             "tmrun_1",
				TeamID:            teamID,
				RequestedByUserID: operator.UserID,
				ExecutorType:      domain.TeamMemexRunExecutorDryRun,
				Status:            domain.TeamMemexRunStatusSucceeded,
				Constraints:       domain.DefaultTeamMemexRunConstraints(),
				IndexMD:           "# Team LLM Wiki\n",
				StartedAt:         now,
				CompletedAt:       &now,
			}
			created, err := store.CreateTeamMemexRun(ctx, UserPrincipal{User: operator}, run)
			require.NoError(t, err)
			require.Equal(t, "tmrun_1", created.RunID)

			created.Constraints.AllowedOperations[0] = "mutated"
			fetched, err := store.GetTeamMemexRun(
				ctx,
				UserPrincipal{User: member},
				teamID,
				"tmrun_1",
			)
			require.NoError(t, err)
			require.Equal(t, "create_doc", fetched.Constraints.AllowedOperations[0])
			require.Equal(t, "# Team LLM Wiki\n", fetched.IndexMD)

			_, err = store.CreateTeamMemexRun(ctx, UserPrincipal{User: member}, TeamMemexRun{
				RunID:             "tmrun_2",
				TeamID:            teamID,
				RequestedByUserID: member.UserID,
				ExecutorType:      domain.TeamMemexRunExecutorDryRun,
				Status:            domain.TeamMemexRunStatusSucceeded,
				Constraints:       domain.DefaultTeamMemexRunConstraints(),
				StartedAt:         now,
				CompletedAt:       &now,
			})
			require.ErrorIs(t, err, ErrUnauthorized)
		},
	)

	t.Run(
		"Given a team memex manifest then publish applies all operations atomically",
		func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
			store := NewMemoryStore(func() time.Time { return now })
			owner, operator, _ := seedTeamUsers(t, ctx, store)
			teamID := "team_1"
			seedTeam(t, ctx, store, teamID, owner, map[string]User{
				domain.TeamRoleOperator: operator,
			}, now)
			store.teamMemexDocuments[teamMemexDocumentKey{TeamID: teamID, Path: "runtime/sessions.md"}] = TeamMemexDocument{
				DocumentID: "memex_doc_1",
				TeamID:     teamID,
				Path:       "runtime/sessions.md",
				Title:      "Sessions",
				Summary:    "Old session notes",
				Tags:       json.RawMessage(`["runtime"]`),
				BodyMD:     "# Sessions\nOld body.\n",
				Status:     domain.TeamMemexDocumentStatusActive,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			store.teamMemexDocuments[teamMemexDocumentKey{TeamID: teamID, Path: "old/cleanup.md"}] = TeamMemexDocument{
				DocumentID: "memex_doc_2",
				TeamID:     teamID,
				Path:       "old/cleanup.md",
				Title:      "Cleanup",
				Status:     domain.TeamMemexDocumentStatusActive,
				CreatedAt:  now,
				UpdatedAt:  now,
			}

			publishedAt := now.Add(time.Minute)
			run := TeamMemexRun{
				RunID:             "tmrun_publish",
				TeamID:            teamID,
				RequestedByUserID: operator.UserID,
				ExecutorType:      domain.TeamMemexRunExecutorDryRun,
				Status:            domain.TeamMemexRunStatusSucceeded,
				Constraints:       domain.DefaultTeamMemexRunConstraints(),
				StartedAt:         now,
				CompletedAt:       &publishedAt,
			}
			_, err := store.PublishTeamMemexRun(
				ctx,
				UserPrincipal{User: operator},
				run,
				[]TeamMemexDocumentOperation{
					{
						Operation:  domain.TeamMemexOperationCreateDoc,
						DocumentID: "memex_doc_3",
						Path:       "product/llm-wiki.md",
						Title:      "LLM Wiki",
						Summary:    "Team wiki maintenance",
						Tags:       json.RawMessage(`["product"]`),
						BodyMD:     "# LLM Wiki\n",
					},
					{
						Operation: domain.TeamMemexOperationUpdateDoc,
						Path:      "runtime/sessions.md",
						Title:     "Sessions",
						Summary:   "Updated session notes",
						Tags:      json.RawMessage(`["runtime","sessions"]`),
						BodyMD:    "# Sessions\nUpdated body.\n",
					},
					{
						Operation: domain.TeamMemexOperationArchiveDoc,
						Path:      "old/cleanup.md",
					},
				},
				publishedAt,
			)
			require.NoError(t, err)

			documents, err := store.ListTeamMemexDocuments(ctx, UserPrincipal{User: owner}, teamID)
			require.NoError(t, err)
			require.Len(t, documents, 2)
			require.Equal(t, "product/llm-wiki.md", documents[0].Path)
			require.Equal(t, "runtime/sessions.md", documents[1].Path)
			require.Equal(t, "Updated session notes", documents[1].Summary)
			require.Equal(t, "# Sessions\nUpdated body.\n", documents[1].BodyMD)

			_, err = store.GetTeamMemexDocument(
				ctx,
				UserPrincipal{User: owner},
				teamID,
				"old/cleanup.md",
			)
			require.ErrorIs(t, err, ErrNotFound)

			_, err = store.PublishTeamMemexRun(ctx, UserPrincipal{User: operator}, TeamMemexRun{
				RunID:             "tmrun_conflict",
				TeamID:            teamID,
				RequestedByUserID: operator.UserID,
				ExecutorType:      domain.TeamMemexRunExecutorDryRun,
				Status:            domain.TeamMemexRunStatusSucceeded,
				Constraints:       domain.DefaultTeamMemexRunConstraints(),
				StartedAt:         now,
				CompletedAt:       &publishedAt,
			}, []TeamMemexDocumentOperation{
				{
					Operation:  domain.TeamMemexOperationCreateDoc,
					DocumentID: "memex_doc_4",
					Path:       "old/cleanup.md",
					Title:      "Cleanup Again",
					Summary:    "Should fail because archived paths are reserved",
					BodyMD:     "# Cleanup Again\n",
				},
				{
					Operation: domain.TeamMemexOperationUpdateDoc,
					Path:      "runtime/sessions.md",
					Title:     "Should Not Publish",
					Summary:   "Should not publish",
					BodyMD:    "# Should Not Publish\n",
				},
			}, publishedAt)
			require.ErrorIs(t, err, ErrConflict)

			document, err := store.GetTeamMemexDocument(
				ctx,
				UserPrincipal{User: owner},
				teamID,
				"runtime/sessions.md",
			)
			require.NoError(t, err)
			require.Equal(t, "Updated session notes", document.Summary)
		},
	)

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
				AddTeamAgentRequest{AgentID: "agent_operator"},
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
				AddTeamAgentRequest{AgentID: "agent_operator"},
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

		_, err = store.AddTeamAgent(
			ctx,
			UserPrincipal{User: owner},
			teamID,
			AddTeamAgentRequest{AgentID: "agent_owner"},
			now,
		)
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
			AddTeamAgentRequest{AgentID: "agent_operator"},
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

	t.Run(
		"Given pending team invites then recipients can list and decline them",
		func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
			store := NewMemoryStore(func() time.Time { return now })
			owner, operator, member := seedTeamUsers(t, ctx, store)
			teamID := "team_1"
			seedTeam(t, ctx, store, teamID, owner, map[string]User{
				domain.TeamRoleOperator: operator,
			}, now)

			members, err := store.ListTeamMembers(ctx, UserPrincipal{User: owner}, teamID)
			require.NoError(t, err)
			require.Len(t, members, 2)
			require.ElementsMatch(t, []string{owner.Email, operator.Email}, []string{
				members[0].Email,
				members[1].Email,
			})

			_, err = store.CreateTeamInvite(ctx, UserPrincipal{User: owner}, TeamInvite{
				InviteID:        "tinv_decline",
				TeamID:          teamID,
				Email:           "MEMBER@example.com",
				RecipientUserID: member.UserID,
				Role:            domain.TeamRoleMember,
				CreatedAt:       now.Add(time.Minute),
			})
			require.NoError(t, err)
			received, err := store.ListTeamInvites(ctx, UserPrincipal{User: member})
			require.NoError(t, err)
			require.Len(t, received, 1)
			require.Equal(t, "member@example.com", received[0].Email)

			declined, err := store.DeclineTeamInvite(
				ctx,
				UserPrincipal{User: member},
				"tinv_decline",
				now.Add(2*time.Minute),
			)
			require.NoError(t, err)
			require.Equal(t, domain.TeamInviteStatusDeclined, declined.Status)
			require.NotNil(t, declined.DeclinedAt)
			received, err = store.ListTeamInvites(ctx, UserPrincipal{User: member})
			require.NoError(t, err)
			require.Empty(t, received)
		},
	)
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
