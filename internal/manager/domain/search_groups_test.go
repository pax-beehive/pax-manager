package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSearchGroupGivenOwnedSessionThenKindsShareOneOwnerAndAgentScopedKey(t *testing.T) {
	doc := SearchDocument{
		OwnerUserID: "owner",
		AgentID:     "agent",
		SessionID:   "session",
		Kind:        "message",
		SourceID:    "m",
	}
	key := SearchDocumentGroupKey(doc)
	doc.Kind, doc.SourceID = "session", "1"
	assert.Equal(t, key, SearchDocumentGroupKey(doc))
	doc.Kind, doc.SourceID = "summary", "summary"
	assert.Equal(t, key, SearchDocumentGroupKey(doc))
	doc.AgentID = "other"
	assert.NotEqual(t, key, SearchDocumentGroupKey(doc))
	doc.AgentID, doc.OwnerUserID = "agent", "other"
	assert.NotEqual(t, key, SearchDocumentGroupKey(doc))
	doc.SessionID = ""
	assert.NotEqual(t, key, SearchDocumentGroupKey(doc))
}
