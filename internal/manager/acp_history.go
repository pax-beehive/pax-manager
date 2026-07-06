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
	Error  json.RawMessage `json:"error,omitempty"`
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
	if err := store.UpsertMessagePart(ctx, &domain.MessagePart{
		MessageID:   msg.MessageID,
		PartIndex:   0,
		PartType:    domain.MessagePartRawJSON,
		PayloadJSON: append(json.RawMessage(nil), payload...),
	}); err != nil {
		return err
	}
	return projectACPPaxInvocationPendingDisplay(ctx, store, msg)
}

func projectACPUserPrompt(
	ctx context.Context,
	agent *ACPTunnelAgent,
	payload []byte,
) error {
	if agent == nil || agent.store == nil {
		return nil
	}
	var rpc acpHistoryRPC
	if err := json.Unmarshal(payload, &rpc); err != nil {
		return nil
	}
	if rpc.Method != "session/prompt" {
		return projectACPUserRawFrame(ctx, agent, payload, rpc)
	}
	sessionID := firstNonEmpty(
		findStringFromRaw(rpc.Params, "sessionId", "session_id"),
		agent.sessionID,
	)
	sessionID = canonicalACPHistorySessionID(
		ctx,
		agent.store,
		agent.ownerUserID,
		agent.agentID,
		sessionID,
	)
	content := acpPromptText(rpc.Params)
	if sessionID == "" || content == "" {
		return nil
	}
	paxMeta, hasPaxMeta := paxInvocationPromptMetadataFromRaw(rpc.Params)
	logicalKey := fmt.Sprintf(
		"acp:%s:%s:%s:%s:user_prompt",
		agent.agentID,
		domain.TransportStreamManagerToPaxd,
		sessionID,
		firstNonEmpty(acpHistoryRPCID(rpc.ID), acpHistoryContentHash(content)),
	)
	msg := domain.Message{
		MessageID:   acpHistoryMessageID(logicalKey),
		OwnerUserID: agent.ownerUserID,
		NodeID:      agent.nodeID,
		AgentID:     agent.agentID,
		SessionID:   sessionID,
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionUserToAgent,
		Role:        "user",
		Status:      "sent",
		MessageType: domain.MessageTypeUser,
		LogicalKey:  logicalKey,
		RawJSON:     append(json.RawMessage(nil), payload...),
	}
	if hasPaxMeta {
		msg.MessageType = domain.MessageTypePaxUser
	}
	if err := agent.store.UpsertMessage(ctx, &msg); err != nil {
		return err
	}
	if err := agent.store.UpsertMessagePart(ctx, &domain.MessagePart{
		MessageID:   msg.MessageID,
		PartIndex:   0,
		PartType:    domain.MessagePartText,
		Text:        content,
		PayloadJSON: append(json.RawMessage(nil), payload...),
	}); err != nil {
		return err
	}
	if hasPaxMeta {
		return projectACPPaxInvocationDisplay(ctx, agent, msg, paxMeta)
	}
	return nil
}

func projectACPPaxInvocationDisplay(
	ctx context.Context,
	agent *ACPTunnelAgent,
	parent domain.Message,
	meta paxInvocationPromptMetadata,
) error {
	logicalKey := "acp:" + agent.agentID + ":" + domain.TransportStreamManagerToPaxd + ":" +
		parent.SessionID + ":" + meta.InvocationID + ":" + meta.Phase + ":" + meta.Side + ":display"
	msg := domain.Message{
		MessageID:       acpHistoryMessageID(logicalKey),
		ConversationID:  parent.ConversationID,
		OwnerUserID:     parent.OwnerUserID,
		NodeID:          parent.NodeID,
		AgentID:         parent.AgentID,
		SessionID:       parent.SessionID,
		Source:          parent.Source,
		Direction:       parent.Direction,
		Role:            parent.Role,
		Status:          parent.Status,
		MessageType:     domain.MessageTypePaxInvocation,
		ParentMessageID: parent.MessageID,
		LogicalKey:      logicalKey,
	}
	msg.RawJSON, _ = paxInvocationDisplayRawForPrompt(parent.MessageID, meta)
	if err := agent.store.UpsertMessage(ctx, &msg); err != nil {
		return err
	}
	_, text := paxInvocationDisplayRawForPrompt(parent.MessageID, meta)
	return agent.store.UpsertMessagePart(ctx, &domain.MessagePart{
		MessageID:   msg.MessageID,
		PartIndex:   0,
		PartType:    domain.MessagePartText,
		Text:        text,
		PayloadJSON: append(json.RawMessage(nil), msg.RawJSON...),
	})
}

func projectACPPaxInvocationPendingDisplay(
	ctx context.Context,
	store domain.Store,
	terminal domain.Message,
) error {
	if terminal.MessageType != "tool_call_update" || !acpHistoryTerminalToolCallUpdate(terminal.RawJSON) {
		return nil
	}
	toolCallID := acpHistoryToolCallIDFromRaw(terminal.RawJSON)
	if toolCallID == "" {
		return nil
	}
	messages, err := store.ListMessages(ctx, terminal.AgentID, terminal.SessionID, 1000)
	if err != nil {
		return err
	}
	pending, pendingRaw, ok := acpHistoryPendingInvocationForToolCall(messages, toolCallID)
	if !ok {
		return nil
	}
	replaces := acpHistoryToolCallReplaceMessageIDs(messages, toolCallID, pendingRaw.PromptMessageID, pending.MessageID)
	if len(replaces) == 0 {
		return nil
	}
	raw, text := paxInvocationDisplayRawForPending(replaces, pendingRaw)
	logicalKey := "agent_conversation:" + pendingRaw.InvocationID + ":" + pendingRaw.Phase + ":" + pendingRaw.Side + ":display"
	msg := domain.Message{
		MessageID:       acpHistoryMessageID(logicalKey),
		ConversationID:  terminal.ConversationID,
		OwnerUserID:     terminal.OwnerUserID,
		NodeID:          terminal.NodeID,
		AgentID:         terminal.AgentID,
		SessionID:       terminal.SessionID,
		Source:          terminal.Source,
		Direction:       terminal.Direction,
		Role:            terminal.Role,
		Status:          terminal.Status,
		MessageType:     domain.MessageTypePaxInvocation,
		ParentMessageID: terminal.MessageID,
		LogicalKey:      logicalKey,
		RawJSON:         raw,
	}
	if pending.ConversationID != "" {
		msg.ConversationID = pending.ConversationID
	}
	if msg.ConversationID == "" {
		msg.ConversationID = terminal.ConversationID
	}
	if err := store.UpsertMessage(ctx, &msg); err != nil {
		return err
	}
	return store.UpsertMessagePart(ctx, &domain.MessagePart{
		MessageID:   msg.MessageID,
		PartIndex:   0,
		PartType:    domain.MessagePartText,
		Text:        text,
		PayloadJSON: append(json.RawMessage(nil), raw...),
	})
}

func acpHistoryPendingInvocationForToolCall(
	messages []domain.Message,
	toolCallID string,
) (domain.Message, paxInvocationPendingRaw, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.MessageType != domain.MessageTypePaxInvocationPending {
			continue
		}
		var raw paxInvocationPendingRaw
		if len(message.RawJSON) == 0 || json.Unmarshal(message.RawJSON, &raw) != nil {
			continue
		}
		if strings.TrimSpace(raw.ToolCallID) != toolCallID || strings.TrimSpace(raw.InvocationID) == "" {
			continue
		}
		if raw.InvocationType == "" {
			raw.InvocationType = "agent_conversation"
		}
		return message, raw, true
	}
	return domain.Message{}, paxInvocationPendingRaw{}, false
}

func acpHistoryToolCallReplaceMessageIDs(
	messages []domain.Message,
	toolCallID string,
	promptMessageID string,
	pendingMessageID string,
) []string {
	replaces := make([]string, 0, 4)
	for _, message := range messages {
		if message.MessageType != "tool_call" && message.MessageType != "tool_call_update" {
			continue
		}
		if acpHistoryToolCallIDFromRaw(message.RawJSON) == toolCallID {
			replaces = appendUniqueString(replaces, message.MessageID)
		}
	}
	replaces = appendUniqueString(replaces, strings.TrimSpace(promptMessageID))
	return appendUniqueString(replaces, strings.TrimSpace(pendingMessageID))
}

func acpHistoryTerminalToolCallUpdate(raw json.RawMessage) bool {
	status := strings.ToLower(acpHistoryToolCallUpdateStatus(raw))
	return status == "completed" || status == "failed" || status == "canceled" || status == "cancelled"
}

func acpHistoryToolCallUpdateStatus(raw json.RawMessage) string {
	var rpc struct {
		Params struct {
			Update map[string]any `json:"update"`
		} `json:"params"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &rpc) != nil || rpc.Params.Update == nil {
		return ""
	}
	return stringMapField(rpc.Params.Update, "status")
}

func acpHistoryToolCallIDFromRaw(raw json.RawMessage) string {
	var rpc struct {
		Params struct {
			Update map[string]any `json:"update"`
		} `json:"params"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &rpc) != nil || rpc.Params.Update == nil {
		return ""
	}
	return firstNonEmpty(
		stringMapField(rpc.Params.Update, "toolCallId"),
		stringMapField(rpc.Params.Update, "tool_call_id"),
	)
}

func stringMapField(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func appendUniqueString(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func projectACPUserRawFrame(
	ctx context.Context,
	agent *ACPTunnelAgent,
	payload []byte,
	rpc acpHistoryRPC,
) error {
	if rpc.Method == "" && len(rpc.Result) == 0 && len(rpc.Error) == 0 {
		return nil
	}
	sessionID := canonicalACPHistorySessionID(
		ctx,
		agent.store,
		agent.ownerUserID,
		agent.agentID,
		agent.sessionID,
	)
	if sessionID == "" {
		return nil
	}
	messageType := acpUserRawFrameMessageType(rpc)
	logicalKey := fmt.Sprintf(
		"acp:%s:%s:%s:%s:%s",
		agent.agentID,
		domain.TransportStreamManagerToPaxd,
		sessionID,
		firstNonEmpty(messageType, "acp_response"),
		firstNonEmpty(acpHistoryRPCID(rpc.ID), acpHistoryContentHash(string(payload))),
	)
	msg := domain.Message{
		MessageID:   acpHistoryMessageID(logicalKey),
		OwnerUserID: agent.ownerUserID,
		NodeID:      agent.nodeID,
		AgentID:     agent.agentID,
		SessionID:   sessionID,
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionUserToAgent,
		Role:        "user",
		Status:      "sent",
		MessageType: messageType,
		LogicalKey:  logicalKey,
		RawJSON:     append(json.RawMessage(nil), payload...),
	}
	if err := agent.store.UpsertMessage(ctx, &msg); err != nil {
		return err
	}
	return agent.store.UpsertMessagePart(ctx, &domain.MessagePart{
		MessageID:   msg.MessageID,
		PartIndex:   0,
		PartType:    domain.MessagePartRawJSON,
		PayloadJSON: append(json.RawMessage(nil), payload...),
	})
}

func acpUserRawFrameMessageType(rpc acpHistoryRPC) string {
	if rpc.Method != "" {
		switch rpc.Method {
		case "initialize":
			return "acp_initialize"
		case "session/new":
			return "acp_session_new"
		default:
			return "acp_request"
		}
	}
	var result struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	if len(rpc.Result) > 0 &&
		json.Unmarshal(rpc.Result, &result) == nil &&
		result.Outcome.Outcome != "" {
		return "permission_response"
	}
	if len(rpc.Error) > 0 {
		return "acp_error_response"
	}
	return "acp_response"
}

func acpPromptText(raw json.RawMessage) string {
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return ""
	}
	prompt, _ := params["prompt"].([]any)
	parts := make([]string, 0, len(prompt))
	for _, item := range prompt {
		if text := findString(item, "text", "content"); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func acpHistoryRPCID(id any) string {
	if id == nil {
		return ""
	}
	return fmt.Sprint(id)
}

func acpHistoryContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", sum[:8])
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
		firstNonEmpty(
			fields.SessionUpdate,
			strings.Trim(fields.EntityType+":"+fields.EventType, ":"),
			"_",
		),
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
	if rpc.Method == "session/request_permission" {
		return fields, acpHistoryProjectionRaw
	}
	if fields.EntityType != "" || fields.EventType != "" {
		return fields, acpHistoryProjectionRaw
	}
	return fields, acpHistoryProjectionNone
}
