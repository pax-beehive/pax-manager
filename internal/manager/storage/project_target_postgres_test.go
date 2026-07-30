package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPostgresProjectTargetLifecycleBDD(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(
		sqlite.Open("file:project-target-lifecycle?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&projectRow{}, &projectTargetRow{}))
	require.NoError(t, db.Exec(`
		CREATE TABLE agents (
			agent_id TEXT PRIMARY KEY,
			owner_user_id TEXT NOT NULL,
			deleted_at DATETIME
		)
	`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO agents (agent_id, owner_user_id)
		VALUES
			('agent_owner', 'user_owner'),
			('agent_owner_2', 'user_owner'),
			('agent_foreign', 'user_other')
	`).Error)

	ctx := context.Background()
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	store := &PostgresStore{gormDB: db, now: func() time.Time { return now }}
	owner := UserPrincipal{User: User{UserID: "user_owner"}}
	project, err := store.CreateProject(ctx, owner, CreateProjectRequest{DisplayName: "Manager"})
	require.NoError(t, err)

	t.Run("given multiple paths on one agent then list retains each target", func(t *testing.T) {
		first, createErr := store.CreateProjectTarget(
			ctx,
			owner,
			project.ProjectID,
			CreateProjectTargetRequest{
				AgentID:     "agent_owner",
				DisplayName: "Main",
				Cwd:         "~/main",
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
				DisplayName: "Feature",
				Cwd:         "~/feature",
			},
		)
		require.NoError(t, createErr)

		targets, listErr := store.ListProjectTargets(ctx, owner, project.ProjectID)
		require.NoError(t, listErr)
		assert.ElementsMatch(t, []string{first.TargetID, second.TargetID}, targetIDs(targets))

		got, getErr := store.GetProjectTarget(ctx, owner, project.ProjectID, second.TargetID)
		require.NoError(t, getErr)
		assert.Equal(t, "~/feature", got.Cwd)
	})

	t.Run("given the same project agent and cwd when creating twice then the existing target is reused", func(t *testing.T) {
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
	})

	t.Run("given a replacement default then exactly one enabled target remains default", func(t *testing.T) {
		first, createErr := store.CreateProjectTarget(
			ctx,
			owner,
			project.ProjectID,
			CreateProjectTargetRequest{
				AgentID:     "agent_owner",
				DisplayName: "Default one",
				Cwd:         "~/one",
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
				DisplayName: "Default two",
				Cwd:         "~/two",
			},
		)
		require.NoError(t, createErr)

		makeDefault := true
		now = now.Add(time.Minute)
		second, updateErr := store.UpdateProjectTarget(
			ctx,
			owner,
			project.ProjectID,
			second.TargetID,
			UpdateProjectTargetRequest{IsDefault: &makeDefault},
		)
		require.NoError(t, updateErr)
		assert.True(t, second.IsDefault)
		assert.Equal(t, now, second.UpdatedAt)

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
		assert.False(t, second.IsDefault)
		assert.False(t, second.Enabled)
	})

	t.Run("given an unowned agent or project then target access is rejected", func(t *testing.T) {
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

		other := UserPrincipal{User: User{UserID: "user_other"}}
		_, listErr := store.ListProjectTargets(ctx, other, project.ProjectID)
		require.ErrorIs(t, listErr, ErrNotFound)
	})

	t.Run("given editable fields when updating then persisted values change together", func(t *testing.T) {
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

		got, getErr := store.GetProjectTarget(ctx, owner, project.ProjectID, target.TargetID)
		require.NoError(t, getErr)
		assert.Equal(t, updated.AgentID, got.AgentID)
		assert.Equal(t, updated.Cwd, got.Cwd)

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
			UpdateProjectTargetRequest{DisplayName: &blank},
		)
		require.ErrorIs(t, updateErr, ErrConflict)
	})

	t.Run("given a disabled default then creation rejects the invalid state", func(t *testing.T) {
		disabled := false
		_, createErr := store.CreateProjectTarget(
			ctx,
			owner,
			project.ProjectID,
			CreateProjectTargetRequest{
				AgentID:     "agent_owner",
				DisplayName: "Disabled default",
				Cwd:         "~/disabled",
				IsDefault:   true,
				Enabled:     &disabled,
			},
		)
		require.ErrorIs(t, createErr, ErrConflict)
	})
}
