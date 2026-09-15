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
	ToolCallID    string
	TerminalID    string
	EntityType    string
	EventType     string
	SessionUpdate string
	StopReason    string
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
	return projectACPTransportMessageForSession(
		ctx,
		store,
		agentID,
		ownerUserID,
		nodeID,
		stream,
		seq,
		historyGroupID,
		"",
		payload,
	)
}

func projectACPTransportMessageForSession(
	ctx context.Context,
	store domain.Store,
	agentID string,
	ownerUserID string,
	nodeID string,
	stream string,
	seq int64,
	historyGroupID string,
	fallbackSessionID string,
	payload json.RawMessage,
) error {
	return projectACPTransportMessageWithTextSink(
		ctx,
		store,
		immediateACPHistoryTextSink{store: store},
		agentID,
		ownerUserID,
		nodeID,
		stream,
		seq,
		historyGroupID,
		fallbackSessionID,
		payload,
	)
}

func projectACPTransportMessageWithTextSink(
	ctx context.Context,
	store domain.Store,
	textSink acpHistoryTextSink,
	agentID string,
	ownerUserID string,
	nodeID string,
	stream string,
	seq int64,
	historyGroupID string,
	fallbackSessionID string,
	payload json.RawMessage,
) error {
	return projectACPTransportMessageWithTextSinkForTurn(
		ctx,
		store,
		textSink,
		agentID,
		ownerUserID,
		nodeID,
		stream,
		seq,
		historyGroupID,
		fallbackSessionID,
		"",
		payload,
	)
}

//nolint:gocyclo // Transcript projection intentionally keeps message classification in one path.
func projectACPTransportMessageWithTextSinkForTurn(
	ctx context.Context,
	store domain.Store,
	textSink acpHistoryTextSink,
	agentID string,
	ownerUserID string,
	nodeID string,
	stream string,
	seq int64,
	historyGroupID string,
	fallbackSessionID string,
	businessTurnID string,
	payload json.RawMessage,
) error {
	if stream != domain.TransportStreamPaxdToManager {
		return nil
	}
	if textSink == nil {
		textSink = immediateACPHistoryTextSink{store: store}
	}
	var rpc acpHistoryRPC
	_ = json.Unmarshal(payload, &rpc)
	direction := domain.MessageDirectionAgentToUser
	role := "assistant"
	fields := extractACPHistoryFields(payload, rpc)
	fields.TurnID = firstNonEmpty(businessTurnID, fields.TurnID)
	fields, projection := classifyACPHistoryProjection(rpc, fields)
	if projection == acpHistoryProjectionNone {
		if len(rpc.Result) > 0 || len(rpc.Error) > 0 {
			return textSink.Flush(ctx)
		}
		return nil
	}
	textProjection := projection == acpHistoryProjectionText
	if !textProjection {
		if err := textSink.Flush(ctx); err != nil {
			return err
		}
		historyGroupID = ""
	}
	if fields.SessionID == "" {
		fields.SessionID = fallbackSessionID
	}
	if fields.SessionID == "" && strings.EqualFold(fields.StopReason, "end_turn") {
		return nil
	}
	fields.SessionID = canonicalACPHistorySessionIDCached(
		ctx,
		store,
		textSink,
		ownerUserID,
		agentID,
		fields.SessionID,
	)
	if fields.Role != "" {
		role = fields.Role
	}
	messageType := firstNonEmpty(
		fields.SessionUpdate,
		fields.StopReason,
		strings.Trim(fields.EntityType+":"+fields.EventType, ":"),
		rpc.Method,
		"acp",
	)
	logicalKey := acpHistoryLogicalKey(agentID, stream, seq, historyGroupID, fields)
	mergeableToolCall := false
	if !textProjection {
		if acpHistoryIsMergeableToolCall(fields) {
			logicalKey = acpHistoryToolCallLogicalKey(
				agentID,
				stream,
				fields.SessionID,
				fields.ToolCallID,
			)
			// Canonicalise so the collapsed row keeps a stable message_type and
			// never trips the client's terminal-output aggregation, which keys
			// off "tool_call_update" message rows that carry text parts.
			messageType = "tool_call"
			mergeableToolCall = true
		} else {
			logicalKey = acpHistoryRawLogicalKey(agentID, stream, seq, fields)
		}
	}
	// Worker tool/terminal IDs may be reused after a failed execution.
	// Tagged turns must never merge those rows into another execution's history.
	if businessTurnID != "" && fields.ToolCallID != "" {
		logicalKey += ":turn:" + businessTurnID
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
	rawPayload := cloneRawJSON(payload)
	if mergeableToolCall {
		rawPayload = mergeACPToolCallFrames(
			existingACPToolCallRawJSON(ctx, store, messageID),
			payload,
		)
	}
	if !textProjection {
		msg.RawJSON = cloneRawJSON(rawPayload)
	}
	if textProjection {
		terminalOutput := fields.ToolCallID != "" && fields.TerminalID != ""
		if terminalOutput {
			msg.RawJSON = append(json.RawMessage(nil), payload...)
		}
		if err := textSink.EnsureMessage(ctx, &msg); err != nil {
			return err
		}
		if terminalOutput {
			// Terminal output is an unbounded stream; roll it across bounded
			// part_index chunks instead of appending forever to part 0.
			return textSink.AppendTerminalText(ctx, msg.MessageID, fields.Content)
		}
		return textSink.AppendText(ctx, msg.MessageID, 0, fields.Content)
	}
	if err := store.UpsertMessage(ctx, &msg); err != nil {
		return err
	}
	if err := store.UpsertMessagePart(ctx, &domain.MessagePart{
		MessageID:   msg.MessageID,
		PartIndex:   0,
		PartType:    domain.MessagePartRawJSON,
		PayloadJSON: cloneRawJSON(rawPayload),
	}); err != nil {
		return err
	}
	if err := projectACPPaxInvocationPendingDisplay(ctx, store, msg); err != nil {
		return err
	}
	return reconcileArtifactPublicationDisplayForTerminal(ctx, store, msg)
}

func projectACPUserPrompt(
	ctx context.Context,
	agent *ACPTunnelAgent,
	payload []byte,
) error {
	managerSessionID := ""
	if agent != nil {
		managerSessionID = agent.currentSessionID()
	}
	return projectACPUserPromptForSession(ctx, agent, managerSessionID, payload)
}

func projectACPUserPromptForSession(
	ctx context.Context,
	agent *ACPTunnelAgent,
	managerSessionID string,
	payload []byte,
) error {
	return projectACPUserPromptForSessionTurn(ctx, agent, managerSessionID, "", payload)
}

func projectACPUserPromptForSessionTurn(
	ctx context.Context,
	agent *ACPTunnelAgent,
	managerSessionID string,
	turnID string,
	payload []byte,
) error {
	if agent == nil || agent.store == nil {
		return nil
	}
	var rpc acpHistoryRPC
	if err := json.Unmarshal(payload, &rpc); err != nil {
		return nil
	}
	if rpc.Method == "" && len(rpc.Result) == 0 && len(rpc.Error) == 0 {
		return nil
	}
	if err := agent.flushHistoryTextBoundary(ctx); err != nil {
		return err
	}
	if rpc.Method != "session/prompt" {
		return projectACPUserRawFrameForSession(
			ctx,
			agent,
			managerSessionID,
			turnID,
			payload,
			rpc,
		)
	}
	sessionID := firstNonEmpty(
		managerSessionID,
		findStringFromRaw(rpc.Params, "sessionId", "session_id"),
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
	projectionID := firstNonEmpty(turnID, acpHistoryRPCID(rpc.ID), acpHistoryContentHash(content))
	if hasPaxMeta && strings.TrimSpace(paxMeta.TurnID) != "" {
		projectionID = paxMeta.TurnID
	}
	logicalKey := fmt.Sprintf(
		"acp:%s:%s:%s:%s:user_prompt",
		agent.agentID,
		domain.TransportStreamManagerToPaxd,
		sessionID,
		projectionID,
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
		TurnID:      turnID,
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
	if strings.TrimSpace(meta.TurnID) != "" {
		logicalKey += ":" + meta.TurnID
	}
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
		TurnID:          parent.TurnID,
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
	// tool_call and tool_call_update frames collapse into a single toolCallId row
	// (message_type "tool_call") whose merged raw carries the latest status, so
	// the terminal-status check on the raw — not the message_type — is what gates
	// the display replacement.
	if terminal.MessageType != "tool_call" && terminal.MessageType != "tool_call_update" {
		return nil
	}
	if !acpHistoryTerminalToolCallUpdate(terminal.RawJSON) {
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
	replaces := acpHistoryToolCallReplaceMessageIDs(
		messages,
		toolCallID,
		pendingRaw.PromptMessageID,
		pending.MessageID,
	)
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
		TurnID:          firstNonEmpty(pending.TurnID, terminal.TurnID),
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
		if strings.TrimSpace(raw.ToolCallID) != toolCallID ||
			strings.TrimSpace(raw.InvocationID) == "" {
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
	return status == "completed" || status == "failed" || status == "canceled" ||
		status == "cancelled"
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

func projectACPUserRawFrameForSession(
	ctx context.Context,
	agent *ACPTunnelAgent,
	managerSessionID string,
	turnID string,
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
		managerSessionID,
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
		TurnID:      turnID,
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

// canonicalACPHistorySessionIDCached resolves the session ID via the agent's
// alias cache when the text sink is agent-backed (the live pipeline), avoiding a
// per-frame ListAgentSessions read. Non-agent sinks (immediate projection used
// by tests and repair paths) fall back to the direct store lookup.
func canonicalACPHistorySessionIDCached(
	ctx context.Context,
	store domain.Store,
	textSink acpHistoryTextSink,
	ownerUserID string,
	agentID string,
	sessionID string,
) string {
	if sessionID == "" {
		return ""
	}
	if sink, ok := textSink.(acpAgentHistoryTextSink); ok && sink.agent != nil {
		return sink.agent.canonicalSessionID(ctx, store, sessionID)
	}
	return canonicalACPHistorySessionID(ctx, store, ownerUserID, agentID, sessionID)
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
	if fields.SessionID != "" && fields.ToolCallID != "" && fields.TerminalID != "" {
		return fmt.Sprintf(
			"acp:%s:%s:%s:%s:%s:%s",
			agentID,
			stream,
			fields.SessionID,
			fields.ToolCallID,
			fields.TerminalID,
			firstNonEmpty(fields.SessionUpdate, "terminal_output_delta"),
		)
	}
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

// acpHistoryIsMergeableToolCall reports whether a raw-projected frame is a
// non-terminal tool_call / tool_call_update that should collapse into a single
// history row keyed by toolCallId. Terminal output deltas are excluded: they
// carry their own terminal-scoped text aggregation and never mutate the tool
// call envelope.
// existingACPToolCallRawJSON returns the accumulated tool_call frame already
// stored for messageID (part 0), or nil when this is the first frame for the
// toolCallId. It is the merge base for the next partial update.
func existingACPToolCallRawJSON(
	ctx context.Context,
	store domain.Store,
	messageID string,
) json.RawMessage {
	parts, err := store.ListMessageParts(ctx, messageID)
	if err != nil {
		return nil
	}
	for _, part := range parts {
		if part.PartIndex == 0 {
			return part.PayloadJSON
		}
	}
	return nil
}

func acpHistoryIsMergeableToolCall(fields acpHistoryFields) bool {
	if fields.ToolCallID == "" || fields.TerminalID != "" {
		return false
	}
	switch fields.SessionUpdate {
	case "tool_call", "tool_call_update":
		return true
	default:
		return false
	}
}

// acpHistoryToolCallLogicalKey groups every tool_call / tool_call_update frame
// for one toolCallId onto the same message row so partial updates field-merge
// into a single record instead of exploding into one raw row per transport seq.
func acpHistoryToolCallLogicalKey(
	agentID string,
	stream string,
	sessionID string,
	toolCallID string,
) string {
	return fmt.Sprintf(
		"acp:%s:%s:%s:tool:%s",
		agentID,
		stream,
		firstNonEmpty(sessionID, "_"),
		toolCallID,
	)
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
		fields.ToolCallID = firstNonEmpty(fields.ToolCallID, nested.ToolCallID)
		fields.TerminalID = firstNonEmpty(fields.TerminalID, nested.TerminalID)
		fields.EntityType = firstNonEmpty(fields.EntityType, nested.EntityType)
		fields.EventType = firstNonEmpty(fields.EventType, nested.EventType)
		fields.SessionUpdate = firstNonEmpty(fields.SessionUpdate, nested.SessionUpdate)
		fields.StopReason = firstNonEmpty(fields.StopReason, nested.StopReason)
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
	terminalID, terminalData := terminalOutputDeltaFromValue(obj)
	return acpHistoryFields{
		SessionID:     findString(obj, "sessionId", "session_id"),
		TurnID:        findString(obj, "turnId", "turn_id"),
		ResponseID:    findString(obj, "responseId", "response_id"),
		ToolCallID:    findString(obj, "toolCallId", "tool_call_id"),
		TerminalID:    terminalID,
		EntityType:    findString(obj, "entityType", "entity_type"),
		EventType:     findString(obj, "eventType", "event_type"),
		SessionUpdate: findString(obj, "sessionUpdate", "session_update"),
		StopReason:    findString(obj, "stopReason", "stop_reason"),
		Role:          findString(obj, "role"),
		Content:       firstNonEmpty(terminalData, findString(obj, "content", "text", "delta")),
	}
}

func terminalOutputDeltaFromValue(v any) (string, string) {
	switch typed := v.(type) {
	case map[string]any:
		if delta, ok := typed["terminal_output_delta"].(map[string]any); ok {
			data, _ := delta["data"].(string)
			return stringMapField(delta, "terminal_id"), data
		}
		for _, value := range typed {
			if terminalID, data := terminalOutputDeltaFromValue(value); terminalID != "" || data != "" {
				return terminalID, data
			}
		}
	case []any:
		for _, value := range typed {
			if terminalID, data := terminalOutputDeltaFromValue(value); terminalID != "" || data != "" {
				return terminalID, data
			}
		}
	}
	return "", ""
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
	if fields.SessionUpdate == "tool_call_update" &&
		fields.ToolCallID != "" &&
		fields.TerminalID != "" {
		return fields, true
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
	if strings.EqualFold(fields.StopReason, "end_turn") {
		fields.SessionUpdate = firstNonEmpty(fields.SessionUpdate, fields.StopReason)
		return fields, acpHistoryProjectionRaw
	}
	if fields.EntityType != "" || fields.EventType != "" {
		return fields, acpHistoryProjectionRaw
	}
	return fields, acpHistoryProjectionNone
}
