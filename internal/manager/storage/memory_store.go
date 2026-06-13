package storage

import (
	"context"
	"sort"
	"sync"
	"time"
)

type MemoryStore struct {
	mu               sync.Mutex
	now              func() time.Time
	nextMailbox      int64
	nextSession      int64
	agents           map[string]Agent
	apiKeys          map[string]string
	users            map[string]User
	usersByEmail     map[string]string
	regTokens        map[string]registrationToken
	userAPIKeys      map[string]UserAPIKey
	userAPIKeyHashes map[string]string
	sessions         map[string]AgentSession
	mailbox          map[int64]MailboxMessage
	offsets          map[string]int64
}

func NewMemoryStore(now func() time.Time) *MemoryStore {
	return &MemoryStore{
		now:              now,
		agents:           make(map[string]Agent),
		apiKeys:          make(map[string]string),
		users:            make(map[string]User),
		usersByEmail:     make(map[string]string),
		regTokens:        make(map[string]registrationToken),
		userAPIKeys:      make(map[string]UserAPIKey),
		userAPIKeyHashes: make(map[string]string),
		sessions:         make(map[string]AgentSession),
		mailbox:          make(map[int64]MailboxMessage),
		offsets:          make(map[string]int64),
	}
}

type registrationToken struct {
	OwnerUserID string
	ExpiresAt   *time.Time
	UsedAt      *time.Time
}

func (s *MemoryStore) EnsureUser(
	ctx context.Context,
	email string,
	displayName string,
	role string,
) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureUserLocked(email, displayName, role)
}

func (s *MemoryStore) GetUserByEmail(ctx context.Context, email string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	userID, ok := s.usersByEmail[normalizeEmail(email)]
	if !ok {
		return User{}, ErrNotFound
	}
	return s.users[userID], nil
}

func (s *MemoryStore) GetUser(ctx context.Context, userID string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[userID]
	if !ok {
		return User{}, ErrNotFound
	}
	return user, nil
}

func (s *MemoryStore) CreateRegistrationToken(
	ctx context.Context,
	ownerUserID string,
	tokenHash string,
	expiresAt *time.Time,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[ownerUserID]; !ok {
		return ErrNotFound
	}
	s.regTokens[tokenHash] = registrationToken{OwnerUserID: ownerUserID, ExpiresAt: expiresAt}
	return nil
}

func (s *MemoryStore) ResolveRegistrationToken(
	ctx context.Context,
	tokenHash string,
) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.regTokens[tokenHash]
	if !ok {
		return User{}, ErrUnauthorized
	}
	if token.UsedAt != nil {
		return User{}, ErrUnauthorized
	}
	now := s.now().UTC()
	if token.ExpiresAt != nil && token.ExpiresAt.Before(now) {
		return User{}, ErrUnauthorized
	}
	user, ok := s.users[token.OwnerUserID]
	if !ok {
		return User{}, ErrUnauthorized
	}
	token.UsedAt = &now
	s.regTokens[tokenHash] = token
	return user, nil
}

func (s *MemoryStore) CreateUserAPIKey(
	ctx context.Context,
	principal UserPrincipal,
	name string,
	keyHash string,
	prefix string,
) (UserAPIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keyID, err := newSecret("key")
	if err != nil {
		return UserAPIKey{}, err
	}
	key := UserAPIKey{
		KeyID:       keyID,
		OwnerUserID: principal.User.UserID,
		Name:        name,
		Prefix:      prefix,
		CreatedAt:   s.now().UTC(),
	}
	s.userAPIKeys[keyID] = key
	s.userAPIKeyHashes[keyHash] = keyID
	return key, nil
}

func (s *MemoryStore) ListUserAPIKeys(
	ctx context.Context,
	principal UserPrincipal,
) ([]UserAPIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]UserAPIKey, 0)
	for _, key := range s.userAPIKeys {
		if !canAccessOwner(principal, key.OwnerUserID) {
			continue
		}
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *MemoryStore) RevokeUserAPIKey(
	ctx context.Context,
	principal UserPrincipal,
	keyID string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.userAPIKeys[keyID]
	if !ok || !canAccessOwner(principal, key.OwnerUserID) {
		return ErrNotFound
	}
	now := s.now().UTC()
	key.RevokedAt = &now
	s.userAPIKeys[keyID] = key
	return nil
}

func (s *MemoryStore) AuthenticateUserAPIKey(ctx context.Context, keyHash string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keyID, ok := s.userAPIKeyHashes[keyHash]
	if !ok {
		return User{}, ErrUnauthorized
	}
	key, ok := s.userAPIKeys[keyID]
	if !ok || key.RevokedAt != nil {
		return User{}, ErrUnauthorized
	}
	user, ok := s.users[key.OwnerUserID]
	if !ok {
		return User{}, ErrUnauthorized
	}
	now := s.now().UTC()
	key.LastUsedAt = &now
	s.userAPIKeys[keyID] = key
	return user, nil
}

func (s *MemoryStore) RegisterAgent(
	ctx context.Context,
	owner User,
	req RegisterAgentRequest,
	apiKeyHash string,
) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[owner.UserID]; !ok {
		return Agent{}, ErrNotFound
	}

	agentID, err := newSecret("agent")
	if err != nil {
		return Agent{}, err
	}
	now := s.now().UTC()
	agent := Agent{
		AgentID:       agentID,
		OwnerUserID:   owner.UserID,
		Name:          defaultAgentName(req),
		Hostname:      req.Hostname,
		AgentType:     defaultAgentType(req),
		MachineType:   req.MachineType,
		OS:            req.OS,
		HermesVersion: req.HermesVersion,
		APIEndpoint:   defaultAPIEndpoint(req.APIEndpoint),
		Status:        "online",
		Online:        true,
		LastHeartbeat: &now,
		RegisteredAt:  now,
		Metadata:      req.Metadata,
	}
	s.agents[agentID] = agent
	s.apiKeys[apiKeyHash] = agentID
	return agent, nil
}

func (s *MemoryStore) AuthenticateAgent(ctx context.Context, apiKeyHash string) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	agentID, ok := s.apiKeys[apiKeyHash]
	if !ok {
		return Agent{}, ErrUnauthorized
	}
	agent, ok := s.agents[agentID]
	if !ok {
		return Agent{}, ErrUnauthorized
	}
	return agent, nil
}

func (s *MemoryStore) UpsertAgentStatus(ctx context.Context, report AgentStatusReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	agent, ok := s.agents[report.AgentID]
	if !ok {
		return ErrNotFound
	}
	now := s.now().UTC()
	agent.Status = "online"
	agent.Online = true
	agent.LastHeartbeat = &now
	if report.Hostname != "" {
		agent.Hostname = report.Hostname
	}
	s.agents[report.AgentID] = agent

	for _, input := range report.Sessions {
		if input.SessionID == "" {
			continue
		}
		existing, exists := s.sessions[sessionKey(report.AgentID, input.SessionID)]
		if !exists {
			s.nextSession++
			existing.ID = s.nextSession
			existing.AgentID = report.AgentID
			existing.SessionID = input.SessionID
			existing.CreatedAt = now
		}
		existing.SessionName = input.SessionName
		existing.AgentType = input.AgentType
		existing.NativeID = input.NativeID
		existing.ProjectID = input.ProjectID
		existing.Preview = input.Preview
		existing.WorkspaceRoots = append([]string(nil), input.WorkspaceRoots...)
		existing.Source = input.Source
		existing.Status = defaultSessionStatus(input.Status)
		existing.CurrentTask = input.CurrentTask
		existing.LastMessageAt = input.LastMessageAt
		existing.MessageCount = input.MessageCount
		existing.TokenInput = input.TokenUsage.Input
		existing.TokenOutput = input.TokenUsage.Output
		existing.TokenTotal = input.TokenUsage.Total
		existing.Model = input.Model
		existing.RunID = input.RunID
		existing.RunStatus = input.RunStatus
		existing.UpdatedAt = now
		s.sessions[sessionKey(report.AgentID, input.SessionID)] = existing
	}

	return nil
}

func (s *MemoryStore) ListAgents(ctx context.Context, principal UserPrincipal) ([]Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Agent, 0, len(s.agents))
	for _, agent := range s.agents {
		if !canAccessOwner(principal, agent.OwnerUserID) {
			continue
		}
		out = append(out, agent)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RegisteredAt.Before(out[j].RegisteredAt)
	})
	return out, nil
}

func (s *MemoryStore) GetAgent(
	ctx context.Context,
	principal UserPrincipal,
	agentID string,
) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	agent, ok := s.agents[agentID]
	if !ok || !canAccessOwner(principal, agent.OwnerUserID) {
		return Agent{}, ErrNotFound
	}
	return agent, nil
}

func (s *MemoryStore) ListAgentSessions(
	ctx context.Context,
	principal UserPrincipal,
	agentID string,
) ([]AgentSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	agent, ok := s.agents[agentID]
	if !ok || !canAccessOwner(principal, agent.OwnerUserID) {
		return nil, ErrNotFound
	}
	out := make([]AgentSession, 0)
	for _, session := range s.sessions {
		if session.AgentID == agentID {
			out = append(out, session)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (s *MemoryStore) GetSession(
	ctx context.Context,
	principal UserPrincipal,
	sessionID string,
) (AgentSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, session := range s.sessions {
		if session.SessionID == sessionID {
			agent, ok := s.agents[session.AgentID]
			if !ok || !canAccessOwner(principal, agent.OwnerUserID) {
				return AgentSession{}, ErrNotFound
			}
			return session, nil
		}
	}
	return AgentSession{}, ErrNotFound
}

func (s *MemoryStore) ListSessionMessages(
	ctx context.Context,
	principal UserPrincipal,
	sessionID string,
) ([]MailboxMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]MailboxMessage, 0)
	for _, msg := range s.mailbox {
		if msg.SessionID == sessionID {
			if !canAccessOwner(principal, msg.OwnerUserID) {
				continue
			}
			out = append(out, msg)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *MemoryStore) CreateMailboxMessage(
	ctx context.Context,
	principal UserPrincipal,
	req CreateMailboxRequest,
) (MailboxMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	agent, ok := s.agents[req.AgentID]
	if !ok || !canAccessOwner(principal, agent.OwnerUserID) {
		return MailboxMessage{}, ErrNotFound
	}
	messageType := defaultMessageType(req.MessageType)
	if messageType == "" {
		return MailboxMessage{}, ErrConflict
	}
	messageID, err := newSecret("msg")
	if err != nil {
		return MailboxMessage{}, err
	}
	payload, err := mailboxPayload(req)
	if err != nil {
		return MailboxMessage{}, ErrConflict
	}
	now := s.now().UTC()
	s.nextMailbox++
	msg := MailboxMessage{
		ID:          s.nextMailbox,
		MessageID:   messageID,
		UserID:      principal.User.UserID,
		OwnerUserID: agent.OwnerUserID,
		AgentID:     req.AgentID,
		SessionID:   req.SessionID,
		Message:     req.Message,
		MessageType: messageType,
		Payload:     payload,
		Status:      "pending",
		CreatedAt:   now,
		ExpiresAt:   expiresAt(now, messageType),
	}
	s.mailbox[msg.ID] = msg
	return msg, nil
}

func (s *MemoryStore) ListMailbox(
	ctx context.Context,
	filter MailboxFilter,
) ([]MailboxMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]MailboxMessage, 0)
	for _, msg := range s.mailbox {
		if !canAccessOwner(filter.Principal, msg.OwnerUserID) {
			continue
		}
		if filter.AgentID != "" && msg.AgentID != filter.AgentID {
			continue
		}
		if filter.SessionID != "" && msg.SessionID != filter.SessionID {
			continue
		}
		if filter.Status != "" && msg.Status != filter.Status {
			continue
		}
		out = append(out, msg)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID > out[j].ID
	})
	return limitMailbox(out, filter.Limit), nil
}

func (s *MemoryStore) PullMailbox(
	ctx context.Context,
	agentID string,
	sessionID string,
	offset int64,
	limit int,
) (MailboxPull, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 || limit > 100 {
		limit = 10
	}

	now := s.now().UTC()
	candidates := make([]MailboxMessage, 0)
	for _, msg := range s.mailbox {
		if msg.AgentID != agentID || msg.ID <= offset {
			continue
		}
		if sessionID != "" && msg.SessionID != sessionID {
			continue
		}
		if msg.Status != "pending" {
			continue
		}
		if msg.ExpiresAt != nil && msg.ExpiresAt.Before(now) {
			msg.Status = "expired"
			s.mailbox[msg.ID] = msg
			continue
		}
		candidates = append(candidates, msg)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ID < candidates[j].ID
	})

	hasMore := len(candidates) > limit
	if hasMore {
		candidates = candidates[:limit]
	}

	maxOffset := offset
	deliveredAt := now
	for i := range candidates {
		candidates[i].Status = "delivered"
		candidates[i].DeliveredAt = &deliveredAt
		s.mailbox[candidates[i].ID] = candidates[i]
		if candidates[i].ID > maxOffset {
			maxOffset = candidates[i].ID
		}
	}

	return MailboxPull{Messages: candidates, MaxOffset: maxOffset, HasMore: hasMore}, nil
}

func (s *MemoryStore) MarkMessageResult(
	ctx context.Context,
	agentID string,
	messageID string,
	req MessageResultRequest,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, msg := range s.mailbox {
		if msg.AgentID == agentID && msg.MessageID == messageID {
			if req.Status != "completed" && req.Status != "failed" {
				return ErrConflict
			}
			completedAt := s.now().UTC()
			if req.CompletedAt != nil {
				completedAt = req.CompletedAt.UTC()
			}
			msg.Status = req.Status
			msg.Result = req.Result
			msg.Error = req.Error
			msg.CompletedAt = &completedAt
			s.mailbox[id] = msg
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) UpdateOffset(ctx context.Context, agentID string, offset int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.agents[agentID]; !ok {
		return ErrNotFound
	}
	if offset > s.offsets[agentID] {
		s.offsets[agentID] = offset
	}
	return nil
}

func sessionKey(agentID, sessionID string) string {
	return agentID + "\x00" + sessionID
}

func defaultAPIEndpoint(v string) string {
	if v != "" {
		return v
	}
	return "http://localhost:8642"
}

func (s *MemoryStore) ensureUserLocked(
	email string,
	displayName string,
	role string,
) (User, error) {
	email = normalizeEmail(email)
	if email == "" {
		return User{}, ErrUnauthorized
	}
	if role == "" {
		role = "user"
	}
	if userID, ok := s.usersByEmail[email]; ok {
		user := s.users[userID]
		now := s.now().UTC()
		user.LastSeenAt = &now
		if displayName != "" {
			user.DisplayName = displayName
		}
		user.Role = role
		s.users[userID] = user
		return user, nil
	}
	userID, err := newSecret("usr")
	if err != nil {
		return User{}, err
	}
	now := s.now().UTC()
	user := User{
		UserID:      userID,
		Email:       email,
		DisplayName: displayName,
		Role:        role,
		CreatedAt:   now,
		LastSeenAt:  &now,
	}
	s.users[userID] = user
	s.usersByEmail[email] = userID
	return user, nil
}

func defaultAgentName(req RegisterAgentRequest) string {
	if req.Name != "" {
		return req.Name
	}
	return req.Hostname
}

func defaultAgentType(req RegisterAgentRequest) string {
	if req.AgentType != "" {
		return req.AgentType
	}
	if req.MachineType != "" {
		return req.MachineType
	}
	return "hermes"
}

func defaultSessionStatus(v string) string {
	if v != "" {
		return v
	}
	return "idle"
}

func limitMailbox(messages []MailboxMessage, limit int) []MailboxMessage {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if len(messages) <= limit {
		return messages
	}
	return messages[:limit]
}
