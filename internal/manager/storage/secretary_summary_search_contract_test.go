package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretarySummarySearchMemoryGivenUnavailableOriginThenKeepsIndependentResultAndChecksFactualSourceAccess(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	origin := store.sessions[sessionKey("source", "session")]
	origin.SessionName = "Private organizing session"
	store.sessions[sessionKey("source", "session")] = origin
	store.sessions[sessionKey("secretary", "related")] = AgentSession{
		ID: 2, AgentID: "secretary", SessionID: "related", RuntimeStatus: domain.RuntimeStatusIdle,
	}
	secretaryMemoryMessage(store, "external", "Supported external fact", 1)
	message := store.messages["external"]
	message.AgentID, message.SessionID = "secretary", "related"
	store.messages["external"] = message
	secretaryEnable(t, store)
	job := secretaryNodesReadyJob(t, store)
	ref := summaryTestRef("external", "Supported external fact")
	job.AllowedRelatedMessages = []domain.MessageVersionRef{ref}
	publication := secretaryNodesPublication(job, "one")
	publication.Outputs[0].Request.Title = "Supported conclusion"
	publication.Outputs[0].Request.Coverage = []domain.MessageVersionRef{ref}
	publication.Outcomes = []domain.SecretaryMessageOutcome{{
		MessageID: "m1", Action: "no_summary", Reason: "The source turn has no additional conclusion",
	}}
	versions, err := store.PublishSecretaryBatch(t.Context(), job, publication)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	doc, err := store.ReadSearchDocument(t.Context(), "owner", "summary", versions[0].SummaryID)
	require.NoError(t, err)
	assert.Equal(t, "source", doc.AgentID)
	assert.Equal(t, "session", doc.SessionID)

	now := store.now()
	origin.ArchivedAt = &now
	store.sessions[sessionKey("source", "session")] = origin
	summary, err := store.GetSummary(t.Context(), "owner", versions[0].SummaryID, 0)
	require.NoError(t, err)
	assert.Equal(t, versions[0].Body, summary.Body)
	assert.Empty(t, summary.OriginSourceID)
	assert.Empty(t, summary.OriginAgentID)
	assert.Empty(t, summary.OriginSessionID)
	assert.Empty(t, summary.OriginSessionTitle)
	independent, err := store.ReadSearchDocument(
		t.Context(),
		"owner",
		"summary",
		versions[0].SummaryID,
	)
	require.NoError(t, err)
	assert.Empty(t, independent.AgentID)
	assert.Empty(t, independent.SessionID)
	group, err := store.ReadSearchGroup(t.Context(), "owner", doc)
	require.NoError(t, err)
	assert.Equal(t, domain.SearchDocumentGroupKey(independent), group.Key)
	assert.Equal(t, "Supported conclusion", group.Title)
	assert.Empty(t, group.AgentID)
	assert.Empty(t, group.SessionID)
	_, err = store.ReadSearchDocument(t.Context(), "other", "summary", versions[0].SummaryID)
	assert.ErrorIs(t, err, domain.ErrNotFound)

	related := store.sessions[sessionKey("secretary", "related")]
	related.ArchivedAt = &now
	store.sessions[sessionKey("secretary", "related")] = related
	_, err = store.GetSummary(t.Context(), "owner", versions[0].SummaryID, 0)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = store.ReadSearchDocument(t.Context(), "owner", "summary", versions[0].SummaryID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}
