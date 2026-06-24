package manager

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	hertzserver "github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	managerconfig "github.com/pax-beehive/pax-manager/internal/manager/config"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
	"github.com/pax-beehive/pax-manager/internal/manager/paxd"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
	httprouter "github.com/pax-beehive/pax-manager/internal/transport/http/router"
)

type Service struct {
	cfg             Config
	store           Store
	clock           func() time.Time
	agentWS         *AgentWSHub
	acpTunnels      *ACPTunnelHub
	acpRuntime      *acpRuntimeProjector
	maxBodyBytes    int64
	apiLimiter      *rateLimiter
	registerLimiter *rateLimiter
	auth            *auth.Service
	secrets         auth.Secrets
	paxd            *paxd.Service
	userapi         *userapi.Service
	paxdArtifacts   paxdArtifactBackend
}

type Server = Service

func newServer(cfg Config, store Store) *Service {
	cfg.AdminEmails = managerconfig.MergeAdminEmails(cfg.AdminEmails)
	if cfg.PaxdVerificationBaseURL == "" {
		cfg.PaxdVerificationBaseURL = managerconfig.DefaultPaxdVerificationBaseURL
	}
	secrets := auth.Secrets{}
	store = newCanonicalSessionStore(store)
	s := &Service{
		cfg:          cfg,
		store:        store,
		clock:        time.Now,
		agentWS:      NewAgentWSHub(),
		acpTunnels:   NewACPTunnelHub(),
		maxBodyBytes: cfg.MaxBodyBytes,
		apiLimiter:   newRateLimiter(cfg.APIRateLimitPerMinute, cfg.APIRateLimitBurst, time.Now),
		registerLimiter: newRateLimiter(
			cfg.RegisterLimitPerMinute,
			cfg.RegisterLimitBurst,
			time.Now,
		),
		secrets: secrets,
	}
	s.acpRuntime = newACPRuntimeProjector(store, func() time.Time { return s.clock() })
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
	s.paxdArtifacts = newGCPPaxdArtifactBackend(cfg)
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
	h.NoHijackConnPool = true
	s.registerRoutes(h)
	return h
}

func (s *Service) routes() http.Handler {
	return hertzHTTPHandler{s.engine(":0")}
}

func (s *Service) registerRoutes(h *hertzserver.Hertz) {
	h.Use(injectService(s), s.protect())

	httprouter.GeneratedRegister(h)
	registerIDLRoutes(h)
	h.GET(routeLegacySessionHistory, ListAgentSessionHistory)
	h.GET(routeSessionHistory, ListAgentSessionHistory)
	h.POST(
		routeCreateKnowledgeCapsule,
		CreateKnowledgeCapsule,
	)
	h.GET(routeListKnowledgeCapsules, ListKnowledgeCapsules)
	h.GET(routeGetKnowledgeCapsule, GetKnowledgeCapsule)
	h.POST(
		routeArchiveKnowledgeCapsule,
		ArchiveKnowledgeCapsule,
	)
	h.POST(
		routeInjectKnowledgeCapsule,
		InjectKnowledgeCapsule,
	)
	h.GET(
		routeListKnowledgeInjections,
		ListKnowledgeInjections,
	)
	h.POST(routeCreateEnvelope, CreateEnvelope)
	h.GET(routeListEnvelopes, ListEnvelopes)
	h.GET(routeGetEnvelope, GetEnvelope)
	h.POST(routeAcceptEnvelope, AcceptEnvelope)
	h.POST(routeArchiveEnvelope, ArchiveEnvelope)
	h.POST(routeCreateFriend, CreateFriend)
	h.GET(routeListFriends, ListFriends)
	h.GET(routeGetFriend, GetFriend)
	h.POST(routeAcceptFriend, AcceptFriend)
	h.POST(routeAliasFriend, UpdateFriendAlias)
	h.POST(routeRemoveFriend, RemoveFriend)
	h.POST(routeBlockFriend, BlockFriend)

	h.GET(routeOpenAPI, OpenAPIUI)
	h.GET(routeOpenAPIJSON, OpenAPIJSON)
	h.POST(routeEcho, Echo)
	h.POST(routeStartNodeRegistration, StartNodeRegistration)
	h.POST(routePollNodeRegistration, PollNodeRegistration)
	h.GET(routeGetNodeRegistration, GetNodeRegistration)
	h.POST(routeApproveNodeRegistration, ApproveNodeRegistration)
	h.POST(routeStartPaxlDeviceLogin, StartPaxlDeviceLogin)
	h.POST(routePollPaxlDeviceLogin, PollPaxlDeviceLogin)
	h.POST(
		routeApprovePaxlDeviceLogin,
		ApprovePaxlDeviceLogin,
	)
	h.GET(
		routeLegacyAgentWS,
		AgentWSAuthPreflight(),
		adaptor.HertzHandler(http.HandlerFunc(s.handleAgentWS)),
	)
	h.GET(
		routeAgentACPTunnel,
		adaptor.HertzHandler(http.HandlerFunc(s.handleAgentACPTunnel)),
	)
	h.GET(
		routeUserACPTunnel,
		adaptor.HertzHandler(http.HandlerFunc(s.handleUserACPTunnel)),
	)
	h.GET(
		routeUserSessionACPTunnel,
		adaptor.HertzHandler(http.HandlerFunc(s.handleUserACPTunnel)),
	)
	h.GET(routeDownloadGenericArtifact, s.handleDownloadGenericArtifact)
	h.GET(routeDownloadPaxdArtifact, s.handleDownloadPaxdArtifact)
	h.GET(routeDownloadPaxlArtifact, s.handleDownloadPaxlArtifact)
	h.GET(routeDownloadPaxdInstaller, s.handleDownloadPaxdInstaller)
	h.GET(routeDownloadPaxlInstaller, s.handleDownloadPaxlInstaller)
	h.POST(routePublishGenericArtifact, s.handlePublishGenericArtifact)
	h.POST(routePublishPaxdArtifact, s.handlePublishPaxdArtifact)

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
		return http.StatusInternalServerError, "internal server error"
	}
}

func writeHTTPError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
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

func nodeFromContext(ctx *app.RequestContext) Node {
	v, ok := ctx.Get("node")
	if !ok {
		return Node{}
	}
	node, _ := v.(Node)
	return node
}

func injectService(s *Service) app.HandlerFunc {
	return func(c context.Context, ctx *app.RequestContext) {
		c, logID := hertzRequestLogContext(c, ctx)
		ctx.Response.Header.Set(logging.HeaderLogID, logID)
		ctx.Set("requestContext", c)
		ctx.Set("service", s)
		ctx.Next(c)
	}
}

func hertzRequestLogContext(c context.Context, ctx *app.RequestContext) (context.Context, string) {
	c, logID := logging.EnsureLogID(
		c,
		string(ctx.GetHeader(logging.HeaderLogID)),
		string(ctx.GetHeader(logging.HeaderRequestID)),
	)
	c = logging.With(
		c,
		slog.String("http_method", string(ctx.Method())),
		slog.String("http_path", string(ctx.Path())),
		slog.String("remote_addr", ctx.RemoteAddr().String()),
	)
	return c, logID
}

func httpRequestLogContext(c context.Context, r *http.Request) (context.Context, string) {
	c, logID := logging.EnsureLogID(
		c,
		r.Header.Get(logging.HeaderLogID),
		r.Header.Get(logging.HeaderRequestID),
	)
	c = logging.With(
		c,
		slog.String("http_method", r.Method),
		slog.String("http_path", r.URL.Path),
		slog.String("remote_addr", r.RemoteAddr),
	)
	return c, logID
}

func websocketResponseHeader(ctx context.Context) http.Header {
	if logID := logging.LogID(ctx); logID != "" {
		return http.Header{logging.HeaderLogID: []string{logID}}
	}
	return nil
}

func AgentAuth() app.HandlerFunc {
	return func(c context.Context, ctx *app.RequestContext) {
		serviceFromContext(ctx).AgentAuth(c, ctx)
	}
}

func NodeAuth() app.HandlerFunc {
	return func(c context.Context, ctx *app.RequestContext) {
		serviceFromContext(ctx).NodeAuth(c, ctx)
	}
}

func (s *Service) NodeAuth(c context.Context, ctx *app.RequestContext) {
	token := paxKeyFromHertz(ctx)
	if token == "" {
		writeError(ctx, http.StatusUnauthorized, "missing pax key")
		return
	}
	node, err := s.store.AuthenticateNode(c, s.secrets.Hash(token))
	if err != nil {
		writeStoreError(ctx, err)
		return
	}
	ctx.Set("node", node)
	ctx.Next(c)
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
		requestAgentID := websocketAgentIDFromHertz(ctx)
		path := string(ctx.Path())
		token := paxKeyFromHertz(ctx)
		if token == "" {
			logging.Warn(
				c,
				"agent websocket auth rejected",
				slog.String("path", path),
				slog.String("query_agent_id", requestAgentID),
				slog.String("reason", "missing_pax_key"),
			)
			writeError(ctx, http.StatusUnauthorized, "missing pax key")
			return
		}
		agent, err := s.store.AuthenticateAgent(c, s.secrets.Hash(token))
		if err != nil {
			status, message := endpointErrorStatus(err)
			logging.Warn(
				c,
				"agent websocket auth rejected",
				slog.String("path", path),
				slog.String("query_agent_id", requestAgentID),
				slog.String("key_prefix", s.secrets.Prefix(token)),
				slog.Int("status", status),
				slog.String("reason", message),
				logging.Err(err),
			)
			writeEndpointError(ctx, err)
			return
		}
		if requestAgentID != "" && requestAgentID != agent.AgentID {
			logging.Warn(
				c,
				"agent websocket auth rejected",
				slog.String("path", path),
				slog.String("query_agent_id", requestAgentID),
				slog.String("authenticated_agent_id", agent.AgentID),
				slog.String("reason", "agent_id_mismatch"),
			)
			writeError(ctx, http.StatusForbidden, "agent_id does not match pax key")
			return
		}
		logging.Info(
			c,
			"agent websocket auth accepted",
			slog.String("path", path),
			slog.String("query_agent_id", requestAgentID),
			slog.String("authenticated_agent_id", agent.AgentID),
			slog.String("key_prefix", s.secrets.Prefix(token)),
		)
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
