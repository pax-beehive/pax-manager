package storage

import (
	"database/sql/driver"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSearchGroupsGivenSecretaryPrimaryThenResolvesExplicitSourceInsteadOfCoverage(
	t *testing.T,
) {
	for _, primary := range []bool{true, false} {
		id := "summary"
		if primary {
			id = domain.SecretarySummaryID("owner", "1")
		}
		script := &scriptedPostgresScript{
			queries: []scriptedRows{
				{
					columns: []string{"type", "subject", "title"},
					values:  [][]driver.Value{{"fragment", "secretary:session:1", "Summary title"}},
				},
			},
		}
		script.queries = append(
			script.queries,
			scriptedRows{
				columns: []string{"agent", "session", "node", "title"},
				values:  [][]driver.Value{{"agent", "session", "node", "Custom current title"}},
			},
		)
		store, cleanup := scriptedPostgresStore(t, script)
		doc := domain.SearchDocument{
			Kind:            "summary",
			SourceID:        id,
			OwnerUserID:     "owner",
			SummaryRevision: 1,
		}
		group, err := store.ReadSearchGroup(t.Context(), "owner", doc)
		require.NoError(t, err)
		assert.Equal(t, "session", group.SessionID)
		assert.Equal(t, "Custom current title", group.Title)
		cleanup()
	}
}

func TestSearchGroupsGivenSessionOrUnavailableMetadataThenUsesFallbackAndFailsClosed(t *testing.T) {
	for _, rows := range [][]scriptedRows{
		{{columns: []string{"agent", "session", "node", "title"}, values: [][]driver.Value{{"agent", "session", "node", ""}}}},
		{{columns: []string{"agent", "session", "node", "title"}}}, nil,
	} {
		script := &scriptedPostgresScript{queries: rows}
		store, cleanup := scriptedPostgresStore(t, script)
		doc := domain.SearchDocument{
			Kind:        "message",
			SourceID:    "m",
			OwnerUserID: "owner",
			AgentID:     "agent",
			SessionID:   "session",
		}
		group, err := store.ReadSearchGroup(t.Context(), "owner", doc)
		if len(rows) > 0 && len(rows[0].values) > 0 {
			require.NoError(t, err)
			assert.Equal(t, "session", group.Title)
		} else {
			require.Error(t, err)
		}
		_, err = store.ReadSearchGroup(t.Context(), "foreign", doc)
		assert.ErrorIs(t, err, domain.ErrNotFound)
		cleanup()
	}
}

func TestSearchGroupsMemoryGivenPrimaryAndStandaloneThenKeepsOwnerAndCurrentSessionNames(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	session := store.sessions[sessionKey("source", "session")]
	session.CustomSessionName = "My name"
	store.sessions[sessionKey("source", "session")] = session
	id := domain.SecretarySummaryID("owner", "1")
	store.secretaryLocked().summaries[id] = []domain.SummaryVersion{
		{
			SummaryID:   id,
			Revision:    1,
			OwnerUserID: "owner",
			SubjectType: "fragment",
			SubjectID:   "secretary:session:1",
		},
	}
	doc := domain.SearchDocument{
		Kind:            "summary",
		SourceID:        id,
		OwnerUserID:     "owner",
		SummaryRevision: 1,
	}
	group, err := store.ReadSearchGroup(t.Context(), "owner", doc)
	require.NoError(t, err)
	assert.Equal(t, "My name", group.Title)
	assert.Equal(t, "session", group.SessionID)
	_, err = store.ReadSearchGroup(t.Context(), "foreign", doc)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	session.ArchivedAt = new(time.Time)
	store.sessions[sessionKey("source", "session")] = session
	group, err = store.ReadSearchGroup(t.Context(), "owner", doc)
	require.NoError(t, err)
	assert.Empty(t, group.SessionID)
	doc.SourceID = "standalone"
	store.secretaryLocked().summaries[doc.SourceID] = []domain.SummaryVersion{
		{
			SummaryID:   doc.SourceID,
			Revision:    1,
			OwnerUserID: "owner",
			SubjectType: "project",
			Title:       "Across sessions",
		},
	}
	group, err = store.ReadSearchGroup(t.Context(), "owner", doc)
	require.NoError(t, err)
	assert.Empty(t, group.SessionID)
	assert.Equal(t, "Across sessions", group.Title)
	doc.SourceID = "missing"
	_, err = store.ReadSearchGroup(t.Context(), "owner", doc)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}
