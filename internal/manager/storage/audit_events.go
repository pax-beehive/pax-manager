package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const auditEventReturningSQL = `
	event_id, owner_user_id, COALESCE(node_id, ''), COALESCE(agent_id, ''),
		COALESCE(session_id, ''), COALESCE(turn_id, ''), COALESCE(message_id, ''),
		COALESCE(approval_id, ''), event_type, source_type, source_id, event_key,
		title, summary, tool_name, COALESCE(tool_input, '{}'::jsonb), reason,
		risk_level, approval_status, decision, decision_scope, decided_by_user_id,
		decided_at, occurred_at, COALESCE(raw, '{}'::jsonb)`

func (s *PostgresStore) UpsertAuditEvent(
	ctx context.Context,
	event AgentAuditEvent,
) (AgentAuditEvent, error) {
	event = normalizeAuditEvent(event, s.now().UTC())
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO agent_audit_events (
			event_id, owner_user_id, node_id, agent_id, session_id, turn_id, message_id,
			approval_id, event_type, source_type, source_id, event_key, title, summary,
			tool_name, tool_input, reason, risk_level, approval_status, decision,
			decision_scope, decided_by_user_id, decided_at, occurred_at, raw, updated_at
		)
		VALUES (
			$1,$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),
			NULLIF($8,''),$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,
			$21,$22,$23,$24,$25,$24
		)
		ON CONFLICT (source_type, source_id, event_type, event_key) DO UPDATE SET
			owner_user_id = EXCLUDED.owner_user_id,
			node_id = EXCLUDED.node_id,
			agent_id = EXCLUDED.agent_id,
			session_id = EXCLUDED.session_id,
			turn_id = EXCLUDED.turn_id,
			message_id = EXCLUDED.message_id,
			approval_id = EXCLUDED.approval_id,
			title = EXCLUDED.title,
			summary = EXCLUDED.summary,
			tool_name = EXCLUDED.tool_name,
			tool_input = EXCLUDED.tool_input,
			reason = EXCLUDED.reason,
			risk_level = EXCLUDED.risk_level,
			approval_status = EXCLUDED.approval_status,
			decision = EXCLUDED.decision,
			decision_scope = EXCLUDED.decision_scope,
			decided_by_user_id = EXCLUDED.decided_by_user_id,
			decided_at = EXCLUDED.decided_at,
			occurred_at = EXCLUDED.occurred_at,
			raw = EXCLUDED.raw,
			updated_at = EXCLUDED.updated_at
		RETURNING `+auditEventReturningSQL+`
	`, event.EventID, event.OwnerUserID, event.NodeID, event.AgentID, event.SessionID,
		event.TurnID, event.MessageID, event.ApprovalID, event.EventType, event.SourceType,
		event.SourceID, event.EventKey, event.Title, event.Summary, event.ToolName,
		jsonDefault(event.ToolInput, "{}"), event.Reason, event.RiskLevel,
		event.ApprovalStatus, event.Decision, event.DecisionScope, event.DecidedByUserID,
		event.DecidedAt, event.OccurredAt, jsonDefault(event.Raw, "{}"))
	return scanAuditEvent(row)
}

func (s *PostgresStore) ListAuditEvents(
	ctx context.Context,
	filter AuditEventFilter,
) ([]AgentAuditEvent, error) {
	query, args := auditEventListQuery(filter)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanAuditEvents(rows)
}

func auditEventListQuery(filter AuditEventFilter) (string, []any) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	clauses := []string{"owner_user_id = $1"}
	args := []any{filter.Principal.User.UserID}
	add := func(column string, value string) {
		if value == "" {
			return
		}
		args = append(args, value)
		clauses = append(clauses, column+" = $"+strconvArg(len(args)))
	}
	add("event_type", filter.EventType)
	add("agent_id", filter.AgentID)
	add("session_id", filter.SessionID)
	add("approval_id", filter.ApprovalID)
	add("decision", filter.Decision)
	if strings.TrimSpace(filter.Query) != "" {
		args = append(args, "%"+strings.ToLower(strings.TrimSpace(filter.Query))+"%")
		idx := "$" + strconvArg(len(args))
		clauses = append(clauses, `(
			lower(title) LIKE `+idx+` OR
			lower(summary) LIKE `+idx+` OR
			lower(tool_name) LIKE `+idx+` OR
			lower(reason) LIKE `+idx+` OR
			lower(raw::text) LIKE `+idx+` OR
			lower(tool_input::text) LIKE `+idx+`
		)`)
	}
	args = append(args, limit)
	return `SELECT ` + auditEventReturningSQL + `
		FROM agent_audit_events
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY occurred_at DESC, event_id DESC
		LIMIT $` + strconvArg(len(args)), args
}

func scanAuditEvent(row rowScanner) (AgentAuditEvent, error) {
	var event AgentAuditEvent
	var toolInput []byte
	var raw []byte
	if err := row.Scan(
		&event.EventID,
		&event.OwnerUserID,
		&event.NodeID,
		&event.AgentID,
		&event.SessionID,
		&event.TurnID,
		&event.MessageID,
		&event.ApprovalID,
		&event.EventType,
		&event.SourceType,
		&event.SourceID,
		&event.EventKey,
		&event.Title,
		&event.Summary,
		&event.ToolName,
		&toolInput,
		&event.Reason,
		&event.RiskLevel,
		&event.ApprovalStatus,
		&event.Decision,
		&event.DecisionScope,
		&event.DecidedByUserID,
		&event.DecidedAt,
		&event.OccurredAt,
		&raw,
	); err != nil {
		return AgentAuditEvent{}, mapSQLError(err)
	}
	event.ToolInput = json.RawMessage(toolInput)
	event.Raw = json.RawMessage(raw)
	return event, nil
}

func scanAuditEvents(rows *sql.Rows) ([]AgentAuditEvent, error) {
	out := make([]AgentAuditEvent, 0)
	for rows.Next() {
		event, err := scanAuditEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *MemoryStore) UpsertAuditEvent(
	ctx context.Context,
	event AgentAuditEvent,
) (AgentAuditEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upsertAuditEventLocked(event), nil
}

func (s *MemoryStore) upsertAuditEventLocked(event AgentAuditEvent) AgentAuditEvent {
	event = normalizeAuditEvent(event, s.now().UTC())
	s.auditEvents[auditEventMapKey(event)] = event
	return event
}

func (s *MemoryStore) ListAuditEvents(
	ctx context.Context,
	filter AuditEventFilter,
) ([]AgentAuditEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AgentAuditEvent, 0)
	for _, event := range s.auditEvents {
		if !auditEventMatchesFilter(event, filter) {
			continue
		}
		out = append(out, event)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].OccurredAt.Equal(out[j].OccurredAt) {
			return out[i].OccurredAt.After(out[j].OccurredAt)
		}
		return out[i].EventID > out[j].EventID
	})
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func normalizeAuditEvent(event AgentAuditEvent, now time.Time) AgentAuditEvent {
	if event.EventID == "" {
		event.EventID, _ = newSecret("aud")
	}
	if event.EventKey == "" {
		event.EventKey = "default"
	}
	if event.SourceID == "" {
		event.SourceID = event.EventID
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = now
	}
	if len(event.ToolInput) == 0 {
		event.ToolInput = json.RawMessage(`{}`)
	}
	if len(event.Raw) == 0 {
		event.Raw = json.RawMessage(`{}`)
	}
	return event
}

func auditEventMapKey(event AgentAuditEvent) string {
	return event.SourceType + "\x00" + event.SourceID + "\x00" + event.EventType + "\x00" + event.EventKey
}

func auditEventMatchesFilter(event AgentAuditEvent, filter AuditEventFilter) bool {
	if event.OwnerUserID != filter.Principal.User.UserID {
		return false
	}
	if filter.EventType != "" && event.EventType != filter.EventType {
		return false
	}
	if filter.AgentID != "" && event.AgentID != filter.AgentID {
		return false
	}
	if filter.SessionID != "" && event.SessionID != filter.SessionID {
		return false
	}
	if filter.ApprovalID != "" && event.ApprovalID != filter.ApprovalID {
		return false
	}
	if filter.Decision != "" && event.Decision != filter.Decision {
		return false
	}
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	if query == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{
		event.Title,
		event.Summary,
		event.ToolName,
		event.Reason,
		string(event.ToolInput),
		string(event.Raw),
	}, "\n"))
	return strings.Contains(haystack, query)
}

func auditEventsForApprovalRequested(approval AgentApproval) []AgentAuditEvent {
	return []AgentAuditEvent{{
		OwnerUserID:    approval.OwnerUserID,
		NodeID:         approval.RequestNodeID,
		AgentID:        approval.RequestAgentID,
		SessionID:      approval.RequestSessionID,
		ApprovalID:     approval.ApprovalID,
		EventType:      domain.AuditEventApprovalRequested,
		SourceType:     domain.AuditSourceApproval,
		SourceID:       approval.ApprovalID,
		EventKey:       "requested",
		Title:          firstNonEmpty(approval.Title, "Approval requested"),
		Summary:        auditApprovalSummary("requested", approval),
		ToolName:       approval.Operation,
		ToolInput:      jsonDefaultRaw(approval.RequestBody, "{}"),
		Reason:         approval.Description,
		RiskLevel:      approval.RiskLevel,
		ApprovalStatus: approval.Status,
		Raw:            auditRaw(approval),
		OccurredAt:     approval.CreatedAt,
	}}
}

func auditEventsForApprovalDecided(approval AgentApproval) []AgentAuditEvent {
	occurredAt := approval.CreatedAt
	if approval.DecidedAt != nil {
		occurredAt = *approval.DecidedAt
	}
	return []AgentAuditEvent{{
		OwnerUserID:     approval.OwnerUserID,
		NodeID:          approval.RequestNodeID,
		AgentID:         approval.RequestAgentID,
		SessionID:       approval.RequestSessionID,
		ApprovalID:      approval.ApprovalID,
		EventType:       domain.AuditEventApprovalDecided,
		SourceType:      domain.AuditSourceApproval,
		SourceID:        approval.ApprovalID,
		EventKey:        "decided",
		Title:           firstNonEmpty(approval.Title, "Approval decided"),
		Summary:         auditApprovalSummary("decided", approval),
		ToolName:        approval.Operation,
		ToolInput:       jsonDefaultRaw(approval.RequestBody, "{}"),
		Reason:          approval.Description,
		RiskLevel:       approval.RiskLevel,
		ApprovalStatus:  approval.Status,
		Decision:        approval.Decision,
		DecisionScope:   approval.DecisionScope,
		DecidedByUserID: approval.DecidedByUserID,
		DecidedAt:       approval.DecidedAt,
		Raw:             auditRaw(approval),
		OccurredAt:      occurredAt,
	}}
}

func auditEventsForApprovalRevoked(approval AgentApproval) []AgentAuditEvent {
	occurredAt := approval.CreatedAt
	if approval.GrantRevokedAt != nil {
		occurredAt = *approval.GrantRevokedAt
	}
	return []AgentAuditEvent{{
		OwnerUserID:     approval.OwnerUserID,
		NodeID:          approval.RequestNodeID,
		AgentID:         approval.RequestAgentID,
		SessionID:       approval.RequestSessionID,
		ApprovalID:      approval.ApprovalID,
		EventType:       domain.AuditEventApprovalRevoked,
		SourceType:      domain.AuditSourceApproval,
		SourceID:        approval.ApprovalID,
		EventKey:        "revoked",
		Title:           firstNonEmpty(approval.Title, "Approval grant revoked"),
		Summary:         auditApprovalSummary("revoked", approval),
		ToolName:        approval.Operation,
		ToolInput:       jsonDefaultRaw(approval.RequestBody, "{}"),
		Reason:          approval.GrantRevocationReason,
		RiskLevel:       approval.RiskLevel,
		ApprovalStatus:  approval.Status,
		Decision:        approval.Decision,
		DecisionScope:   approval.DecisionScope,
		DecidedByUserID: approval.GrantRevokedByUserID,
		Raw:             auditRaw(approval),
		OccurredAt:      occurredAt,
	}}
}

func auditEventsForMailbox(msg MailboxMessage) []AgentAuditEvent {
	events := make([]AgentAuditEvent, 0)
	if msg.Status == "completed" || msg.Status == "failed" {
		events = append(events, AgentAuditEvent{
			OwnerUserID: msg.OwnerUserID,
			NodeID:      msg.NodeID,
			AgentID:     msg.AgentID,
			SessionID:   msg.SessionID,
			TurnID:      msg.TurnID,
			MessageID:   msg.MessageID,
			EventType:   domain.AuditEventMessageCompleted,
			SourceType:  domain.AuditSourceMailbox,
			SourceID:    msg.MessageID,
			EventKey:    "message_result",
			Title:       "Message completed",
			Summary:     firstNonEmpty(msg.Result, msg.Error, msg.Status),
			Raw:         auditRaw(msg),
			OccurredAt:  timeOrDefault(msg.CompletedAt, msg.CreatedAt),
		})
	}
	events = append(events, auditToolEventsFromRaw(msg)...)
	for i, change := range msg.FileChanges {
		events = append(events, AgentAuditEvent{
			OwnerUserID: msg.OwnerUserID,
			NodeID:      msg.NodeID,
			AgentID:     msg.AgentID,
			SessionID:   msg.SessionID,
			TurnID:      msg.TurnID,
			MessageID:   msg.MessageID,
			EventType:   domain.AuditEventFileChanged,
			SourceType:  domain.AuditSourceMailbox,
			SourceID:    msg.MessageID,
			EventKey:    fmt.Sprintf("file:%d:%s", i, change.Path),
			Title:       "File changed: " + change.Path,
			Summary:     firstNonEmpty(change.Tool, "file changed"),
			ToolName:    change.Tool,
			Raw:         auditRaw(change),
			OccurredAt:  timeOrDefault(msg.CompletedAt, msg.CreatedAt),
		})
	}
	return events
}

func auditToolEventsFromRaw(msg MailboxMessage) []AgentAuditEvent {
	var raws []json.RawMessage
	if !json.Valid(msg.Events) || len(msg.Events) == 0 {
		return nil
	}
	if err := json.Unmarshal(msg.Events, &raws); err != nil {
		var wrapped struct {
			Events []json.RawMessage `json:"events"`
		}
		if err := json.Unmarshal(msg.Events, &wrapped); err != nil {
			return nil
		}
		raws = wrapped.Events
	}
	out := make([]AgentAuditEvent, 0)
	for i, raw := range raws {
		fields := map[string]any{}
		if err := json.Unmarshal(raw, &fields); err != nil {
			continue
		}
		entityType := stringField(fields, "entity_type", "entityType")
		eventType := stringField(fields, "event_type", "eventType", "type")
		if entityType != "tool" {
			continue
		}
		callID := stringField(fields, "callId", "call_id", "tool_call_id")
		toolName := stringField(fields, "name", "tool", "tool_name", "title")
		turnID := firstNonEmpty(stringField(fields, "turnId", "turn_id"), msg.TurnID)
		occurredAt := timeOrDefault(msg.CompletedAt, msg.CreatedAt)
		switch eventType {
		case "call":
			input := rawToolInput(fields)
			out = append(out, AgentAuditEvent{
				OwnerUserID: msg.OwnerUserID,
				NodeID:      msg.NodeID,
				AgentID:     msg.AgentID,
				SessionID:   msg.SessionID,
				TurnID:      turnID,
				MessageID:   msg.MessageID,
				EventType:   domain.AuditEventToolCallRequested,
				SourceType:  domain.AuditSourceMailbox,
				SourceID:    msg.MessageID,
				EventKey:    fmt.Sprintf("tool_call:%d:%s", i, callID),
				Title:       "Tool call: " + firstNonEmpty(toolName, callID, "unknown"),
				Summary:     firstNonEmpty(toolName, callID, "tool call requested"),
				ToolName:    toolName,
				ToolInput:   input,
				Raw:         raw,
				OccurredAt:  occurredAt,
			})
		case "result":
			out = append(out, AgentAuditEvent{
				OwnerUserID: msg.OwnerUserID,
				NodeID:      msg.NodeID,
				AgentID:     msg.AgentID,
				SessionID:   msg.SessionID,
				TurnID:      turnID,
				MessageID:   msg.MessageID,
				EventType:   domain.AuditEventToolCallCompleted,
				SourceType:  domain.AuditSourceMailbox,
				SourceID:    msg.MessageID,
				EventKey:    fmt.Sprintf("tool_result:%d:%s", i, callID),
				Title:       "Tool completed: " + firstNonEmpty(toolName, callID, "unknown"),
				Summary: firstNonEmpty(
					stringField(fields, "error"),
					stringField(fields, "output"),
					"tool call completed",
				),
				ToolName:   toolName,
				Raw:        raw,
				OccurredAt: occurredAt,
			})
		}
	}
	return out
}

func stringField(fields map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := fields[name]; ok {
			if str, ok := value.(string); ok {
				return strings.TrimSpace(str)
			}
		}
	}
	return ""
}

func rawToolInput(fields map[string]any) json.RawMessage {
	for _, name := range []string{"arguments", "input", "raw_input", "tool_input"} {
		value, ok := fields[name]
		if !ok {
			continue
		}
		if str, ok := value.(string); ok {
			if json.Valid([]byte(str)) {
				return json.RawMessage(str)
			}
			data, _ := json.Marshal(map[string]string{"arguments": str})
			return data
		}
		data, err := json.Marshal(value)
		if err == nil && json.Valid(data) {
			return data
		}
	}
	return json.RawMessage(`{}`)
}

func auditApprovalSummary(action string, approval AgentApproval) string {
	subject := firstNonEmpty(
		approval.Operation,
		approval.ResourceType,
		approval.ResourceRef,
		"approval",
	)
	switch action {
	case "requested":
		return "Approval requested for " + subject
	case "decided":
		return firstNonEmpty(approval.Decision, "decision recorded") + " for " + subject
	case "revoked":
		return "Approval grant revoked for " + subject
	default:
		return subject
	}
}

func auditRaw(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil || !json.Valid(data) {
		return json.RawMessage(`{}`)
	}
	return data
}

func jsonDefaultRaw(raw json.RawMessage, fallback string) json.RawMessage {
	return json.RawMessage(jsonDefault(raw, fallback))
}

func timeOrDefault(value *time.Time, fallback time.Time) time.Time {
	if value != nil {
		return *value
	}
	return fallback
}

func upsertAuditEvents(ctx context.Context, store interface {
	UpsertAuditEvent(context.Context, AgentAuditEvent) (AgentAuditEvent, error)
}, events []AgentAuditEvent) error {
	for _, event := range events {
		if _, err := store.UpsertAuditEvent(ctx, event); err != nil {
			return err
		}
	}
	return nil
}
