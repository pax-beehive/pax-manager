package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

const SearchCanonicalizationVersion = "visible-text-v1"

type SearchDocument struct {
	Kind                    string    `json:"kind"`
	SourceID                string    `json:"source_id"`
	OwnerUserID             string    `json:"owner_user_id,omitempty"`
	AgentID                 string    `json:"agent_id,omitempty"`
	SessionID               string    `json:"session_id,omitempty"`
	ConversationID          string    `json:"conversation_id,omitempty"`
	Title                   string    `json:"title,omitempty"`
	Body                    string    `json:"body,omitempty"`
	Role                    string    `json:"role,omitempty"`
	SessionSeq              int64     `json:"session_seq,omitempty"`
	ContentHash             string    `json:"content_hash,omitempty"`
	CanonicalizationVersion string    `json:"canonicalization_version,omitempty"`
	Revision                int64     `json:"revision"`
	SummaryRevision         int64     `json:"summary_revision,omitempty"`
	CreatedAt               time.Time `json:"created_at"`
	Deleted                 bool      `json:"deleted"`
}

func (d SearchDocument) DocumentID() string { return d.Kind + ":" + d.SourceID }

type SearchQuery struct {
	Query       string   `json:"query"`
	OwnerUserID string   `json:"-"`
	Cursor      string   `json:"cursor,omitempty"`
	Kinds       []string `json:"kinds,omitempty"`
	SessionIDs  []string `json:"session_ids,omitempty"`
	Limit       int      `json:"limit,omitempty"`
}

type SearchPage struct {
	Hits       []SearchDocument `json:"hits"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

type SearchWork struct {
	Document   SearchDocument
	LeaseToken string
}

type SearchBackend interface {
	EnsureIndex(context.Context) error
	Index(context.Context, SearchDocument) error
	Search(context.Context, SearchQuery) (SearchPage, error)
}

type SearchSourceStore interface {
	ReadSearchDocument(ctx context.Context, ownerID, kind, sourceID string) (SearchDocument, error)
	ReadSearchContext(
		ctx context.Context,
		ownerID, messageID string,
		before, after int,
	) ([]SearchDocument, error)
}

type SearchQueueStore interface {
	BackfillSearch(context.Context) error
	ClaimSearchBatch(context.Context, int, time.Duration) ([]SearchWork, error)
	CompleteSearchWork(context.Context, SearchWork, error) error
}

func SearchContentHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Part boundaries do not introduce characters into the visible message.
func CanonicalSearchMessage(message MessageWithParts) (SearchDocument, error) {
	if !searchVisibleMessage(message) {
		return SearchDocument{}, ErrNotFound
	}
	parts := append([]MessagePart(nil), message.Parts...)
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartIndex < parts[j].PartIndex })
	var body strings.Builder
	for i, part := range parts {
		if part.PartIndex < 0 || (i > 0 && part.PartIndex == parts[i-1].PartIndex) {
			return SearchDocument{}, ErrNotFound
		}
		if part.PartType == MessagePartText {
			body.WriteString(part.Text)
		}
	}
	text := body.String()
	if strings.TrimSpace(text) == "" {
		return SearchDocument{}, ErrNotFound
	}
	return SearchDocument{Kind: "message", SourceID: message.MessageID,
		OwnerUserID: message.OwnerUserID, AgentID: message.AgentID, SessionID: message.SessionID,
		ConversationID: message.ConversationID, Role: message.Role, SessionSeq: message.SessionSeq,
		Body: text, ContentHash: SearchContentHash(text), CanonicalizationVersion: SearchCanonicalizationVersion,
		CreatedAt: message.CreatedAt}, nil
}

func searchVisibleMessage(message MessageWithParts) bool {
	if message.MessageID == "" || (message.Role != "user" && message.Role != "assistant") {
		return false
	}
	switch message.MessageType {
	case "", "chat", "text", "assistant_message", "agent_message_chunk", "message_delta",
		MessageTypeUser, MessageTypePaxUser, MessageTypePaxInvocation,
		MessageTypePaxInvocationPending, MessageTypePaxArtifact:
		return !searchHiddenEnvelope(message.RawJSON)
	default:
		return false
	}
}

func searchHiddenEnvelope(raw json.RawMessage) bool {
	var frame map[string]any
	if len(raw) == 0 {
		return false
	}
	if json.Unmarshal(raw, &frame) != nil {
		return true
	}
	return searchHiddenFields(frame, 0)
}

func searchHiddenFields(frame map[string]any, depth int) bool {
	if depth > 5 {
		return true
	}
	for _, key := range []string{"sessionUpdate", "session_update", "type", "channel"} {
		value, _ := frame[key].(string)
		switch strings.ToLower(value) {
		case "agent_thought_chunk", "reasoning", "thinking", "analysis":
			return true
		}
	}
	for _, key := range []string{"params", "update"} {
		if nested, ok := frame[key].(map[string]any); ok && searchHiddenFields(nested, depth+1) {
			return true
		}
	}
	return false
}
