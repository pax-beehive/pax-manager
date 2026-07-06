package manager

import (
	"encoding/json"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const paxInvocationPromptParam = "pax_invocation"

type paxInvocationPromptEndpoint struct {
	RepresentativeAgentID string `json:"representative_agent_id,omitempty"`
	AgentID               string `json:"agent_id"`
	SessionID             string `json:"session_id"`
}

type paxInvocationPromptContent struct {
	DisplayText  string `json:"display_text"`
	OriginalText string `json:"original_text"`
}

type paxInvocationPromptMetadata struct {
	InvocationID   string                      `json:"invocation_id"`
	InvocationType string                      `json:"invocation_type"`
	Phase          string                      `json:"phase"`
	Side           string                      `json:"side"`
	Sender         paxInvocationPromptEndpoint `json:"sender"`
	Receiver       paxInvocationPromptEndpoint `json:"receiver"`
	Content        paxInvocationPromptContent  `json:"content"`
}

type paxInvocationPromptDisplayRaw struct {
	InvocationID      string                      `json:"invocation_id"`
	InvocationType    string                      `json:"invocation_type"`
	Phase             string                      `json:"phase"`
	Side              string                      `json:"side"`
	ReplacesMessageID []string                    `json:"replaces_message_ids"`
	Sender            paxInvocationPromptEndpoint `json:"sender"`
	Receiver          paxInvocationPromptEndpoint `json:"receiver"`
	Content           paxInvocationPromptContent  `json:"content"`
}

type paxInvocationPendingRaw struct {
	InvocationID      string                      `json:"invocation_id"`
	InvocationType    string                      `json:"invocation_type"`
	Phase             string                      `json:"phase"`
	Side              string                      `json:"side"`
	ToolCallID        string                      `json:"tool_call_id,omitempty"`
	PromptMessageID   string                      `json:"prompt_message_id,omitempty"`
	ReplacesMessageID []string                    `json:"replaces_message_ids"`
	Sender            paxInvocationPromptEndpoint `json:"sender"`
	Receiver          paxInvocationPromptEndpoint `json:"receiver"`
	Content           paxInvocationPromptContent  `json:"content"`
}

func conversationTurnInvocationMetadata(
	invocation domain.ConversationAgentInvocation,
	phase string,
	side string,
	sender paxInvocationPromptEndpoint,
	receiver paxInvocationPromptEndpoint,
	displayText string,
	originalText string,
) paxInvocationPromptMetadata {
	return paxInvocationPromptMetadata{
		InvocationID:   invocation.InvocationID,
		InvocationType: "agent_conversation",
		Phase:          phase,
		Side:           side,
		Sender:         sender,
		Receiver:       receiver,
		Content: paxInvocationPromptContent{
			DisplayText:  strings.TrimSpace(displayText),
			OriginalText: strings.TrimSpace(originalText),
		},
	}
}

func paxInvocationPromptMetadataFromRaw(raw json.RawMessage) (paxInvocationPromptMetadata, bool) {
	if len(raw) == 0 {
		return paxInvocationPromptMetadata{}, false
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(raw, &params); err != nil {
		return paxInvocationPromptMetadata{}, false
	}
	metaRaw, ok := params[paxInvocationPromptParam]
	if !ok || len(metaRaw) == 0 {
		return paxInvocationPromptMetadata{}, false
	}
	var meta paxInvocationPromptMetadata
	if err := json.Unmarshal(metaRaw, &meta); err != nil || strings.TrimSpace(meta.InvocationID) == "" {
		return paxInvocationPromptMetadata{}, false
	}
	if meta.InvocationType == "" {
		meta.InvocationType = "agent_conversation"
	}
	return meta, true
}

func paxInvocationDisplayRawForPrompt(
	parentMessageID string,
	meta paxInvocationPromptMetadata,
) (json.RawMessage, string) {
	text := firstNonEmpty(
		strings.TrimSpace(meta.Content.DisplayText),
		strings.TrimSpace(meta.Content.OriginalText),
		"Pax agent conversation update.",
	)
	raw, _ := json.Marshal(paxInvocationPromptDisplayRaw{
		InvocationID:      meta.InvocationID,
		InvocationType:    firstNonEmpty(meta.InvocationType, "agent_conversation"),
		Phase:             meta.Phase,
		Side:              meta.Side,
		ReplacesMessageID: []string{parentMessageID},
		Sender:            meta.Sender,
		Receiver:          meta.Receiver,
		Content: paxInvocationPromptContent{
			DisplayText:  text,
			OriginalText: strings.TrimSpace(meta.Content.OriginalText),
		},
	})
	return raw, text
}

func paxInvocationDisplayRawForPending(
	replacesMessageIDs []string,
	pending paxInvocationPendingRaw,
) (json.RawMessage, string) {
	text := firstNonEmpty(
		strings.TrimSpace(pending.Content.DisplayText),
		strings.TrimSpace(pending.Content.OriginalText),
		"Pax agent conversation update.",
	)
	raw, _ := json.Marshal(paxInvocationPromptDisplayRaw{
		InvocationID:      pending.InvocationID,
		InvocationType:    firstNonEmpty(pending.InvocationType, "agent_conversation"),
		Phase:             pending.Phase,
		Side:              pending.Side,
		ReplacesMessageID: replacesMessageIDs,
		Sender:            pending.Sender,
		Receiver:          pending.Receiver,
		Content: paxInvocationPromptContent{
			DisplayText:  text,
			OriginalText: strings.TrimSpace(pending.Content.OriginalText),
		},
	})
	return raw, text
}
