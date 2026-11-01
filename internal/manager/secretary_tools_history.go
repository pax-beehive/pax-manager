package manager

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

type secretaryHistoryRequest struct {
	SessionID string `json:"session_id"`
	AfterSeq  int64  `json:"after_seq,omitempty"`
	BeforeSeq int64  `json:"before_seq,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type secretaryHistoryPage struct {
	Messages   []userapi.WorkspaceMessageRead  `json:"messages"`
	Pagination domain.MessageHistoryPagination `json:"pagination"`
}

func (s *Service) secretaryToolHistory(
	ctx context.Context,
	grant *secretaryToolGrant,
	search *userapi.WorkspaceSearchService,
	arguments string,
) (secretaryHistoryPage, error) {
	var request secretaryHistoryRequest
	if err := decodeSecretaryToolJSON(arguments, &request); err != nil {
		return secretaryHistoryPage{}, err
	}
	if request.SessionID == "" || request.AfterSeq < 0 || request.BeforeSeq < 0 ||
		(request.AfterSeq != 0 && request.BeforeSeq != 0) || request.Limit < 0 || request.Limit > 50 {
		return secretaryHistoryPage{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "invalid secretary history limits",
		}
	}
	if request.Limit == 0 {
		request.Limit = 20
	}
	principal := secretaryToolPrincipal{owner: grant.job.OwnerUserID}
	if err := s.secretaryHistoryVisible(ctx, principal.owner, request.SessionID); err != nil {
		return secretaryHistoryPage{}, err
	}
	// Reuse the console history service and its seq cursors, then render only canonical sources.
	historyService := userapi.NewService(s.store, s.clock, principal, s.secrets)
	_, data, err := historyService.ListSessionHistorySummary(
		ctx,
		auth.RequestMetadata{},
		request.SessionID,
		request.AfterSeq,
		request.BeforeSeq,
		request.Limit,
	)
	if err != nil {
		return secretaryHistoryPage{}, err
	}
	page := data.(map[string]any)
	result := secretaryHistoryPage{
		Messages:   []userapi.WorkspaceMessageRead{},
		Pagination: page["pagination"].(domain.MessageHistoryPagination),
	}
	seen := make(map[string]bool)
	readCount := 0
	for _, message := range page["messages"].([]domain.MessageWithParts) {
		if !secretaryHistoryInWindow(message, request, result.Pagination) ||
			seen[message.MessageID] {
			continue
		}
		if readCount == request.Limit {
			break
		}
		seen[message.MessageID] = true
		readCount++
		read, readErr := search.Read(ctx, auth.RequestMetadata{}, userapi.WorkspaceReadRequest{
			Ref: userapi.SearchRef{Kind: "message", ID: message.MessageID},
		})
		if errors.Is(readErr, domain.ErrNotFound) {
			continue
		}
		if readErr != nil {
			return secretaryHistoryPage{}, readErr
		}
		result.Messages = append(result.Messages, read.(userapi.WorkspaceMessageRead))
	}
	if err = s.secretaryHistoryVisible(ctx, principal.owner, request.SessionID); err != nil {
		return secretaryHistoryPage{}, err
	}
	return result, nil
}

func secretaryHistoryInWindow(
	message domain.MessageWithParts,
	request secretaryHistoryRequest,
	page domain.MessageHistoryPagination,
) bool {
	return page.NextBeforeSeq > 0 && page.NextAfterSeq > 0 &&
		message.SessionSeq >= page.NextBeforeSeq && message.SessionSeq <= page.NextAfterSeq &&
		(request.AfterSeq == 0 || message.SessionSeq > request.AfterSeq) &&
		(request.BeforeSeq == 0 || message.SessionSeq < request.BeforeSeq)
}

func (s *Service) secretaryHistoryVisible(ctx context.Context, owner, sessionID string) error {
	principal := domain.UserPrincipal{User: domain.User{UserID: owner}}
	session, err := s.store.GetSession(ctx, principal, sessionID)
	if err != nil {
		return err
	}
	if session.ArchivedAt != nil || session.Transport == domain.SessionTransportE2EE ||
		session.Source == domain.SecretaryInternalSource || strings.HasPrefix(session.SessionID, domain.SecretaryInternalSessionPrefix) {
		return domain.ErrNotFound
	}
	agent, err := s.store.GetAgent(ctx, principal, session.AgentID)
	if err != nil || agent.OwnerUserID != owner {
		return domain.ErrNotFound
	}
	for _, nodeID := range []string{agent.NodeID, session.NodeID} {
		if nodeID != "" {
			if _, err = s.store.GetNode(ctx, principal, nodeID); err != nil {
				return domain.ErrNotFound
			}
		}
	}
	return nil
}
