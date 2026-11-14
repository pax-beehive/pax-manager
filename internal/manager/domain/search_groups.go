package domain

import "context"

type SearchGroup struct {
	Key     string
	Matches []SearchDocument
	HasMore bool
}

type SearchGroupPage struct {
	Groups            []SearchGroup
	NextCursor        string
	PaginationLimited bool
}

type GroupedSearchBackend interface {
	SearchGroups(context.Context, SearchQuery) (SearchGroupPage, error)
}

type SearchGroupMetadata struct {
	Key       string `json:"key"`
	Title     string `json:"title"`
	NodeID    string `json:"node_id,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type SearchGroupSourceStore interface {
	ReadSearchGroup(context.Context, string, SearchDocument) (SearchGroupMetadata, error)
}

func SearchDocumentGroupKey(doc SearchDocument) string {
	if doc.AgentID != "" && doc.SessionID != "" {
		return "session:" + SearchContentHash(
			doc.OwnerUserID+"\x00"+doc.AgentID+"\x00"+doc.SessionID,
		)
	}
	return "document:" + SearchContentHash(doc.OwnerUserID+"\x00"+doc.DocumentID())
}
