package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryProjectLifecycleBDD(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner := UserPrincipal{User: User{UserID: "user_owner"}}

	t.Run(
		"given a root project when a child is created then hierarchy and ownership are preserved",
		func(t *testing.T) {
			root, err := store.CreateProject(
				ctx,
				owner,
				CreateProjectRequest{DisplayName: "  Pax  "},
			)
			require.NoError(t, err)
			assert.Equal(t, "Pax", root.DisplayName)
			assert.Equal(t, owner.User.UserID, root.OwnerUserID)
			assert.Empty(t, root.ParentProjectID)
			assert.Equal(t, now, root.CreatedAt)
			assert.Equal(t, now, root.UpdatedAt)

			child, err := store.CreateProject(ctx, owner, CreateProjectRequest{
				DisplayName:     "Console",
				ParentProjectID: root.ProjectID,
			})
			require.NoError(t, err)
			assert.Equal(t, root.ProjectID, child.ParentProjectID)

			projects, err := store.ListProjects(ctx, owner, ListProjectsFilter{})
			require.NoError(t, err)
			require.Len(t, projects, 2)
			assert.ElementsMatch(t, []string{root.ProjectID, child.ProjectID}, projectIDs(projects))
		},
	)

	t.Run(
		"given another owner when accessing a project then its existence is hidden",
		func(t *testing.T) {
			project, err := store.CreateProject(
				ctx,
				owner,
				CreateProjectRequest{DisplayName: "Private"},
			)
			require.NoError(t, err)
			other := UserPrincipal{User: User{UserID: "user_other"}}

			_, err = store.GetProject(ctx, other, project.ProjectID)
			require.ErrorIs(t, err, ErrNotFound)

			name := "Stolen"
			_, err = store.UpdateProject(
				ctx,
				other,
				project.ProjectID,
				UpdateProjectRequest{DisplayName: &name},
			)
			require.ErrorIs(t, err, ErrNotFound)

			_, err = store.ArchiveProject(ctx, other, project.ProjectID)
			require.ErrorIs(t, err, ErrNotFound)
		},
	)

	t.Run(
		"given a descendant when moving its ancestor below it then the cycle is rejected",
		func(t *testing.T) {
			root, err := store.CreateProject(
				ctx,
				owner,
				CreateProjectRequest{DisplayName: "Cycle root"},
			)
			require.NoError(t, err)
			child, err := store.CreateProject(ctx, owner, CreateProjectRequest{
				DisplayName:     "Cycle child",
				ParentProjectID: root.ProjectID,
			})
			require.NoError(t, err)

			_, err = store.UpdateProject(ctx, owner, root.ProjectID, UpdateProjectRequest{
				ParentProjectID: &child.ProjectID,
			})
			require.ErrorIs(t, err, ErrConflict)
		},
	)

	t.Run(
		"given an archived project when listing active projects then it is hidden without cascading",
		func(t *testing.T) {
			root, err := store.CreateProject(
				ctx,
				owner,
				CreateProjectRequest{DisplayName: "Archive root"},
			)
			require.NoError(t, err)
			child, err := store.CreateProject(ctx, owner, CreateProjectRequest{
				DisplayName:     "Archive child",
				ParentProjectID: root.ProjectID,
			})
			require.NoError(t, err)

			now = now.Add(time.Hour)
			archived, err := store.ArchiveProject(ctx, owner, root.ProjectID)
			require.NoError(t, err)
			require.NotNil(t, archived.ArchivedAt)
			assert.Equal(t, now, *archived.ArchivedAt)

			active, err := store.ListProjects(ctx, owner, ListProjectsFilter{})
			require.NoError(t, err)
			assert.NotContains(t, projectIDs(active), root.ProjectID)
			assert.Contains(t, projectIDs(active), child.ProjectID)

			all, err := store.ListProjects(ctx, owner, ListProjectsFilter{IncludeArchived: true})
			require.NoError(t, err)
			assert.Contains(t, projectIDs(all), root.ProjectID)

			_, err = store.CreateProject(ctx, owner, CreateProjectRequest{
				DisplayName:     "Rejected child",
				ParentProjectID: root.ProjectID,
			})
			require.ErrorIs(t, err, ErrNotFound)
		},
	)
}

func TestMemoryProjectUpdateBDD(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner := UserPrincipal{User: User{UserID: "user_owner"}}
	root, err := store.CreateProject(ctx, owner, CreateProjectRequest{DisplayName: "Root"})
	require.NoError(t, err)
	child, err := store.CreateProject(ctx, owner, CreateProjectRequest{
		DisplayName:     "Child",
		ParentProjectID: root.ProjectID,
	})
	require.NoError(t, err)

	t.Run(
		"given a child when renamed and moved to the root then both changes persist",
		func(t *testing.T) {
			now = now.Add(time.Minute)
			name := "  Renamed  "
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

	t.Run("given blank display names then create and update reject them", func(t *testing.T) {
		_, createErr := store.CreateProject(ctx, owner, CreateProjectRequest{DisplayName: "   "})
		require.ErrorIs(t, createErr, ErrConflict)

		blank := "\t"
		_, updateErr := store.UpdateProject(ctx, owner, child.ProjectID, UpdateProjectRequest{
			DisplayName: &blank,
		})
		require.ErrorIs(t, updateErr, ErrConflict)
	})
}

func projectIDs(projects []Project) []string {
	ids := make([]string, 0, len(projects))
	for _, project := range projects {
		ids = append(ids, project.ProjectID)
	}
	return ids
}
