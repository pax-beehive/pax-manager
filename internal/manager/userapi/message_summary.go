package userapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *Service) ListSessionHistorySummary(
	ctx context.Context,
	meta auth.RequestMetadata,
	sessionID string,
	afterSeq, beforeSeq int64,
	limit int,
) (int, any, error) {
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	session, err := s.store.GetSession(ctx, principal, sessionID)
	if err != nil {
		return 0, nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	page, err := s.store.ListMessageSummaryPage(
		ctx,
		session.AgentID,
		session.SessionID,
		afterSeq,
		beforeSeq,
		limit,
	)
	if err != nil {
		return 0, nil, err
	}
	history := make([]domain.MessageWithParts, 0, len(page.Messages))
	ids := make([]string, 0, len(page.Messages))
	for _, msg := range page.Messages {
		item := domain.MessageWithParts{Message: msg, Parts: []domain.MessagePart{}}
		item.SessionID = session.SessionID
		switch msg.MessageType {
		case "tool_call", "tool_call_update", "tool_call_content_chunk":
			item.Tool = &domain.MessageToolSummary{}
			if err := json.Unmarshal(msg.RawJSON, item.Tool); err != nil {
				return 0, nil, err
			}
			item.RawJSON = nil
			item.HasDetail = true
		default:
			// A user prompt frame contains text and attachments. Keep it once and do
			// not fetch its duplicate extracted text/payload parts.
			if !isSummaryPrompt(msg) {
				ids = append(ids, msg.MessageID)
			}
		}
		history = append(history, item)
	}
	parts, err := s.store.ListMessageSummaryParts(ctx, ids)
	if err != nil {
		return 0, nil, err
	}
	for i := range history {
		if values := parts[history[i].MessageID]; values != nil {
			history[i].Parts = values
		}
	}
	history = domain.NormalTranscriptMessages(history)
	return http.StatusOK, map[string]any{
		"messages": history,
		"pagination": domain.MessageHistoryPagination{
			NextBeforeID: page.NextBeforeID, HasMore: page.HasMore, HeadSeq: page.HeadSeq, HasOlder: page.HasOlder,
			HasNewer: page.HasNewer, NextBeforeSeq: page.NextBeforeSeq, NextAfterSeq: page.NextAfterSeq,
		},
	}, nil
}

func (s *Service) GetSessionMessageDetail(
	ctx context.Context,
	meta auth.RequestMetadata,
	sessionID, messageID, section, revision string,
	offset, limit int,
) (int, any, error) {
	if section == "" {
		section = "output"
	}
	if (section != "input" && section != "output") || offset < 0 || offset > 1_000_000_000 ||
		limit < 0 {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "invalid section, offset or limit",
		}
	}
	if offset > 0 && strings.TrimSpace(revision) == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "revision is required after the first page",
		}
	}
	if limit == 0 || limit > domain.MessageDetailPageSize {
		limit = domain.MessageDetailPageSize
	}
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	session, err := s.store.GetSession(ctx, principal, sessionID)
	if err != nil {
		return 0, nil, err
	}
	page, err := s.store.GetMessageDetailPage(
		ctx,
		session.AgentID,
		session.SessionID,
		messageID,
		section,
		offset,
		limit,
	)
	if err != nil {
		return 0, nil, err
	}
	if revision != "" && revision != page.Revision {
		return 0, nil, apperr.Error{
			Status:  http.StatusConflict,
			Message: "message changed; reload details from the first page",
		}
	}
	return http.StatusOK, page, nil
}

func isSummaryPrompt(msg domain.Message) bool {
	if msg.Role != "user" {
		return false
	}
	var frame struct {
		Method string `json:"method"`
	}
	return json.Unmarshal(msg.RawJSON, &frame) == nil && frame.Method == "session/prompt"
}
