package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
			_, err := s.hostingAgentHarness(ctx, owner, nodeID, agentID)
			return err
		},
	)
	s.hosting.PreviousNodeRemoved = func(ctx context.Context, owner, nodeID string) (bool, error) {
		principal := domain.UserPrincipal{User: domain.User{UserID: owner}}
		_, err := s.store.GetNode(ctx, principal, nodeID)
		if errors.Is(err, domain.ErrNotFound) {
			return true, nil
		}
		return false, err
	}
	s.configureSandboxNodes(base)
	s.hosting.Sandbox = s.sandboxNodes
	s.hosting.ModelAgentHarness = s.hostingAgentHarness
}

func (s *Service) hostingAgentHarness(
	ctx context.Context,
	owner, nodeID, agentID string,
) (string, error) {
	user, err := s.store.GetUser(ctx, owner)
	if err != nil {
		return "", err
	}
	node, err := s.store.GetNode(ctx, domain.UserPrincipal{User: user}, nodeID)
	if err != nil || node.OwnerUserID != owner {
		return "", domain.ErrUnauthorized
	}
	agent, err := s.store.GetNodeAgent(ctx, nodeID, agentID)
	if err != nil || agent.OwnerUserID != owner || agent.NodeID != nodeID {
		return "", domain.ErrUnauthorized
	}
	return agent.AgentType, nil
}

func hostingError(ctx *app.RequestContext, err error) {
	status, message := http.StatusInternalServerError, "starter_unavailable"
	switch {
	case errors.Is(err, hosting.ErrReasoningEffort):
		status, message = http.StatusBadRequest, "invalid_reasoning_effort"
	case errors.Is(err, hosting.ErrInvalidModelRequest):
		status, message = http.StatusBadRequest, "invalid_model_request"
	case errors.Is(err, hosting.ErrModelDisabled):
		status, message = http.StatusForbidden, "model_disabled"
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
	owner := principal.User.UserID
	if action == "wake" {
		target, valid := parseHostingWakeTarget(ctx)
		if !valid {
			return
		}
		if target != nil {
			result, err = wakeStarterTarget(c, s, owner, target)
		} else {
			result, err = s.hosting.Wake(c, owner)
		}
	} else {
		result, err = s.hosting.Status(c, owner, action == "ensure")
	}
	if err == nil {
		err = s.hosting.IncludeSandboxModelAccess(c, owner, result)
	}
	if err != nil {
		hostingError(ctx, err)
		return
	}
	ctx.Header("Cache-Control", "no-store")
	writeData(ctx, http.StatusOK, result)
}

func wakeStarterTarget(
	ctx context.Context, s *Service, owner string, target *hosting.WakeTarget,
) (*hosting.Status, error) {
	matched, err := s.hosting.WakeSandboxModel(ctx, owner, target)
	if err != nil {
		return nil, err
	}
	if matched {
		return s.hosting.Status(ctx, owner, false)
	}
	if !ownedPiAgent(ctx, s, owner, target) {
		return nil, hosting.ErrUnauthorized
	}
	return s.hosting.WakeForAgent(ctx, owner, target)
}

func parseHostingWakeTarget(ctx *app.RequestContext) (*hosting.WakeTarget, bool) {
	raw := bytes.TrimSpace(ctx.Request.Body())
	if len(raw) == 0 {
		return nil, true
	}
	var fields struct {
		NodeID  json.RawMessage `json:"node_id"`
		AgentID json.RawMessage `json:"agent_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fields); err != nil || decoder.Decode(new(any)) != io.EOF {
		writeError(ctx, http.StatusBadRequest, "invalid Starter target")
		return nil, false
	}
	if fields.NodeID == nil && fields.AgentID == nil {
		return nil, true
	}
	var target hosting.WakeTarget
	if json.Unmarshal(fields.NodeID, &target.NodeID) != nil ||
		json.Unmarshal(fields.AgentID, &target.AgentID) != nil ||
		target.NodeID == "" || target.AgentID == "" {
		writeError(ctx, http.StatusBadRequest, "invalid Starter target")
		return nil, false
	}
	return &target, true
}

func ownedPiAgent(c context.Context, s *Service, owner string, target *hosting.WakeTarget) bool {
	agent, err := s.store.GetNodeAgent(c, target.NodeID, target.AgentID)
	return err == nil && agent.OwnerUserID == owner && agent.NodeID == target.NodeID &&
		agent.AgentType == "pi"
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

func EnrollHostingNode(c context.Context, ctx *app.RequestContext) {
	s, ok := hostingHost(c, ctx)
	if !ok {
		return
	}
	var req struct {
		HostID string `json:"host_id"`
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(ctx.Request.Body(), &req); err != nil || req.NodeID == "" {
		writeError(ctx, http.StatusBadRequest, "invalid Node enrollment")
		return
	}
	key := string(ctx.GetHeader("X-Pax-Key"))
	if key == "" {
		hostingError(ctx, hosting.ErrUnauthorized)
		return
	}
	node, err := s.store.AuthenticateNode(c, s.secrets.Hash(key))
	if err != nil || node.NodeID != req.NodeID {
		hostingError(ctx, hosting.ErrUnauthorized)
		return
	}
	result, err := s.hosting.EnrollNode(c, node.OwnerUserID, node.NodeID, req.HostID)
	if err != nil {
		hostingError(ctx, err)
		return
	}
	ctx.Header("Cache-Control", "no-store")
	writeData(ctx, http.StatusOK, result)
}

func ClaimHostingJob(c context.Context, ctx *app.RequestContext) {
	s, kind, ok := hostingJobHost(ctx)
	if !ok {
		return
	}
	var body struct {
		hosting.Claim
		Capacity         *int            `json:"capacity"`
		OwnerScope       json.RawMessage `json:"owner_user_id"`
		EnvironmentScope json.RawMessage `json:"environment_id"`
	}
	if err := json.Unmarshal(ctx.Request.Body(), &body); err != nil {
		writeError(ctx, 400, "invalid JSON body")
		return
	}
	if !validHostingJobKind(ctx, kind, body.Kind) {
		return
	}
	if body.OwnerScope != nil || body.EnvironmentScope != nil {
		if json.Unmarshal(body.OwnerScope, &body.OwnerUserID) != nil ||
			json.Unmarshal(body.EnvironmentScope, &body.EnvironmentID) != nil ||
			body.OwnerUserID == nil || body.EnvironmentID == nil {
			hostingError(ctx, hosting.ErrConflict)
			return
		}
	}
	// An omitted capacity keeps the existing zero-capacity behavior without
	// claiming the worker measured zero free slots.
	body.CapacityMissing = body.Capacity == nil
	if body.Capacity != nil {
		body.Claim.Capacity = *body.Capacity
	}
	var job *hosting.Job
	var err error
	response := make(map[string]any, 2)
	switch body.Kind {
	case hosting.SandboxKind:
		var queuedCount int
		job, queuedCount, err = s.sandboxNodes.Claim(c, &body.Claim)
		response["queued_count"] = queuedCount
	case hosting.SandboxModelKind:
		job, err = s.hosting.ClaimSandboxModel(c, &body.Claim)
	default:
		job, err = s.hosting.Claim(c, &body.Claim)
	}
	if err != nil {
		hostingError(ctx, err)
		return
	}
	ctx.Header("Cache-Control", "no-store")
	response["job"] = job
	writeData(ctx, 200, response)
}
func updateHostingJob(c context.Context, ctx *app.RequestContext, complete bool) {
	s, kind, ok := hostingJobHost(ctx)
	if !ok {
		return
	}
	var req hosting.Update
	if err := json.Unmarshal(ctx.Request.Body(), &req); err != nil {
		writeError(ctx, 400, "invalid JSON body")
		return
	}
	if !validHostingJobKind(ctx, kind, req.Kind) {
		return
	}
	service := s.hosting.Update
	switch req.Kind {
	case hosting.SandboxKind:
		service = s.sandboxNodes.Update
	case hosting.SandboxModelKind:
		service = s.hosting.UpdateSandboxModel
	}
	expiry, err := service(c, &req, complete)
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
