package manager

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	hertzserver "github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	managerconfig "github.com/pax-beehive/pax-manager/internal/manager/config"
	"github.com/pax-beehive/pax-manager/internal/manager/paxd"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
	httprouter "github.com/pax-beehive/pax-manager/internal/transport/http/router"
)

type Service struct {
	cfg             Config
	store           Store
	clock           func() time.Time
	agentWS         *AgentWSHub
	maxBodyBytes    int64
	apiLimiter      *rateLimiter
	registerLimiter *rateLimiter
	auth            *auth.Service
	secrets         auth.Secrets
	paxd            *paxd.Service
	userapi         *userapi.Service
}

type Server = Service

func newServer(cfg Config, store Store) *Service {
	cfg.AdminEmails = managerconfig.MergeAdminEmails(cfg.AdminEmails)
	secrets := auth.Secrets{}
	s := &Service{
		cfg:          cfg,
		store:        store,
		clock:        time.Now,
		agentWS:      NewAgentWSHub(),
		maxBodyBytes: cfg.MaxBodyBytes,
		apiLimiter:   newRateLimiter(cfg.APIRateLimitPerMinute, cfg.APIRateLimitBurst, time.Now),
		registerLimiter: newRateLimiter(
			cfg.RegisterLimitPerMinute,
			cfg.RegisterLimitBurst,
			time.Now,
		),
		secrets: secrets,
	}
	authService := auth.NewService(store, store, serviceAdminPolicy{s: s}, secrets, auth.Config{
		RegistrationToken:      cfg.RegistrationToken,
		RegistrationOwnerEmail: cfg.RegistrationOwnerEmail,
		LocalUserEmail:         cfg.LocalUserID,
		AllowLocalUserHeader:   cfg.AllowLocalUserHeader,
		IdentityVerifier:       cloudflareVerifier(cfg),
	})
	s.auth = authService
	s.paxd = paxd.NewService(store, func() time.Time { return s.clock() }, authService, secrets)
	s.userapi = userapi.NewService(
		store,
		func() time.Time { return s.clock() },
		authService,
		secrets,
	)
	return s
}

type serviceAdminPolicy struct {
	s *Service
}

func (p serviceAdminPolicy) IsAdmin(email string) bool {
	return p.s.cfg.AdminEmails[normalizeEmail(email)]
}

func (p serviceAdminPolicy) RoleForEmail(email string) string {
	if p.IsAdmin(email) {
		return "admin"
	}
	return "user"
}

func (s *Service) engine(addr string) *hertzserver.Hertz {
	h := hertzserver.Default(hertzserver.WithHostPorts(addr))
	s.registerRoutes(h)
	return h
}

func (s *Service) routes() http.Handler {
	return hertzHTTPHandler{s.engine(":0")}
}

func (s *Service) registerRoutes(h *hertzserver.Hertz) {
	h.Use(injectService(s), s.protect())

	httprouter.GeneratedRegister(h)

	h.GET("/openapi", OpenAPIUI)
	h.GET("/openapi.json", OpenAPIJSON)
	h.POST("/api/echo", Echo)
	h.GET(
		"/api/agent/ws",
		AgentWSAuthPreflight(),
		adaptor.HertzHandler(http.HandlerFunc(s.handleAgentWS)),
	)

	h.Static("/", "static")
}

type hertzHTTPHandler struct {
	h *hertzserver.Hertz
}

func (h hertzHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := h.h.NewContext()
	if err := adaptor.CopyToHertzRequest(r, &ctx.Request); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if agentID := agentIDFromPath(r.URL.Path); agentID != "" {
		ctx.Set("routeAgentID", agentID)
	}
	if sessionID := sessionIDFromPath(r.URL.Path); sessionID != "" {
		ctx.Set("routeSessionID", sessionID)
	}
	h.h.ServeHTTP(r.Context(), ctx)
	ctx.Response.Header.VisitAll(func(k, v []byte) {
		w.Header().Add(string(k), string(v))
	})
	status := ctx.Response.Header.StatusCode()
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(ctx.Response.Body())
}

func Echo(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).handleEcho(c, ctx)
}

func (s *Service) handleHealth(_ context.Context, ctx *app.RequestContext) {
	writeData(ctx, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Service) handleEcho(_ context.Context, ctx *app.RequestContext) {
	var payload json.RawMessage
	if err := json.Unmarshal(ctx.Request.Body(), &payload); err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid JSON body")
		return
	}
	writeJSON(ctx, http.StatusOK, map[string]any{
		"ok":        true,
		"request":   payload,
		"timestamp": s.clock().UTC().Format(time.RFC3339),
	})
}

func (s *Service) userPrincipal(c context.Context, ctx *app.RequestContext) (UserPrincipal, error) {
	return s.auth.Principal(c, requestMetadata(ctx))
}

func writeJSON(ctx *app.RequestContext, status int, v any) {
	ctx.JSON(status, v)
}

type apiResponse struct {
	Data    any    `json:"data"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeData(ctx *app.RequestContext, status int, data any) {
	writeJSON(ctx, status, apiResponse{Data: data, Code: status, Message: "ok"})
}

func writeEndpointResult(ctx *app.RequestContext, status int, data any, err error) {
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, status, data)
}

func writeEndpointError(ctx *app.RequestContext, err error) {
	status, message := endpointErrorStatus(err)
	writeError(ctx, status, message)
}

func writeStoreError(ctx *app.RequestContext, err error) {
	status, message := endpointErrorStatus(err)
	writeError(ctx, status, message)
}

func writeError(ctx *app.RequestContext, status int, message string) {
	writeJSON(ctx, status, apiResponse{Data: nil, Code: status, Message: message})
	ctx.Abort()
}

func writeHTTPEndpointError(w http.ResponseWriter, err error) {
	status, message := endpointErrorStatus(err)
	writeHTTPError(w, status, message)
}

func endpointErrorStatus(err error) (int, string) {
	var httpErr apperr.Error
	if errors.As(err, &httpErr) {
		return httpErr.Status, httpErr.Message
	}
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, ErrUnauthorized):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, ErrConflict):
		return http.StatusConflict, "conflict"
	default:
		log.Printf("store error: %v", err)
		return http.StatusInternalServerError, "internal server error"
	}
}

func writeHTTPError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
		log.Printf("write response: %v", err)
	}
}

func serviceFromContext(ctx *app.RequestContext) *Service {
	v, ok := ctx.Get("service")
	if !ok {
		panic("manager service missing from Hertz context")
	}
	s, ok := v.(*Service)
	if !ok {
		panic("manager service has unexpected type")
	}
	return s
}

func agentFromContext(ctx *app.RequestContext) Agent {
	v, ok := ctx.Get("agent")
	if !ok {
		return Agent{}
	}
	agent, _ := v.(Agent)
	return agent
}

func injectService(s *Service) app.HandlerFunc {
	return func(c context.Context, ctx *app.RequestContext) {
		ctx.Set("requestContext", c)
		ctx.Set("service", s)
		ctx.Next(c)
	}
}

func AgentAuth() app.HandlerFunc {
	return func(c context.Context, ctx *app.RequestContext) {
		serviceFromContext(ctx).AgentAuth(c, ctx)
	}
}

func (s *Service) AgentAuth(c context.Context, ctx *app.RequestContext) {
	token := paxKeyFromHertz(ctx)
	if token == "" {
		writeError(ctx, http.StatusUnauthorized, "missing pax key")
		return
	}
	agent, err := s.store.AuthenticateAgent(c, s.secrets.Hash(token))
	if err != nil {
		writeStoreError(ctx, err)
		return
	}
	ctx.Set("agent", agent)
	ctx.Next(c)
}

func cloudflareVerifier(cfg Config) auth.UserIdentityVerifier {
	if cfg.CloudflareAccessDisabled {
		return nil
	}
	if cfg.CloudflareAccessIssuer == "" || cfg.CloudflareAccessAud == "" ||
		cfg.CloudflareAccessJWKS == "" {
		return nil
	}
	return auth.NewCloudflareAccessVerifier(
		cfg.CloudflareAccessIssuer,
		cfg.CloudflareAccessAud,
		cfg.CloudflareAccessJWKS,
	)
}

func AgentWSAuthPreflight() app.HandlerFunc {
	return func(c context.Context, ctx *app.RequestContext) {
		s := serviceFromContext(ctx)
		token := paxKeyFromHertz(ctx)
		if token == "" {
			writeError(ctx, http.StatusUnauthorized, "missing pax key")
			return
		}
		agent, err := s.store.AuthenticateAgent(c, s.secrets.Hash(token))
		if err != nil {
			writeEndpointError(ctx, err)
			return
		}
		if requestAgentID := websocketAgentIDFromHertz(ctx); requestAgentID != "" &&
			requestAgentID != agent.AgentID {
			writeError(ctx, http.StatusForbidden, "agent_id does not match pax key")
			return
		}
		ctx.Next(c)
	}
}

func websocketAgentIDFromHertz(ctx *app.RequestContext) string {
	if agentID := ctx.Query("agent_id"); agentID != "" {
		return agentID
	}
	if agentID := ctx.Query("agentId"); agentID != "" {
		return agentID
	}
	return ctx.Query("agentKey")
}

func paxKeyFromHertz(ctx *app.RequestContext) string {
	if token := string(ctx.GetHeader("X-Pax-Key")); token != "" {
		return token
	}
	if token := auth.BearerToken(string(ctx.GetHeader("Authorization"))); token != "" {
		return token
	}
	if token := ctx.Query("pax_key"); token != "" {
		return token
	}
	if token := ctx.Query("paxKey"); token != "" {
		return token
	}
	return ctx.Query("key")
}
