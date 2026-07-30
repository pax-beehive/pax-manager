package userapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	projectTargetDisplayNameLimit = 100
	projectTargetCwdLimit         = 4096
)

func (s *Service) CreateProjectTarget(
	ctx context.Context,
	meta auth.RequestMetadata,
	projectID string,
	req domain.CreateProjectTargetRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	projectID, err = requireProjectID(projectID)
	if err != nil {
		return 0, nil, err
	}
	req, err = normalizeCreateProjectTargetRequest(req)
	if err != nil {
		return 0, nil, err
	}
	target, err := s.store.CreateProjectTarget(ctx, principal, projectID, req)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, map[string]any{"target": target}, nil
}

func (s *Service) ListProjectTargets(
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
	targets, err := s.store.ListProjectTargets(ctx, principal, projectID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"targets": targets}, nil
}

func (s *Service) GetProjectTarget(
	ctx context.Context,
	meta auth.RequestMetadata,
	projectID string,
	targetID string,
) (int, any, error) {
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	projectID, err = requireProjectID(projectID)
	if err != nil {
		return 0, nil, err
	}
	targetID, err = requireProjectTargetID(targetID)
	if err != nil {
		return 0, nil, err
	}
	target, err := s.store.GetProjectTarget(ctx, principal, projectID, targetID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"target": target}, nil
}

func (s *Service) UpdateProjectTarget(
	ctx context.Context,
	meta auth.RequestMetadata,
	projectID string,
	targetID string,
	req domain.UpdateProjectTargetRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	projectID, err = requireProjectID(projectID)
	if err != nil {
		return 0, nil, err
	}
	targetID, err = requireProjectTargetID(targetID)
	if err != nil {
		return 0, nil, err
	}
	req, err = normalizeUpdateProjectTargetRequest(req)
	if err != nil {
		return 0, nil, err
	}
	target, err := s.store.UpdateProjectTarget(ctx, principal, projectID, targetID, req)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"target": target}, nil
}

func normalizeCreateProjectTargetRequest(
	req domain.CreateProjectTargetRequest,
) (domain.CreateProjectTargetRequest, error) {
	var err error
	req.AgentID, err = normalizeRequiredProjectTargetField("agent_id", req.AgentID, 512)
	if err != nil {
		return domain.CreateProjectTargetRequest{}, err
	}
	req.DisplayName, err = normalizeRequiredProjectTargetField(
		"display_name",
		req.DisplayName,
		projectTargetDisplayNameLimit,
	)
	if err != nil {
		return domain.CreateProjectTargetRequest{}, err
	}
	req.Cwd, err = normalizeRequiredProjectTargetField("cwd", req.Cwd, projectTargetCwdLimit)
	if err != nil {
		return domain.CreateProjectTargetRequest{}, err
	}
	if req.IsDefault && req.Enabled != nil && !*req.Enabled {
		return domain.CreateProjectTargetRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "a default target must be enabled",
		}
	}
	return req, nil
}

func normalizeUpdateProjectTargetRequest(
	req domain.UpdateProjectTargetRequest,
) (domain.UpdateProjectTargetRequest, error) {
	fields := []struct {
		name  string
		value **string
		limit int
	}{
		{name: "agent_id", value: &req.AgentID, limit: 512},
		{name: "display_name", value: &req.DisplayName, limit: projectTargetDisplayNameLimit},
		{name: "cwd", value: &req.Cwd, limit: projectTargetCwdLimit},
	}
	for _, field := range fields {
		if *field.value == nil {
			continue
		}
		value, err := normalizeRequiredProjectTargetField(
			field.name,
			**field.value,
			field.limit,
		)
		if err != nil {
			return domain.UpdateProjectTargetRequest{}, err
		}
		*field.value = &value
	}
	if req.IsDefault != nil && *req.IsDefault &&
		req.Enabled != nil && !*req.Enabled {
		return domain.UpdateProjectTargetRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "a default target must be enabled",
		}
	}
	return req, nil
}

func normalizeRequiredProjectTargetField(name string, value string, limit int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", apperr.Error{
			Status:  http.StatusBadRequest,
			Message: name + " is required",
		}
	}
	if len(value) > limit {
		return "", apperr.Error{
			Status:  http.StatusBadRequest,
			Message: name + " is too long",
		}
	}
	return value, nil
}

func requireProjectTargetID(targetID string) (string, error) {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return "", apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "target_id is required",
		}
	}
	return targetID, nil
}
