package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *PostgresStore) LatestSessionMessage(
	ctx context.Context,
	agentID, sessionID string,
) (Message, error) {
	ids, err := s.messageHistorySessionIDs(ctx, agentID, sessionID)
	if err != nil {
		return Message{}, err
	}
	args := []any{agentID}
	placeholders := make([]string, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
		placeholders = append(placeholders, "$"+strconvArg(len(args)))
	}
	var message Message
	err = s.db.QueryRowContext(ctx, `SELECT message_id, session_seq, COALESCE(turn_id, '')
 FROM messages WHERE agent_id = $1 AND session_id IN (`+strings.Join(placeholders, ",")+`)
 AND session_seq IS NOT NULL ORDER BY session_seq DESC LIMIT 1`, args...).Scan(
		&message.MessageID, &message.SessionSeq, &message.TurnID)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, nil
	}
	return message, err
}

func (s *MemoryStore) LatestSessionMessage(
	ctx context.Context,
	agentID, sessionID string,
) (Message, error) {
	page, err := s.ListMessageHistoryPageBySeq(ctx, agentID, sessionID, 0, 0, 1)
	if err != nil || len(page.Messages) == 0 {
		return Message{}, err
	}
	message := page.Messages[0]
	return Message{
		MessageID:  message.MessageID,
		SessionSeq: message.SessionSeq,
		TurnID:     message.TurnID,
	}, nil
}

func (s *PostgresStore) ListTurnSummaryPage(ctx context.Context, agentID, sessionID, turnID string,
	afterSeq, beforeSeq int64, limit int,
) (domain.MessageHistoryPage, error) {
	// Unlike the general summary view, turn pages do not expand context beyond limit.
	return s.listMessageHistoryPageBySeq(
		ctx,
		agentID,
		sessionID,
		afterSeq,
		beforeSeq,
		limit,
		messageSummaryReturningSQL,
		turnID,
	)
}

func (s *MemoryStore) ListTurnSummaryPage(ctx context.Context, agentID, sessionID, turnID string,
	afterSeq, beforeSeq int64, limit int,
) (domain.MessageHistoryPage, error) {
	page, err := s.listMessageHistoryPageBySeq(
		ctx,
		agentID,
		sessionID,
		afterSeq,
		beforeSeq,
		limit,
		turnID,
	)
	if err != nil {
		return page, err
	}
	return summarizeMemoryMessages(page)
}
