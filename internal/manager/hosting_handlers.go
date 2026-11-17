package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

func (s *Service) configureHosting(base Store) {
	store, _ := base.(hosting.Store)
	c := s.cfg
	config := hosting.Config{
		HostKey:                 c.HostingHostKey,
		TokenSecret:             c.HostingTokenSecret,
		PublicURL:               c.HostingPublicURL,
		CloudURL:                c.HostingCloudURL,
		GatewayURL:              c.HostingGatewayURL,
		GatewayKey:              c.HostingGatewayKey,
		Model:                   c.HostingModel,
		GrantMicrousd:           int64(c.HostingGrantMicrousd),
		GrantTokens:             int64(c.HostingGrantTokens),
		GlobalMicrousd:          int64(c.HostingGlobalMicrousd),
		InputPrice:              int64(c.HostingInputPrice),
		OutputPrice:             int64(c.HostingOutputPrice),
		MaxOutputTokens:         int64(c.HostingMaxOutputTokens),
		LeaseTTL:                180 * time.Second,
		RequestsPerMinute:       c.HostingRequestsPerMinute,
		GlobalRequestsPerMinute: c.HostingGlobalRequestsPerMinute,
	}
	if c.HostingModelsFile != "" {
		registry, err := hosting.LoadModelRegistry(c.HostingModelsFile)
		if err != nil {
			config.Model = ""
		} else {
			config.Model, config.Models = registry.DefaultModel, registry.Models
			for _, policy := range registry.Models {
				if policy.ID == config.Model {
					config.MaxOutputTokens = policy.MaxOutputTokens
				}
			}
		}
	}

	s.hosting = hosting.New(
		&config,
		store,
		func() time.Time { return s.clock() },
		func(ctx context.Context, owner, nodeID, agentID string) error {
			user, err := s.store.GetUser(ctx, owner)
			if err != nil {
				return err
			}
			node, err := s.store.GetNode(ctx, domain.UserPrincipal{User: user}, nodeID)
			if err != nil || node.OwnerUserID != owner {
				return domain.ErrUnauthorized
			}
			agent, err := s.store.GetNodeAgent(ctx, nodeID, agentID)
			if err != nil || agent.OwnerUserID != owner || agent.NodeID != nodeID {
				return domain.ErrUnauthorized
			}
			return nil
		},
	)
}

func hostingError(ctx *app.RequestContext, err error) {
	status, message := http.StatusInternalServerError, "starter_unavailable"
	switch {
	case errors.Is(err, hosting.ErrRateLimited):
		status, message = http.StatusTooManyRequests, "starter_rate_limited"
		ctx.Header("Retry-After", "60")
	case errors.Is(err, hosting.ErrPlatformBudget):
		status, message = http.StatusServiceUnavailable, "starter_platform_budget_exhausted"
	case errors.Is(err, hosting.ErrDisabled):
		status, message = http.StatusServiceUnavailable, "starter_disabled"
	case errors.Is(err, hosting.ErrBudget):
		status, message = http.StatusPaymentRequired, "starter_quota_exhausted"
	case errors.Is(err, hosting.ErrAccessPaused):
		status, message = http.StatusForbidden, "starter_access_paused"
	case errors.Is(err, hosting.ErrConflict):
		status, message = http.StatusConflict, "starter_lease_conflict"
	case errors.Is(err, hosting.ErrUnauthorized):
		status, message = http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, hosting.ErrNotFound):
		status, message = http.StatusNotFound, "starter_not_found"
	}
	writeError(ctx, status, message)
}

func starterResult(c context.Context, ctx *app.RequestContext, action string) {
	s := serviceFromContext(ctx)
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	var result *hosting.Status
	if action == "wake" {
		result, err = s.hosting.Wake(c, principal.User.UserID)
	} else {
		result, err = s.hosting.Status(c, principal.User.UserID, action == "ensure")
	}
	if err != nil {
		hostingError(ctx, err)
		return
	}
	ctx.Header("Cache-Control", "no-store")
	writeData(ctx, http.StatusOK, result)
}

func GetStarter(c context.Context, ctx *app.RequestContext)    { starterResult(c, ctx, "status") }
func EnsureStarter(c context.Context, ctx *app.RequestContext) { starterResult(c, ctx, "ensure") }
func WakeStarter(c context.Context, ctx *app.RequestContext)   { starterResult(c, ctx, "wake") }

func hostingHost(c context.Context, ctx *app.RequestContext) (*Service, bool) {
	s := serviceFromContext(ctx)
	if !s.hosting.HostAuthorized(auth.BearerToken(string(ctx.GetHeader("Authorization")))) {
		writeError(ctx, http.StatusUnauthorized, "unauthorized")
		return s, false
	}
	return s, true
}
func ClaimHostingJob(c context.Context, ctx *app.RequestContext) {
	s, ok := hostingHost(c, ctx)
	if !ok {
		return
	}
	var req hosting.Claim
	if err := json.Unmarshal(ctx.Request.Body(), &req); err != nil {
		writeError(ctx, 400, "invalid JSON body")
		return
	}
	job, err := s.hosting.Claim(c, &req)
	if err != nil {
		hostingError(ctx, err)
		return
	}
	ctx.Header("Cache-Control", "no-store")
	writeData(ctx, 200, map[string]any{"job": job})
}
func updateHostingJob(c context.Context, ctx *app.RequestContext, complete bool) {
	s, ok := hostingHost(c, ctx)
	if !ok {
		return
	}
	var req hosting.Update
	if err := json.Unmarshal(ctx.Request.Body(), &req); err != nil {
		writeError(ctx, 400, "invalid JSON body")
		return
	}
	expiry, err := s.hosting.Update(c, &req, complete)
	if err != nil {
		hostingError(ctx, err)
		return
	}
	ctx.Header("Cache-Control", "no-store")
	writeData(ctx, 200, map[string]any{"permitted": true, "lease_expires_at": expiry})
}

func HeartbeatHostingJob(
	c context.Context,
	ctx *app.RequestContext,
) {
	updateHostingJob(c, ctx, false)
}

func CompleteHostingJob(
	c context.Context,
	ctx *app.RequestContext,
) {
	updateHostingJob(c, ctx, true)
}
