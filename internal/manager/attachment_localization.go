package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const attachmentDownloadTicketTTL = 15 * time.Minute

var (
	attachmentPushCheckInterval  = 50 * time.Millisecond
	attachmentStatusPollInterval = time.Second
	attachmentControlTimeout     = 5 * time.Second
)

type attachmentLocalizationResult struct {
	Attachment domain.UserAttachment
	State      nodeControlAttachmentLocalState
}

func (s *Service) ensureAttachmentsLocal(
	ctx context.Context,
	principal UserPrincipal,
	nodeID string,
	attachmentIDs []string,
) (map[string]attachmentLocalizationResult, error) {
	attachments, err := s.completedUserAttachments(ctx, principal, attachmentIDs)
	if err != nil {
		return nil, err
	}
	if len(attachments) == 0 {
		return map[string]attachmentLocalizationResult{}, nil
	}
	if s.nodeControls == nil {
		return nil, apperr.Error{
			Status:  http.StatusServiceUnavailable,
			Message: "node control tunnel is unavailable",
		}
	}

	ids := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		ids = append(ids, attachment.AttachmentID)
	}
	if states, queryErr := s.queryAttachmentLocalStatus(ctx, nodeID, ids); queryErr == nil {
		s.observeAttachmentStates(nodeID, states)
	}
	if ready, err := s.readyAttachmentResults(nodeID, attachments, false); ready != nil ||
		err != nil {
		return ready, err
	}

	if err := s.dispatchMissingAttachments(ctx, nodeID, attachments); err != nil {
		return nil, err
	}

	pushTicker := time.NewTicker(attachmentPushCheckInterval)
	defer pushTicker.Stop()
	pollTicker := time.NewTicker(attachmentStatusPollInterval)
	defer pollTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-pushTicker.C:
			if ready, err := s.readyAttachmentResults(nodeID, attachments, true); ready != nil ||
				err != nil {
				return ready, err
			}
		case <-pollTicker.C:
			states, queryErr := s.queryAttachmentLocalStatus(ctx, nodeID, ids)
			if queryErr != nil {
				continue
			}
			s.observeAttachmentStates(nodeID, states)
			if ready, err := s.readyAttachmentResults(nodeID, attachments, true); ready != nil ||
				err != nil {
				return ready, err
			}
		}
	}
}

func (s *Service) completedUserAttachments(
	ctx context.Context,
	principal UserPrincipal,
	attachmentIDs []string,
) ([]domain.UserAttachment, error) {
	seen := make(map[string]struct{}, len(attachmentIDs))
	result := make([]domain.UserAttachment, 0, len(attachmentIDs))
	for _, rawID := range attachmentIDs {
		attachmentID := strings.TrimSpace(rawID)
		if attachmentID == "" {
			return nil, apperr.Error{
				Status:  http.StatusBadRequest,
				Message: "attachment_id is required",
			}
		}
		if _, ok := seen[attachmentID]; ok {
			continue
		}
		seen[attachmentID] = struct{}{}
		attachment, err := s.store.GetUserAttachment(ctx, principal, attachmentID)
		if err != nil {
			return nil, err
		}
		if attachment.UploadStatus != domain.UserAttachmentUploadCompleted {
			return nil, apperr.Error{
				Status:  http.StatusConflict,
				Message: "attachment upload is not completed",
			}
		}
		result = append(result, attachment)
	}
	return result, nil
}

func (s *Service) dispatchEnsureAttachmentLocal(
	ctx context.Context,
	nodeID string,
	attachment domain.UserAttachment,
) error {
	expiresAt := s.clock().UTC().Add(attachmentDownloadTicketTTL)
	downloadURL, err := s.paxdArtifacts.SignObjectDownloadURL(
		ctx,
		attachment.Bucket,
		attachment.Object,
		attachment.Generation,
		expiresAt,
		nil,
	)
	if err != nil {
		return err
	}
	commandID, err := auth.NewSecret("cmdatt")
	if err != nil {
		return err
	}
	command := map[string]any{
		"command_id": commandID,
		"type":       "attachment.ensure_local",
		"ensure_attachment_local": map[string]any{
			"attachment": map[string]any{
				"attachment_id": attachment.AttachmentID,
				"filename":      attachment.Filename,
				"content_type":  attachment.ContentType,
				"size_bytes":    attachment.SizeBytes,
				"sha256":        attachment.SHA256,
				"generation":    attachment.Generation,
			},
			"download": map[string]any{
				"url":        downloadURL,
				"expires_at": expiresAt.Format(time.RFC3339Nano),
			},
		},
	}
	requestCtx, cancel := context.WithTimeout(ctx, attachmentControlTimeout)
	defer cancel()
	raw, err := s.nodeControls.Command(requestCtx, nodeID, commandID, command)
	if err != nil {
		return err
	}
	var ack struct {
		OK     bool   `json:"ok"`
		Status string `json:"status"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(raw, &ack); err != nil {
		return fmt.Errorf("decode attachment ensure acknowledgement: %w", err)
	}
	if !ack.OK {
		message := "paxd rejected attachment localization"
		if ack.Error != nil && ack.Error.Message != "" {
			message = ack.Error.Message
		}
		return apperr.Error{Status: http.StatusBadGateway, Message: message}
	}
	return nil
}

func (s *Service) queryAttachmentLocalStatus(
	ctx context.Context,
	nodeID string,
	attachmentIDs []string,
) ([]nodeControlAttachmentLocalState, error) {
	requestID, err := auth.NewSecret("ctlqatt")
	if err != nil {
		return nil, err
	}
	query := map[string]any{
		"type": "attachment.local_status",
		"get_attachment_local_status": map[string]any{
			"attachment_ids": attachmentIDs,
		},
	}
	requestCtx, cancel := context.WithTimeout(ctx, attachmentControlTimeout)
	defer cancel()
	raw, err := s.nodeControls.Query(requestCtx, nodeID, requestID, query)
	if err != nil {
		return nil, err
	}
	var result struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
		AttachmentLocalStatus *struct {
			Items []nodeControlAttachmentLocalState `json:"items"`
		} `json:"attachment_local_status,omitempty"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decode attachment local status: %w", err)
	}
	if result.Error != nil {
		return nil, errors.New(
			firstNonEmpty(result.Error.Message, "paxd attachment status query failed"),
		)
	}
	if result.AttachmentLocalStatus == nil {
		return nil, errors.New("paxd attachment status response is missing attachment_local_status")
	}
	return result.AttachmentLocalStatus.Items, nil
}

func (s *Service) observeAttachmentStates(
	nodeID string,
	states []nodeControlAttachmentLocalState,
) {
	for _, state := range states {
		s.nodeControls.ObserveAttachmentState(nodeID, state)
	}
}

func (s *Service) readyAttachmentResults(
	nodeID string,
	attachments []domain.UserAttachment,
	failOnFailed bool,
) (map[string]attachmentLocalizationResult, error) {
	result := make(map[string]attachmentLocalizationResult, len(attachments))
	for _, attachment := range attachments {
		state, ok := s.nodeControls.AttachmentState(nodeID, attachment.AttachmentID)
		if !ok || state.State != "ready" {
			if failOnFailed && ok && state.State == "failed" {
				return nil, apperr.Error{
					Status: http.StatusBadGateway,
					Message: firstNonEmpty(
						state.ErrorMessage,
						"paxd failed to localize attachment",
					),
				}
			}
			return nil, nil
		}
		if strings.TrimSpace(state.LocalURI) == "" {
			return nil, apperr.Error{
				Status:  http.StatusBadGateway,
				Message: "paxd reported ready attachment without local_uri",
			}
		}
		result[attachment.AttachmentID] = attachmentLocalizationResult{
			Attachment: attachment,
			State:      state,
		}
	}
	return result, nil
}
