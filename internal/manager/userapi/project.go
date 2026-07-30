package userapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const projectDisplayNameLimit = 100

func (s *Service) CreateProject(
	ctx context.Context,
	meta auth.RequestMetadata,
	req domain.CreateProjectRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	req.DisplayName, err = normalizeProjectDisplayName(req.DisplayName)
	if err != nil {
		return 0, nil, err
	}
	req.ParentProjectID = strings.TrimSpace(req.ParentProjectID)
	project, err := s.store.CreateProject(ctx, principal, req)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, map[string]any{"project": project}, nil
}

func (s *Service) ListProjects(
	ctx context.Context,
	meta auth.RequestMetadata,
	includeArchived bool,
) (int, any, error) {
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	projects, err := s.store.ListProjects(ctx, principal, domain.ListProjectsFilter{
		IncludeArchived: includeArchived,
	})
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"projects": projects}, nil
}

func (s *Service) GetProject(
	ctx context.Context,
	meta auth.RequestMetadata,
	projectID string,
) (int, any, error) {
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	projectID, err = requireProjectID(projectID)
	if err != nil {
		return 0, nil, err
	}
	project, err := s.store.GetProject(ctx, principal, projectID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"project": project}, nil
}

func (s *Service) UpdateProject(
	ctx context.Context,
	meta auth.RequestMetadata,
	projectID string,
	req domain.UpdateProjectRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	projectID, err = requireProjectID(projectID)
	if err != nil {
		return 0, nil, err
	}
	if req.DisplayName != nil {
		displayName, normalizeErr := normalizeProjectDisplayName(*req.DisplayName)
		if normalizeErr != nil {
			return 0, nil, normalizeErr
		}
		req.DisplayName = &displayName
	}
	if req.ParentProjectID != nil {
		parentProjectID := strings.TrimSpace(*req.ParentProjectID)
		req.ParentProjectID = &parentProjectID
	}
	project, err := s.store.UpdateProject(ctx, principal, projectID, req)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"project": project}, nil
}

func (s *Service) ArchiveProject(
	ctx context.Context,
	meta auth.RequestMetadata,
	projectID string,
) (int, any, error) {
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	projectID, err = requireProjectID(projectID)
	if err != nil {
		return 0, nil, err
	}
	project, err := s.store.ArchiveProject(ctx, principal, projectID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"project": project}, nil
}

func normalizeProjectDisplayName(displayName string) (string, error) {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return "", apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "display_name is required",
		}
	}
	if len(displayName) > projectDisplayNameLimit {
		return "", apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "display_name is too long",
		}
	}
	return displayName, nil
}

func requireProjectID(projectID string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "project_id is required",
		}
	}
	return projectID, nil
}
