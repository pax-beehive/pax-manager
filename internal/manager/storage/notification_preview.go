package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func completionReplyPreview(ctx context.Context, tx *sql.Tx, msg Message) (string, error) {
	var text string
	err := tx.QueryRowContext(ctx, `SELECT LEFT(p.text, 1024)
		FROM messages m JOIN message_parts p ON p.message_id=m.message_id
		WHERE m.owner_user_id=$1 AND m.node_id=$2 AND m.agent_id=$3
		AND m.session_id=$4 AND m.turn_id=$5 AND m.source=$6 AND m.direction=$7
		AND m.session_seq < $8 AND m.role='assistant'
		AND m.message_type IN ('agent_message_chunk','message_delta')
		AND p.part_type='text' AND p.part_index=0 AND p.text <> ''
		ORDER BY m.session_seq DESC, m.message_id DESC LIMIT 1`,
		msg.OwnerUserID, msg.NodeID, msg.AgentID, msg.SessionID, msg.TurnID,
		domain.MessageSourceACPTunnel, domain.MessageDirectionAgentToUser, msg.SessionSeq,
	).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return text, err
}

func (s *MemoryStore) completionReplyPreviewLocked(terminal Message) string {
	var latest Message
	var preview string
	for _, msg := range s.messages {
		if !isCompletionReply(msg, terminal) || msg.SessionSeq < latest.SessionSeq ||
			(msg.SessionSeq == latest.SessionSeq && msg.MessageID <= latest.MessageID) {
			continue
		}
		part := s.messageParts[messagePartKey{MessageID: msg.MessageID, Index: 0}]
		if part.PartType != domain.MessagePartText || part.Text == "" {
			continue
		}
		latest = msg
		preview = part.Text
	}
	// Match the bounded PostgreSQL projection before normalizing the display body.
	count := 0
	for offset := range preview {
		if count == 1024 {
			return preview[:offset]
		}
		count++
	}
	return preview
}

func isCompletionReply(msg, terminal Message) bool {
	return msg.OwnerUserID == terminal.OwnerUserID && msg.NodeID == terminal.NodeID &&
		msg.AgentID == terminal.AgentID && msg.SessionID == terminal.SessionID &&
		msg.TurnID == terminal.TurnID && msg.SessionSeq < terminal.SessionSeq &&
		msg.Source == domain.MessageSourceACPTunnel && msg.Direction == domain.MessageDirectionAgentToUser &&
		msg.Role == "assistant" && (msg.MessageType == "agent_message_chunk" || msg.MessageType == "message_delta")
}
