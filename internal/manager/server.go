package manager

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	hertzserver "github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/pax-beehive/paxkit/reliablemq"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	managerconfig "github.com/pax-beehive/pax-manager/internal/manager/config"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
	"github.com/pax-beehive/pax-manager/internal/manager/paxd"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

type Service struct {
	shortPairingStore  domain.ShortPairingStore
	queueContext       context.Context
	queueCancel        context.CancelFunc
	cfg                Config
	store              Store
	transportStore     reliablemq.DurableStore
	transportProducers *reliablemq.ProducerRegistry
	transportFlusher   interface {
		Flush(context.Context) error
		Close(context.Context) error
	}
	clock             func() time.Time
	agentWS           *AgentWSHub
	acpTunnels        *ACPTunnelHub
	nodeControls      *NodeControlHub
	conversationTurns *conversationTurnQueue
	queueChecks       sync.Map
	queueCheckSlots   chan struct{}
	queueRunSlots     chan struct{}
	acpRuntime        *acpRuntimeProjector
	maxBodyBytes      int64
	apiLimiter        *rateLimiter
	registerLimiter   *rateLimiter
	regionalUsers     regionalUserStore
	auth              *auth.Service
	secrets           auth.Secrets
	paxd              *paxd.Service
	userapi           *userapi.Service
	paxdArtifacts     paxdArtifactBackend
	backgroundRunner  func(context.Context, func(context.Context))
	e2eeEventWakes    *e2eeWakeRegistry
}

type Server = Service

func newServer(cfg Config, store Store) *Service {
	cfg.AdminEmails = managerconfig.MergeAdminEmails(cfg.AdminEmails)
	if cfg.PaxdVerificationBaseURL == "" {
		cfg.PaxdVerificationBaseURL = managerconfig.DefaultPaxdVerificationBaseURL
	}
	secrets := auth.Secrets{}
	shortPairingStore, _ := store.(domain.ShortPairingStore)
	transportBaseStore := store
	store = newCanonicalSessionStore(store)
	transportStore := reliablemq.NewProducerWriteBehindStore(
		transportBaseStore,
		reliablemq.WithProducerWriteBehindRequireBatchStore(),
		reliablemq.WithProducerWriteBehindFlushFailureHandler(
			func(err error, stats reliablemq.ProducerWriteBehindStats) {
				logging.Error(
					context.Background(),
					"acp transport producer write-behind flush failed",
					logging.Err(err),
					slog.Int("dirty_frames", stats.DirtyFrames),
					slog.Int("dirty_patches", stats.DirtyPatches),
					slog.Int64("dirty_bytes", stats.DirtyBytes),
					slog.Int("consecutive_failures", stats.ConsecutiveFlushFailures),
					slog.String("last_flush_error", stats.LastFlushError),
				)
			},
		),
	)
	transportProducers, err := reliablemq.NewProducerRegistry(
		transportStore,
		reliablemq.ProducerConfig{
			OnError: func(err error) {
				logging.Error(
					context.Background(),
					"acp transport producer failed",
					logging.Err(err),
				)
			},
		},
	)
	if err != nil {
		panic(err)
	}
	s := &Service{
		cfg:                cfg,
		shortPairingStore:  shortPairingStore,
		store:              store,
		transportStore:     transportStore,
		transportProducers: transportProducers,
		transportFlusher:   transportStore,
		clock:              time.Now,
		agentWS:            NewAgentWSHub(),
		acpTunnels:         NewACPTunnelHub(),
		nodeControls:       NewNodeControlHub(),
		maxBodyBytes:       cfg.MaxBodyBytes,
		apiLimiter: newRateLimiter(
			cfg.APIRateLimitPerMinute,
			cfg.APIRateLimitBurst,
			time.Now,
		),
		registerLimiter: newRateLimiter(
			cfg.RegisterLimitPerMinute,
			cfg.RegisterLimitBurst,
			time.Now,
		),
		secrets:        secrets,
		e2eeEventWakes: newE2EEWakeRegistry(),
		backgroundRunner: func(ctx context.Context, task func(context.Context)) {
			go task(context.WithoutCancel(ctx))
		},
	}
	s.conversationTurns = newConversationTurnQueue(transportBaseStore)
	s.queueContext, s.queueCancel = context.WithCancel(context.Background())
	s.queueCheckSlots = make(chan struct{}, 8)
	s.queueRunSlots = make(chan struct{}, 32)
	s.acpRuntime = newACPRuntimeProjector(func() time.Time { return s.clock() })
	authService := auth.NewService(store, store, serviceAdminPolicy{s: s}, secrets, auth.Config{
		RegistrationToken:      cfg.RegistrationToken,
		RegistrationOwnerEmail: cfg.RegistrationOwnerEmail,
		LocalUserEmail:         cfg.LocalUserID,
		AllowLocalUserHeader:   cfg.AllowLocalUserHeader,
		IdentityVerifier:       cloudflareVerifier(cfg),
		RequireProvisionedUser: cfg.Region != "",
	})
	s.regionalUsers, _ = transportBaseStore.(regionalUserStore)
	s.auth = authService
	s.paxd = paxd.NewService(store, func() time.Time { return s.clock() }, authService, secrets)
	s.userapi = userapi.NewService(
		store,
		func() time.Time { return s.clock() },
		authService,
		secrets,
	)
	s.userapi.SetNodeControlClient(s.nodeControls)
	s.userapi.SetHistoryReconciler(func(
		ctx context.Context,
		agentID string,
		sessionID string,
	) error {
		return reconcileArtifactPublicationDisplaysForSession(
			ctx, s.store, agentID, sessionID,
		)
	})
	s.configureTeamMemexExecutor()
	s.paxdArtifacts = newS3PaxdArtifactBackend(cfg)
	return s
}

func (s *Service) CloseTransportStore(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.queueCancel != nil {
		s.queueCancel()
	}
	var err error
	if s.transportProducers != nil {
		err = errors.Join(err, s.transportProducers.Close(ctx))
	}
	if s.transportFlusher != nil {
		err = errors.Join(err, s.transportFlusher.Close(ctx))
	}
	return err
}

func (s *Service) configureTeamMemexExecutor() {
	switch strings.ToLower(strings.TrimSpace(s.cfg.TeamMemexExecutor)) {
	case "", domain.TeamMemexRunExecutorDryRun:
		return
	case domain.TeamMemexRunExecutorDeepSeek:
		executor, err := userapi.NewDeepSeekTeamMemexExecutor(userapi.DeepSeekTeamMemexConfig{
			APIKey:      s.cfg.DeepSeekAPIKey,
			BaseURL:     s.cfg.DeepSeekBaseURL,
			Model:       s.cfg.DeepSeekModel,
			Timeout:     s.cfg.DeepSeekTimeout,
			MaxTokens:   s.cfg.DeepSeekMaxTokens,
			Temperature: s.cfg.DeepSeekTemperature,
		})
		if err != nil {
			logging.Warn(
				context.Background(),
				"team memex deepseek executor disabled",
				slog.String("error", err.Error()),
			)
			return
		}
		s.userapi.SetTeamMemexExecutor(executor)
	default:
		logging.Warn(
			context.Background(),
			"unknown team memex executor; using dry run",
			slog.String("executor", s.cfg.TeamMemexExecutor),
		)
	}
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

	registerIDLRoutes(h)
	h.GET(routeLegacyHealth, Health)
	h.POST(routeLegacyAgentRegister, RegisterAgent)
	h.POST(routeLegacyAgentStatus, AgentAuth(), ReportAgentStatus)
	h.GET(routeLegacyAgentMailbox, AgentAuth(), PullMailbox)
	h.GET(routeLegacySessionMailbox, AgentAuth(), PullSessionMailbox)
	h.POST(routeLegacyAgentOffset, AgentAuth(), UpdateMailboxOffset)
	h.POST(routeLegacyMessageResult, AgentAuth(), ReportMessageResult)
	h.GET(routeLegacyListAgents, ListAgents)
	h.GET(routeLegacyGetAgent, GetAgent)
	h.DELETE(routeLegacyGetAgent, DeleteAgent)
	h.GET(routeUserAgents, ListAgents)
	h.GET(routeUserAgent, GetAgent)
	h.DELETE(routeUserAgent, DeleteAgent)
	h.GET(routeUserAgentPermissionCatalog, s.handleGetAgentPermissionCatalog)
	h.POST(routeUserSessionPermission, s.handleSetSessionPermission)
	h.GET(routeUserSessionConfig, s.handleGetSessionConfig)
	h.POST(routeUserSessionConfigRefresh, s.handleRefreshSessionConfig)
	h.PATCH(routeUserSessionConfigOption, s.handleSetSessionConfigOption)
	h.GET(routeLegacyListAgentSessions, ListAgentSessions)
	h.GET(routeLegacyGetAgentSession, GetAgentSession)
	h.GET(routeLegacyAgentMessages, ListAgentMessages)
	h.POST(routeLegacyAgentMessages, CreateAgentMessage)
	h.GET(routeLegacySessionMessages, ListAgentSessionMessages)
	h.POST(routeLegacySessionMessages, CreateSessionMessage)
	h.GET(routeLegacyUserAPIKeys, ListUserAPIKeys)
	h.POST(routeLegacyUserAPIKeys, CreateUserAPIKey)
	h.DELETE(routeLegacyUserAPIKey, RevokeUserAPIKey)
	h.POST(routeLegacyRegistrationTokens, CreateAgentRegistrationToken)
	h.GET(routeLegacySessionHistory, ListAgentSessionHistory)
	h.GET(routeSessionHistory, ListAgentSessionHistory)
	h.GET(routeUserSessionHistory, ListSessionHistory)
	h.GET(routeUserSessionMessageDetail, GetSessionMessageDetail)
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
	h.POST(routeCreateTeam, CreateTeam)
	h.GET(routeListTeams, ListTeams)
	h.GET(routeGetTeam, GetTeam)
	h.POST(routeArchiveTeam, ArchiveTeam)
	h.GET(routeListTeamMembers, ListTeamMembers)
	h.POST(routeUpdateTeamMember, UpdateTeamMemberRole)
	h.DELETE(routeRemoveTeamMember, RemoveTeamMember)
	h.POST(routeLeaveTeam, LeaveTeam)
	h.POST(routeCreateTeamInvite, CreateTeamInvite)
	h.GET(routeListTeamSentInvites, ListTeamSentInvites)
	h.POST(routeCancelTeamInvite, CancelTeamInvite)
	h.GET(routeListTeamInvites, ListTeamInvites)
	h.POST(routeAcceptTeamInvite, AcceptTeamInvite)
	h.POST(routeDeclineTeamInvite, DeclineTeamInvite)
	h.GET(routeListTeamAgents, ListTeamAgents)
	h.POST(routeAddTeamAgent, AddTeamAgent)
	h.DELETE(routeRemoveTeamAgent, RemoveTeamAgent)
	h.GET(routeListTeamAudit, ListTeamAuditEvents)
	h.GET(routeTeamMemexIndex, GetTeamMemexIndex)
	h.GET(routeTeamMemexDocuments, ListTeamMemexDocuments)
	h.GET(routeTeamMemexDocument, GetTeamMemexDocument)
	h.POST(routeTeamMemexRuns, CreateTeamMemexRun)
	h.GET(routeTeamMemexRun, GetTeamMemexRun)

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
		routeNodeControlTunnel,
		adaptor.HertzHandler(http.HandlerFunc(s.handleNodeControlTunnel)),
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
	h.POST(
		routeUserSessionTurnStop,
		adaptor.HertzHandler(http.HandlerFunc(s.handleConversationTurnStop)),
	)
	h.GET(
		routeUserSessionTurnQueue,
		adaptor.HertzHandler(http.HandlerFunc(s.handleConversationTurnQueue)),
	)
	h.POST(
		routeUserSessionTurnQueue,
		adaptor.HertzHandler(http.HandlerFunc(s.handleConversationTurnQueue)),
	)
	h.PATCH(
		routeUserSessionTurnQueue,
		adaptor.HertzHandler(http.HandlerFunc(s.handleConversationTurnQueue)),
	)
	h.DELETE(
		routeUserSessionTurnQueue,
		adaptor.HertzHandler(http.HandlerFunc(s.handleConversationTurnQueue)),
	)
	h.POST(
		routeUserSessionTurnSteer,
		adaptor.HertzHandler(http.HandlerFunc(s.handleConversationTurnSteer)),
	)
	h.GET(
		routeUserSessionEvents,
		adaptor.HertzHandler(http.HandlerFunc(s.handleSessionObserverEvents)),
	)
	h.POST(
		routeUserSessionE2EECommands,
		adaptor.HertzHandler(http.HandlerFunc(s.handleE2EECommands)),
	)
	h.GET(
		routeUserSessionE2EEEvents,
		adaptor.HertzHandler(http.HandlerFunc(s.handleE2EEEvents)),
	)
	h.GET(
		routeUserSessionE2EEHistory,
		adaptor.HertzHandler(http.HandlerFunc(s.handleE2EEHistory)),
	)
	h.POST(routeUserAgentE2EEPairings, s.handleCreateE2EEPairing)
	h.GET(routeUserAgentE2EEPairings, s.handleListE2EEPairings)
	h.GET(routeUserAgentE2EEPairing, s.handleGetUserE2EEPairing)
	h.GET(routeUserAgentE2EEPairing+"/attempts", s.handleShortPairingAttempts)
	h.POST(routeUserAgentE2EEPairing+"/attempts", s.handleShortPairingAttempts)
	h.GET(routeUserAgentE2EEPairing+"/attempts/:attempt_id", s.handleShortPairingAttempt)
	h.POST(routeUserAgentE2EEPairing+"/attempts/:attempt_id", s.handleShortPairingAttempt)
	h.POST(routeUserAgentE2EEPairing+"/end", s.handleEndShortPairing)
	h.POST(routeUserAgentE2EEPairingPackage, s.handleCompleteUserE2EEPairing)
	h.GET(routeUserAgentE2EEKeyPackage, s.handleGetE2EEKeyPackage)
	h.GET(routeNodeAgentE2EEPairing, NodeAuth(), s.handleGetNodeE2EEPairing)
	h.POST(
		routeNodeAgentE2EEPairingPackage,
		NodeAuth(),
		s.handleCompleteNodeE2EEPairing,
	)
	h.POST(
		routeUserSessionRuntimeReset,
		adaptor.HertzHandler(http.HandlerFunc(s.handleSessionRuntimeReset)),
	)
	h.POST(
		routeUserConversation,
		adaptor.HertzHandler(http.HandlerFunc(s.handleConversation)),
	)
	h.POST(routeDeliverAgentConversation, NodeAuth(), DeliverAgentConversation)
	h.POST(routeStartAgentConversation, NodeAuth(), StartAgentConversation)
	h.GET(routeNodeOwnerAgents, NodeAuth(), ListNodeOwnerAgents)
	h.GET(routeGetAgentOwnerInfo, GetAgentOwnerInfo)
	h.GET(routeListRepresentativeAgents, ListRepresentativeAgents)
	h.POST(routeUpsertRepresentativeAgent, UpsertRepresentativeAgent)
	h.POST(routeStartUserAgentInquiry, StartUserAgentInquiry)
	h.GET(routeListAgentConversationMessages, ListAgentConversationMessages)
	h.POST(routeCreateUserAttachment, s.handleCreateUserAttachment)
	h.POST(routeCompleteUserAttachment, s.handleCompleteUserAttachment)
	h.GET(routeUserAttachmentContent, s.handleUserAttachmentContent)
	h.PUT(routePutArtifactPublication, NodeAuth(), s.handlePutArtifactPublication)
	h.POST(routePrepareArtifactPublication, NodeAuth(), s.handlePrepareArtifactPublication)
	h.POST(routeFailArtifactPublication, NodeAuth(), s.handleFailArtifactPublication)
	h.POST(routeCompleteNodeArtifactUpload, NodeAuth(), s.handleCompleteNodeArtifactUpload)
	h.GET(routeGetArtifactPublication, s.handleGetArtifactPublication)
	h.GET(routeArtifactPublicationContent, s.handleGetArtifactPublicationContent)
	h.POST(routeCreateArtifactUpload, s.handleCreateArtifactUpload)
	h.POST(routeCompleteArtifactUpload, s.handleCompleteArtifactUpload)
	h.POST(routeCreateSessionArtifact, s.handleCreateSessionArtifact)
	h.GET(routeGetSessionArtifact, s.handleGetSessionArtifact)
	h.GET(routeArtifactContent, s.handleGetArtifactContent)
	h.POST(routeAttachSessionArtifact, s.handleAttachSessionArtifact)
	h.GET(routeListSessionArtifacts, s.handleListSessionArtifacts)
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
	case errors.Is(err, domain.ErrShortPairingLimited):
		return http.StatusTooManyRequests, err.Error()
	case errors.Is(err, domain.ErrShortPairingEnded):
		return http.StatusConflict, err.Error()
	case errors.Is(err, domain.ErrE2EEPairingSuperseded):
		return http.StatusConflict, err.Error()
	case errors.Is(err, domain.ErrE2EEPairingExpired):
		return http.StatusGone, err.Error()
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
	_ = json.NewEncoder(w).Encode(apiResponse{
		Data:    nil,
		Code:    status,
		Message: message,
	})
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
	ctx.Header("X-Pax-User-ID", node.OwnerUserID)
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
	primary := auth.NewCloudflareAccessVerifier(
		cfg.CloudflareAccessIssuer,
		cfg.CloudflareAccessAud,
		cfg.CloudflareAccessJWKS,
	)
	if !cfg.CloudflareAccessMigrationEnabled {
		return primary
	}
	secondary := auth.NewCloudflareAccessVerifier(
		cfg.CloudflareAccessMigrationIssuer,
		cfg.CloudflareAccessMigrationAud,
		cfg.CloudflareAccessMigrationJWKS,
	)
	return auth.NewUserIdentityVerifierChain(primary, secondary)
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
