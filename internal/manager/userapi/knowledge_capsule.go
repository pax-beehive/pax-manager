package userapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	knowledgeKeywordLimit       = 80
	knowledgeTitleLimit         = 120
	knowledgeSummaryLimit       = 1200
	knowledgeContentLimit       = 32000
	knowledgeDeliveryLimit      = 8000
	knowledgeHistoryScanLimit   = 1000
	knowledgeExtractLineLimit   = 120
	redactedSecretPlaceholder   = "[redacted]"
	defaultKnowledgeArrayString = "[]"
)

func (s *Service) CreateKnowledgeCapsule(
	c context.Context,
	meta auth.RequestMetadata,
	sourceSessionID string,
	req domain.CreateKnowledgeCapsuleRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	keyword := strings.TrimSpace(req.Keyword)
	if keyword == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "keyword is required"}
	}
	if len(keyword) > knowledgeKeywordLimit {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "keyword is too long"}
	}
	source, err := s.store.GetSession(c, principal, sourceSessionID)
	if err != nil {
		return 0, nil, err
	}
	history, err := s.sessionHistory(c, source.AgentID, source.SessionID, knowledgeHistoryScanLimit)
	if err != nil {
		return 0, nil, err
	}
	capsule, err := s.buildKnowledgeCapsule(principal, source, keyword, history)
	if err != nil {
		return 0, nil, err
	}
	created, err := s.store.CreateKnowledgeCapsule(c, capsule)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"capsule": created}, nil
}

func (s *Service) ListKnowledgeCapsules(
	c context.Context,
	meta auth.RequestMetadata,
	filter domain.ListKnowledgeCapsulesFilter,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	filter.Principal = principal
	capsules, err := s.store.ListKnowledgeCapsules(c, filter)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"capsules": capsules}, nil
}

func (s *Service) GetKnowledgeCapsule(
	c context.Context,
	meta auth.RequestMetadata,
	capsuleID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	capsule, err := s.store.GetKnowledgeCapsule(c, principal, capsuleID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"capsule": capsule}, nil
}

func (s *Service) ArchiveKnowledgeCapsule(
	c context.Context,
	meta auth.RequestMetadata,
	capsuleID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	capsule, err := s.store.ArchiveKnowledgeCapsule(c, principal, capsuleID, s.clock().UTC())
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"capsule": capsule}, nil
}

func (s *Service) InjectKnowledgeCapsule(
	c context.Context,
	meta auth.RequestMetadata,
	targetSessionID string,
	req domain.InjectKnowledgeCapsuleRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if strings.TrimSpace(req.CapsuleID) == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "capsule_id is required",
		}
	}
	target, err := s.store.GetSession(c, principal, targetSessionID)
	if err != nil {
		return 0, nil, err
	}
	capsule, err := s.store.GetKnowledgeCapsule(c, principal, req.CapsuleID)
	if err != nil {
		return 0, nil, err
	}
	if capsule.Status != domain.KnowledgeCapsuleStatusActive {
		return 0, nil, apperr.Error{Status: http.StatusConflict, Message: "capsule is not active"}
	}
	message, deliveryTruncated := renderKnowledgeHandoff(capsule, knowledgeDeliveryLimit)
	payload, err := json.Marshal(map[string]any{
		"label":              domain.MessageTypeSystemHandoff,
		"capsule_id":         capsule.CapsuleID,
		"keyword":            capsule.Keyword,
		"source_session_id":  capsule.SourceSessionID,
		"source_agent_id":    capsule.SourceAgentID,
		"target_session_id":  target.SessionID,
		"delivery_method":    domain.KnowledgeInjectionDeliveryMailboxSteer,
		"delivery_truncated": deliveryTruncated,
	})
	if err != nil {
		return 0, nil, err
	}
	mailbox, err := s.store.CreateMailboxMessage(c, principal, domain.CreateMailboxRequest{
		NodeID:      target.NodeID,
		AgentID:     target.AgentID,
		SessionID:   target.SessionID,
		Message:     message,
		MessageType: domain.MessageTypeSystemHandoff,
		Payload:     payload,
	})
	if err != nil {
		return 0, nil, err
	}
	now := s.clock().UTC()
	injectionID, err := s.secrets.New("kinj")
	if err != nil {
		return 0, nil, err
	}
	injection, err := s.store.CreateKnowledgeInjection(c, domain.SessionKnowledgeInjection{
		InjectionID:         injectionID,
		OwnerUserID:         principal.User.UserID,
		CapsuleID:           capsule.CapsuleID,
		TargetSessionID:     target.SessionID,
		TargetAgentID:       target.AgentID,
		TargetNodeID:        target.NodeID,
		CreatedByUserID:     principal.User.UserID,
		DeliveredAsUserID:   principal.User.UserID,
		DeliveryMethod:      domain.KnowledgeInjectionDeliveryMailboxSteer,
		DeliveryMessageID:   mailbox.MessageID,
		DeliveryMessageType: domain.MessageTypeSystemHandoff,
		Status:              domain.KnowledgeInjectionStatusDelivered,
		CreatedAt:           now,
		DeliveredAt:         &now,
	})
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"injection": injection, "message": mailbox}, nil
}

func (s *Service) ListKnowledgeInjections(
	c context.Context,
	meta auth.RequestMetadata,
	sessionID string,
	filter domain.ListKnowledgeInjectionsFilter,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if _, err := s.store.GetSession(c, principal, sessionID); err != nil {
		return 0, nil, err
	}
	filter.Principal = principal
	filter.TargetSessionID = sessionID
	injections, err := s.store.ListKnowledgeInjections(c, filter)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"injections": injections}, nil
}

func (s *Service) sessionHistory(
	c context.Context,
	agentID string,
	sessionID string,
	limit int,
) ([]domain.MessageWithParts, error) {
	messages, err := s.store.ListMessages(c, agentID, sessionID, limit)
	if err != nil {
		return nil, err
	}
	history := make([]domain.MessageWithParts, 0, len(messages))
	for _, message := range messages {
		parts, err := s.store.ListMessageParts(c, message.MessageID)
		if err != nil {
			return nil, err
		}
		history = append(history, domain.MessageWithParts{Message: message, Parts: parts})
	}
	return history, nil
}

func (s *Service) buildKnowledgeCapsule(
	principal domain.UserPrincipal,
	source domain.AgentSession,
	keyword string,
	history []domain.MessageWithParts,
) (domain.KnowledgeCapsule, error) {
	capsuleID, err := s.secrets.New("kcap")
	if err != nil {
		return domain.KnowledgeCapsule{}, err
	}
	now := s.clock().UTC()
	content, originalChars, truncated := extractKnowledgeContent(keyword, history)
	if strings.TrimSpace(content) == "" {
		content = "No matching session history was found for this keyword."
	}
	summary := truncateString(
		fmt.Sprintf(
			"Extracted knowledge related to %q from session %s. Review source context before relying on this handoff.",
			keyword,
			source.SessionID,
		),
		knowledgeSummaryLimit,
	)
	title := truncateString("Knowledge capsule: "+keyword, knowledgeTitleLimit)
	return domain.KnowledgeCapsule{
		CapsuleID:              capsuleID,
		OwnerUserID:            principal.User.UserID,
		SourceSessionID:        source.SessionID,
		SourceAgentID:          source.AgentID,
		SourceNodeID:           source.NodeID,
		CreatedByUserID:        principal.User.UserID,
		Keyword:                keyword,
		Title:                  title,
		Summary:                summary,
		Content:                content,
		SuggestedSkills:        json.RawMessage(defaultKnowledgeArrayString),
		References:             json.RawMessage(defaultKnowledgeArrayString),
		OpenQuestions:          json.RawMessage(defaultKnowledgeArrayString),
		Risks:                  json.RawMessage(defaultKnowledgeArrayString),
		Redactions:             json.RawMessage(defaultKnowledgeArrayString),
		Status:                 domain.KnowledgeCapsuleStatusActive,
		Truncated:              truncated,
		OriginalEstimatedChars: int64(originalChars),
		CreatedAt:              now,
	}, nil
}

func extractKnowledgeContent(
	keyword string,
	history []domain.MessageWithParts,
) (string, int, bool) {
	needle := strings.ToLower(keyword)
	var builder strings.Builder
	originalChars := 0
	contentChars := 0
	lines := 0
	for _, item := range history {
		messageText := messageSearchText(item)
		if !strings.Contains(strings.ToLower(messageText), needle) {
			continue
		}
		if lines >= knowledgeExtractLineLimit {
			break
		}
		line := fmt.Sprintf(
			"- [%s %s] %s",
			item.Role,
			item.CreatedAt.UTC().Format("2006-01-02 15:04:05Z"),
			redactKnowledgeSecrets(strings.TrimSpace(messageText)),
		)
		lineChars := runeLen(line)
		originalChars += lineChars
		separatorChars := 0
		if builder.Len() > 0 {
			separatorChars = 1
		}
		if contentChars+separatorChars+lineChars > knowledgeContentLimit {
			if contentChars == 0 {
				return truncateString(line, knowledgeContentLimit), originalChars, true
			}
			return builder.String(), originalChars, true
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(line)
		contentChars += separatorChars + lineChars
		lines++
	}
	truncated := lines >= knowledgeExtractLineLimit
	return builder.String(), originalChars, truncated
}

func messageSearchText(item domain.MessageWithParts) string {
	segments := make([]string, 0, len(item.Parts)+1)
	for _, part := range item.Parts {
		if part.Text != "" {
			segments = append(segments, part.Text)
		}
		if len(part.PayloadJSON) > 0 {
			segments = append(segments, string(part.PayloadJSON))
		}
	}
	if len(item.RawJSON) > 0 {
		segments = append(segments, string(item.RawJSON))
	}
	return strings.Join(segments, " ")
}

func renderKnowledgeHandoff(capsule domain.KnowledgeCapsule, limit int) (string, bool) {
	body := fmt.Sprintf(
		"system_handoff\n\nTitle: %s\nKeyword: %s\nSource session: %s\n\nSummary:\n%s\n\nContent:\n%s",
		capsule.Title,
		capsule.Keyword,
		capsule.SourceSessionID,
		capsule.Summary,
		capsule.Content,
	)
	return truncateString(body, limit), stringExceedsLimit(body, limit)
}

func redactKnowledgeSecrets(input string) string {
	fields := strings.Fields(input)
	for i, field := range fields {
		lower := strings.ToLower(field)
		if strings.Contains(lower, "api_key=") ||
			strings.Contains(lower, "token=") ||
			strings.Contains(lower, "authorization:") ||
			strings.HasPrefix(field, "sk-") ||
			strings.HasPrefix(field, "pax_") {
			fields[i] = redactedSecretPlaceholder
		}
	}
	return strings.Join(fields, " ")
}

func truncateString(input string, limit int) string {
	runes := []rune(input)
	if limit <= 0 || len(runes) <= limit {
		return input
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func stringExceedsLimit(input string, limit int) bool {
	return limit > 0 && runeLen(input) > limit
}

func runeLen(input string) int {
	return len([]rune(input))
}
