package userapi_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
	userapimocks "github.com/pax-beehive/pax-manager/internal/manager/userapi/mocks"
)

func TestProjectCRUDServiceBDD(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	principal := userPrincipal("usr_owner", false)
	store := storage.NewMemoryStore(fixedUserClock)
	principals := userapimocks.NewMockPrincipalResolver(t)
	principals.EXPECT().
		Principal(ctx, auth.RequestMetadata{}).
		Return(principal, nil).
		Times(8)
	svc := userapi.NewService(
		store,
		fixedUserClock,
		principals,
		userapimocks.NewMockSecretIssuer(t),
	)

	t.Run(
		"given an owner when creating a hierarchy then CRUD responses preserve it",
		func(t *testing.T) {
			status, data, err := svc.CreateProject(
				ctx,
				auth.RequestMetadata{},
				domain.CreateProjectRequest{
					DisplayName: " Root ",
				},
			)
			require.NoError(t, err)
			assert.Equal(t, http.StatusCreated, status)
			root := data.(map[string]any)["project"].(domain.Project)
			assert.Equal(t, "Root", root.DisplayName)
			assert.Equal(t, principal.User.UserID, root.OwnerUserID)

			status, data, err = svc.CreateProject(
				ctx,
				auth.RequestMetadata{},
				domain.CreateProjectRequest{
					DisplayName:     "Child",
					ParentProjectID: root.ProjectID,
				},
			)
			require.NoError(t, err)
			assert.Equal(t, http.StatusCreated, status)
			child := data.(map[string]any)["project"].(domain.Project)
			assert.Equal(t, root.ProjectID, child.ParentProjectID)

			status, data, err = svc.ListProjects(ctx, auth.RequestMetadata{}, false)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, status)
			assert.Len(t, data.(map[string]any)["projects"], 2)

			status, data, err = svc.GetProject(ctx, auth.RequestMetadata{}, child.ProjectID)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, status)
			assert.Equal(
				t,
				child.ProjectID,
				data.(map[string]any)["project"].(domain.Project).ProjectID,
			)

			name := " Renamed "
			rootParent := ""
			status, data, err = svc.UpdateProject(
				ctx,
				auth.RequestMetadata{},
				child.ProjectID,
				domain.UpdateProjectRequest{DisplayName: &name, ParentProjectID: &rootParent},
			)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, status)
			updated := data.(map[string]any)["project"].(domain.Project)
			assert.Equal(t, "Renamed", updated.DisplayName)
			assert.Empty(t, updated.ParentProjectID)

			status, data, err = svc.ArchiveProject(ctx, auth.RequestMetadata{}, root.ProjectID)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, status)
			assert.NotNil(t, data.(map[string]any)["project"].(domain.Project).ArchivedAt)

			_, data, err = svc.ListProjects(ctx, auth.RequestMetadata{}, false)
			require.NoError(t, err)
			assert.Len(t, data.(map[string]any)["projects"], 1)

			_, data, err = svc.ListProjects(ctx, auth.RequestMetadata{}, true)
			require.NoError(t, err)
			assert.Len(t, data.(map[string]any)["projects"], 2)
		},
	)
}

func TestProjectServiceValidationBDD(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	principal := userPrincipal("usr_owner", false)
	store := storage.NewMemoryStore(fixedUserClock)
	principals := userapimocks.NewMockPrincipalResolver(t)
	principals.EXPECT().
		Principal(ctx, auth.RequestMetadata{}).
		Return(principal, nil).
		Times(3)
	svc := userapi.NewService(
		store,
		fixedUserClock,
		principals,
		userapimocks.NewMockSecretIssuer(t),
	)

	t.Run("given invalid names then create and update return bad request", func(t *testing.T) {
		_, _, err := svc.CreateProject(ctx, auth.RequestMetadata{}, domain.CreateProjectRequest{
			DisplayName: " ",
		})
		var apiErr apperr.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusBadRequest, apiErr.Status)

		_, data, err := svc.CreateProject(ctx, auth.RequestMetadata{}, domain.CreateProjectRequest{
			DisplayName: "Valid",
		})
		require.NoError(t, err)
		project := data.(map[string]any)["project"].(domain.Project)

		blank := "\t"
		_, _, err = svc.UpdateProject(
			ctx,
			auth.RequestMetadata{},
			project.ProjectID,
			domain.UpdateProjectRequest{DisplayName: &blank},
		)
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusBadRequest, apiErr.Status)
	})
}

func TestProjectServiceErrorsBDD(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	meta := auth.RequestMetadata{}
	principal := userPrincipal("usr_owner", false)
	storeErr := errors.New("database unavailable")

	newService := func(t *testing.T) (*userapi.Service, *userapimocks.MockStore) {
		t.Helper()
		store := userapimocks.NewMockStore(t)
		principals := userapimocks.NewMockPrincipalResolver(t)
		principals.EXPECT().Principal(ctx, meta).Return(principal, nil).Once()
		return userapi.NewService(
			store,
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		), store
	}

	t.Run("given a list storage failure then the error is propagated", func(t *testing.T) {
		svc, store := newService(t)
		store.EXPECT().
			ListProjects(ctx, principal, domain.ListProjectsFilter{}).
			Return(nil, storeErr).
			Once()

		_, _, err := svc.ListProjects(ctx, meta, false)
		require.ErrorIs(t, err, storeErr)
	})

	t.Run("given a get storage failure then the error is propagated", func(t *testing.T) {
		svc, store := newService(t)
		store.EXPECT().
			GetProject(ctx, principal, "proj_1").
			Return(domain.Project{}, storeErr).
			Once()

		_, _, err := svc.GetProject(ctx, meta, "proj_1")
		require.ErrorIs(t, err, storeErr)
	})

	t.Run("given an archive storage failure then the error is propagated", func(t *testing.T) {
		svc, store := newService(t)
		store.EXPECT().
			ArchiveProject(ctx, principal, "proj_1").
			Return(domain.Project{}, storeErr).
			Once()

		_, _, err := svc.ArchiveProject(ctx, meta, "proj_1")
		require.ErrorIs(t, err, storeErr)
	})

	t.Run("given missing IDs then get and archive return bad request", func(t *testing.T) {
		svc, _ := newService(t)
		_, _, err := svc.GetProject(ctx, meta, " ")
		var apiErr apperr.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusBadRequest, apiErr.Status)

		svc, _ = newService(t)
		_, _, err = svc.ArchiveProject(ctx, meta, "")
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusBadRequest, apiErr.Status)
	})

	t.Run(
		"given names over the limit then create and update return bad request",
		func(t *testing.T) {
			longName := strings.Repeat("p", 101)
			svc, _ := newService(t)
			_, _, err := svc.CreateProject(ctx, meta, domain.CreateProjectRequest{
				DisplayName: longName,
			})
			var apiErr apperr.Error
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, http.StatusBadRequest, apiErr.Status)

			svc, _ = newService(t)
			_, _, err = svc.UpdateProject(ctx, meta, "proj_1", domain.UpdateProjectRequest{
				DisplayName: &longName,
			})
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, http.StatusBadRequest, apiErr.Status)
		},
	)
}
