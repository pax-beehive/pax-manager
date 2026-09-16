package storage

import (
	"context"
	"sort"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// A transport-row page can start after the user prompt and the first-touch
// assistant aggregate. Include the non-tool context for its turns while keeping
// the original row cursor, so older tool pages remain reachable.
func (s *PostgresStore) withSummaryTurnContext(
	ctx context.Context, agentID, sessionID string, page domain.MessageHistoryPage,
) (domain.MessageHistoryPage, error) {
	turns := summaryPageTurns(page)
	if len(turns) == 0 {
		return page, nil
	}
	sessions, err := s.messageHistorySessionIDs(ctx, agentID, sessionID)
	if err != nil {
		return page, err
	}
	args := []any{agentID}
	placeholders := func(values []string) string {
		out := make([]string, 0, len(values))
		for _, value := range values {
			args = append(args, value)
			out = append(out, "$"+strconvArg(len(args)))
		}
		return strings.Join(out, ",")
	}
	filter := "agent_id = $1 AND session_id IN (" + placeholders(sessions) + ")"
	filter += " AND turn_id IN (" + placeholders(
		turns,
	) + ") AND (message_type IS NULL OR NOT (" + toolMessageSQL + "))"
	args = append(args, page.HeadSeq)
	filter += " AND session_seq <= $" + strconvArg(len(args))
	rows, err := s.db.QueryContext(
		ctx,
		"SELECT "+messageReturningSQL+" FROM messages WHERE "+filter+" ORDER BY session_seq",
		args...)
	if err != nil {
		return page, err
	}
	defer func() { _ = rows.Close() }()
	var contextMessages []Message
	for rows.Next() {
		msg, scanErr := scanMessage(rows)
		if scanErr != nil {
			return page, scanErr
		}
		contextMessages = append(contextMessages, msg)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	page.Messages = mergeSummaryContext(page.Messages, contextMessages)
	return page, nil
}

func (s *MemoryStore) withSummaryTurnContext(
	agentID, sessionID string, page domain.MessageHistoryPage,
) domain.MessageHistoryPage {
	turns := summaryPageTurns(page)
	if len(turns) == 0 {
		return page
	}
	wanted := make(map[string]bool, len(turns))
	for _, id := range turns {
		wanted[id] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	managerID := s.virtualSessionIDLocked(agentID, sessionID)
	nativeID := s.nativeSessionIDLocked(agentID, managerID)
	var contextMessages []Message
	for _, msg := range s.messages {
		if msg.AgentID == agentID && wanted[msg.TurnID] && msg.SessionSeq <= page.HeadSeq &&
			!isToolMessage(msg.MessageType) &&
			(msg.SessionID == sessionID || msg.SessionID == managerID || (nativeID != "" && msg.SessionID == nativeID)) {
			contextMessages = append(contextMessages, cloneMessage(msg))
		}
	}
	page.Messages = mergeSummaryContext(page.Messages, contextMessages)
	return page
}

func summaryPageTurns(page domain.MessageHistoryPage) []string {
	seen := make(map[string]bool)
	var turns []string
	for _, msg := range page.Messages {
		if msg.TurnID != "" && !seen[msg.TurnID] {
			seen[msg.TurnID] = true
			turns = append(turns, msg.TurnID)
		}
	}
	return turns
}

func mergeSummaryContext(messages, contextMessages []Message) []Message {
	seen := make(map[string]bool, len(messages))
	for _, msg := range messages {
		seen[msg.MessageID] = true
	}
	for _, msg := range contextMessages {
		if !seen[msg.MessageID] {
			messages = append(messages, msg)
			seen[msg.MessageID] = true
		}
	}
	sort.SliceStable(
		messages,
		func(i, j int) bool { return messages[i].SessionSeq < messages[j].SessionSeq },
	)
	return messages
}
