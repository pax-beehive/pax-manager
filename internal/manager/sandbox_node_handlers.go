package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
	"github.com/pax-beehive/pax-manager/internal/manager/runtimeconfig"
)

func (s *Service) configureSandboxNodes(base Store) {
	store, _ := base.(hosting.SandboxStore)
	cfg := s.cfg
	config := hosting.SandboxConfig{
		HostKey: cfg.HostingSandboxHostKey, HostID: cfg.HostingSandboxHostID,
		CloudURL: cfg.HostingSandboxCloudURL,
	}
	s.sandboxNodes = hosting.NewSandbox(
		store,
		config,
		func() time.Time { return s.clock() },
		func(ctx context.Context, owner string) bool {
			return s.featureForOwner(ctx, owner, runtimeconfig.FeatureHostedSandbox)
		},
		func(ctx context.Context, owner, nodeID string) error {
			user, err := s.store.GetUser(ctx, owner)
			if err != nil {
				return err
			}
			node, err := s.store.GetNode(ctx, domain.UserPrincipal{User: user}, nodeID)
			if err != nil || node.OwnerUserID != owner {
				return domain.ErrUnauthorized
			}
			return nil
		},
	)
}

func hostingJobHost(ctx *app.RequestContext) (*Service, string, bool) {
	s := serviceFromContext(ctx)
	bearer := auth.BearerToken(string(ctx.GetHeader("Authorization")))
	if s.sandboxNodes.HostAuthorized(bearer) {
		return s, hosting.SandboxKind, true
	}
	if s.hosting.HostAuthorized(bearer) {
		return s, "", true
	}
	writeError(ctx, http.StatusUnauthorized, "unauthorized")
	return s, "", false
}

func validHostingJobKind(ctx *app.RequestContext, expected, received string) bool {
	if expected == received ||
		(expected == hosting.SandboxKind && received == hosting.SandboxModelKind) {
		return true
	}
	writeError(ctx, http.StatusUnauthorized, "unauthorized")
	return false
}

func sandboxNodeResult(c context.Context, ctx *app.RequestContext, wake bool) {
	s := serviceFromContext(ctx)
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	ctx.Header("Cache-Control", "no-store")
	if wake {
		if raw := bytes.TrimSpace(ctx.Request.Body()); len(raw) != 0 {
			var body map[string]json.RawMessage
			if err := json.Unmarshal(raw, &body); err != nil || body == nil || len(body) != 0 {
				writeError(ctx, http.StatusBadRequest, "invalid sandbox request")
				return
			}
		}
	}
	result, err := s.sandboxNodes.Status(c, principal.User.UserID, wake)
	if errors.Is(err, hosting.ErrNotFound) || errors.Is(err, hosting.ErrDisabled) {
		writeError(ctx, http.StatusNotFound, "sandbox_unavailable")
		return
	}
	if err != nil {
		writeError(ctx, http.StatusInternalServerError, "sandbox_unavailable")
		return
	}
	writeData(ctx, http.StatusOK, result)
}

func GetSandboxNode(
	c context.Context,
	ctx *app.RequestContext,
) {
	sandboxNodeResult(c, ctx, false)
}

func StartSandboxNode(
	c context.Context,
	ctx *app.RequestContext,
) {
	sandboxNodeResult(c, ctx, true)
}
