package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type acpHistoryRPC struct {
	ID     any             `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

type acpHistoryFields struct {
	SessionID  string
	TurnID     string
	ResponseID string
	EntityType string
	EventType  string
	Role       string
	Content    string
}

func projectACPTransportMessage(
	ctx context.Context,
	store domain.Store,
	agentID string,
	stream string,
	seq int64,
	payload json.RawMessage,
) error {
	var rpc acpHistoryRPC
	_ = json.Unmarshal(payload, &rpc)
	direction := domain.MessageDirectionAgentToUser
	role := "assistant"
	localDirection := domain.TransportDirectionInbound
	if stream == domain.TransportStreamManagerToPaxd {
		direction = domain.MessageDirectionUserToAgent
		role = "user"
		localDirection = domain.TransportDirectionOutbound
	}
	fields := extractACPHistoryFields(payload, rpc)
	if fields.Role != "" {
		role = fields.Role
	}
	messageType := firstNonEmpty(
		strings.Trim(fields.EntityType+":"+fields.EventType, ":"),
		rpc.Method,
		"acp",
	)
	messageID := acpHistoryMessageID(agentID, stream, localDirection, seq, rpc, fields)
	msg := domain.Message{
		MessageID:   messageID,
		AgentID:     agentID,
		SessionID:   fields.SessionID,
		Source:      domain.MessageSourceACPTunnel,
		Direction:   direction,
		Role:        role,
		Status:      "received",
		MessageType: messageType,
		TurnID:      fields.TurnID,
		ResponseID:  fields.ResponseID,
		LogicalKey:  messageID,
		RawJSON:     append(json.RawMessage(nil), payload...),
	}
	if err := store.UpsertMessage(ctx, &msg); err != nil {
		return err
	}
	if isACPTextDelta(fields) {
		return store.AppendMessagePartText(ctx, msg.MessageID, 0, fields.Content, payload)
	}
	partType := domain.MessagePartRawJSON
	text := ""
	if fields.Content != "" {
		partType = domain.MessagePartText
		text = fields.Content
	}
	return store.UpsertMessagePart(ctx, &domain.MessagePart{
		MessageID:   msg.MessageID,
		PartIndex:   0,
		PartType:    partType,
		Text:        text,
		PayloadJSON: append(json.RawMessage(nil), payload...),
	})
}

func acpHistoryMessageID(
	agentID string,
	stream string,
	localDirection string,
	seq int64,
	rpc acpHistoryRPC,
	fields acpHistoryFields,
) string {
	if isACPTextDelta(fields) && fields.TurnID != "" {
		return fmt.Sprintf(
			"acp:%s:%s:%s:%s:%s",
			agentID,
			stream,
			firstNonEmpty(fields.SessionID, "_"),
			fields.TurnID,
			firstNonEmpty(fields.Role, "_"),
		)
	}
	if rpc.ID != nil {
		return fmt.Sprintf("acp:%s:%s:rpc:%v", agentID, localDirection, rpc.ID)
	}
	return fmt.Sprintf("acp:%s:%s:%d", agentID, localDirection, seq)
}

func extractACPHistoryFields(payload json.RawMessage, rpc acpHistoryRPC) acpHistoryFields {
	fields := fieldsFromRaw(payload)
	for _, raw := range []json.RawMessage{rpc.Params, rpc.Result} {
		nested := fieldsFromRaw(raw)
		fields.SessionID = firstNonEmpty(fields.SessionID, nested.SessionID)
		fields.TurnID = firstNonEmpty(fields.TurnID, nested.TurnID)
		fields.ResponseID = firstNonEmpty(fields.ResponseID, nested.ResponseID)
		fields.EntityType = firstNonEmpty(fields.EntityType, nested.EntityType)
		fields.EventType = firstNonEmpty(fields.EventType, nested.EventType)
		fields.Role = firstNonEmpty(fields.Role, nested.Role)
		fields.Content = firstNonEmpty(fields.Content, nested.Content)
	}
	return fields
}

func fieldsFromRaw(raw json.RawMessage) acpHistoryFields {
	if len(raw) == 0 {
		return acpHistoryFields{}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return acpHistoryFields{}
	}
	obj, _ := v.(map[string]any)
	return acpHistoryFields{
		SessionID:  findString(obj, "sessionId", "session_id"),
		TurnID:     findString(obj, "turnId", "turn_id"),
		ResponseID: findString(obj, "responseId", "response_id"),
		EntityType: findString(obj, "entityType", "entity_type"),
		EventType:  findString(obj, "eventType", "event_type"),
		Role:       findString(obj, "role"),
		Content:    findString(obj, "content", "text", "delta"),
	}
}

func findString(v any, keys ...string) string {
	switch typed := v.(type) {
	case map[string]any:
		for _, key := range keys {
			if val, ok := typed[key]; ok {
				if str, ok := val.(string); ok {
					return str
				}
			}
		}
		for _, val := range typed {
			if str := findString(val, keys...); str != "" {
				return str
			}
		}
	case []any:
		for _, val := range typed {
			if str := findString(val, keys...); str != "" {
				return str
			}
		}
	}
	return ""
}

func isACPTextDelta(fields acpHistoryFields) bool {
	return fields.Content != "" &&
		(strings.EqualFold(fields.EntityType, "message") &&
			strings.EqualFold(fields.EventType, "delta"))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
