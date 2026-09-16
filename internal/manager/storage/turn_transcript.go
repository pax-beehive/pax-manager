package storage

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *PostgresStore) ListTurnTranscript(
	ctx context.Context, agentID, sessionID, turnID string,
) ([]domain.MessageWithParts, error) {
	if agentID == "" || sessionID == "" || turnID == "" {
		return nil, errors.New("agent, session and turn are required")
	}
	sessionIDs, err := s.messageHistorySessionIDs(ctx, agentID, sessionID)
	if err != nil {
		return nil, err
	}
	args := []any{agentID, turnID}
	placeholders := make([]string, 0, len(sessionIDs))
	for _, id := range sessionIDs {
		args = append(args, id)
		placeholders = append(placeholders, "$"+strconvArg(len(args)))
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageSummaryReturningSQL+`
		FROM messages WHERE agent_id = $1 AND turn_id = $2
		AND session_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY session_seq`, args...)
	if err != nil {
		return nil, err
	}
	var messages []Message
	for rows.Next() {
		msg, scanErr := scanMessage(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		messages = append(messages, msg)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	return buildTurnTranscript(ctx, sessionID, messages, s.ListMessageSummaryParts)
}

func (s *MemoryStore) ListTurnTranscript(
	ctx context.Context, agentID, sessionID, turnID string,
) ([]domain.MessageWithParts, error) {
	if agentID == "" || sessionID == "" || turnID == "" {
		return nil, errors.New("agent, session and turn are required")
	}
	s.mu.Lock()
	managerID := s.virtualSessionIDLocked(agentID, sessionID)
	nativeID := s.nativeSessionIDLocked(agentID, managerID)
	var messages []Message
	for _, msg := range s.messages {
		if msg.AgentID == agentID && msg.TurnID == turnID &&
			(msg.SessionID == sessionID || msg.SessionID == managerID || (nativeID != "" && msg.SessionID == nativeID)) {
			messages = append(messages, cloneMessage(msg))
		}
	}
	s.mu.Unlock()
	sort.Slice(
		messages,
		func(i, j int) bool { return messages[i].SessionSeq < messages[j].SessionSeq },
	)
	for i := range messages {
		if !isToolMessage(messages[i].MessageType) {
			continue
		}
		u := toolUpdate(messages[i].RawJSON)
		title := []rune(toolSummaryString(u, "title", "name"))
		if len(title) > 512 {
			title = title[:512]
		}
		messages[i].RawJSON, _ = json.Marshal(domain.MessageToolSummary{
			ToolCallID: toolSummaryString(u, "toolCallId", "tool_call_id"),
			Title:      string(title), Status: toolSummaryString(u, "status"),
			Kind: toolSummaryString(u, "kind"), ContextCompaction: toolSummaryCompaction(u),
		})
	}
	return buildTurnTranscript(ctx, sessionID, messages, s.ListMessageSummaryParts)
}

func buildTurnTranscript(
	ctx context.Context, sessionID string, messages []Message,
	readParts func(context.Context, []string) (map[string][]MessagePart, error),
) ([]domain.MessageWithParts, error) {
	items := make([]domain.MessageWithParts, 0, len(messages))
	var ids []string
	for _, msg := range messages {
		item := domain.MessageWithParts{Message: msg, Parts: []MessagePart{}}
		item.SessionID = sessionID
		if isToolMessage(msg.MessageType) {
			item.Tool = &domain.MessageToolSummary{}
			if err := json.Unmarshal(msg.RawJSON, item.Tool); err != nil {
				return nil, err
			}
			item.RawJSON = nil
			item.HasDetail = true
		} else {
			ids = append(ids, msg.MessageID)
		}
		items = append(items, item)
	}
	parts, err := readParts(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if values := parts[items[i].MessageID]; values != nil {
			items[i].Parts = values
		}
	}
	return domain.NormalTranscriptMessages(items), nil
}
