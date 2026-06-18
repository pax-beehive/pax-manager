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
	SessionID     string
	TurnID        string
	ResponseID    string
	EntityType    string
	EventType     string
	SessionUpdate string
	Role          string
	Content       string
}

func projectACPTransportMessage(
	ctx context.Context,
	store domain.Store,
	agentID string,
	ownerUserID string,
	nodeID string,
	stream string,
	seq int64,
	payload json.RawMessage,
) error {
	if stream != domain.TransportStreamPaxdToManager {
		return nil
	}
	var rpc acpHistoryRPC
	_ = json.Unmarshal(payload, &rpc)
	direction := domain.MessageDirectionAgentToUser
	role := "assistant"
	fields := extractACPHistoryFields(payload, rpc)
	fields, ok := normalizeACPTextUpdate(rpc, fields)
	if !ok {
		return nil
	}
	if fields.Role != "" {
		role = fields.Role
	}
	messageType := firstNonEmpty(
		fields.SessionUpdate,
		strings.Trim(fields.EntityType+":"+fields.EventType, ":"),
		rpc.Method,
		"acp",
	)
	messageID := acpHistoryMessageID(agentID, stream, seq, fields)
	msg := domain.Message{
		MessageID:   messageID,
		OwnerUserID: ownerUserID,
		NodeID:      nodeID,
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
	return store.AppendMessagePartText(ctx, msg.MessageID, 0, fields.Content, payload)
}

func acpHistoryMessageID(
	agentID string,
	stream string,
	seq int64,
	fields acpHistoryFields,
) string {
	if fields.SessionID != "" && fields.TurnID != "" {
		return fmt.Sprintf(
			"acp:%s:%s:%s:%s:%s:%s",
			agentID,
			stream,
			firstNonEmpty(fields.SessionID, "_"),
			fields.TurnID,
			firstNonEmpty(fields.SessionUpdate, "_"),
			firstNonEmpty(fields.Role, "_"),
		)
	}
	if fields.SessionID != "" {
		return fmt.Sprintf(
			"acp:%s:%s:%s:%s:%s",
			agentID,
			stream,
			fields.SessionID,
			firstNonEmpty(fields.SessionUpdate, "_"),
			firstNonEmpty(fields.Role, "_"),
		)
	}
	return fmt.Sprintf("acp:%s:%s:text:%d", agentID, stream, seq)
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
		fields.SessionUpdate = firstNonEmpty(fields.SessionUpdate, nested.SessionUpdate)
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
		SessionID:     findString(obj, "sessionId", "session_id"),
		TurnID:        findString(obj, "turnId", "turn_id"),
		ResponseID:    findString(obj, "responseId", "response_id"),
		EntityType:    findString(obj, "entityType", "entity_type"),
		EventType:     findString(obj, "eventType", "event_type"),
		SessionUpdate: findString(obj, "sessionUpdate", "session_update"),
		Role:          findString(obj, "role"),
		Content:       findString(obj, "content", "text", "delta"),
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

func normalizeACPTextUpdate(rpc acpHistoryRPC, fields acpHistoryFields) (acpHistoryFields, bool) {
	if fields.Content == "" {
		return fields, false
	}
	if strings.EqualFold(fields.EntityType, "message") &&
		strings.EqualFold(fields.EventType, "delta") {
		if fields.SessionUpdate == "" {
			fields.SessionUpdate = "message_delta"
		}
		return fields, true
	}
	return fields, rpc.Method == "session/update" && fields.SessionUpdate != ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
