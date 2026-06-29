package userapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

const (
	envelopeMessageLimit    = 1000
	envelopePayloadLimit    = 128 * 1024
	envelopeRouteValueLimit = 256

	paxlKnowledgeCapsuleEnvelopePayloadVersionV1 = "paxl.envelope_payload.knowledge_capsule.v1"
	paxlKnowledgeCapsuleEnvelopePayloadVersionV2 = "paxl.envelope_payload.knowledge_capsule.v2"
)

type paxlKnowledgeCapsuleEnvelopePayload struct {
	SchemaVersion string                             `json:"schema_version"`
	Capsule       paxlKnowledgeCapsulePayloadCapsule `json:"capsule"`
	Route         *paxlKnowledgeCapsulePayloadRoute  `json:"route,omitempty"`
}

type paxlKnowledgeCapsulePayloadCapsule struct {
	CapsuleID              string `json:"capsule_id"`
	SourceNodeID           string `json:"source_node_id,omitempty"`
	SourceSessionID        string `json:"source_session_id"`
	SourceAgent            string `json:"source_agent"`
	Keyword                string `json:"keyword"`
	Title                  string `json:"title"`
	Summary                string `json:"summary"`
	Content                string `json:"content"`
	Status                 string `json:"status"`
	Truncated              bool   `json:"truncated"`
	OriginalEstimatedChars int64  `json:"original_estimated_chars"`
}

type paxlKnowledgeCapsulePayloadRoute struct {
	MatchType   string `json:"match_type"`
	MatchValue  string `json:"match_value,omitempty"`
	TargetAgent string `json:"target_agent,omitempty"`
}

func (s *Service) CreateEnvelope(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.CreateEnvelopeRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	recipientEmail := domain.NormalizeEmail(req.RecipientEmail)
	payloadType := strings.TrimSpace(req.PayloadType)
	if payloadType == "" {
		payloadType = domain.EnvelopePayloadKnowledgeCapsule
	}
	if payloadType != domain.EnvelopePayloadKnowledgeCapsule {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "unsupported payload_type",
		}
	}
	if len(req.PayloadJSON) == 0 || !json.Valid(req.PayloadJSON) {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "payload_json is required",
		}
	}
	if len(req.PayloadJSON) > envelopePayloadLimit {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "payload_json is too large",
		}
	}
	if err := validateEnvelopePayload(payloadType, req.PayloadJSON); err != nil {
		return 0, nil, err
	}
	message := strings.TrimSpace(req.Message)
	if len(message) > envelopeMessageLimit {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "message is too long"}
	}
	fromAgentID := strings.TrimSpace(req.FromAgentID)
	toAgentID := strings.TrimSpace(req.ToAgentID)
	recipientUserID, resolvedRecipientEmail, err := s.resolveEnvelopeRecipient(
		c,
		principal,
		recipientEmail,
		fromAgentID,
		toAgentID,
	)
	if err != nil {
		return 0, nil, err
	}
	envelopeID, err := s.secrets.New("env")
	if err != nil {
		return 0, nil, err
	}
	envelope, err := s.store.CreateEnvelope(c, domain.Envelope{
		EnvelopeID:      envelopeID,
		SenderUserID:    principal.User.UserID,
		SenderEmail:     principal.User.Email,
		RecipientUserID: recipientUserID,
		RecipientEmail:  resolvedRecipientEmail,
		FromAgentID:     fromAgentID,
		ToAgentID:       toAgentID,
		PayloadType:     payloadType,
		PayloadJSON:     req.PayloadJSON,
		Message:         message,
		Status:          domain.EnvelopeStatusPending,
		CreatedAt:       s.clock().UTC(),
	})
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"envelope": envelope}, nil
}

func (s *Service) resolveEnvelopeRecipient(
	ctx context.Context,
	principal domain.UserPrincipal,
	recipientEmail string,
	fromAgentID string,
	toAgentID string,
) (string, string, error) {
	if fromAgentID == "" && toAgentID == "" {
		if recipientEmail == "" {
			return "", "", apperr.Error{
				Status:  http.StatusBadRequest,
				Message: "recipient_email is required",
			}
		}
		friend, err := s.store.GetAcceptedFriendByEmail(ctx, principal, recipientEmail)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return "", "", apperr.Error{
					Status:  http.StatusForbidden,
					Message: "recipient must be an accepted friend",
				}
			}
			return "", "", err
		}
		return friendCounterpartyUserID(principal, friend), recipientEmail, nil
	}
	if fromAgentID == "" || toAgentID == "" {
		return "", "", apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "from_agent_id and to_agent_id must be provided together",
		}
	}
	recipient, err := s.store.GetEnvelopeAgentRecipient(ctx, principal, fromAgentID, toAgentID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return "", "", apperr.Error{
				Status:  http.StatusForbidden,
				Message: "agents must share an active team",
			}
		}
		return "", "", err
	}
	resolvedEmail := domain.NormalizeEmail(recipient.Email)
	if recipientEmail != "" && recipientEmail != resolvedEmail {
		return "", "", apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "recipient_email must match target agent owner",
		}
	}
	return recipient.UserID, resolvedEmail, nil
}

func (s *Service) scheduleEnvelopeCapsuleUnpack(ctx context.Context, envelope domain.Envelope) {
	if envelope.PayloadType != domain.EnvelopePayloadKnowledgeCapsule ||
		len(envelope.PayloadJSON) == 0 ||
		strings.TrimSpace(envelope.RecipientUserID) == "" {
		return
	}
	s.backgroundRunner(ctx, func(taskCtx context.Context) {
		if err := s.unpackEnvelopeCapsule(taskCtx, envelope); err != nil {
			logging.Warn(
				taskCtx,
				"auto-unpack envelope knowledge capsule failed",
				slog.String("envelope_id", envelope.EnvelopeID),
				slog.String("recipient_user_id", envelope.RecipientUserID),
				logging.Err(err),
			)
		}
	})
}

func (s *Service) unpackEnvelopeCapsule(
	ctx context.Context,
	envelope domain.Envelope,
) error {
	var payload paxlKnowledgeCapsuleEnvelopePayload
	if err := json.Unmarshal(envelope.PayloadJSON, &payload); err != nil {
		return err
	}
	payload.SchemaVersion = strings.TrimSpace(payload.SchemaVersion)
	if payload.SchemaVersion == "" {
		return nil
	}
	if err := validateKnowledgeCapsuleEnvelopePayload(payload); err != nil {
		return err
	}
	capsuleID, err := s.secrets.New("kcap")
	if err != nil {
		return err
	}
	now := s.clock().UTC()
	capsulePayload := payload.Capsule
	sourceSessionID := strings.TrimSpace(capsulePayload.SourceSessionID)
	if sourceSessionID == "" {
		sourceSessionID = "remote_envelope:" + envelope.EnvelopeID
	}
	content, truncated, originalEstimatedChars := importedEnvelopeCapsuleContent(capsulePayload)
	capsule := domain.KnowledgeCapsule{
		CapsuleID:       capsuleID,
		OwnerUserID:     envelope.RecipientUserID,
		SourceSessionID: sourceSessionID,
		SourceAgentID:   strings.TrimSpace(capsulePayload.SourceAgent),
		SourceNodeID:    strings.TrimSpace(capsulePayload.SourceNodeID),
		CreatedByUserID: envelope.SenderUserID,
		Keyword: truncateString(
			strings.TrimSpace(capsulePayload.Keyword),
			knowledgeKeywordLimit,
		),
		Title: truncateString(
			strings.TrimSpace(capsulePayload.Title),
			knowledgeTitleLimit,
		),
		Summary: truncateString(
			strings.TrimSpace(capsulePayload.Summary),
			knowledgeSummaryLimit,
		),
		Content:                content,
		SuggestedSkills:        json.RawMessage(defaultKnowledgeArrayString),
		References:             envelopeCapsuleReferences(payload.Route, envelope.EnvelopeID),
		OpenQuestions:          json.RawMessage(defaultKnowledgeArrayString),
		Risks:                  json.RawMessage(defaultKnowledgeArrayString),
		Redactions:             json.RawMessage(defaultKnowledgeArrayString),
		Status:                 domain.KnowledgeCapsuleStatusActive,
		Truncated:              truncated,
		OriginalEstimatedChars: originalEstimatedChars,
		CreatedAt:              now,
	}
	if capsule.SourceAgentID == "" {
		capsule.SourceAgentID = "paxl"
	}
	if capsule.Keyword == "" {
		capsule.Keyword = "shared"
	}
	if capsule.Title == "" {
		capsule.Title = "Shared knowledge capsule"
	}
	if capsule.Summary == "" {
		capsule.Summary = "Imported from a shared paxl envelope."
	}
	if capsule.Content == "" {
		capsule.Content = capsule.Summary
	}
	_, err = s.store.CreateKnowledgeCapsule(ctx, capsule)
	return err
}

func importedEnvelopeCapsuleContent(
	capsulePayload paxlKnowledgeCapsulePayloadCapsule,
) (string, bool, int64) {
	rawContent := strings.TrimSpace(capsulePayload.Content)
	contentTruncated := stringExceedsLimit(rawContent, knowledgeContentLimit)
	originalEstimatedChars := capsulePayload.OriginalEstimatedChars
	if contentTruncated {
		rawContentChars := int64(runeLen(rawContent))
		if rawContentChars > originalEstimatedChars {
			originalEstimatedChars = rawContentChars
		}
	}
	return truncateString(rawContent, knowledgeContentLimit),
		capsulePayload.Truncated || contentTruncated,
		originalEstimatedChars
}

func validateEnvelopePayload(payloadType string, raw json.RawMessage) error {
	if payloadType != domain.EnvelopePayloadKnowledgeCapsule {
		return nil
	}
	var envelope struct {
		SchemaVersion string                            `json:"schema_version"`
		Capsule       json.RawMessage                   `json:"capsule"`
		Route         *paxlKnowledgeCapsulePayloadRoute `json:"route,omitempty"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "payload_json must be an object",
		}
	}
	envelope.SchemaVersion = strings.TrimSpace(envelope.SchemaVersion)
	if envelope.SchemaVersion == "" {
		return nil
	}
	if envelope.SchemaVersion == paxlKnowledgeCapsuleEnvelopePayloadVersionV1 ||
		envelope.SchemaVersion == paxlKnowledgeCapsuleEnvelopePayloadVersionV2 {
		if !jsonRawValuePresent(envelope.Capsule) {
			return apperr.Error{Status: http.StatusBadRequest, Message: "capsule is required"}
		}
	}
	var payload paxlKnowledgeCapsuleEnvelopePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	payload.SchemaVersion = envelope.SchemaVersion
	payload.Route = envelope.Route
	return validateKnowledgeCapsuleEnvelopePayload(payload)
}

func jsonRawValuePresent(value json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(value))
	return trimmed != "" && trimmed != "null"
}

func validateKnowledgeCapsuleEnvelopePayload(
	payload paxlKnowledgeCapsuleEnvelopePayload,
) error {
	switch strings.TrimSpace(payload.SchemaVersion) {
	case paxlKnowledgeCapsuleEnvelopePayloadVersionV1:
		return nil
	case paxlKnowledgeCapsuleEnvelopePayloadVersionV2:
		return validateKnowledgeCapsuleEnvelopeRoute(payload.Route)
	default:
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "unsupported envelope payload schema",
		}
	}
}

func validateKnowledgeCapsuleEnvelopeRoute(route *paxlKnowledgeCapsulePayloadRoute) error {
	if route == nil {
		return apperr.Error{Status: http.StatusBadRequest, Message: "route is required"}
	}
	matchType := strings.TrimSpace(route.MatchType)
	matchValue := strings.TrimSpace(route.MatchValue)
	switch matchType {
	case "any":
		if matchValue != "" {
			return apperr.Error{
				Status:  http.StatusBadRequest,
				Message: "route match_value must be empty for any",
			}
		}
	case "project", "keyword":
		if matchValue == "" {
			return apperr.Error{
				Status:  http.StatusBadRequest,
				Message: "route match_value is required",
			}
		}
	default:
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "unsupported route match_type",
		}
	}
	if len(matchValue) > envelopeRouteValueLimit {
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "route match_value is too long",
		}
	}
	targetAgent := strings.TrimSpace(route.TargetAgent)
	if targetAgent != "" && !isSupportedPaxlAgent(targetAgent) {
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "unsupported route target_agent",
		}
	}
	return nil
}

func isSupportedPaxlAgent(agent string) bool {
	switch strings.TrimSpace(agent) {
	case "codex", "claude", "gemini", "kiro", "pi":
		return true
	default:
		return false
	}
}

func envelopeCapsuleReferences(
	route *paxlKnowledgeCapsulePayloadRoute,
	envelopeID string,
) json.RawMessage {
	if route == nil {
		return json.RawMessage(defaultKnowledgeArrayString)
	}
	references, err := json.Marshal([]map[string]string{
		{
			"type":               "paxl.envelope_route",
			"envelope_id":        envelopeID,
			"route_match_type":   strings.TrimSpace(route.MatchType),
			"route_match_value":  strings.TrimSpace(route.MatchValue),
			"route_target_agent": strings.TrimSpace(route.TargetAgent),
		},
	})
	if err != nil {
		return json.RawMessage(defaultKnowledgeArrayString)
	}
	return references
}

func friendCounterpartyUserID(principal domain.UserPrincipal, friend domain.Friend) string {
	if friend.RequesterUserID == principal.User.UserID {
		return friend.RecipientUserID
	}
	if friend.RecipientUserID == principal.User.UserID {
		return friend.RequesterUserID
	}
	return ""
}

func (s *Service) ListEnvelopes(
	c context.Context,
	meta auth.RequestMetadata,
	filter domain.ListEnvelopesFilter,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	filter.Principal = principal
	envelopes, err := s.store.ListEnvelopes(c, filter)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"envelopes": envelopes}, nil
}

func (s *Service) GetEnvelope(
	c context.Context,
	meta auth.RequestMetadata,
	envelopeID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	envelope, err := s.store.GetEnvelope(c, principal, envelopeID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"envelope": envelope}, nil
}

func (s *Service) AcceptEnvelope(
	c context.Context,
	meta auth.RequestMetadata,
	envelopeID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	envelope, err := s.store.AcceptEnvelope(c, principal, envelopeID, s.clock().UTC())
	if err != nil {
		return 0, nil, err
	}
	s.scheduleEnvelopeCapsuleUnpack(c, envelope)
	return http.StatusOK, map[string]any{"envelope": envelope}, nil
}

func (s *Service) ArchiveEnvelope(
	c context.Context,
	meta auth.RequestMetadata,
	envelopeID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	envelope, err := s.store.ArchiveEnvelope(c, principal, envelopeID, s.clock().UTC())
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"envelope": envelope}, nil
}
