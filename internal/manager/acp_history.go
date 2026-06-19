package manager

import (
	"context"
	"crypto/sha256"
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

type acpHistoryProjectionKind string

const (
	acpHistoryProjectionNone acpHistoryProjectionKind = ""
	acpHistoryProjectionText acpHistoryProjectionKind = "text"
	acpHistoryProjectionRaw  acpHistoryProjectionKind = "raw"
)

func projectACPTransportMessage(
	ctx context.Context,
	store domain.Store,
	agentID string,
	ownerUserID string,
	nodeID string,
	stream string,
	seq int64,
	historyGroupID string,
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
	fields, projection := classifyACPHistoryProjection(rpc, fields)
	if projection == acpHistoryProjectionNone {
		return nil
	}
	textProjection := projection == acpHistoryProjectionText
	if !textProjection {
		historyGroupID = ""
	}
	fields.SessionID = canonicalACPHistorySessionID(
		ctx,
		store,
		ownerUserID,
		agentID,
		fields.SessionID,
	)
	if fields.Role != "" {
		role = fields.Role
	}
	messageType := firstNonEmpty(
		fields.SessionUpdate,
		strings.Trim(fields.EntityType+":"+fields.EventType, ":"),
		rpc.Method,
		"acp",
	)
	logicalKey := acpHistoryLogicalKey(agentID, stream, seq, historyGroupID, fields)
	if !textProjection {
		logicalKey = acpHistoryRawLogicalKey(agentID, stream, seq, fields)
	}
	messageID := acpHistoryMessageID(logicalKey)
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
		LogicalKey:  logicalKey,
	}
	if !textProjection {
		msg.RawJSON = append(json.RawMessage(nil), payload...)
	}
	if err := store.UpsertMessage(ctx, &msg); err != nil {
		return err
	}
	if textProjection {
		return store.AppendMessagePartText(ctx, msg.MessageID, 0, fields.Content, nil)
	}
	return store.UpsertMessagePart(ctx, &domain.MessagePart{
		MessageID:   msg.MessageID,
		PartIndex:   0,
		PartType:    domain.MessagePartRawJSON,
		PayloadJSON: append(json.RawMessage(nil), payload...),
	})
}

func canonicalACPHistorySessionID(
	ctx context.Context,
	store domain.Store,
	ownerUserID string,
	agentID string,
	sessionID string,
) string {
	if sessionID == "" {
		return ""
	}
	sessions, err := store.ListAgentSessions(
		ctx,
		domain.UserPrincipal{User: domain.User{UserID: ownerUserID}},
		agentID,
	)
	if err != nil {
		return sessionID
	}
	for _, session := range sessions {
		if session.SessionID == sessionID || session.NativeID == sessionID {
			return session.SessionID
		}
	}
	return sessionID
}

func acpHistoryLogicalKey(
	agentID string,
	stream string,
	seq int64,
	historyGroupID string,
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
	if fields.SessionID != "" && historyGroupID != "" {
		return fmt.Sprintf(
			"acp:%s:%s:%s:%s:%s:%s",
			agentID,
			stream,
			fields.SessionID,
			historyGroupID,
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

func acpHistoryRawLogicalKey(
	agentID string,
	stream string,
	seq int64,
	fields acpHistoryFields,
) string {
	return fmt.Sprintf(
		"acp:%s:%s:%s:%s:raw:%d",
		agentID,
		stream,
		firstNonEmpty(fields.SessionID, "_"),
		firstNonEmpty(fields.SessionUpdate, strings.Trim(fields.EntityType+":"+fields.EventType, ":"), "_"),
		seq,
	)
}

func acpHistoryMessageID(logicalKey string) string {
	sum := sha256.Sum256([]byte(logicalKey))
	return fmt.Sprintf("msg_%x", sum[:24])
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
				if str := findString(val, keys...); str != "" {
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
	if rpc.Method != "session/update" {
		return fields, false
	}
	switch fields.SessionUpdate {
	case "agent_message_chunk", "agent_thought_chunk", "message_delta":
		return fields, true
	default:
		return fields, false
	}
}

func classifyACPHistoryProjection(
	rpc acpHistoryRPC,
	fields acpHistoryFields,
) (acpHistoryFields, acpHistoryProjectionKind) {
	fields, ok := normalizeACPTextUpdate(rpc, fields)
	if ok {
		return fields, acpHistoryProjectionText
	}
	if rpc.Method == "session/update" && fields.SessionUpdate != "" {
		return fields, acpHistoryProjectionRaw
	}
	if fields.EntityType != "" || fields.EventType != "" {
		return fields, acpHistoryProjectionRaw
	}
	return fields, acpHistoryProjectionNone
}
