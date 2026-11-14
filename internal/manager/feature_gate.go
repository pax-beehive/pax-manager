package manager

import (
	"context"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/runtimeconfig"
)

// featureRoute gates every route whose template equals Path or starts with
// Path + "/". Methods restricts the rule when set.
type featureRoute struct {
	Feature string
	Path    string
	Methods []string
}

// featureRoutes is the server-side gate list. Hiding entries in Console is
// not enough; these routes return 404 feature_unavailable when disabled.
var featureRoutes = []featureRoute{
	// Secretary settings, run/retry, progress, summaries, tasks, guidance, operations.
	{Feature: runtimeconfig.FeatureSecretary, Path: "/api/v1/user/:user_id/secretary"},
	// Push receiver registration and test sends. Listing and revoking stay
	// open so users can always remove a receiver.
	{
		Feature: runtimeconfig.FeatureNotifications,
		Path:    "/api/v1/user/self/notifications/receivers/web-push",
		Methods: []string{http.MethodPut},
	},
	{
		Feature: runtimeconfig.FeatureNotifications,
		Path:    "/api/v1/user/self/notifications/receivers/:receiver_id/test",
		Methods: []string{http.MethodPost},
	},
	// Collaboration: teams (including team memex), invites, friends, envelopes.
	{Feature: runtimeconfig.FeatureCollaboration, Path: "/api/v1/user/:user_id/teams"},
	{Feature: runtimeconfig.FeatureCollaboration, Path: "/api/v1/user/:user_id/team-invites"},
	{Feature: runtimeconfig.FeatureCollaboration, Path: "/api/v1/user/:user_id/friends"},
	{Feature: runtimeconfig.FeatureCollaboration, Path: "/api/v1/user/:user_id/envelopes"},
	// Knowledge capsules and injections.
	{
		Feature: runtimeconfig.FeatureKnowledgeCapsules,
		Path:    "/api/v1/user/:user_id/knowledge-capsules",
	},
	{
		Feature: runtimeconfig.FeatureKnowledgeCapsules,
		Path:    "/api/v1/user/:user_id/sessions/:session_id/knowledge-capsules",
	},
	{
		Feature: runtimeconfig.FeatureKnowledgeCapsules,
		Path:    "/api/v1/user/:user_id/sessions/:session_id/knowledge-injections",
	},
}

func (r featureRoute) matches(method, template string) bool {
	if template != r.Path && !strings.HasPrefix(template, r.Path+"/") {
		return false
	}
	if len(r.Methods) == 0 {
		return true
	}
	for _, candidate := range r.Methods {
		if candidate == method {
			return true
		}
	}
	return false
}

// gatedFeature returns the feature guarding a route template, if any.
func gatedFeature(method, template string) (string, bool) {
	for _, route := range featureRoutes {
		if route.matches(method, template) {
			return route.Feature, true
		}
	}
	return "", false
}

// featureGate applies requireFeature to every route in featureRoutes,
// including generated IDL routes that cannot be wrapped individually.
func (s *Service) featureGate() app.HandlerFunc {
	return func(c context.Context, ctx *app.RequestContext) {
		feature, gated := gatedFeature(string(ctx.Method()), ctx.FullPath())
		if !gated {
			ctx.Next(c)
			return
		}
		next := func(c context.Context, ctx *app.RequestContext) { ctx.Next(c) }
		s.requireFeature(feature, next)(c, ctx)
	}
}

// requireFeature rejects callers for whom the feature is disabled. Requests
// that fail authentication pass through so the handler reports 401 as before.
func (s *Service) requireFeature(feature string, next app.HandlerFunc) app.HandlerFunc {
	return func(c context.Context, ctx *app.RequestContext) {
		principal, err := s.userPrincipal(c, ctx)
		if err == nil && !s.runtimeConfig.Feature(principal, feature) {
			writeErrorData(ctx, http.StatusNotFound, "feature unavailable", map[string]string{
				"error":   runtimeconfig.CodeFeatureUnavailable,
				"feature": feature,
			})
			return
		}
		next(c, ctx)
	}
}
