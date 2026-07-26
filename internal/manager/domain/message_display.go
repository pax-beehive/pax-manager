package domain

import "encoding/json"

type invocationDisplayRaw struct {
	ReplacesMessageIDs []string `json:"replaces_message_ids"`
}

// NormalTranscriptMessages applies the Pax invocation display contract used by
// console transcript views. Debug views should use the raw message slice.
func NormalTranscriptMessages(messages []MessageWithParts) []MessageWithParts {
	if len(messages) == 0 {
		return nil
	}
	displayByParent := map[string][]MessageWithParts{}
	displayFallback := make([]MessageWithParts, 0)
	hidden := map[string]struct{}{}
	for _, message := range messages {
		if !isInvocationDisplayMessage(message.MessageType) {
			continue
		}
		parentID := message.ParentMessageID
		replaced := invocationReplacedMessageIDs(message)
		if len(replaced) == 0 && parentID != "" {
			replaced = []string{parentID}
		}
		for _, id := range replaced {
			if id != "" {
				hidden[id] = struct{}{}
			}
		}
		if parentID != "" {
			displayByParent[parentID] = append(displayByParent[parentID], message)
			continue
		}
		displayFallback = append(displayFallback, message)
	}
	if len(displayByParent) == 0 && len(displayFallback) == 0 {
		return messages
	}
	out := make([]MessageWithParts, 0, len(messages))
	renderedDisplays := map[string]struct{}{}
	for _, message := range messages {
		if displays := displayByParent[message.MessageID]; len(displays) > 0 {
			for _, display := range displays {
				if _, ok := hidden[display.MessageID]; ok {
					continue
				}
				out = append(out, display)
				renderedDisplays[display.MessageID] = struct{}{}
			}
		}
		if _, ok := hidden[message.MessageID]; ok {
			continue
		}
		if isInvocationDisplayMessage(message.MessageType) {
			if _, ok := renderedDisplays[message.MessageID]; !ok {
				out = append(out, message)
				renderedDisplays[message.MessageID] = struct{}{}
			}
			continue
		}
		out = append(out, message)
	}
	for _, display := range displayFallback {
		if _, ok := renderedDisplays[display.MessageID]; ok {
			continue
		}
		out = append(out, display)
	}
	return out
}

func isInvocationDisplayMessage(messageType string) bool {
	return messageType == MessageTypePaxInvocation ||
		messageType == MessageTypePaxInvocationPending ||
		messageType == MessageTypePaxArtifact
}

func invocationReplacedMessageIDs(message MessageWithParts) []string {
	var raw invocationDisplayRaw
	if len(message.RawJSON) == 0 || json.Unmarshal(message.RawJSON, &raw) != nil {
		return nil
	}
	return raw.ReplacesMessageIDs
}
