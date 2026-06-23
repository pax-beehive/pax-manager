package userapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	envelopeMessageLimit = 1000
	envelopePayloadLimit = 128 * 1024
)

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
	envelopeID, err := s.secrets.New("env")
	if err != nil {
		return 0, nil, err
	}
	recipientUserID := ""
	recipient, err := s.store.GetUserByEmail(c, recipientEmail)
	if err == nil {
		recipientUserID = recipient.UserID
	} else if !errors.Is(err, domain.ErrNotFound) {
		return 0, nil, err
	}
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
	return http.StatusOK, map[string]any{"envelope": envelope}, nil
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
