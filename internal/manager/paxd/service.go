package paxd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
	vaultsecrets "github.com/pax-beehive/pax-manager/internal/manager/secrets"
)

type Store interface {
	RegisterAgent(
		ctx context.Context,
		owner domain.User,
		req domain.RegisterAgentRequest,
		apiKeyHash string,
	) (domain.Agent, error)
	AuthenticateNode(ctx context.Context, apiKeyHash string) (domain.Node, error)
	UpsertAgentStatus(ctx context.Context, report domain.AgentStatusReport) error
	PullMailbox(
		ctx context.Context,
		agentID string,
		sessionID string,
		offset int64,
		limit int,
	) (domain.MailboxPull, error)
	UpdateOffset(ctx context.Context, agentID string, offset int64) error
	MarkMessageResult(
		ctx context.Context,
		agentID string,
		messageID string,
		req domain.MessageResultRequest,
	) error
	RegisterNode(
		ctx context.Context,
		owner domain.User,
		req domain.RegisterNodeRequest,
		apiKeyHash string,
	) (domain.Node, error)
	CreateNodeAgent(
		ctx context.Context,
		principal domain.UserPrincipal,
		req domain.CreateAgentRequest,
	) (domain.Agent, domain.MailboxMessage, error)
	UpsertNodeStatus(ctx context.Context, node domain.Node, report domain.NodeStatusReport) error
	PullNodeMailbox(
		ctx context.Context,
		nodeID string,
		agentID string,
		sessionID string,
		offset int64,
		limit int,
	) (domain.MailboxPull, error)
	UpdateNodeOffset(ctx context.Context, nodeID string, offset int64) error
	MarkNodeMessageResult(
		ctx context.Context,
		nodeID string,
		messageID string,
		req domain.MessageResultRequest,
	) error
	MarkNodeMessageDelivered(
		ctx context.Context,
		nodeID string,
		req domain.MarkDeliveredRequest,
	) error
	CreateNodeOutboundMessage(
		ctx context.Context,
		node domain.Node,
		req domain.CreateOutboundMessageRequest,
	) (domain.MailboxMessage, error)
	GetSecretVersionForNode(
		ctx context.Context,
		node domain.Node,
		agentID string,
		secretID string,
		versionSelector string,
	) (domain.Secret, domain.SecretVersion, error)
	CreateSecretVersion(
		ctx context.Context,
		node domain.Node,
		agentID string,
		req domain.WriteSecretVersionRequest,
		encrypted domain.SecretVersion,
	) (domain.SecretVersion, bool, error)
	RecordSecretAccess(ctx context.Context, event domain.SecretAccessEvent) error
	CreateApproval(
		ctx context.Context,
		node domain.Node,
		req domain.CreateApprovalRequest,
	) (domain.AgentApproval, error)
	FindReusableApprovalGrant(
		ctx context.Context,
		lookup domain.ApprovalGrantLookup,
	) (domain.AgentApproval, error)
}

type RegistrationOwnerResolver interface {
	RegistrationOwner(context.Context, auth.RequestMetadata) (domain.User, error)
}

type SecretIssuer interface {
	New(prefix string) (string, error)
	Hash(secret string) string
}

type Service struct {
	store             Store
	clock             func() time.Time
	registrationOwner RegistrationOwnerResolver
	secrets           SecretIssuer
	vault             *vaultsecrets.Cipher
}

func NewService(
	store Store,
	clock func() time.Time,
	registrationOwner RegistrationOwnerResolver,
	secrets SecretIssuer,
	vault ...*vaultsecrets.Cipher,
) *Service {
	cipher := firstVaultCipher(vault)
	return &Service{
		store:             store,
		clock:             clock,
		registrationOwner: registrationOwner,
		secrets:           secrets,
		vault:             cipher,
	}
}

func firstVaultCipher(values []*vaultsecrets.Cipher) *vaultsecrets.Cipher {
	if len(values) > 0 && values[0] != nil {
		return values[0]
	}
	cipher, err := vaultsecrets.NewCipherFromEnv()
	if err != nil {
		panic(err)
	}
	return cipher
}

func (s *Service) RegisterAgent(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.RegisterAgentRequest,
) (int, any, error) {
	if req.Hostname == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "hostname is required"}
	}
	if req.OS == "" {
		req.OS = "unknown"
	}
	owner, err := s.registrationOwner.RegistrationOwner(c, meta)
	if err != nil {
		return 0, nil, err
	}

	apiKey, err := s.secrets.New("pax")
	if err != nil {
		return 0, nil, apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "could not generate api key",
		}
	}

	agent, err := s.store.RegisterAgent(c, owner, req, s.secrets.Hash(apiKey))
	if err != nil {
		return 0, nil, err
	}

	logging.Info(
		c,
		"agent registered",
		slog.String("agent_id", agent.AgentID),
		slog.String("owner_user_id", owner.UserID),
	)
	return http.StatusOK, domain.RegisterAgentResponse{
		AgentID: agent.AgentID,
		APIKey:  apiKey,
	}, nil
}

func (s *Service) RegisterNode(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.RegisterNodeRequest,
) (int, any, error) {
	if req.Hostname == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "hostname is required"}
	}
	if req.OS == "" {
		req.OS = "unknown"
	}
	owner, err := s.registrationOwner.RegistrationOwner(c, meta)
	if err != nil {
		return 0, nil, err
	}
	apiKey, err := s.secrets.New("pax")
	if err != nil {
		return 0, nil, apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "could not generate api key",
		}
	}
	node, err := s.store.RegisterNode(c, owner, req, s.secrets.Hash(apiKey))
	if err != nil {
		return 0, nil, err
	}
	logging.Info(
		c,
		"node registered",
		slog.String("node_id", node.NodeID),
		slog.String("owner_user_id", owner.UserID),
	)
	return http.StatusOK, domain.RegisterNodeResponse{NodeID: node.NodeID, APIKey: apiKey}, nil
}

func (s *Service) RegisterNodeAgent(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.RegisterNodeAgentRequest,
) (int, any, error) {
	paxKey := firstNonEmpty(
		meta.Header("X-Pax-Key"),
		auth.BearerToken(meta.Header("Authorization")),
	)
	registrationToken := meta.Header("X-Registration-Token")
	if paxKey != "" && registrationToken != "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "provide either X-Pax-Key or X-Registration-Token, not both",
		}
	}

	var node domain.Node
	apiKey := ""
	if paxKey != "" {
		var err error
		node, err = s.store.AuthenticateNode(c, s.secrets.Hash(paxKey))
		if err != nil {
			return 0, nil, err
		}
	} else {
		if registrationToken == "" {
			return 0, nil, apperr.Error{
				Status:  http.StatusUnauthorized,
				Message: "missing pax key or registration token",
			}
		}
		if req.Node.Hostname == "" {
			return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "node.hostname is required"}
		}
		if req.Node.OS == "" {
			req.Node.OS = "unknown"
		}
		owner, err := s.registrationOwner.RegistrationOwner(c, meta)
		if err != nil {
			return 0, nil, err
		}
		apiKey, err = s.secrets.New("pax")
		if err != nil {
			return 0, nil, apperr.Error{
				Status:  http.StatusInternalServerError,
				Message: "could not generate api key",
			}
		}
		node, err = s.store.RegisterNode(c, owner, req.Node, s.secrets.Hash(apiKey))
		if err != nil {
			return 0, nil, err
		}
	}

	agentReq := req.Agent
	agentReq.NodeID = node.NodeID
	principal := domain.UserPrincipal{User: domain.User{UserID: node.OwnerUserID}}
	agent, _, err := s.store.CreateNodeAgent(c, principal, agentReq)
	if err != nil {
		return 0, nil, err
	}

	return http.StatusOK, domain.RegisterNodeAgentResponse{
		NodeID:  node.NodeID,
		APIKey:  apiKey,
		AgentID: agent.AgentID,
		Agent:   agent,
	}, nil
}

func (s *Service) ReportStatus(
	c context.Context,
	agent domain.Agent,
	report domain.AgentStatusReport,
) (int, any, error) {
	if report.AgentID == "" {
		report.AgentID = agent.AgentID
	}
	if report.AgentID != agent.AgentID {
		return 0, nil, apperr.Error{
			Status:  http.StatusForbidden,
			Message: "agent_id does not match token",
		}
	}
	if report.Timestamp.IsZero() {
		report.Timestamp = s.clock().UTC()
	}

	if err := s.store.UpsertAgentStatus(c, report); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]bool{"ok": true}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *Service) ReportNodeStatus(
	c context.Context,
	node domain.Node,
	report domain.NodeStatusReport,
) (int, any, error) {
	if report.NodeID == "" {
		report.NodeID = node.NodeID
	}
	if report.NodeID != node.NodeID {
		return 0, nil, apperr.Error{
			Status:  http.StatusForbidden,
			Message: "node_id does not match token",
		}
	}
	if report.Timestamp.IsZero() {
		report.Timestamp = s.clock().UTC()
	}
	if err := s.store.UpsertNodeStatus(c, node, report); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]bool{"ok": true}, nil
}

func (s *Service) PullNodeMailbox(
	c context.Context,
	node domain.Node,
	agentID string,
	sessionID string,
	offset int64,
	limit int,
) (int, any, error) {
	if limit == 0 {
		limit = 10
	}
	pull, err := s.store.PullNodeMailbox(c, node.NodeID, agentID, sessionID, offset, limit)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, pull, nil
}

func (s *Service) UpdateNodeOffset(
	c context.Context,
	node domain.Node,
	offset int64,
) (int, any, error) {
	if offset < 0 {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "offset must be non-negative",
		}
	}
	if err := s.store.UpdateNodeOffset(c, node.NodeID, offset); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]bool{"ok": true}, nil
}

func (s *Service) ReportNodeMessageResult(
	c context.Context,
	node domain.Node,
	req domain.MessageResultRequest,
) (int, any, error) {
	if req.MessageID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "message_id is required",
		}
	}
	if req.Status == "" {
		req.Status = "completed"
	}
	if err := s.store.MarkNodeMessageResult(c, node.NodeID, req.MessageID, req); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]bool{"ok": true}, nil
}

func (s *Service) MarkNodeMessageDelivered(
	c context.Context,
	node domain.Node,
	req domain.MarkDeliveredRequest,
) (int, any, error) {
	if req.MessageID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "message_id is required",
		}
	}
	if err := s.store.MarkNodeMessageDelivered(c, node.NodeID, req); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]bool{"ok": true}, nil
}

func (s *Service) CreateNodeOutboundMessage(
	c context.Context,
	node domain.Node,
	req domain.CreateOutboundMessageRequest,
) (int, any, error) {
	if req.AgentID == "" || req.Content == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "agent_id and content are required",
		}
	}
	msg, err := s.store.CreateNodeOutboundMessage(c, node, req)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, msg, nil
}

func (s *Service) PullMailbox(
	c context.Context,
	agent domain.Agent,
	offset int64,
	limit int,
) (int, any, error) {
	if limit == 0 {
		limit = 10
	}
	pull, err := s.store.PullMailbox(c, agent.AgentID, "", offset, limit)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, pull, nil
}

func (s *Service) PullSessionMailbox(
	c context.Context,
	agent domain.Agent,
	sessionID string,
	offset int64,
	limit int,
) (int, any, error) {
	if sessionID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "session_id is required",
		}
	}
	if limit == 0 {
		limit = 10
	}
	pull, err := s.store.PullMailbox(c, agent.AgentID, sessionID, offset, limit)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, pull, nil
}

func (s *Service) UpdateOffset(
	c context.Context,
	agent domain.Agent,
	offset int64,
) (int, any, error) {
	if offset < 0 {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "offset must be non-negative",
		}
	}
	if err := s.store.UpdateOffset(c, agent.AgentID, offset); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]bool{"ok": true}, nil
}

func (s *Service) ReportMessageResult(
	c context.Context,
	agent domain.Agent,
	req domain.MessageResultRequest,
) (int, any, error) {
	if req.Status == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "status is required"}
	}
	if err := s.store.MarkMessageResult(c, agent.AgentID, req.MessageID, req); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]bool{"ok": true}, nil
}

func (s *Service) ResolveSecret(
	c context.Context,
	node domain.Node,
	req domain.ResolveSecretRequest,
) (int, any, error) {
	if req.SecretID == "" || req.AgentID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "secret_id and agent_id are required",
		}
	}
	version := defaultSecretVersion(req.Version)
	secret, secretVersion, err := s.store.GetSecretVersionForNode(
		c,
		node,
		req.AgentID,
		req.SecretID,
		version,
	)
	if err != nil {
		_ = s.store.RecordSecretAccess(c, domain.SecretAccessEvent{
			SecretID:  req.SecretID,
			NodeID:    node.NodeID,
			AgentID:   req.AgentID,
			SessionID: req.SessionID,
			Action:    "read_value",
			Result:    "denied",
		})
		return 0, nil, err
	}
	fingerprint := secretActionFingerprint(
		"read_value",
		req.SecretID,
		version,
		req.AgentID,
		req.SessionID,
	)
	if _, err := s.store.FindReusableApprovalGrant(c, domain.ApprovalGrantLookup{
		OwnerUserID:       node.OwnerUserID,
		RequestNodeID:     node.NodeID,
		RequestAgentID:    req.AgentID,
		RequestSessionID:  req.SessionID,
		Domain:            "secret_vault",
		Operation:         "read_value",
		ActionFingerprint: fingerprint,
	}); err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			return 0, nil, err
		}
		approval, createErr := s.createSecretApproval(
			c,
			node,
			req.AgentID,
			req.SessionID,
			"read_value",
			req.SecretID,
			version,
			fingerprint,
			map[string]any{"reveals_secret_value": true},
		)
		if createErr != nil {
			return 0, nil, createErr
		}
		_ = s.store.RecordSecretAccess(c, domain.SecretAccessEvent{
			SecretID:  req.SecretID,
			NodeID:    node.NodeID,
			AgentID:   req.AgentID,
			SessionID: req.SessionID,
			Action:    "read_value",
			Result:    "approval_required",
		})
		return http.StatusAccepted, domain.ResolveSecretResponse{
			Status:     "approval_required",
			ApprovalID: approval.ApprovalID,
			Approval:   approval,
		}, nil
	}
	value, err := s.vault.Decrypt(vaultsecrets.EncryptedValue{
		Ciphertext: secretVersion.Ciphertext,
		Nonce:      secretVersion.Nonce,
		KeyID:      secretVersion.KeyID,
	}, nil)
	if err != nil {
		return 0, nil, err
	}
	_ = s.store.RecordSecretAccess(c, domain.SecretAccessEvent{
		SecretID:  secret.SecretID,
		VersionID: secretVersion.VersionID,
		NodeID:    node.NodeID,
		AgentID:   req.AgentID,
		SessionID: req.SessionID,
		Action:    "read_value",
		Result:    "allowed",
	})
	return http.StatusOK, domain.ResolveSecretResponse{
		Status:        "ok",
		SecretID:      secret.SecretID,
		VersionID:     secretVersion.VersionID,
		VersionNumber: secretVersion.VersionNumber,
		Value:         value,
	}, nil
}

func (s *Service) WriteSecretVersion(
	c context.Context,
	node domain.Node,
	req domain.WriteSecretVersionRequest,
) (int, any, error) {
	if req.SecretID == "" || req.AgentID == "" || req.Value == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "secret_id, agent_id and value are required",
		}
	}
	if _, _, err := s.store.GetSecretVersionForNode(
		c,
		node,
		req.AgentID,
		req.SecretID,
		"latest",
	); err != nil {
		_ = s.store.RecordSecretAccess(c, domain.SecretAccessEvent{
			SecretID:  req.SecretID,
			NodeID:    node.NodeID,
			AgentID:   req.AgentID,
			SessionID: req.SessionID,
			Action:    "write_value",
			Result:    "denied",
		})
		return 0, nil, err
	}
	fingerprint := secretActionFingerprint(
		"write_value",
		req.SecretID,
		"",
		req.AgentID,
		req.SessionID,
	)
	if _, err := s.store.FindReusableApprovalGrant(c, domain.ApprovalGrantLookup{
		OwnerUserID:       node.OwnerUserID,
		RequestNodeID:     node.NodeID,
		RequestAgentID:    req.AgentID,
		RequestSessionID:  req.SessionID,
		Domain:            "secret_vault",
		Operation:         "write_value",
		ActionFingerprint: fingerprint,
	}); err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			return 0, nil, err
		}
		approval, createErr := s.createSecretApproval(
			c,
			node,
			req.AgentID,
			req.SessionID,
			"write_value",
			req.SecretID,
			"",
			fingerprint,
			map[string]any{
				"creates_new_version":          true,
				"make_current":                 req.MakeCurrent,
				"expected_current_version_id":  req.ExpectedCurrentVersionID,
				"idempotency_key_present":      req.IdempotencyKey != "",
				"secret_value_is_not_recorded": true,
			},
		)
		if createErr != nil {
			return 0, nil, createErr
		}
		_ = s.store.RecordSecretAccess(c, domain.SecretAccessEvent{
			SecretID:  req.SecretID,
			NodeID:    node.NodeID,
			AgentID:   req.AgentID,
			SessionID: req.SessionID,
			Action:    "write_value",
			Result:    "approval_required",
		})
		return http.StatusAccepted, domain.WriteSecretVersionResponse{
			Status:     "approval_required",
			ApprovalID: approval.ApprovalID,
			Approval:   approval,
		}, nil
	}
	encrypted, err := s.vault.Encrypt(req.Value, nil)
	if err != nil {
		return 0, nil, err
	}
	version, current, err := s.store.CreateSecretVersion(
		c,
		node,
		req.AgentID,
		req,
		domain.SecretVersion{
			Ciphertext: encrypted.Ciphertext,
			Nonce:      encrypted.Nonce,
			KeyID:      encrypted.KeyID,
		},
	)
	if err != nil {
		result := "denied"
		if errors.Is(err, domain.ErrConflict) {
			result = "conflict"
		}
		_ = s.store.RecordSecretAccess(c, domain.SecretAccessEvent{
			SecretID:  req.SecretID,
			NodeID:    node.NodeID,
			AgentID:   req.AgentID,
			SessionID: req.SessionID,
			Action:    "write_value",
			Result:    result,
		})
		return 0, nil, err
	}
	_ = s.store.RecordSecretAccess(c, domain.SecretAccessEvent{
		SecretID:  req.SecretID,
		VersionID: version.VersionID,
		NodeID:    node.NodeID,
		AgentID:   req.AgentID,
		SessionID: req.SessionID,
		Action:    "write_value",
		Result:    "allowed",
	})
	return http.StatusOK, domain.WriteSecretVersionResponse{
		Status:        "ok",
		SecretID:      version.SecretID,
		VersionID:     version.VersionID,
		VersionNumber: version.VersionNumber,
		Current:       current,
	}, nil
}

func (s *Service) createSecretApproval(
	ctx context.Context,
	node domain.Node,
	agentID string,
	sessionID string,
	operation string,
	secretID string,
	version string,
	fingerprint string,
	effects map[string]any,
) (domain.AgentApproval, error) {
	requestBody := map[string]any{
		"secret_id": secretID,
		"version":   version,
	}
	if version == "" {
		delete(requestBody, "version")
	}
	body, _ := json.Marshal(requestBody)
	requestedEffects, _ := json.Marshal(effects)
	return s.store.CreateApproval(ctx, node, domain.CreateApprovalRequest{
		AgentID:           agentID,
		SessionID:         sessionID,
		Domain:            "secret_vault",
		Operation:         operation,
		ResourceType:      "secret",
		ResourceRef:       secretResourceRef(secretID, version),
		Title:             secretApprovalTitle(operation),
		Description:       secretApprovalDescription(operation, secretID, version),
		RiskLevel:         "high",
		ActionFingerprint: fingerprint,
		RequestBody:       body,
		RequestedEffects:  requestedEffects,
		Options: []domain.ApprovalOption{
			{
				OptionID: "allow_for_this_agent",
				Label:    "Allow for this agent",
				Decision: "allow",
				Scope:    "agent",
			},
			{
				OptionID: "allow_for_this_node",
				Label:    "Allow for this node",
				Decision: "allow",
				Scope:    "node",
			},
			{OptionID: "deny", Label: "Deny", Decision: "deny", Scope: "once"},
		},
	})
}

func defaultSecretVersion(version string) string {
	if strings.TrimSpace(version) == "" {
		return "latest"
	}
	return strings.TrimSpace(version)
}

func secretActionFingerprint(
	operation string,
	secretID string,
	version string,
	agentID string,
	sessionID string,
) string {
	raw, _ := json.Marshal(map[string]string{
		"domain":     "secret_vault",
		"operation":  operation,
		"secret_id":  secretID,
		"version":    version,
		"agent_id":   agentID,
		"session_id": sessionID,
	})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func secretResourceRef(secretID string, version string) string {
	if version == "" {
		return secretID
	}
	return secretID + "@" + version
}

func secretApprovalTitle(operation string) string {
	if operation == "write_value" {
		return "Write secret vault value"
	}
	return "Read secret vault value"
}

func secretApprovalDescription(operation string, secretID string, version string) string {
	if operation == "write_value" {
		return "Agent requested permission to create a new secret version for " + secretID + "."
	}
	return "Agent requested permission to read " + secretResourceRef(secretID, version) + "."
}
