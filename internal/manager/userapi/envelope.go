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
	envelopeMessageLimit = 1000
	envelopePayloadLimit = 128 * 1024

	paxlKnowledgeCapsuleEnvelopePayloadVersion = "paxl.envelope_payload.knowledge_capsule.v1"
)

type paxlKnowledgeCapsuleEnvelopePayload struct {
	SchemaVersion string                             `json:"schema_version"`
	Capsule       paxlKnowledgeCapsulePayloadCapsule `json:"capsule"`
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
	if recipientEmail == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "recipient_email is required",
		}
	}
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
	message := strings.TrimSpace(req.Message)
	if len(message) > envelopeMessageLimit {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "message is too long"}
	}
	friend, err := s.store.GetAcceptedFriendByEmail(c, principal, recipientEmail)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return 0, nil, apperr.Error{
				Status:  http.StatusForbidden,
				Message: "recipient must be an accepted friend",
			}
		}
		return 0, nil, err
	}
	envelopeID, err := s.secrets.New("env")
	if err != nil {
		return 0, nil, err
	}
	recipientUserID := friendCounterpartyUserID(principal, friend)
	envelope, err := s.store.CreateEnvelope(c, domain.Envelope{
		EnvelopeID:      envelopeID,
		SenderUserID:    principal.User.UserID,
		SenderEmail:     principal.User.Email,
		RecipientUserID: recipientUserID,
		RecipientEmail:  recipientEmail,
		PayloadType:     payloadType,
		PayloadJSON:     req.PayloadJSON,
		Message:         message,
		Status:          domain.EnvelopeStatusPending,
		CreatedAt:       s.clock().UTC(),
	})
	if err != nil {
		return 0, nil, err
	}
	s.scheduleEnvelopeCapsuleUnpack(c, envelope)
	return http.StatusOK, map[string]any{"envelope": envelope}, nil
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
	if payload.SchemaVersion != paxlKnowledgeCapsuleEnvelopePayloadVersion {
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "unsupported envelope payload schema",
		}
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
		Content: truncateString(
			strings.TrimSpace(capsulePayload.Content),
			knowledgeContentLimit,
		),
		SuggestedSkills:        json.RawMessage(defaultKnowledgeArrayString),
		References:             json.RawMessage(defaultKnowledgeArrayString),
		OpenQuestions:          json.RawMessage(defaultKnowledgeArrayString),
		Risks:                  json.RawMessage(defaultKnowledgeArrayString),
		Redactions:             json.RawMessage(defaultKnowledgeArrayString),
		Status:                 domain.KnowledgeCapsuleStatusActive,
		Truncated:              capsulePayload.Truncated,
		OriginalEstimatedChars: capsulePayload.OriginalEstimatedChars,
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
