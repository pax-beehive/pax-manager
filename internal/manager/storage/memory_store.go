package storage

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type MemoryStore struct {
	mu                        sync.Mutex
	now                       func() time.Time
	nextMailbox               int64
	nextSession               int64
	nodes                     map[string]Node
	agents                    map[string]Agent
	apiKeys                   map[string]string
	nodeAPIKeys               map[string]string
	users                     map[string]User
	usersByEmail              map[string]string
	regTokens                 map[string]registrationToken
	nodeRegistrations         map[string]NodeRegistrationSession
	nodeRegistrationPairCodes map[string]string
	userAPIKeys               map[string]UserAPIKey
	userAPIKeyHashes          map[string]string
	sessions                  map[string]AgentSession
	mailbox                   map[int64]MailboxMessage
	offsets                   map[string]int64
	nextTransportID           int64
	transportJournal          map[transportFrameKey]TransportFrame
	nextMessageID             int64
	nextPartID                int64
	messages                  map[string]Message
	messageLogical            map[string]string
	messageParts              map[messagePartKey]MessagePart
	approvals                 map[string]AgentApproval
	secrets                   map[string]Secret
	secretVersions            map[string]SecretVersion
	secretVersionIDs          map[string][]string
	secretAccess              []SecretAccessEvent
	paxdArtifacts             map[string]PaxdArtifact
	paxdArtifactKeys          map[string]string
}

func NewMemoryStore(now func() time.Time) *MemoryStore {
	return &MemoryStore{
		now:                       now,
		nodes:                     make(map[string]Node),
		agents:                    make(map[string]Agent),
		apiKeys:                   make(map[string]string),
		nodeAPIKeys:               make(map[string]string),
		users:                     make(map[string]User),
		usersByEmail:              make(map[string]string),
		regTokens:                 make(map[string]registrationToken),
		nodeRegistrations:         make(map[string]NodeRegistrationSession),
		nodeRegistrationPairCodes: make(map[string]string),
		userAPIKeys:               make(map[string]UserAPIKey),
		userAPIKeyHashes:          make(map[string]string),
		sessions:                  make(map[string]AgentSession),
		mailbox:                   make(map[int64]MailboxMessage),
		offsets:                   make(map[string]int64),
		transportJournal:          make(map[transportFrameKey]TransportFrame),
		messages:                  make(map[string]Message),
		messageLogical:            make(map[string]string),
		messageParts:              make(map[messagePartKey]MessagePart),
		approvals:                 make(map[string]AgentApproval),
		secrets:                   make(map[string]Secret),
		secretVersions:            make(map[string]SecretVersion),
		secretVersionIDs:          make(map[string][]string),
		paxdArtifacts:             make(map[string]PaxdArtifact),
		paxdArtifactKeys:          make(map[string]string),
	}
}

type transportFrameKey struct {
	AgentID        string
	Stream         string
	Seq            int64
	LocalDirection string
}

type messagePartKey struct {
	MessageID string
	Index     int
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

func (s *MemoryStore) CreateSecret(
	ctx context.Context,
	principal UserPrincipal,
	req CreateSecretRequest,
	encrypted SecretVersion,
) (Secret, SecretVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secretID, err := newSecret("sec")
	if err != nil {
		return Secret{}, SecretVersion{}, err
	}
	versionID, err := newSecret("secver")
	if err != nil {
		return Secret{}, SecretVersion{}, err
	}
	now := s.now().UTC()
	secret := Secret{
		SecretID:         secretID,
		OwnerUserID:      principal.User.UserID,
		Name:             req.Name,
		Kind:             req.Kind,
		Description:      req.Description,
		Metadata:         jsonDefault(req.Metadata, "{}"),
		CurrentVersionID: versionID,
		CurrentVersion:   1,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	version := encrypted
	version.VersionID = versionID
	version.SecretID = secretID
	version.VersionNumber = 1
	version.State = "active"
	version.CreatedAt = now
	version.CreatedByUserID = principal.User.UserID
	s.secrets[secretID] = secret
	s.secretVersions[versionID] = version
	s.secretVersionIDs[secretID] = append(s.secretVersionIDs[secretID], versionID)
	return secret, version, nil
}

func (s *MemoryStore) ListSecrets(ctx context.Context, principal UserPrincipal) ([]Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Secret, 0)
	for _, secret := range s.secrets {
		if secret.DeletedAt != nil || !canAccessOwner(principal, secret.OwnerUserID) {
			continue
		}
		out = append(out, secret)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *MemoryStore) GetSecret(
	ctx context.Context,
	principal UserPrincipal,
	secretID string,
) (Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret, ok := s.secrets[secretID]
	if !ok || secret.DeletedAt != nil || !canAccessOwner(principal, secret.OwnerUserID) {
		return Secret{}, ErrNotFound
	}
	return secret, nil
}

func (s *MemoryStore) GetSecretVersionForNode(
	ctx context.Context,
	node Node,
	agentID string,
	secretID string,
	versionSelector string,
) (Secret, SecretVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[agentID]
	if !ok || agent.NodeID != node.NodeID || agent.OwnerUserID != node.OwnerUserID {
		return Secret{}, SecretVersion{}, ErrNotFound
	}
	secret, ok := s.secrets[secretID]
	if !ok || secret.DeletedAt != nil || secret.OwnerUserID != node.OwnerUserID {
		return Secret{}, SecretVersion{}, ErrNotFound
	}
	versionID := secret.CurrentVersionID
	if selector := strings.TrimSpace(versionSelector); selector != "" && selector != "latest" {
		selector = strings.TrimPrefix(selector, "version:")
		versionID = ""
		for _, candidateID := range s.secretVersionIDs[secretID] {
			candidate := s.secretVersions[candidateID]
			if strconv.FormatInt(candidate.VersionNumber, 10) == selector {
				versionID = candidateID
				break
			}
		}
	}
	version, ok := s.secretVersions[versionID]
	if !ok || version.State != "active" {
		return Secret{}, SecretVersion{}, ErrNotFound
	}
	return secret, version, nil
}

func (s *MemoryStore) CreateSecretVersion(
	ctx context.Context,
	node Node,
	agentID string,
	req WriteSecretVersionRequest,
	encrypted SecretVersion,
) (SecretVersion, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[agentID]
	if !ok || agent.NodeID != node.NodeID || agent.OwnerUserID != node.OwnerUserID {
		return SecretVersion{}, false, ErrNotFound
	}
	secret, ok := s.secrets[req.SecretID]
	if !ok || secret.DeletedAt != nil || secret.OwnerUserID != node.OwnerUserID {
		return SecretVersion{}, false, ErrNotFound
	}
	if req.IdempotencyKey != "" {
		for _, version := range s.secretVersions {
			if version.SecretID == req.SecretID &&
				version.CreatedByNodeID == node.NodeID &&
				version.CreatedByAgentID == agentID &&
				version.IdempotencyKey == req.IdempotencyKey {
				return version, secret.CurrentVersionID == version.VersionID, nil
			}
		}
	}
	versionID, err := newSecret("secver")
	if err != nil {
		return SecretVersion{}, false, err
	}
	version := encrypted
	version.VersionID = versionID
	version.SecretID = req.SecretID
	version.VersionNumber = secret.CurrentVersion + 1
	version.State = "active"
	version.CreatedAt = s.now().UTC()
	version.CreatedByNodeID = node.NodeID
	version.CreatedByAgentID = agentID
	version.IdempotencyKey = req.IdempotencyKey
	if req.MakeCurrent {
		if req.ExpectedCurrentVersionID == "" ||
			req.ExpectedCurrentVersionID != secret.CurrentVersionID {
			return SecretVersion{}, false, ErrConflict
		}
		secret.CurrentVersionID = versionID
		secret.CurrentVersion = version.VersionNumber
		secret.UpdatedAt = version.CreatedAt
		s.secrets[req.SecretID] = secret
	}
	s.secretVersions[versionID] = version
	s.secretVersionIDs[req.SecretID] = append(s.secretVersionIDs[req.SecretID], versionID)
	return version, req.MakeCurrent, nil
}

func (s *MemoryStore) RecordSecretAccess(ctx context.Context, event SecretAccessEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.AgentID != "" && event.SessionID != "" {
		event.SessionID = s.virtualSessionIDLocked(event.AgentID, event.SessionID)
	}
	s.secretAccess = append(s.secretAccess, event)
	return nil
}

func (s *MemoryStore) CreatePaxdArtifact(
	ctx context.Context,
	req CreatePaxdArtifactRequest,
	createdBy string,
) (PaxdArtifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now().UTC()
	key := paxdArtifactObjectKey(req.Bucket, req.Object, req.Generation)
	artifactID := s.paxdArtifactKeys[key]
	if artifactID == "" {
		var err error
		artifactID, err = newSecret("paxdart")
		if err != nil {
			return PaxdArtifact{}, err
		}
	}
	artifact := PaxdArtifact{
		ArtifactID:  artifactID,
		Platform:    req.Platform,
		Tags:        append([]string(nil), req.Tags...),
		Version:     req.Version,
		BuildID:     req.BuildID,
		Bucket:      req.Bucket,
		Object:      req.Object,
		Generation:  req.Generation,
		SHA256:      req.SHA256,
		SizeBytes:   req.SizeBytes,
		ContentType: req.ContentType,
		CreatedBy:   createdBy,
		CreatedAt:   now,
	}
	if existing, ok := s.paxdArtifacts[artifactID]; ok {
		artifact.CreatedAt = existing.CreatedAt
	}
	s.paxdArtifacts[artifactID] = artifact
	s.paxdArtifactKeys[key] = artifactID
	return artifact, nil
}

func (s *MemoryStore) FindPaxdArtifact(
	ctx context.Context,
	req FindPaxdArtifactRequest,
) (PaxdArtifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var found PaxdArtifact
	for _, artifact := range s.paxdArtifacts {
		if artifact.DeletedAt != nil ||
			artifact.Platform != req.Platform ||
			!paxdArtifactHasTags(artifact.Tags, req.Tags) {
			continue
		}
		if found.ArtifactID == "" ||
			artifact.CreatedAt.After(found.CreatedAt) ||
			(artifact.CreatedAt.Equal(found.CreatedAt) && artifact.ArtifactID > found.ArtifactID) {
			found = artifact
		}
	}
	if found.ArtifactID == "" {
		return PaxdArtifact{}, ErrNotFound
	}
	found.Tags = append([]string(nil), found.Tags...)
	return found, nil
}

func paxdArtifactObjectKey(bucket string, object string, generation int64) string {
	return bucket + "\x00" + object + "\x00" + strconv.FormatInt(generation, 10)
}

func paxdArtifactHasTags(artifactTags []string, requiredTags []string) bool {
	if len(requiredTags) == 0 {
		return true
	}
	seen := make(map[string]bool, len(artifactTags))
	for _, tag := range artifactTags {
		seen[tag] = true
	}
	for _, tag := range requiredTags {
		if !seen[tag] {
			return false
		}
	}
	return true
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
	nodeID, err := newSecret("node")
	if err != nil {
		return Agent{}, err
	}
	now := s.now().UTC()
	node := Node{
		NodeID:        nodeID,
		OwnerUserID:   owner.UserID,
		Name:          defaultAgentName(req),
		Hostname:      req.Hostname,
		MachineType:   req.MachineType,
		OS:            req.OS,
		PaxdVersion:   req.HermesVersion,
		APIEndpoint:   defaultAPIEndpoint(req.APIEndpoint),
		Status:        "online",
		Online:        true,
		LastHeartbeat: &now,
		RegisteredAt:  now,
		Metadata:      req.Metadata,
	}
	agent := Agent{
		AgentID:       agentID,
		NodeID:        nodeID,
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
	s.nodes[nodeID] = node
	s.agents[agentID] = agent
	s.apiKeys[apiKeyHash] = agentID
	s.nodeAPIKeys[apiKeyHash] = nodeID
	return agent, nil
}

func (s *MemoryStore) RegisterNode(
	ctx context.Context,
	owner User,
	req RegisterNodeRequest,
	apiKeyHash string,
) (Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[owner.UserID]; !ok {
		return Node{}, ErrNotFound
	}
	nodeID, err := newSecret("node")
	if err != nil {
		return Node{}, err
	}
	now := s.now().UTC()
	node := Node{
		NodeID:        nodeID,
		OwnerUserID:   owner.UserID,
		Name:          defaultNodeName(req),
		Hostname:      req.Hostname,
		MachineType:   req.MachineType,
		OS:            defaultOS(req.OS),
		Arch:          req.Arch,
		PaxdVersion:   req.PaxdVersion,
		APIEndpoint:   defaultAPIEndpoint(req.APIEndpoint),
		Status:        "online",
		Online:        true,
		LastHeartbeat: &now,
		RegisteredAt:  now,
		Metadata:      req.Metadata,
	}
	s.nodes[nodeID] = node
	s.nodeAPIKeys[apiKeyHash] = nodeID
	return node, nil
}

func (s *MemoryStore) CreateNodeRegistrationSession(
	ctx context.Context,
	session NodeRegistrationSession,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodeRegistrations[session.RegistrationID]; ok {
		return ErrConflict
	}
	if _, ok := s.nodeRegistrationPairCodes[session.PairCode]; ok {
		return ErrConflict
	}
	s.nodeRegistrations[session.RegistrationID] = session
	s.nodeRegistrationPairCodes[session.PairCode] = session.RegistrationID
	return nil
}

func (s *MemoryStore) DeleteStaleNodeRegistrationSessions(
	ctx context.Context,
	cutoff time.Time,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for registrationID, session := range s.nodeRegistrations {
		if session.ExpiresAt.After(cutoff) &&
			session.Status != domain.NodeRegistrationStatusConsumed &&
			session.Status != domain.NodeRegistrationStatusDenied &&
			session.Status != domain.NodeRegistrationStatusExpired {
			continue
		}
		delete(s.nodeRegistrations, registrationID)
		delete(s.nodeRegistrationPairCodes, session.PairCode)
	}
	return nil
}

func (s *MemoryStore) GetNodeRegistrationSession(
	ctx context.Context,
	pairCode string,
) (NodeRegistrationSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	registrationID, ok := s.nodeRegistrationPairCodes[pairCode]
	if !ok {
		return NodeRegistrationSession{}, ErrNotFound
	}
	session := s.nodeRegistrations[registrationID]
	if session.Status == domain.NodeRegistrationStatusPending &&
		!session.ExpiresAt.After(s.now().UTC()) {
		session.Status = domain.NodeRegistrationStatusExpired
	}
	return session, nil
}

func (s *MemoryStore) ApproveNodeRegistrationSession(
	ctx context.Context,
	principal UserPrincipal,
	pairCode string,
) (NodeRegistrationSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	registrationID, ok := s.nodeRegistrationPairCodes[pairCode]
	if !ok {
		return NodeRegistrationSession{}, ErrNotFound
	}
	session := s.nodeRegistrations[registrationID]
	if session.Status != domain.NodeRegistrationStatusPending {
		return NodeRegistrationSession{}, ErrConflict
	}
	now := s.now().UTC()
	if !session.ExpiresAt.After(now) {
		session.Status = domain.NodeRegistrationStatusExpired
		s.nodeRegistrations[registrationID] = session
		return NodeRegistrationSession{}, ErrUnauthorized
	}
	session.Status = domain.NodeRegistrationStatusApproved
	session.OwnerUserID = principal.User.UserID
	session.ApprovedAt = &now
	s.nodeRegistrations[registrationID] = session
	return session, nil
}

func (s *MemoryStore) PollNodeRegistrationSession(
	ctx context.Context,
	registrationID string,
	pollTokenHash string,
) (NodeRegistrationSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.nodeRegistrations[registrationID]
	if !ok || session.PollTokenHash != pollTokenHash {
		return NodeRegistrationSession{}, ErrUnauthorized
	}
	if session.Status == domain.NodeRegistrationStatusPending &&
		!session.ExpiresAt.After(s.now().UTC()) {
		session.Status = domain.NodeRegistrationStatusExpired
		s.nodeRegistrations[registrationID] = session
	}
	return session, nil
}

func (s *MemoryStore) ConsumeNodeRegistrationSession(
	ctx context.Context,
	registrationID string,
	pollTokenHash string,
	apiKeyHash string,
) (Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.nodeRegistrations[registrationID]
	if !ok || session.PollTokenHash != pollTokenHash {
		return Node{}, ErrUnauthorized
	}
	if session.Status != domain.NodeRegistrationStatusApproved || session.OwnerUserID == "" {
		return Node{}, ErrConflict
	}
	now := s.now().UTC()
	if !session.ExpiresAt.After(now) {
		session.Status = domain.NodeRegistrationStatusExpired
		s.nodeRegistrations[registrationID] = session
		return Node{}, ErrUnauthorized
	}
	owner, ok := s.users[session.OwnerUserID]
	if !ok {
		return Node{}, ErrUnauthorized
	}
	nodeID, err := newSecret("node")
	if err != nil {
		return Node{}, err
	}
	node := Node{
		NodeID:        nodeID,
		OwnerUserID:   owner.UserID,
		Name:          defaultNodeName(session.Request),
		Hostname:      session.Request.Hostname,
		MachineType:   session.Request.MachineType,
		OS:            defaultOS(session.Request.OS),
		Arch:          session.Request.Arch,
		PaxdVersion:   session.Request.PaxdVersion,
		APIEndpoint:   defaultAPIEndpoint(session.Request.APIEndpoint),
		Status:        "online",
		Online:        true,
		LastHeartbeat: &now,
		RegisteredAt:  now,
		Metadata:      session.Request.Metadata,
	}
	s.nodes[nodeID] = node
	s.nodeAPIKeys[apiKeyHash] = nodeID
	session.Status = domain.NodeRegistrationStatusConsumed
	session.NodeID = nodeID
	session.ConsumedAt = &now
	s.nodeRegistrations[registrationID] = session
	return node, nil
}

func (s *MemoryStore) AuthenticateNode(ctx context.Context, apiKeyHash string) (Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	nodeID, ok := s.nodeAPIKeys[apiKeyHash]
	if !ok {
		return Node{}, ErrUnauthorized
	}
	node, ok := s.nodes[nodeID]
	if !ok {
		return Node{}, ErrUnauthorized
	}
	return node, nil
}

func (s *MemoryStore) UpsertNodeStatus(
	ctx context.Context,
	node Node,
	report NodeStatusReport,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.nodes[node.NodeID]
	if !ok {
		return ErrNotFound
	}
	now := s.now().UTC()
	current.Status = "online"
	current.Online = true
	current.LastHeartbeat = &now
	if report.Hostname != "" {
		current.Hostname = report.Hostname
	}
	if len(report.Metadata) > 0 {
		current.Metadata = report.Metadata
	}
	s.nodes[node.NodeID] = current

	for _, input := range report.Agents {
		agent, err := s.upsertNodeAgentLocked(current, input, now)
		if err != nil {
			return err
		}
		for _, session := range input.Sessions {
			if session.SessionID == "" {
				continue
			}
			session, err = s.normalizeReportedSessionLocked(agent.AgentID, session)
			if err != nil {
				return err
			}
			s.upsertSessionLocked(current.NodeID, agent.AgentID, session, now)
		}
	}
	return nil
}

func (s *MemoryStore) ListNodes(ctx context.Context, principal UserPrincipal) ([]Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Node, 0, len(s.nodes))
	for _, node := range s.nodes {
		if canAccessOwner(principal, node.OwnerUserID) {
			out = append(out, node)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RegisteredAt.Before(out[j].RegisteredAt)
	})
	return out, nil
}

func (s *MemoryStore) GetNode(
	ctx context.Context,
	principal UserPrincipal,
	nodeID string,
) (Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[nodeID]
	if !ok || !canAccessOwner(principal, node.OwnerUserID) {
		return Node{}, ErrNotFound
	}
	return node, nil
}

func (s *MemoryStore) GetNodeAgent(
	ctx context.Context,
	nodeID string,
	agentID string,
) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[agentID]
	if !ok || agent.NodeID != nodeID {
		return Agent{}, ErrNotFound
	}
	return agent, nil
}

func (s *MemoryStore) ListNodeAgents(
	ctx context.Context,
	principal UserPrincipal,
	nodeID string,
) ([]Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[nodeID]
	if !ok || !canAccessOwner(principal, node.OwnerUserID) {
		return nil, ErrNotFound
	}
	out := make([]Agent, 0)
	for _, agent := range s.agents {
		if agent.NodeID == nodeID {
			out = append(out, agent)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RegisteredAt.Before(out[j].RegisteredAt)
	})
	return out, nil
}

func (s *MemoryStore) CreateNodeAgent(
	ctx context.Context,
	principal UserPrincipal,
	req CreateAgentRequest,
) (Agent, MailboxMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[req.NodeID]
	if !ok || !canAccessOwner(principal, node.OwnerUserID) {
		return Agent{}, MailboxMessage{}, ErrNotFound
	}
	now := s.now().UTC()
	agent, err := s.createNodeAgentLocked(node, req.Name, req.AgentType, req.Metadata, now)
	if err != nil {
		return Agent{}, MailboxMessage{}, err
	}
	msg, err := s.createMailboxLocked(
		principal.User.UserID,
		node.OwnerUserID,
		node.NodeID,
		agent.AgentID,
		"",
		"bootstrap",
		"command",
		req.Metadata,
		"pending",
		now,
	)
	if err != nil {
		return Agent{}, MailboxMessage{}, err
	}
	return agent, msg, nil
}

func (s *MemoryStore) CreateNodeAgentSession(
	ctx context.Context,
	principal UserPrincipal,
	req CreateSessionRequest,
) (AgentSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[req.AgentID]
	if !ok || agent.NodeID != req.NodeID || !canAccessOwner(principal, agent.OwnerUserID) {
		return AgentSession{}, ErrNotFound
	}
	now := s.now().UTC()
	input := SessionStatusInput{
		SessionID:      req.SessionID,
		AgentType:      req.AgentType,
		NativeID:       req.NativeID,
		SessionName:    req.SessionName,
		ProjectID:      req.ProjectID,
		WorkspaceRoots: req.WorkspaceRoots,
		Source:         req.Source,
		Status:         "idle",
	}
	if input.SessionID == "" {
		generated, err := newSecret("sess")
		if err != nil {
			return AgentSession{}, err
		}
		input.SessionID = generated
	}
	return s.upsertSessionLocked(req.NodeID, req.AgentID, input, now), nil
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
		nodeID := agent.NodeID
		var err error
		input, err = s.normalizeReportedSessionLocked(report.AgentID, input)
		if err != nil {
			return err
		}
		s.upsertSessionLocked(nodeID, report.AgentID, input, now)
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

func (s *MemoryStore) UpdateSessionRuntimeState(
	ctx context.Context,
	state SessionRuntimeState,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state.AgentID == "" || state.SessionID == "" {
		return ErrNotFound
	}
	agent, ok := s.agents[state.AgentID]
	if !ok {
		return ErrNotFound
	}
	if state.NodeID == "" {
		state.NodeID = agent.NodeID
	}
	if state.OwnerUserID == "" {
		state.OwnerUserID = agent.OwnerUserID
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = s.now().UTC()
	}
	status, currentTask, runID, runStatus := state.StatusSummary()
	session, ok := s.sessions[sessionKey(state.AgentID, state.SessionID)]
	if !ok {
		session = AgentSession{
			ID:        int64(len(s.sessions) + 1),
			NodeID:    state.NodeID,
			AgentID:   state.AgentID,
			SessionID: state.SessionID,
			Status:    status,
			CreatedAt: state.UpdatedAt,
		}
	}
	session.NodeID = firstNonEmpty(state.NodeID, session.NodeID)
	session.Status = status
	session.CurrentTask = currentTask
	session.RunID = runID
	session.RunStatus = runStatus
	session.UpdatedAt = state.UpdatedAt
	session.RuntimeState = &state
	session.Metadata = runtimeMetadata(session.Metadata, state)
	s.sessions[sessionKey(state.AgentID, state.SessionID)] = session
	return nil
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
	if req.NodeID != "" && agent.NodeID != req.NodeID {
		return MailboxMessage{}, ErrNotFound
	}
	if req.SessionID != "" {
		session, ok := s.sessions[sessionKey(req.AgentID, req.SessionID)]
		if !ok {
			return MailboxMessage{}, ErrNotFound
		}
		if req.NodeID != "" && session.NodeID != "" && session.NodeID != agent.NodeID {
			return MailboxMessage{}, ErrNotFound
		}
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
		NodeID:      agent.NodeID,
		AgentID:     req.AgentID,
		SessionID:   req.SessionID,
		Message:     req.Message,
		MessageType: messageType,
		Payload:     payload,
		Status:      "pending",
		Direction:   "user_to_node",
		CreatedAt:   now,
		ExpiresAt:   expiresAt(now, messageType),
	}
	s.mailbox[msg.ID] = msg
	if err := s.saveMailboxHistoryLocked(msg); err != nil {
		return MailboxMessage{}, err
	}
	return msg, nil
}

func (s *MemoryStore) CreateApproval(
	ctx context.Context,
	node Node,
	req CreateApprovalRequest,
) (AgentApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[req.AgentID]
	if !ok || agent.NodeID != node.NodeID || agent.OwnerUserID != node.OwnerUserID {
		return AgentApproval{}, ErrNotFound
	}
	sessionID := req.SessionID
	if sessionID != "" {
		sessionID = s.virtualSessionIDLocked(req.AgentID, sessionID)
	}
	approvalID, err := newSecret("appr")
	if err != nil {
		return AgentApproval{}, err
	}
	approval := AgentApproval{
		ApprovalID:        approvalID,
		OwnerUserID:       node.OwnerUserID,
		RequestNodeID:     node.NodeID,
		RequestAgentID:    req.AgentID,
		RequestSessionID:  sessionID,
		SourceMessageID:   req.SourceMessageID,
		Domain:            defaultApprovalDomain(req.Domain),
		Operation:         req.Operation,
		ResourceType:      req.ResourceType,
		ResourceRef:       req.ResourceRef,
		Title:             req.Title,
		Description:       req.Description,
		RiskLevel:         defaultApprovalRiskLevel(req.RiskLevel),
		ActionFingerprint: req.ActionFingerprint,
		RequestBody:       jsonDefault(req.RequestBody, "{}"),
		RequestedEffects:  jsonDefault(req.RequestedEffects, "[]"),
		Options:           append([]ApprovalOption(nil), req.Options...),
		Status:            "pending",
		CreatedAt:         s.now().UTC(),
		ExpiresAt:         req.ExpiresAt,
		RawPayload:        jsonDefault(req.RawPayload, "{}"),
	}
	s.approvals[approvalID] = approval
	return approval, nil
}

func (s *MemoryStore) GetApproval(
	ctx context.Context,
	principal UserPrincipal,
	approvalID string,
) (AgentApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	approval, ok := s.approvals[approvalID]
	if !ok || !canAccessOwner(principal, approval.OwnerUserID) {
		return AgentApproval{}, ErrNotFound
	}
	return s.translateApprovalToNativeLocked(approval), nil
}

func (s *MemoryStore) GetNodeApproval(
	ctx context.Context,
	node Node,
	agentID string,
	approvalID string,
) (AgentApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	approval, ok := s.approvals[approvalID]
	if !ok ||
		approval.OwnerUserID != node.OwnerUserID ||
		approval.RequestNodeID != node.NodeID ||
		approval.RequestAgentID != agentID {
		return AgentApproval{}, ErrNotFound
	}
	return s.translateApprovalToNativeLocked(approval), nil
}

func (s *MemoryStore) ListApprovals(
	ctx context.Context,
	filter ApprovalFilter,
) ([]AgentApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AgentApproval, 0)
	for _, approval := range s.approvals {
		if !approvalMatchesFilter(approval, filter) {
			continue
		}
		out = append(out, approval)
	}
	sortApprovals(out)
	return limitApprovals(out, filter.Limit), nil
}

func (s *MemoryStore) DecideApproval(
	ctx context.Context,
	principal UserPrincipal,
	approvalID string,
	req ApprovalDecisionRequest,
) (AgentApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	approval, ok := s.approvals[approvalID]
	if !ok || !canAccessOwner(principal, approval.OwnerUserID) {
		return AgentApproval{}, ErrNotFound
	}
	if approval.Status != "pending" {
		return AgentApproval{}, ErrConflict
	}
	decision, scope, grantNodeID, grantAgentID, grantSessionID, err := approvalDecisionGrant(
		approval,
		req,
	)
	if err != nil {
		return AgentApproval{}, err
	}
	now := s.now().UTC()
	approval.Status = "decided"
	approval.Decision = decision
	approval.DecisionOption = req.DecisionOption
	approval.DecisionScope = scope
	approval.GrantNodeID = grantNodeID
	approval.GrantAgentID = grantAgentID
	approval.GrantSessionID = grantSessionID
	approval.GrantBody = jsonDefault(req.GrantBody, "{}")
	approval.DecidedByUserID = principal.User.UserID
	approval.DecidedAt = &now
	s.approvals[approvalID] = approval
	return approval, nil
}

func (s *MemoryStore) ListApprovalGrants(
	ctx context.Context,
	filter ApprovalGrantFilter,
) ([]AgentApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AgentApproval, 0)
	for _, approval := range s.approvals {
		if !approvalMatchesGrantFilter(approval, filter) {
			continue
		}
		out = append(out, approval)
	}
	sortApprovals(out)
	return limitApprovals(out, filter.Limit), nil
}

func (s *MemoryStore) FindReusableApprovalGrant(
	ctx context.Context,
	lookup ApprovalGrantLookup,
) (AgentApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	matches := make([]AgentApproval, 0)
	for _, approval := range s.approvals {
		if approval.OwnerUserID != lookup.OwnerUserID ||
			approval.Domain != lookup.Domain ||
			approval.Operation != lookup.Operation ||
			approval.ActionFingerprint != lookup.ActionFingerprint ||
			approval.Status != "decided" ||
			approval.Decision != "allow" ||
			approval.DecisionScope == "once" ||
			approval.GrantRevokedAt != nil {
			continue
		}
		if approval.ExpiresAt != nil && !approval.ExpiresAt.After(now) {
			continue
		}
		if !wildcardMatch(approval.GrantNodeID, lookup.RequestNodeID) ||
			!wildcardMatch(approval.GrantAgentID, lookup.RequestAgentID) ||
			!wildcardMatch(approval.GrantSessionID, lookup.RequestSessionID) {
			continue
		}
		matches = append(matches, approval)
	}
	if len(matches) == 0 {
		return AgentApproval{}, ErrNotFound
	}
	sort.Slice(matches, func(i, j int) bool {
		left := approvalSpecificity(matches[i])
		right := approvalSpecificity(matches[j])
		if left != right {
			return left > right
		}
		return timeAfter(matches[i].DecidedAt, matches[j].DecidedAt)
	})
	return matches[0], nil
}

func (s *MemoryStore) RevokeApprovalGrant(
	ctx context.Context,
	principal UserPrincipal,
	grantID string,
	req RevokeApprovalGrantRequest,
) (AgentApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	approval, ok := s.approvals[grantID]
	if !ok || !canAccessOwner(principal, approval.OwnerUserID) {
		return AgentApproval{}, ErrNotFound
	}
	if approval.Decision != "allow" || approval.DecisionScope == "" ||
		approval.DecisionScope == "once" {
		return AgentApproval{}, ErrConflict
	}
	if approval.GrantRevokedAt != nil {
		return approval, nil
	}
	now := s.now().UTC()
	approval.GrantRevokedAt = &now
	approval.GrantRevokedByUserID = principal.User.UserID
	approval.GrantRevocationReason = req.Reason
	s.approvals[grantID] = approval
	return approval, nil
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
		if filter.NodeID != "" && msg.NodeID != filter.NodeID {
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
	querySessionID := sessionID
	if querySessionID != "" {
		querySessionID = s.virtualSessionIDLocked(agentID, querySessionID)
	}

	now := s.now().UTC()
	candidates := make([]MailboxMessage, 0)
	for _, msg := range s.mailbox {
		if msg.AgentID != agentID || msg.ID <= offset {
			continue
		}
		if querySessionID != "" && msg.SessionID != querySessionID {
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
	s.translateMailboxMessagesToNativeLocked(candidates)

	return MailboxPull{Messages: candidates, MaxOffset: maxOffset, HasMore: hasMore}, nil
}

func (s *MemoryStore) PullNodeMailbox(
	ctx context.Context,
	nodeID string,
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
	if _, ok := s.nodes[nodeID]; !ok {
		return MailboxPull{}, ErrNotFound
	}
	querySessionID := sessionID
	if agentID != "" && querySessionID != "" {
		querySessionID = s.virtualSessionIDLocked(agentID, querySessionID)
	}
	now := s.now().UTC()
	candidates := make([]MailboxMessage, 0)
	for _, msg := range s.mailbox {
		if msg.NodeID != nodeID || msg.ID <= offset {
			continue
		}
		if agentID != "" && msg.AgentID != agentID {
			continue
		}
		if querySessionID != "" && msg.SessionID != querySessionID {
			continue
		}
		if msg.Direction == "node_to_user" || msg.Status != "pending" {
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
	s.translateMailboxMessagesToNativeLocked(candidates)
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

func (s *MemoryStore) MarkNodeMessageResult(
	ctx context.Context,
	nodeID string,
	messageID string,
	req MessageResultRequest,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, msg := range s.mailbox {
		if msg.NodeID == nodeID && msg.MessageID == messageID {
			if req.Status != "completed" && req.Status != "failed" {
				return ErrConflict
			}
			completedAt := s.now().UTC()
			if req.CompletedAt != nil {
				completedAt = req.CompletedAt.UTC()
			}
			msg.Status = req.Status
			msg.Result = firstNonEmpty(req.Result, req.Content, req.ResultMessageID)
			msg.Error = req.Error
			msg.Payload = req.Payload
			msg.Events = req.Events
			msg.FileChanges = append([]FileChange(nil), req.FileChanges...)
			msg.TokenUsage = req.TokenUsage
			msg.CompletedAt = &completedAt
			s.mailbox[id] = msg
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) MarkNodeMessageDelivered(
	ctx context.Context,
	nodeID string,
	req MarkDeliveredRequest,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, msg := range s.mailbox {
		if msg.NodeID == nodeID && msg.MessageID == req.MessageID {
			deliveredAt := s.now().UTC()
			if req.DeliveredAt != nil {
				deliveredAt = req.DeliveredAt.UTC()
			}
			if msg.Status == "pending" {
				msg.Status = "delivered"
			}
			msg.DeliveredAt = &deliveredAt
			s.mailbox[id] = msg
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) CreateNodeOutboundMessage(
	ctx context.Context,
	node Node,
	req CreateOutboundMessageRequest,
) (MailboxMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[req.AgentID]
	if !ok || agent.NodeID != node.NodeID {
		return MailboxMessage{}, ErrNotFound
	}
	sessionID := req.SessionID
	if sessionID != "" {
		sessionID = s.virtualSessionIDLocked(req.AgentID, sessionID)
	}
	createdAt := s.now().UTC()
	if req.CreatedAt != nil {
		createdAt = req.CreatedAt.UTC()
	}
	msg, err := s.createMailboxLocked(
		node.OwnerUserID,
		node.OwnerUserID,
		node.NodeID,
		req.AgentID,
		sessionID,
		req.Content,
		defaultOutboundMessageType(req.MessageType),
		req.Payload,
		defaultOutboundStatus(req.Status),
		createdAt,
	)
	if err != nil {
		return MailboxMessage{}, err
	}
	msg.Direction = "node_to_user"
	msg.ParentMessageID = req.ParentMessageID
	msg.TurnID = req.TurnID
	msg.ResponseID = req.ResponseID
	msg.Events = req.Events
	msg.FileChanges = append([]FileChange(nil), req.FileChanges...)
	msg.TokenUsage = req.TokenUsage
	s.mailbox[msg.ID] = msg
	if err := s.saveMailboxHistoryLocked(msg); err != nil {
		return MailboxMessage{}, err
	}
	msg.SessionID = s.nativeSessionIDLocked(msg.AgentID, msg.SessionID)
	msg.Payload = replacePayloadSessionID(msg.Payload, msg.SessionID)
	return msg, nil
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

func (s *MemoryStore) UpdateNodeOffset(ctx context.Context, nodeID string, offset int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[nodeID]; !ok {
		return ErrNotFound
	}
	if offset > s.offsets[nodeID] {
		s.offsets[nodeID] = offset
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

func (s *MemoryStore) upsertNodeAgentLocked(
	node Node,
	input AgentStatusInput,
	now time.Time,
) (Agent, error) {
	if input.AgentID != "" {
		if agent, ok := s.agents[input.AgentID]; ok {
			agent.NodeID = node.NodeID
			agent.OwnerUserID = node.OwnerUserID
			agent.Name = firstNonEmpty(input.Name, agent.Name)
			agent.AgentType = firstNonEmpty(input.AgentType, agent.AgentType)
			agent.Status = defaultSessionStatus(input.Status)
			agent.Online = input.Online || input.Status == "online"
			if input.LastHeartbeat != nil {
				agent.LastHeartbeat = input.LastHeartbeat
			} else {
				agent.LastHeartbeat = &now
			}
			agent.Metadata = input.Metadata
			s.agents[agent.AgentID] = agent
			return agent, nil
		}
	}
	return s.createNodeAgentLocked(node, input.Name, input.AgentType, input.Metadata, now)
}

func (s *MemoryStore) createNodeAgentLocked(
	node Node,
	name string,
	agentType string,
	metadata []byte,
	now time.Time,
) (Agent, error) {
	agentID, err := newSecret("agent")
	if err != nil {
		return Agent{}, err
	}
	agent := Agent{
		AgentID:       agentID,
		NodeID:        node.NodeID,
		OwnerUserID:   node.OwnerUserID,
		Name:          firstNonEmpty(name, "agent"),
		Hostname:      node.Hostname,
		AgentType:     firstNonEmpty(agentType, "hermes"),
		MachineType:   node.MachineType,
		OS:            node.OS,
		HermesVersion: node.PaxdVersion,
		APIEndpoint:   node.APIEndpoint,
		Status:        "online",
		Online:        true,
		LastHeartbeat: &now,
		RegisteredAt:  now,
		Metadata:      metadata,
	}
	s.agents[agentID] = agent
	return agent, nil
}

func (s *MemoryStore) upsertSessionLocked(
	nodeID string,
	agentID string,
	input SessionStatusInput,
	now time.Time,
) AgentSession {
	existing, exists := s.sessions[sessionKey(agentID, input.SessionID)]
	if !exists {
		s.nextSession++
		existing.ID = s.nextSession
		existing.NodeID = nodeID
		existing.AgentID = agentID
		existing.SessionID = input.SessionID
		existing.CreatedAt = now
	}
	existing.NodeID = nodeID
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
	existing.TokenUsage = input.TokenUsage
	existing.TokenInput = input.TokenUsage.Input
	existing.TokenOutput = input.TokenUsage.Output
	existing.TokenTotal = input.TokenUsage.Total
	existing.Model = input.Model
	existing.RunID = input.RunID
	existing.RunStatus = input.RunStatus
	existing.UpdatedAt = now
	s.sessions[sessionKey(agentID, input.SessionID)] = existing
	return existing
}

func (s *MemoryStore) normalizeReportedSessionLocked(
	agentID string,
	input SessionStatusInput,
) (SessionStatusInput, error) {
	nativeID := reportedNativeSessionID(input)
	if nativeID == "" {
		return input, nil
	}
	for _, session := range s.sessions {
		if session.AgentID == agentID && session.NativeID == nativeID {
			input.SessionID = session.SessionID
			input.NativeID = nativeID
			return input, nil
		}
	}
	if input.NativeID == "" || input.SessionID == "" || !isManagerSessionID(input.SessionID) {
		generated, err := newSecret("sess")
		if err != nil {
			return input, err
		}
		input.SessionID = generated
	}
	input.NativeID = nativeID
	return input, nil
}

func (s *MemoryStore) virtualSessionIDLocked(agentID string, sessionID string) string {
	for _, session := range s.sessions {
		if session.AgentID == agentID && session.NativeID == sessionID {
			return session.SessionID
		}
	}
	return sessionID
}

func (s *MemoryStore) nativeSessionIDLocked(agentID string, sessionID string) string {
	session, ok := s.sessions[sessionKey(agentID, sessionID)]
	if !ok || session.NativeID == "" {
		return sessionID
	}
	return session.NativeID
}

func (s *MemoryStore) translateMailboxMessagesToNativeLocked(messages []MailboxMessage) {
	for i := range messages {
		nativeID := s.nativeSessionIDLocked(messages[i].AgentID, messages[i].SessionID)
		messages[i].SessionID = nativeID
		messages[i].Payload = replacePayloadSessionID(messages[i].Payload, nativeID)
	}
}

func (s *MemoryStore) translateApprovalToNativeLocked(approval AgentApproval) AgentApproval {
	if approval.RequestAgentID != "" && approval.RequestSessionID != "" {
		approval.RequestSessionID = s.nativeSessionIDLocked(
			approval.RequestAgentID,
			approval.RequestSessionID,
		)
	}
	if approval.GrantAgentID != "" && approval.GrantSessionID != "" {
		approval.GrantSessionID = s.nativeSessionIDLocked(
			approval.GrantAgentID,
			approval.GrantSessionID,
		)
	}
	return approval
}

func (s *MemoryStore) createMailboxLocked(
	userID string,
	ownerUserID string,
	nodeID string,
	agentID string,
	sessionID string,
	message string,
	messageType string,
	payload []byte,
	status string,
	createdAt time.Time,
) (MailboxMessage, error) {
	messageID, err := newSecret("msg")
	if err != nil {
		return MailboxMessage{}, err
	}
	s.nextMailbox++
	msg := MailboxMessage{
		ID:          s.nextMailbox,
		MessageID:   messageID,
		UserID:      userID,
		OwnerUserID: ownerUserID,
		NodeID:      nodeID,
		AgentID:     agentID,
		SessionID:   sessionID,
		Message:     message,
		MessageType: messageType,
		Payload:     payload,
		Status:      status,
		CreatedAt:   createdAt,
		ExpiresAt:   expiresAt(createdAt, messageType),
	}
	s.mailbox[msg.ID] = msg
	if err := s.saveMailboxHistoryLocked(msg); err != nil {
		return MailboxMessage{}, err
	}
	return msg, nil
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

func approvalMatchesFilter(approval AgentApproval, filter ApprovalFilter) bool {
	if !canAccessOwner(filter.Principal, approval.OwnerUserID) {
		return false
	}
	if !stringFiltersMatch([]stringFilter{
		{filter.Status, approval.Status},
		{filter.Decision, approval.Decision},
		{filter.Domain, approval.Domain},
		{filter.Operation, approval.Operation},
		{filter.ResourceType, approval.ResourceType},
		{filter.ResourceRef, approval.ResourceRef},
		{filter.RequestNodeID, approval.RequestNodeID},
		{filter.RequestAgentID, approval.RequestAgentID},
		{filter.RequestSessionID, approval.RequestSessionID},
		{filter.DecisionScope, approval.DecisionScope},
	}) {
		return false
	}
	return filter.IncludeRevoked || approval.GrantRevokedAt == nil
}

func approvalMatchesGrantFilter(approval AgentApproval, filter ApprovalGrantFilter) bool {
	if !canAccessOwner(filter.Principal, approval.OwnerUserID) {
		return false
	}
	if approval.Status != "decided" || approval.Decision != "allow" ||
		approval.DecisionScope == "once" {
		return false
	}
	if !stringFiltersMatch([]stringFilter{
		{filter.Domain, approval.Domain},
		{filter.Operation, approval.Operation},
		{filter.ResourceType, approval.ResourceType},
		{filter.ResourceRef, approval.ResourceRef},
		{filter.DecisionScope, approval.DecisionScope},
		{filter.GrantNodeID, approval.GrantNodeID},
		{filter.GrantAgentID, approval.GrantAgentID},
		{filter.GrantSessionID, approval.GrantSessionID},
	}) {
		return false
	}
	return !filter.ActiveOnly || approval.GrantRevokedAt == nil
}

type stringFilter struct {
	want string
	got  string
}

func stringFiltersMatch(filters []stringFilter) bool {
	for _, filter := range filters {
		if filter.want != "" && filter.got != filter.want {
			return false
		}
	}
	return true
}

func sortApprovals(approvals []AgentApproval) {
	sort.Slice(approvals, func(i, j int) bool {
		return approvals[i].CreatedAt.After(approvals[j].CreatedAt)
	})
}

func limitApprovals(approvals []AgentApproval, limit int) []AgentApproval {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if len(approvals) <= limit {
		return approvals
	}
	return approvals[:limit]
}

func wildcardMatch(pattern string, value string) bool {
	return pattern == "*" || pattern == value
}

func approvalSpecificity(approval AgentApproval) int {
	score := 0
	if approval.GrantNodeID != "*" {
		score++
	}
	if approval.GrantAgentID != "*" {
		score++
	}
	if approval.GrantSessionID != "*" {
		score++
	}
	return score
}

func timeAfter(left *time.Time, right *time.Time) bool {
	if left == nil {
		return false
	}
	if right == nil {
		return true
	}
	return left.After(*right)
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

func defaultNodeName(req RegisterNodeRequest) string {
	if req.Name != "" {
		return req.Name
	}
	return req.Hostname
}

func defaultOS(v string) string {
	if v != "" {
		return v
	}
	return "unknown"
}

func defaultOutboundMessageType(v string) string {
	if v != "" {
		return v
	}
	return "turn_result"
}

func defaultOutboundStatus(v string) string {
	if v != "" {
		return v
	}
	return "completed"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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
