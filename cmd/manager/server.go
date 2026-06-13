package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	cfg    Config
	store  Store
	clock  func() time.Time
	legacy *LegacyHub
}

func newServer(cfg Config, store Store) *Server {
	cfg.AdminEmails = mergeAdminEmails(cfg.AdminEmails)
	return &Server{
		cfg:    cfg,
		store:  store,
		clock:  time.Now,
		legacy: NewLegacyHub(),
	}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /api/echo", s.handleEcho)

	mux.HandleFunc("POST /api/agent/register", s.handleAgentRegister)
	mux.HandleFunc("POST /api/agent/status", s.withAgent(s.handleAgentStatus))
	mux.HandleFunc("GET /api/agent/mailbox", s.withAgent(s.handleAgentMailbox))
	mux.HandleFunc("POST /api/agent/messages/offset", s.withAgent(s.handleAgentOffset))
	mux.HandleFunc("POST /api/agent/messages/", s.withAgent(s.handleAgentMessageResult))
	mux.HandleFunc("GET /api/agent/ws", s.handleAgentWS)

	mux.HandleFunc("GET /api/user/agents", s.handleUserAgents)
	mux.HandleFunc("GET /api/user/agents/", s.handleUserAgentSubroutes)
	mux.HandleFunc("GET /api/user/sessions/", s.handleUserSessionSubroutes)
	mux.HandleFunc("POST /api/user/message", s.handleUserMessage)
	mux.HandleFunc("GET /api/user/mailbox", s.handleUserMailbox)
	mux.HandleFunc("POST /api/user/agent-registration-tokens", s.handleCreateRegistrationToken)
	mux.HandleFunc("GET /api/user/api-keys", s.handleListUserAPIKeys)
	mux.HandleFunc("POST /api/user/api-keys", s.handleCreateUserAPIKey)
	mux.HandleFunc("DELETE /api/user/api-keys/", s.handleRevokeUserAPIKey)

	mux.Handle("/", http.FileServer(http.Dir("static")))
	return requestLog(mux)
}

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleEcho(w http.ResponseWriter, r *http.Request) {
	var payload json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"request":   payload,
		"timestamp": s.clock().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleAgentRegister(w http.ResponseWriter, r *http.Request) {
	var req RegisterAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Hostname == "" {
		writeError(w, http.StatusBadRequest, "hostname is required")
		return
	}
	if req.OS == "" {
		req.OS = "unknown"
	}
	owner, err := s.registrationOwner(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	apiKey, err := newSecret("pax")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate api key")
		return
	}

	agent, err := s.store.RegisterAgent(r.Context(), owner, req, hashSecret(apiKey))
	if err != nil {
		writeStoreError(w, err)
		return
	}

	log.Printf("agent registered: %s", agent.AgentID)
	writeJSON(w, http.StatusCreated, RegisterAgentResponse{AgentID: agent.AgentID, APIKey: apiKey})
}

func (s *Server) handleAgentStatus(w http.ResponseWriter, r *http.Request, agent Agent) {
	var report AgentStatusReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if report.AgentID == "" {
		report.AgentID = agent.AgentID
	}
	if report.AgentID != agent.AgentID {
		writeError(w, http.StatusForbidden, "agent_id does not match token")
		return
	}
	if report.Timestamp.IsZero() {
		report.Timestamp = s.clock().UTC()
	}

	if err := s.store.UpsertAgentStatus(r.Context(), report); err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAgentMailbox(w http.ResponseWriter, r *http.Request, agent Agent) {
	offset, err := parseInt64Query(r, "offset", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "offset must be an integer")
		return
	}
	limit, err := parseIntQuery(r, "limit", 10)
	if err != nil {
		writeError(w, http.StatusBadRequest, "limit must be an integer")
		return
	}

	pull, err := s.store.PullMailbox(r.Context(), agent.AgentID, offset, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pull)
}

func (s *Server) handleAgentOffset(w http.ResponseWriter, r *http.Request, agent Agent) {
	var req OffsetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Offset < 0 {
		writeError(w, http.StatusBadRequest, "offset must be non-negative")
		return
	}
	if err := s.store.UpdateOffset(r.Context(), agent.AgentID, req.Offset); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAgentMessageResult(w http.ResponseWriter, r *http.Request, agent Agent) {
	messageID, ok := resultMessageID(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown agent message route")
		return
	}

	var req MessageResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Status == "" {
		writeError(w, http.StatusBadRequest, "status is required")
		return
	}

	if err := s.store.MarkMessageResult(r.Context(), agent.AgentID, messageID, req); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleUserAgents(w http.ResponseWriter, r *http.Request) {
	principal, err := s.userPrincipal(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	agents, err := s.store.ListAgents(r.Context(), principal)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (s *Server) handleUserAgentSubroutes(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/user/agents/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) == 2 && parts[1] == "sessions" {
		principal, err := s.userPrincipal(r)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		sessions, err := s.store.ListAgentSessions(r.Context(), principal, parts[0])
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
		return
	}
	writeError(w, http.StatusNotFound, "unknown user agent route")
}

func (s *Server) handleUserSessionSubroutes(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/user/sessions/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) == 1 && parts[0] != "" {
		principal, err := s.userPrincipal(r)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		session, err := s.store.GetSession(r.Context(), principal, parts[0])
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, session)
		return
	}
	if len(parts) == 2 && parts[1] == "messages" {
		principal, err := s.userPrincipal(r)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		messages, err := s.store.ListSessionMessages(r.Context(), principal, parts[0])
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"messages": messages})
		return
	}
	writeError(w, http.StatusNotFound, "unknown user session route")
}

func (s *Server) handleUserMessage(w http.ResponseWriter, r *http.Request) {
	principal, err := s.userPrincipal(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req CreateMailboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.AgentID == "" || req.Message == "" {
		writeError(w, http.StatusBadRequest, "agent_id and message are required")
		return
	}
	if defaultMessageType(req.MessageType) == "" {
		writeError(w, http.StatusBadRequest, "message_type must be chat, steer, or command")
		return
	}

	msg, err := s.store.CreateMailboxMessage(r.Context(), principal, req)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, msg)
}

func (s *Server) handleUserMailbox(w http.ResponseWriter, r *http.Request) {
	principal, err := s.userPrincipal(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	limit, err := parseIntQuery(r, "limit", 50)
	if err != nil {
		writeError(w, http.StatusBadRequest, "limit must be an integer")
		return
	}
	messages, err := s.store.ListMailbox(r.Context(), MailboxFilter{
		Principal: principal,
		AgentID:   r.URL.Query().Get("agent_id"),
		SessionID: r.URL.Query().Get("session_id"),
		Status:    r.URL.Query().Get("status"),
		Limit:     limit,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages})
}

func (s *Server) handleCreateRegistrationToken(w http.ResponseWriter, r *http.Request) {
	principal, err := s.userPrincipal(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	var req CreateRegistrationTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	owner := principal.User
	if req.OwnerEmail != "" || req.OwnerUserID != "" {
		if !principal.IsAdmin {
			writeError(w, http.StatusForbidden, "only admins can mint tokens for another user")
			return
		}
		if req.OwnerEmail != "" {
			owner, err = s.store.GetUserByEmail(r.Context(), req.OwnerEmail)
			if err != nil {
				writeStoreError(w, err)
				return
			}
		} else {
			owner, err = s.store.GetUser(r.Context(), req.OwnerUserID)
			if err != nil {
				writeStoreError(w, err)
				return
			}
		}
	}

	token, err := newSecret("reg")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate token")
		return
	}
	var expiresAt *time.Time
	if req.ExpiresInSeconds > 0 {
		t := s.clock().UTC().Add(time.Duration(req.ExpiresInSeconds) * time.Second)
		expiresAt = &t
	}
	if err := s.store.CreateRegistrationToken(r.Context(), owner.UserID, hashSecret(token), expiresAt); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, CreateRegistrationTokenResponse{
		Token:       token,
		OwnerUserID: owner.UserID,
		ExpiresAt:   expiresAt,
	})
}

func (s *Server) handleCreateUserAPIKey(w http.ResponseWriter, r *http.Request) {
	principal, err := s.userPrincipal(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req CreateUserAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	key, err := newSecret("paxu")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate api key")
		return
	}
	meta, err := s.store.CreateUserAPIKey(r.Context(), principal, req.Name, hashSecret(key), keyPrefix(key))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, CreateUserAPIKeyResponse{APIKey: meta, Key: key})
}

func (s *Server) handleListUserAPIKeys(w http.ResponseWriter, r *http.Request) {
	principal, err := s.userPrincipal(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	keys, err := s.store.ListUserAPIKeys(r.Context(), principal)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"apiKeys": keys})
}

func (s *Server) handleRevokeUserAPIKey(w http.ResponseWriter, r *http.Request) {
	principal, err := s.userPrincipal(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	keyID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/user/api-keys/"), "/")
	if keyID == "" {
		writeError(w, http.StatusNotFound, "unknown api key route")
		return
	}
	if err := s.store.RevokeUserAPIKey(r.Context(), principal, keyID); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) withAgent(next func(http.ResponseWriter, *http.Request, Agent)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r.Header.Get("Authorization"))
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		agent, err := s.store.AuthenticateAgent(r.Context(), hashSecret(token))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		next(w, r, agent)
	}
}

func (s *Server) userPrincipal(r *http.Request) (UserPrincipal, error) {
	email, err := s.userEmail(r)
	if err != nil {
		return UserPrincipal{}, err
	}
	role := "user"
	if s.cfg.AdminEmails[strings.ToLower(email)] {
		role = "admin"
	}
	user, err := s.store.EnsureUser(r.Context(), email, "", role)
	if err != nil {
		return UserPrincipal{}, err
	}
	return UserPrincipal{User: user, IsAdmin: role == "admin"}, nil
}

func (s *Server) registrationOwner(r *http.Request) (User, error) {
	token := r.Header.Get("X-Registration-Token")
	if token != "" {
		owner, err := s.store.ResolveRegistrationToken(r.Context(), hashSecret(token))
		if err == nil {
			return owner, nil
		}
		if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrUnauthorized) {
			return User{}, err
		}
	}
	if s.cfg.RegistrationToken != "" && token == s.cfg.RegistrationToken {
		email := s.cfg.RegistrationOwnerEmail
		if email == "" {
			email = s.cfg.LocalUserID
		}
		return s.store.EnsureUser(r.Context(), email, "", roleForEmail(email, s.cfg.AdminEmails))
	}
	return User{}, ErrUnauthorized
}

func (s *Server) userEmail(r *http.Request) (string, error) {
	if v := r.Header.Get("Cf-Access-Authenticated-User-Email"); v != "" {
		return normalizeEmail(v), nil
	}
	if s.cfg.AllowLocalUserHeader {
		if v := r.Header.Get("X-User-Email"); v != "" {
			return normalizeEmail(v), nil
		}
		if s.cfg.LocalUserID != "" {
			return normalizeEmail(s.cfg.LocalUserID), nil
		}
	}
	return "", ErrUnauthorized
}

func roleForEmail(email string, admins map[string]bool) string {
	if admins[strings.ToLower(email)] {
		return "admin"
	}
	return "user"
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}

func websocketAPIKey(r *http.Request) string {
	if token := bearerToken(r.Header.Get("Authorization")); token != "" {
		return token
	}
	if token := r.URL.Query().Get("apiKey"); token != "" {
		return token
	}
	return r.URL.Query().Get("key")
}

func keyPrefix(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:12]
}

func resultMessageID(path string) (string, bool) {
	trimmed := strings.TrimPrefix(path, "/api/agent/messages/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "result" {
		return "", false
	}
	return parts[0], true
}

func parseIntQuery(r *http.Request, key string, fallback int) (int, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback, nil
	}
	return strconv.Atoi(raw)
}

func parseInt64Query(r *http.Request, key string, fallback int64) (int64, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback, nil
	}
	return strconv.ParseInt(raw, 10, 64)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, ErrConflict):
		writeError(w, http.StatusConflict, "conflict")
	default:
		log.Printf("store error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
