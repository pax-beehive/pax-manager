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
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
	userapimocks "github.com/pax-beehive/pax-manager/internal/manager/userapi/mocks"
)

func TestProjectTargetCRUDServiceBDD(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	meta := auth.RequestMetadata{}
	principal := userPrincipal("usr_owner", false)
	store := userapimocks.NewMockStore(t)
	principals := userapimocks.NewMockPrincipalResolver(t)
	principals.EXPECT().Principal(ctx, meta).Return(principal, nil).Times(4)
	svc := userapi.NewService(
		store,
		fixedUserClock,
		principals,
		userapimocks.NewMockSecretIssuer(t),
	)
	target := domain.ProjectTarget{
		TargetID:    "ptgt_1",
		ProjectID:   "proj_1",
		AgentID:     "agent_1",
		DisplayName: "Mac main",
		Cwd:         "~/pax_workspace/pax-manager",
		Enabled:     true,
	}

	t.Run("given a target when creating listing getting and updating then wire data stays nested", func(t *testing.T) {
		enabled := true
		store.EXPECT().
			CreateProjectTarget(
				ctx,
				principal,
				"proj_1",
				domain.CreateProjectTargetRequest{
					AgentID:     "agent_1",
					DisplayName: "Mac main",
					Cwd:         "~/pax_workspace/pax-manager",
					Enabled:     &enabled,
				},
			).
			Return(target, nil).
			Once()

		status, data, err := svc.CreateProjectTarget(
			ctx,
			meta,
			" proj_1 ",
			domain.CreateProjectTargetRequest{
				AgentID:     " agent_1 ",
				DisplayName: " Mac main ",
				Cwd:         " ~/pax_workspace/pax-manager ",
				Enabled:     &enabled,
			},
		)
		require.NoError(t, err)
		assert.Equal(t, http.StatusCreated, status)
		assert.Equal(t, target, data.(map[string]any)["target"])

		store.EXPECT().
			ListProjectTargets(ctx, principal, "proj_1").
			Return([]domain.ProjectTarget{target}, nil).
			Once()
		status, data, err = svc.ListProjectTargets(ctx, meta, "proj_1")
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, []domain.ProjectTarget{target}, data.(map[string]any)["targets"])

		store.EXPECT().
			GetProjectTarget(ctx, principal, "proj_1", "ptgt_1").
			Return(target, nil).
			Once()
		status, data, err = svc.GetProjectTarget(ctx, meta, "proj_1", "ptgt_1")
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, target, data.(map[string]any)["target"])

		name := " Mac feature "
		cwd := " ~/worktrees/feature "
		normalizedName := "Mac feature"
		normalizedCwd := "~/worktrees/feature"
		store.EXPECT().
			UpdateProjectTarget(
				ctx,
				principal,
				"proj_1",
				"ptgt_1",
				domain.UpdateProjectTargetRequest{
					DisplayName: &normalizedName,
					Cwd:         &normalizedCwd,
				},
			).
			Return(target, nil).
			Once()
		status, _, err = svc.UpdateProjectTarget(
			ctx,
			meta,
			"proj_1",
			"ptgt_1",
			domain.UpdateProjectTargetRequest{DisplayName: &name, Cwd: &cwd},
		)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, status)
	})
}

func TestCreateProjectTargetDefaultNameBDD(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	meta := auth.RequestMetadata{}
	principal := userPrincipal("usr_owner", false)

	tests := []struct {
		name        string
		cwd         string
		displayName string
	}{
		{name: "repository path", cwd: "/Users/kai/pax-manager", displayName: "pax-manager"},
		{name: "worktree path", cwd: "~/worktrees/kev-8", displayName: "kev-8"},
		{name: "home path", cwd: "~", displayName: "Home"},
		{name: "root path", cwd: "/", displayName: "Root"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().Principal(ctx, meta).Return(principal, nil).Once()
			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			expected := domain.CreateProjectTargetRequest{
				AgentID:     "agent_1",
				DisplayName: tc.displayName,
				Cwd:         tc.cwd,
			}
			target := domain.ProjectTarget{
				TargetID:    "ptgt_1",
				ProjectID:   "proj_1",
				AgentID:     "agent_1",
				DisplayName: tc.displayName,
				Cwd:         tc.cwd,
			}
			store.EXPECT().
				CreateProjectTarget(ctx, principal, "proj_1", expected).
				Return(target, nil).
				Once()

			status, data, err := svc.CreateProjectTarget(
				ctx,
				meta,
				"proj_1",
				domain.CreateProjectTargetRequest{
					AgentID: "agent_1",
					Cwd:     tc.cwd,
				},
			)

			require.NoError(t, err)
			assert.Equal(t, http.StatusCreated, status)
			assert.Equal(t, target, data.(map[string]any)["target"])
		})
	}
}

func TestProjectTargetServiceValidationBDD(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	meta := auth.RequestMetadata{}
	principal := userPrincipal("usr_owner", false)

	newService := func(t *testing.T) *userapi.Service {
		t.Helper()
		principals := userapimocks.NewMockPrincipalResolver(t)
		principals.EXPECT().Principal(ctx, meta).Return(principal, nil).Once()
		return userapi.NewService(
			userapimocks.NewMockStore(t),
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
	}

	cases := []struct {
		name string
		req  domain.CreateProjectTargetRequest
	}{
		{
			name: "missing agent",
			req: domain.CreateProjectTargetRequest{
				DisplayName: "Target",
				Cwd:         "~/repo",
			},
		},
		{
			name: "missing cwd",
			req: domain.CreateProjectTargetRequest{
				AgentID:     "agent_1",
				DisplayName: "Target",
			},
		},
		{
			name: "cwd too long",
			req: domain.CreateProjectTargetRequest{
				AgentID:     "agent_1",
				DisplayName: "Target",
				Cwd:         strings.Repeat("x", 4097),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService(t)
			_, _, err := svc.CreateProjectTarget(ctx, meta, "proj_1", tc.req)
			var apiErr apperr.Error
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, http.StatusBadRequest, apiErr.Status)
		})
	}

	t.Run("given a disabled default then create and update reject it", func(t *testing.T) {
		disabled := false
		svc := newService(t)
		_, _, err := svc.CreateProjectTarget(
			ctx,
			meta,
			"proj_1",
			domain.CreateProjectTargetRequest{
				AgentID:     "agent_1",
				DisplayName: "Target",
				Cwd:         "~/repo",
				IsDefault:   true,
				Enabled:     &disabled,
			},
		)
		var apiErr apperr.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusBadRequest, apiErr.Status)

		makeDefault := true
		svc = newService(t)
		_, _, err = svc.UpdateProjectTarget(
			ctx,
			meta,
			"proj_1",
			"ptgt_1",
			domain.UpdateProjectTargetRequest{
				IsDefault: &makeDefault,
				Enabled:   &disabled,
			},
		)
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusBadRequest, apiErr.Status)
	})

	t.Run("given missing resource IDs then requests return bad request", func(t *testing.T) {
		svc := newService(t)
		_, _, err := svc.ListProjectTargets(ctx, meta, "")
		var apiErr apperr.Error
		require.ErrorAs(t, err, &apiErr)

		svc = newService(t)
		_, _, err = svc.GetProjectTarget(ctx, meta, "proj_1", " ")
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusBadRequest, apiErr.Status)
	})
}

func TestProjectTargetServicePropagatesStorageErrorsBDD(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	meta := auth.RequestMetadata{}
	principal := userPrincipal("usr_owner", false)
	storeErr := errors.New("target storage unavailable")

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

	t.Run("create", func(t *testing.T) {
		svc, store := newService(t)
		req := domain.CreateProjectTargetRequest{
			AgentID:     "agent_1",
			DisplayName: "Target",
			Cwd:         "~/repo",
		}
		store.EXPECT().
			CreateProjectTarget(ctx, principal, "proj_1", req).
			Return(domain.ProjectTarget{}, storeErr).
			Once()
		_, _, err := svc.CreateProjectTarget(ctx, meta, "proj_1", req)
		require.ErrorIs(t, err, storeErr)
	})

	t.Run("list", func(t *testing.T) {
		svc, store := newService(t)
		store.EXPECT().
			ListProjectTargets(ctx, principal, "proj_1").
			Return(nil, storeErr).
			Once()
		_, _, err := svc.ListProjectTargets(ctx, meta, "proj_1")
		require.ErrorIs(t, err, storeErr)
	})

	t.Run("get", func(t *testing.T) {
		svc, store := newService(t)
		store.EXPECT().
			GetProjectTarget(ctx, principal, "proj_1", "ptgt_1").
			Return(domain.ProjectTarget{}, storeErr).
			Once()
		_, _, err := svc.GetProjectTarget(ctx, meta, "proj_1", "ptgt_1")
		require.ErrorIs(t, err, storeErr)
	})

	t.Run("update", func(t *testing.T) {
		svc, store := newService(t)
		req := domain.UpdateProjectTargetRequest{}
		store.EXPECT().
			UpdateProjectTarget(ctx, principal, "proj_1", "ptgt_1", req).
			Return(domain.ProjectTarget{}, storeErr).
			Once()
		_, _, err := svc.UpdateProjectTarget(ctx, meta, "proj_1", "ptgt_1", req)
		require.ErrorIs(t, err, storeErr)
	})
}
