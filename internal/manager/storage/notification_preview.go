package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func completionReplyPreview(ctx context.Context, tx *sql.Tx, msg Message) (string, error) {
	rows, err := tx.QueryContext(ctx, `WITH replies AS (
		SELECT m.session_seq, m.message_type,
		COALESCE(m.raw_json->>'text_layout', '') AS layout, LEFT(p.text, 1024) AS text
		FROM messages m JOIN message_parts p ON p.message_id=m.message_id
		WHERE m.owner_user_id=$1 AND m.node_id=$2 AND m.agent_id=$3
		AND m.session_id=$4 AND m.turn_id=$5 AND m.source=$6 AND m.direction=$7
		AND m.session_seq < $8 AND m.role='assistant'
		AND m.message_type IN ('agent_message_chunk','message_delta')
		AND p.part_type='text' AND p.part_index=0 AND p.text <> ''
	), latest AS (
		SELECT * FROM replies ORDER BY session_seq DESC LIMIT 1
	), segments AS (
		SELECT r.session_seq, r.text, l.session_seq AS last_seq,
		ROW_NUMBER() OVER (ORDER BY r.session_seq DESC) AS distance
		FROM replies r CROSS JOIN latest l
		WHERE l.layout='segment' AND r.layout='segment' AND r.message_type=l.message_type
	)
	SELECT text FROM (
		SELECT session_seq, text FROM segments WHERE session_seq+distance=last_seq+1
		UNION ALL SELECT session_seq, text FROM latest WHERE layout<>'segment'
	) selected ORDER BY session_seq LIMIT 1024`,
		msg.OwnerUserID, msg.NodeID, msg.AgentID, msg.SessionID, msg.TurnID,
		domain.MessageSourceACPTunnel, domain.MessageDirectionAgentToUser, msg.SessionSeq,
	)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var chunks []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return "", err
		}
		chunks = append(chunks, text)
	}
	return previewPrefix(chunks), rows.Err()
}

func (s *MemoryStore) completionReplyPreviewLocked(terminal Message) string {
	var replies []Message
	for _, msg := range s.messages {
		if !isCompletionReply(msg, terminal) {
			continue
		}
		part := s.messageParts[messagePartKey{MessageID: msg.MessageID, Index: 0}]
		if part.PartType != domain.MessagePartText || part.Text == "" {
			continue
		}
		replies = append(replies, msg)
	}
	if len(replies) == 0 {
		return ""
	}
	sort.Slice(
		replies,
		func(i, j int) bool { return replies[i].SessionSeq < replies[j].SessionSeq },
	)
	last := len(replies) - 1
	first := last
	if segmentedReply(replies[last]) {
		for first > 0 && replies[first-1].SessionSeq+1 == replies[first].SessionSeq &&
			replies[first-1].MessageType == replies[last].MessageType && segmentedReply(replies[first-1]) {
			first--
		}
	}
	var chunks []string
	for _, msg := range replies[first:] {
		chunks = append(
			chunks,
			s.messageParts[messagePartKey{MessageID: msg.MessageID, Index: 0}].Text,
		)
	}
	return previewPrefix(chunks)
}

func segmentedReply(msg Message) bool {
	var raw struct {
		TextLayout string `json:"text_layout"`
	}
	return json.Unmarshal(msg.RawJSON, &raw) == nil && raw.TextLayout == "segment"
}

func previewPrefix(chunks []string) string {
	// Keep only the beginning, with the same Unicode limit as the SQL projection.
	var preview strings.Builder
	count := 0
	for _, chunk := range chunks {
		for _, character := range chunk {
			if count == 1024 {
				return preview.String()
			}
			preview.WriteRune(character)
			count++
		}
	}
	return preview.String()
}

func isCompletionReply(msg, terminal Message) bool {
	return msg.OwnerUserID == terminal.OwnerUserID && msg.NodeID == terminal.NodeID &&
		msg.AgentID == terminal.AgentID && msg.SessionID == terminal.SessionID &&
		msg.TurnID == terminal.TurnID && msg.SessionSeq < terminal.SessionSeq &&
		msg.Source == domain.MessageSourceACPTunnel && msg.Direction == domain.MessageDirectionAgentToUser &&
		msg.Role == "assistant" && (msg.MessageType == "agent_message_chunk" || msg.MessageType == "message_delta")
}
