package manager

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const artifactPublicationURIProtocol = "artifact-publication://"

type artifactPublicationDisplayRaw struct {
	PublicationID     string   `json:"publication_id"`
	ReplacesMessageID []string `json:"replaces_message_ids"`
}

func reconcileArtifactPublicationDisplaysForSession(
	ctx context.Context,
	store domain.Store,
	agentID string,
	sessionID string,
) error {
	messages, err := store.ListMessages(ctx, agentID, sessionID, 1000)
	if err != nil {
		return err
	}
	for _, terminal := range messages {
		if err := reconcileArtifactPublicationDisplay(ctx, store, messages, terminal); err != nil {
			return err
		}
	}
	return nil
}

func reconcileArtifactPublicationDisplayForTerminal(
	ctx context.Context,
	store domain.Store,
	terminal domain.Message,
) error {
	if !isArtifactPublicationTerminal(terminal) {
		return nil
	}
	messages, err := store.ListMessages(ctx, terminal.AgentID, terminal.SessionID, 1000)
	if err != nil {
		return err
	}
	return reconcileArtifactPublicationDisplay(ctx, store, messages, terminal)
}

func reconcileArtifactPublicationDisplay(
	ctx context.Context,
	store domain.Store,
	messages []domain.Message,
	terminal domain.Message,
) error {
	if !isArtifactPublicationTerminal(terminal) {
		return nil
	}
	publicationID := artifactPublicationIDFromTerminal(terminal.RawJSON)
	if publicationID == "" {
		return nil
	}
	publication, err := store.GetArtifactPublication(
		ctx,
		domain.UserPrincipal{User: domain.User{UserID: terminal.OwnerUserID}},
		publicationID,
	)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return err
	}
	if publication.OwnerUserID != terminal.OwnerUserID ||
		publication.NodeID != terminal.NodeID ||
		publication.AgentID != terminal.AgentID ||
		publication.SessionID != terminal.SessionID {
		return nil
	}
	toolCallID := acpHistoryToolCallIDFromRaw(terminal.RawJSON)
	if toolCallID == "" {
		return nil
	}
	replaces := acpHistoryToolCallReplaceMessageIDs(messages, toolCallID, "", "")
	raw, err := json.Marshal(artifactPublicationDisplayRaw{
		PublicationID:     publication.PublicationID,
		ReplacesMessageID: replaces,
	})
	if err != nil {
		return err
	}
	logicalKey := "artifact-publication:" + terminal.SessionID + ":" + toolCallID + ":display"
	message := domain.Message{
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
		MessageType:     domain.MessageTypePaxArtifact,
		ParentMessageID: terminal.MessageID,
		LogicalKey:      logicalKey,
		RawJSON:         raw,
	}
	if err := store.UpsertMessage(ctx, &message); err != nil {
		return err
	}
	return store.UpsertMessagePart(ctx, &domain.MessagePart{
		MessageID:   message.MessageID,
		PartIndex:   0,
		PartType:    domain.MessagePartArtifact,
		PayloadJSON: append(json.RawMessage(nil), raw...),
		ArtifactURI: artifactPublicationURIProtocol + publication.PublicationID + "/main",
	})
}

func isArtifactPublicationTerminal(terminal domain.Message) bool {
	return terminal.MessageType == "tool_call_update" &&
		strings.ToLower(acpHistoryToolCallUpdateStatus(terminal.RawJSON)) == "completed" &&
		artifactPublicationIDFromTerminal(terminal.RawJSON) != ""
}

func artifactPublicationIDFromTerminal(raw json.RawMessage) string {
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return findArtifactPublicationID(value, 0)
}

func findArtifactPublicationID(value any, depth int) string {
	if depth > 12 {
		return ""
	}
	switch typed := value.(type) {
	case map[string]any:
		if marker, ok := typed["pax_artifact_publication"].(map[string]any); ok {
			if publicationID, ok := marker["publication_id"].(string); ok {
				return strings.TrimSpace(publicationID)
			}
		}
		for key, child := range typed {
			if key == "rawInput" || key == "raw_input" {
				continue
			}
			if publicationID := findArtifactPublicationID(child, depth+1); publicationID != "" {
				return publicationID
			}
		}
	case []any:
		for _, child := range typed {
			if publicationID := findArtifactPublicationID(child, depth+1); publicationID != "" {
				return publicationID
			}
		}
	case string:
		text := strings.TrimSpace(typed)
		if !strings.HasPrefix(text, "{") && !strings.HasPrefix(text, "[") {
			return ""
		}
		var child any
		if json.Unmarshal([]byte(text), &child) == nil {
			return findArtifactPublicationID(child, depth+1)
		}
	}
	return ""
}
