package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryProjectTargetLifecycleBDD(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner := UserPrincipal{User: User{UserID: "user_owner"}}
	store.agents["agent_owner"] = Agent{
		AgentID:     "agent_owner",
		OwnerUserID: owner.User.UserID,
		NodeID:      "node_mac",
	}
	store.agents["agent_owner_2"] = Agent{
		AgentID:     "agent_owner_2",
		OwnerUserID: owner.User.UserID,
		NodeID:      "node_linux",
	}
	store.agents["agent_foreign"] = Agent{
		AgentID:     "agent_foreign",
		OwnerUserID: "user_other",
		NodeID:      "node_linux",
	}
	project, err := store.CreateProject(ctx, owner, CreateProjectRequest{DisplayName: "Manager"})
	require.NoError(t, err)

	t.Run(
		"given one agent when creating multiple cwd targets then both snapshots are retained",
		func(t *testing.T) {
			first, createErr := store.CreateProjectTarget(
				ctx,
				owner,
				project.ProjectID,
				CreateProjectTargetRequest{
					AgentID:     "agent_owner",
					DisplayName: " Mac main ",
					Cwd:         " ~/pax_workspace/pax-manager ",
					IsDefault:   true,
				},
			)
			require.NoError(t, createErr)
			assert.Equal(t, "Mac main", first.DisplayName)
			assert.Equal(t, "~/pax_workspace/pax-manager", first.Cwd)
			assert.True(t, first.Enabled)
			assert.True(t, first.IsDefault)

			second, createErr := store.CreateProjectTarget(
				ctx,
				owner,
				project.ProjectID,
				CreateProjectTargetRequest{
					AgentID:     "agent_owner",
					DisplayName: "Mac feature",
					Cwd:         "~/worktrees/pax-manager-feature",
				},
			)
			require.NoError(t, createErr)

			targets, listErr := store.ListProjectTargets(ctx, owner, project.ProjectID)
			require.NoError(t, listErr)
			require.Len(t, targets, 2)
			assert.ElementsMatch(t, []string{first.TargetID, second.TargetID}, targetIDs(targets))
			assert.NotEqual(t, first.Cwd, second.Cwd)
		},
	)

	t.Run(
		"given the same project agent and cwd when creating twice then the existing target is reused",
		func(t *testing.T) {
			before, listErr := store.ListProjectTargets(ctx, owner, project.ProjectID)
			require.NoError(t, listErr)
			first, createErr := store.CreateProjectTarget(
				ctx,
				owner,
				project.ProjectID,
				CreateProjectTargetRequest{
					AgentID:     "agent_owner",
					DisplayName: "Original",
					Cwd:         " ~/dedupe ",
				},
			)
			require.NoError(t, createErr)
			second, createErr := store.CreateProjectTarget(
				ctx,
				owner,
				project.ProjectID,
				CreateProjectTargetRequest{
					AgentID:     " agent_owner ",
					DisplayName: "Ignored replacement",
					Cwd:         "~/dedupe",
				},
			)
			require.NoError(t, createErr)

			assert.Equal(t, first, second)
			after, listErr := store.ListProjectTargets(ctx, owner, project.ProjectID)
			require.NoError(t, listErr)
			assert.Len(t, after, len(before)+1)
		},
	)

	t.Run(
		"given a new default when updating then the previous default is cleared",
		func(t *testing.T) {
			first, createErr := store.CreateProjectTarget(
				ctx,
				owner,
				project.ProjectID,
				CreateProjectTargetRequest{
					AgentID:     "agent_owner",
					DisplayName: "First default",
					Cwd:         "~/first",
					IsDefault:   true,
				},
			)
			require.NoError(t, createErr)
			second, createErr := store.CreateProjectTarget(
				ctx,
				owner,
				project.ProjectID,
				CreateProjectTargetRequest{
					AgentID:     "agent_owner",
					DisplayName: "Second default",
					Cwd:         "~/second",
				},
			)
			require.NoError(t, createErr)

			makeDefault := true
			second, updateErr := store.UpdateProjectTarget(
				ctx,
				owner,
				project.ProjectID,
				second.TargetID,
				UpdateProjectTargetRequest{IsDefault: &makeDefault},
			)
			require.NoError(t, updateErr)
			assert.True(t, second.IsDefault)

			first, getErr := store.GetProjectTarget(ctx, owner, project.ProjectID, first.TargetID)
			require.NoError(t, getErr)
			assert.False(t, first.IsDefault)

			enabled := false
			second, updateErr = store.UpdateProjectTarget(
				ctx,
				owner,
				project.ProjectID,
				second.TargetID,
				UpdateProjectTargetRequest{Enabled: &enabled},
			)
			require.NoError(t, updateErr)
			assert.False(t, second.Enabled)
			assert.False(t, second.IsDefault)
		},
	)

	t.Run(
		"given a foreign agent or disabled default then creation is rejected",
		func(t *testing.T) {
			_, createErr := store.CreateProjectTarget(
				ctx,
				owner,
				project.ProjectID,
				CreateProjectTargetRequest{
					AgentID:     "agent_foreign",
					DisplayName: "Foreign",
					Cwd:         "~/foreign",
				},
			)
			require.ErrorIs(t, createErr, ErrNotFound)

			disabled := false
			_, createErr = store.CreateProjectTarget(
				ctx,
				owner,
				project.ProjectID,
				CreateProjectTargetRequest{
					AgentID:     "agent_owner",
					DisplayName: "Invalid default",
					Cwd:         "~/invalid",
					IsDefault:   true,
					Enabled:     &disabled,
				},
			)
			require.ErrorIs(t, createErr, ErrConflict)
		},
	)

	t.Run(
		"given editable fields when updating then agent name and cwd change together",
		func(t *testing.T) {
			target, createErr := store.CreateProjectTarget(
				ctx,
				owner,
				project.ProjectID,
				CreateProjectTargetRequest{
					AgentID:     "agent_owner",
					DisplayName: "Editable",
					Cwd:         "~/before",
				},
			)
			require.NoError(t, createErr)

			agentID := " agent_owner_2 "
			displayName := " Edited "
			cwd := " ~/after "
			now = now.Add(time.Minute)
			updated, updateErr := store.UpdateProjectTarget(
				ctx,
				owner,
				project.ProjectID,
				target.TargetID,
				UpdateProjectTargetRequest{
					AgentID:     &agentID,
					DisplayName: &displayName,
					Cwd:         &cwd,
				},
			)
			require.NoError(t, updateErr)
			assert.Equal(t, "agent_owner_2", updated.AgentID)
			assert.Equal(t, "Edited", updated.DisplayName)
			assert.Equal(t, "~/after", updated.Cwd)
			assert.Equal(t, now, updated.UpdatedAt)

			foreignAgent := "agent_foreign"
			_, updateErr = store.UpdateProjectTarget(
				ctx,
				owner,
				project.ProjectID,
				target.TargetID,
				UpdateProjectTargetRequest{AgentID: &foreignAgent},
			)
			require.ErrorIs(t, updateErr, ErrNotFound)

			blank := " "
			_, updateErr = store.UpdateProjectTarget(
				ctx,
				owner,
				project.ProjectID,
				target.TargetID,
				UpdateProjectTargetRequest{Cwd: &blank},
			)
			require.ErrorIs(t, updateErr, ErrConflict)
		},
	)

	t.Run(
		"given another owner when reading targets then the project is hidden",
		func(t *testing.T) {
			other := UserPrincipal{User: User{UserID: "user_other"}}
			_, listErr := store.ListProjectTargets(ctx, other, project.ProjectID)
			require.ErrorIs(t, listErr, ErrNotFound)
		},
	)
}

func targetIDs(targets []ProjectTarget) []string {
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, target.TargetID)
	}
	return ids
}
