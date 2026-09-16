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

func TestPostgresProjectLifecycleBDD(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(
		sqlite.Open("file:project-lifecycle?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&projectRow{}))

	ctx := context.Background()
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := &PostgresStore{gormDB: db, now: func() time.Time { return now }}
	owner := UserPrincipal{User: User{UserID: "user_owner"}}
	other := UserPrincipal{User: User{UserID: "user_other"}}

	t.Run(
		"given projects when creating listing and getting then owner scope is enforced",
		func(t *testing.T) {
			root, createErr := store.CreateProject(
				ctx,
				owner,
				CreateProjectRequest{DisplayName: " Root "},
			)
			require.NoError(t, createErr)
			assert.Equal(t, "Root", root.DisplayName)

			child, createErr := store.CreateProject(ctx, owner, CreateProjectRequest{
				DisplayName:     "Child",
				ParentProjectID: root.ProjectID,
			})
			require.NoError(t, createErr)

			projects, listErr := store.ListProjects(ctx, owner, ListProjectsFilter{})
			require.NoError(t, listErr)
			assert.ElementsMatch(t, []string{root.ProjectID, child.ProjectID}, projectIDs(projects))

			got, getErr := store.GetProject(ctx, owner, child.ProjectID)
			require.NoError(t, getErr)
			assert.Equal(t, child, got)

			_, getErr = store.GetProject(ctx, other, child.ProjectID)
			require.ErrorIs(t, getErr, ErrNotFound)

			_, createErr = store.CreateProject(ctx, other, CreateProjectRequest{
				DisplayName:     "Cross-owner child",
				ParentProjectID: root.ProjectID,
			})
			require.ErrorIs(t, createErr, ErrNotFound)
		},
	)

	t.Run(
		"given a hierarchy when updating then moves work and cycles are rejected",
		func(t *testing.T) {
			root, createErr := store.CreateProject(
				ctx,
				owner,
				CreateProjectRequest{DisplayName: "Move root"},
			)
			require.NoError(t, createErr)
			child, createErr := store.CreateProject(ctx, owner, CreateProjectRequest{
				DisplayName:     "Move child",
				ParentProjectID: root.ProjectID,
			})
			require.NoError(t, createErr)

			_, updateErr := store.UpdateProject(ctx, owner, root.ProjectID, UpdateProjectRequest{
				ParentProjectID: &child.ProjectID,
			})
			require.ErrorIs(t, updateErr, ErrConflict)

			now = now.Add(time.Minute)
			name := " Renamed "
			rootParent := ""
			updated, updateErr := store.UpdateProject(
				ctx,
				owner,
				child.ProjectID,
				UpdateProjectRequest{
					DisplayName:     &name,
					ParentProjectID: &rootParent,
				},
			)
			require.NoError(t, updateErr)
			assert.Equal(t, "Renamed", updated.DisplayName)
			assert.Empty(t, updated.ParentProjectID)
			assert.Equal(t, now, updated.UpdatedAt)
		},
	)

	t.Run(
		"given an archived project then it is filtered and cannot receive changes or children",
		func(t *testing.T) {
			project, createErr := store.CreateProject(
				ctx,
				owner,
				CreateProjectRequest{DisplayName: "Archive"},
			)
			require.NoError(t, createErr)

			now = now.Add(time.Minute)
			archived, archiveErr := store.ArchiveProject(ctx, owner, project.ProjectID)
			require.NoError(t, archiveErr)
			require.NotNil(t, archived.ArchivedAt)
			assert.Equal(t, now, *archived.ArchivedAt)

			active, listErr := store.ListProjects(ctx, owner, ListProjectsFilter{})
			require.NoError(t, listErr)
			assert.NotContains(t, projectIDs(active), project.ProjectID)
			all, listErr := store.ListProjects(
				ctx,
				owner,
				ListProjectsFilter{IncludeArchived: true},
			)
			require.NoError(t, listErr)
			assert.Contains(t, projectIDs(all), project.ProjectID)

			name := "No mutation"
			_, updateErr := store.UpdateProject(ctx, owner, project.ProjectID, UpdateProjectRequest{
				DisplayName: &name,
			})
			require.ErrorIs(t, updateErr, ErrConflict)

			_, createErr = store.CreateProject(ctx, owner, CreateProjectRequest{
				DisplayName:     "No child",
				ParentProjectID: project.ProjectID,
			})
			require.ErrorIs(t, createErr, ErrNotFound)

			_, archiveErr = store.ArchiveProject(ctx, other, project.ProjectID)
			require.ErrorIs(t, archiveErr, ErrNotFound)
		},
	)

	t.Run("given blank names then persistence rejects them", func(t *testing.T) {
		_, createErr := store.CreateProject(ctx, owner, CreateProjectRequest{DisplayName: " "})
		require.ErrorIs(t, createErr, ErrConflict)
	})
}
