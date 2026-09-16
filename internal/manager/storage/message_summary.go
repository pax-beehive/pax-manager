package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const toolMessageSQL = `message_type IN ('tool_call', 'tool_call_update', 'tool_call_content_chunk')`
const toolUpdateSQL = `COALESCE(NULLIF(raw_json #> '{params,update}', 'null'::jsonb), raw_json)`

var messageSummaryReturningSQL = strings.Replace(messageReturningSQL,
	`COALESCE(raw_json, '{}'::jsonb)`,
	`CASE WHEN `+toolMessageSQL+` THEN (
 SELECT jsonb_build_object(
 'tool_call_id', COALESCE(u->>'toolCallId', u->>'tool_call_id', ''),
 'title', LEFT(COALESCE(u->>'title', u->>'name', ''), 512),
 'status', COALESCE(u->>'status', ''), 'kind', COALESCE(u->>'kind', ''),
 'context_compaction', COALESCE(u #> '{_meta,contextCompaction}', u #> '{_meta,context_compaction}', 'false'::jsonb) = 'true'::jsonb)
 FROM (SELECT `+toolUpdateSQL+` AS u) tool
 ) ELSE COALESCE(raw_json, '{}'::jsonb) END`, 1)

func (s *PostgresStore) ListMessageSummaryPage(
	ctx context.Context, agentID, sessionID string, afterSeq, beforeSeq int64, limit int,
) (domain.MessageHistoryPage, error) {
	page, err := s.listMessageHistoryPageBySeq(
		ctx, agentID, sessionID, afterSeq, beforeSeq, limit, messageSummaryReturningSQL,
	)
	if err != nil {
		return page, err
	}
	return s.withSummaryTurnContext(ctx, agentID, sessionID, page)
}

func (s *PostgresStore) ListMessageSummaryParts(
	ctx context.Context,
	ids []string,
) (map[string][]MessagePart, error) {
	out := make(map[string][]MessagePart, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	placeholders := make([]string, len(ids))
	for i, id := range ids {
		args[i] = id
		placeholders[i] = "$" + strconvArg(i+1)
	}
	// Payloads already carried by the message are never fetched a second time.
	projection := strings.Replace(
		messagePartReturningSQL,
		`COALESCE(payload_json, '{}'::jsonb)`,
		`CASE WHEN EXISTS (
 SELECT 1 FROM messages m WHERE m.message_id = message_parts.message_id
 AND m.raw_json = message_parts.payload_json
 ) THEN '{}'::jsonb ELSE COALESCE(payload_json, '{}'::jsonb) END`,
		1,
	)
	rows, err := s.db.QueryContext(ctx, `SELECT `+projection+` FROM message_parts
 WHERE message_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY message_id, part_index`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		part, err := scanMessagePart(rows)
		if err != nil {
			return nil, err
		}
		if string(part.PayloadJSON) == "{}" {
			part.PayloadJSON = nil
		}
		out[part.MessageID] = append(out[part.MessageID], part)
	}
	return out, rows.Err()
}

func isToolMessage(kind string) bool {
	return kind == "tool_call" || kind == "tool_call_update" || kind == "tool_call_content_chunk"
}

func toolUpdate(raw json.RawMessage) map[string]any {
	var frame map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&frame)
	if params, ok := frame["params"].(map[string]any); ok {
		if update, ok := params["update"].(map[string]any); ok {
			return update
		}
	}
	return frame
}

func toolSummaryString(update map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := update[name].(string); ok {
			return value
		}
	}
	return ""
}

func (s *MemoryStore) ListMessageSummaryPage(
	ctx context.Context, agentID, sessionID string, afterSeq, beforeSeq int64, limit int,
) (domain.MessageHistoryPage, error) {
	page, err := s.ListMessageHistoryPageBySeq(ctx, agentID, sessionID, afterSeq, beforeSeq, limit)
	if err != nil {
		return page, err
	}
	page = s.withSummaryTurnContext(agentID, sessionID, page)
	for i := range page.Messages {
		msg := &page.Messages[i]
		if !isToolMessage(msg.MessageType) {
			continue
		}
		u := toolUpdate(msg.RawJSON)
		title := []rune(toolSummaryString(u, "title", "name"))
		if len(title) > 512 {
			title = title[:512]
		}
		msg.RawJSON, err = json.Marshal(domain.MessageToolSummary{
			ToolCallID: toolSummaryString(u, "toolCallId", "tool_call_id"), Title: string(title),
			Status: toolSummaryString(u, "status"), Kind: toolSummaryString(u, "kind"),
			ContextCompaction: toolSummaryCompaction(u),
		})
		if err != nil {
			return page, err
		}
	}
	return page, nil
}

func (s *MemoryStore) ListMessageSummaryParts(
	ctx context.Context,
	ids []string,
) (map[string][]MessagePart, error) {
	parts, err := s.ListMessagePartsByMessageIDs(ctx, ids)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range parts {
		raw := s.messages[id].RawJSON
		for i := range parts[id] {
			if len(raw) > 0 && bytes.Equal(raw, parts[id][i].PayloadJSON) {
				parts[id][i].PayloadJSON = nil
			}
		}
	}
	return parts, err
}

// Slice inside PostgreSQL, including legacy single-part outputs. The service
// never receives a complete large tool payload just to truncate it afterwards.
func (s *PostgresStore) GetMessageDetailPage(
	ctx context.Context, agentID, sessionID, messageID, section string, offset, limit int,
) (domain.MessageDetailPage, error) {
	ids, err := s.messageHistorySessionIDs(ctx, agentID, sessionID)
	if err != nil {
		return domain.MessageDetailPage{}, err
	}
	args := []any{agentID, messageID, section, offset + 1, limit + 1}
	placeholders := make([]string, len(ids))
	for i, id := range ids {
		args = append(args, id)
		placeholders[i] = "$" + strconvArg(len(args))
	}
	var text, format string
	var revision time.Time
	err = s.db.QueryRowContext(ctx, `
 WITH selected AS (
  SELECT message_id, updated_at, `+toolUpdateSQL+` AS u FROM messages
  WHERE agent_id=$1 AND message_id=$2 AND session_id IN (`+strings.Join(placeholders, ",")+`)
 ), content AS (
  SELECT GREATEST(updated_at, (SELECT MAX(updated_at) FROM message_parts WHERE message_id=$2)) AS revision,
   CASE WHEN $3='input' THEN COALESCE(u->'rawInput',u->'raw_input',u->'input',u->'arguments','null'::jsonb)
   ELSE COALESCE(u->'rawOutput',u->'raw_output',u->'output',u->'content',u->'result','null'::jsonb) END AS payload,
   CASE WHEN $3 <> 'input' THEN (SELECT string_agg(text, '' ORDER BY part_index) FROM message_parts
     WHERE message_id=$2 AND part_type='text' AND text IS NOT NULL AND text <> '') END AS body
  FROM selected
 )
 SELECT revision, CASE WHEN body IS NOT NULL THEN 'text' ELSE 'json' END,
 substring(COALESCE(body, payload::text) FROM $4 FOR $5) FROM content`, args...).Scan(&revision, &format, &text)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MessageDetailPage{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.MessageDetailPage{}, err
	}
	return detailPage(messageID, section, format, text, revision, offset, limit), nil
}

func detailPage(
	id, section, format, text string,
	revision time.Time,
	offset, limit int,
) domain.MessageDetailPage {
	chars := []rune(text)
	more := len(chars) > limit
	if more {
		chars = chars[:limit]
	}
	return domain.MessageDetailPage{
		MessageID: id,
		Section:   section,
		Format:    format,
		Text:      string(chars),
		Revision: revision.UTC().
			Format(time.RFC3339Nano),
		NextOffset: offset + len(chars),
		HasMore:    more,
	}
}

func (s *MemoryStore) GetMessageDetailPage(
	ctx context.Context, agentID, sessionID, messageID, section string, offset, limit int,
) (domain.MessageDetailPage, error) {
	s.mu.Lock()
	managerID := s.virtualSessionIDLocked(agentID, sessionID)
	nativeID := s.nativeSessionIDLocked(agentID, managerID)
	msg, ok := s.messages[messageID]
	if !ok || msg.AgentID != agentID ||
		(msg.SessionID != sessionID && msg.SessionID != managerID && msg.SessionID != nativeID) {
		s.mu.Unlock()
		return domain.MessageDetailPage{}, domain.ErrNotFound
	}
	msg = cloneMessage(msg)
	parts := make([]MessagePart, 0)
	for key, part := range s.messageParts {
		if key.MessageID == messageID {
			parts = append(parts, cloneMessagePart(part))
		}
	}
	s.mu.Unlock()
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartIndex < parts[j].PartIndex })
	u := toolUpdate(msg.RawJSON)
	keys := []string{"rawOutput", "raw_output", "output", "content", "result"}
	if section == "input" {
		keys = []string{"rawInput", "raw_input", "input", "arguments"}
	}
	var value any
	for _, key := range keys {
		if v, ok := u[key]; ok {
			value = v
			break
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return domain.MessageDetailPage{}, err
	}
	text, format := string(raw), "json"
	var body strings.Builder
	revision := msg.UpdatedAt
	for _, part := range parts {
		if part.UpdatedAt.After(revision) {
			revision = part.UpdatedAt
		}
		if section != "input" && part.PartType == domain.MessagePartText {
			body.WriteString(part.Text)
		}
	}
	if body.Len() > 0 {
		text = body.String()
		format = "text"
	}
	chars := []rune(text)
	if offset > len(chars) {
		offset = len(chars)
	}
	return detailPage(
		messageID,
		section,
		format,
		string(chars[offset:]),
		revision,
		offset,
		limit,
	), nil
}

func toolSummaryCompaction(u map[string]any) bool {
	meta, _ := u["_meta"].(map[string]any)
	return meta["contextCompaction"] == true || meta["context_compaction"] == true
}
