package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/logging"
	"github.com/pax-beehive/pax-manager/internal/manager/runtimeconfig"
)

const (
	routeAdminConfig       = "/api/v1/admin/config"
	routeAdminConfigKey    = "/api/v1/admin/config/:key"
	routeAdminConfigTest   = "/api/v1/admin/config/test"
	routeAdminConfigAudit  = "/api/v1/admin/config/audit"
	routeAdminConfigRevert = "/api/v1/admin/config/audit/:id/revert"
	routeUserFeatures      = "/api/v1/user/self/features"
)

// writeErrorData keeps the standard envelope and adds a machine-readable
// data.error code (plus field_errors or version where relevant).
func writeErrorData(ctx *app.RequestContext, status int, message string, data any) {
	writeJSON(ctx, status, apiResponse{Data: data, Code: status, Message: message})
	ctx.Abort()
}

func writeRuntimeConfigError(c context.Context, ctx *app.RequestContext, err error) {
	var configErr *runtimeconfig.Error
	if errors.As(err, &configErr) {
		writeErrorData(ctx, configErr.Status, configErr.Message, configErr.Data())
		return
	}
	logging.Error(c, "runtime config request failed", logging.Err(err))
	writeError(ctx, http.StatusInternalServerError, "runtime config request failed")
}

// adminConfigPrincipal enforces IsAdmin for every /api/v1/admin/config route.
func (s *Service) adminConfigPrincipal(
	c context.Context,
	ctx *app.RequestContext,
) (UserPrincipal, bool) {
	ctx.Header("Cache-Control", "private, no-store")
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return UserPrincipal{}, false
	}
	if !principal.IsAdmin {
		writeErrorData(ctx, http.StatusForbidden, "administrator access required",
			map[string]string{"error": runtimeconfig.CodeNotAdmin})
		return UserPrincipal{}, false
	}
	return principal, true
}

func (s *Service) handleAdminConfigList(c context.Context, ctx *app.RequestContext) {
	if _, ok := s.adminConfigPrincipal(c, ctx); !ok {
		return
	}
	writeData(ctx, http.StatusOK, s.runtimeConfig.View())
}

type adminConfigPutRequest struct {
	Value           json.RawMessage `json:"value"`
	ExpectedVersion *int64          `json:"expected_version"`
	Reason          string          `json:"reason"`
}

func (s *Service) handleAdminConfigPut(c context.Context, ctx *app.RequestContext) {
	principal, ok := s.adminConfigPrincipal(c, ctx)
	if !ok {
		return
	}
	var request adminConfigPutRequest
	if json.Unmarshal(ctx.Request.Body(), &request) != nil {
		writeFieldError(ctx, "body", "must be a JSON object")
		return
	}
	if request.ExpectedVersion == nil {
		writeFieldError(ctx, "expected_version", "is required; use 0 when no override exists")
		return
	}
	entry, err := s.runtimeConfig.Set(c, ctx.Param("key"), request.Value,
		*request.ExpectedVersion, request.Reason, principal.User.Email)
	if err != nil {
		writeRuntimeConfigError(c, ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, entry)
}

func (s *Service) handleAdminConfigDelete(c context.Context, ctx *app.RequestContext) {
	principal, ok := s.adminConfigPrincipal(c, ctx)
	if !ok {
		return
	}
	expected, err := strconv.ParseInt(string(ctx.Query("expected_version")), 10, 64)
	if err != nil {
		writeFieldError(ctx, "expected_version", "is required")
		return
	}
	entry, err := s.runtimeConfig.Reset(c, ctx.Param("key"), expected,
		string(ctx.Query("reason")), principal.User.Email)
	if err != nil {
		writeRuntimeConfigError(c, ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, entry)
}

type adminConfigTestRequest struct {
	Group     string                     `json:"group"`
	Overrides map[string]json.RawMessage `json:"overrides"`
}

func (s *Service) handleAdminConfigTest(c context.Context, ctx *app.RequestContext) {
	if _, ok := s.adminConfigPrincipal(c, ctx); !ok {
		return
	}
	var request adminConfigTestRequest
	if json.Unmarshal(ctx.Request.Body(), &request) != nil {
		writeFieldError(ctx, "body", "must be a JSON object")
		return
	}
	result, err := s.runtimeConfig.Test(c, strings.TrimSpace(request.Group), request.Overrides)
	if err != nil {
		writeRuntimeConfigError(c, ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, result)
}

func (s *Service) handleAdminConfigAudit(c context.Context, ctx *app.RequestContext) {
	if _, ok := s.adminConfigPrincipal(c, ctx); !ok {
		return
	}
	limit := 0
	if raw := string(ctx.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeFieldError(ctx, "limit", "must be a positive integer")
			return
		}
		limit = parsed
	}
	page, err := s.runtimeConfig.Audit(
		c,
		string(ctx.Query("key")),
		string(ctx.Query("cursor")),
		limit,
	)
	if err != nil {
		writeRuntimeConfigError(c, ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, page)
}

type adminConfigRevertRequest struct {
	ExpectedVersion *int64 `json:"expected_version"`
	Reason          string `json:"reason"`
}

func (s *Service) handleAdminConfigRevert(c context.Context, ctx *app.RequestContext) {
	principal, ok := s.adminConfigPrincipal(c, ctx)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(ctx.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeFieldError(ctx, "id", "must be a positive integer")
		return
	}
	var request adminConfigRevertRequest
	if json.Unmarshal(ctx.Request.Body(), &request) != nil {
		writeFieldError(ctx, "body", "must be a JSON object")
		return
	}
	if request.ExpectedVersion == nil {
		writeFieldError(ctx, "expected_version", "is required")
		return
	}
	entry, err := s.runtimeConfig.Revert(c, id, *request.ExpectedVersion, request.Reason,
		principal.User.Email)
	if err != nil {
		writeRuntimeConfigError(c, ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, entry)
}

func writeFieldError(ctx *app.RequestContext, field, message string) {
	writeErrorData(ctx, http.StatusBadRequest, "validation failed", map[string]any{
		"error":        runtimeconfig.CodeValidationFailed,
		"field_errors": map[string]string{field: message},
	})
}

// handleUserFeatures never fails on storage errors: it evaluates the
// in-memory snapshot. as=user only changes the result for administrators.
func (s *Service) handleUserFeatures(c context.Context, ctx *app.RequestContext) {
	ctx.Header("Cache-Control", "private, no-store")
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if principal.IsAdmin && string(ctx.Query("as")) == "user" {
		principal.IsAdmin = false
	}
	snapshot := s.runtimeConfig.Snapshot()
	writeData(ctx, http.StatusOK, map[string]any{
		"revision": snapshot.Revision,
		"features": snapshot.Features(principal),
	})
}
