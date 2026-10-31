package storage

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func searchMessageRows(t *testing.T, messageID, owner string) scriptedRows {
	t.Helper()
	message, err := json.Marshal(
		domain.Message{
			MessageID:   messageID,
			AgentID:     "agent",
			SessionID:   "session",
			Role:        "user",
			MessageType: "user_message",
		},
	)
	require.NoError(t, err)
	parts, err := json.Marshal(
		[]domain.MessagePart{
			{MessageID: messageID, PartIndex: 0, PartType: "text", Text: "data"},
			{MessageID: messageID, PartIndex: 1, PartType: "text", Text: "base"},
		},
	)
	require.NoError(t, err)
	return scriptedRows{
		columns: []string{"message", "owner", "parts", "revision"},
		values:  [][]driver.Value{{message, owner, parts, int64(7)}},
	}
}

func TestSearchReadGivenOwnerWhenMessageHasMultiplePartsThenReturnsCanonicalWhole(t *testing.T) {
	script := &scriptedPostgresScript{
		queries: []scriptedRows{searchMessageRows(t, "message", "owner")},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	doc, err := store.ReadSearchDocument(t.Context(), "owner", "message", "message")
	require.NoError(t, err)
	assert.Equal(t, "database", doc.Body)
	assert.Equal(t, "owner", doc.OwnerUserID)
	assert.Equal(t, int64(7), doc.Revision)
	assert.True(t, script.committed)
	assert.Contains(t, script.queryTexts[0], "a.owner_user_id")
	assert.Contains(t, script.queryTexts[0], "s.archived_at IS NULL")
	assert.Contains(t, script.queryTexts[0], "replaces_message_ids")
}

func TestSearchReadGivenForeignOrMissingSourceThenReturnsNotFound(t *testing.T) {
	for _, owner := range []string{"other", ""} {
		t.Run("owner="+owner, func(t *testing.T) {
			script := &scriptedPostgresScript{
				queries: []scriptedRows{searchMessageRows(t, "message", owner)},
			}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			_, err := store.ReadSearchDocument(t.Context(), "owner", "message", "message")
			assert.ErrorIs(t, err, ErrNotFound)
			assert.False(t, script.committed)
		})
	}
}

func TestSearchQueueGivenClaimThenLoadsWholeLatestMessageWithLease(t *testing.T) {
	script := &scriptedPostgresScript{queries: []scriptedRows{
		{
			columns: []string{"kind", "source_id", "revision", "token"},
			values:  [][]driver.Value{{"message", "message", int64(9), "lease"}},
		},
		searchMessageRows(t, "message", "owner"),
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	work, err := store.ClaimSearchBatch(t.Context(), 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, work, 1)
	assert.Equal(t, "database", work[0].Document.Body)
	assert.Equal(t, int64(9), work[0].Document.Revision)
	assert.Equal(t, "lease", work[0].LeaseToken)
	assert.Contains(t, script.queryTexts[0], "SKIP LOCKED")
	assert.True(t, script.committed)
}

func TestSearchQueueGivenMissingSourceThenReturnsVersionedTombstone(t *testing.T) {
	script := &scriptedPostgresScript{queries: []scriptedRows{
		{
			columns: []string{"kind", "source_id", "revision", "token"},
			values:  [][]driver.Value{{"message", "removed", int64(12), "lease"}},
		},
		{columns: []string{"message", "owner", "parts", "revision"}},
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	work, err := store.ClaimSearchBatch(t.Context(), 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, work, 1)
	assert.True(t, work[0].Document.Deleted)
	assert.Equal(t, int64(12), work[0].Document.Revision)
}

func TestSearchQueueGivenCompletionThenOnlyAcknowledgesClaimedVersionAndToken(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "retry"}[failed], func(t *testing.T) {
			script := &scriptedPostgresScript{}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			var syncErr error
			if failed {
				syncErr = errors.New("unavailable")
			}
			err := store.CompleteSearchWork(
				t.Context(),
				domain.SearchWork{
					Document: domain.SearchDocument{
						Kind:     "message",
						SourceID: "message",
						Revision: 7,
					},
					LeaseToken: "lease",
				},
				syncErr,
			)
			require.NoError(t, err)
			require.Len(t, script.execTexts, 1)
			assert.Contains(t, script.execTexts[0], "lease_token = $3")
			if failed {
				assert.Contains(t, script.execTexts[0], "attempts + 1")
			} else {
				assert.Contains(t, script.execTexts[0], "GREATEST(indexed_revision, $4)")
			}
		})
	}
}

func TestSearchReadGivenSessionTitleThenReturnsIndependentTitleDocument(t *testing.T) {
	for _, title := range []string{"Title", ""} {
		script := &scriptedPostgresScript{
			queries: []scriptedRows{
				{
					columns: []string{
						"owner",
						"agent",
						"session",
						"conversation",
						"title",
						"created",
						"revision",
					},
					values: [][]driver.Value{
						{"owner", "agent", "session", "conversation", title, time.Now(), int64(3)},
					},
				},
			},
		}
		store, cleanup := scriptedPostgresStore(t, script)
		doc, err := store.ReadSearchDocument(t.Context(), "owner", "session", "10")
		if title == "" {
			assert.ErrorIs(t, err, ErrNotFound)
		} else {
			require.NoError(t, err)
			assert.Equal(t, title, doc.Title)
			assert.Equal(t, domain.SearchContentHash(title), doc.ContentHash)
		}
		cleanup()
	}
}

func TestSearchReadGivenInvalidSourceThenRejectsWithoutDisclosure(t *testing.T) {
	for _, tc := range []struct{ owner, kind, id string }{{"", "message", "id"}, {"owner", "message", ""}, {"owner", "unknown", "id"}} {
		script := &scriptedPostgresScript{}
		store, cleanup := scriptedPostgresStore(t, script)
		_, err := store.ReadSearchDocument(t.Context(), tc.owner, tc.kind, tc.id)
		assert.ErrorIs(t, err, ErrNotFound)
		cleanup()
	}
}

func TestSearchReadGivenInvalidPersistedMessageThenReturnsError(t *testing.T) {
	for _, tc := range []struct{ message, parts string }{{"broken", "[]"}, {`{"message_id":"id","role":"user"}`, "broken"}, {`{"message_id":"id","role":"assistant","message_type":"agent_thought_chunk"}`, "[]"}} {
		script := &scriptedPostgresScript{
			queries: []scriptedRows{
				{
					columns: []string{"message", "owner", "parts", "revision"},
					values: [][]driver.Value{
						{[]byte(tc.message), "owner", []byte(tc.parts), int64(1)},
					},
				},
			},
		}
		store, cleanup := scriptedPostgresStore(t, script)
		_, err := store.ReadSearchDocument(t.Context(), "owner", "message", "id")
		assert.Error(t, err)
		cleanup()
	}
}

func TestSearchContextGivenNeighborsThenLoadsWithinSameSnapshot(t *testing.T) {
	script := &scriptedPostgresScript{queries: []scriptedRows{
		searchMessageRows(t, "anchor", "owner"),
		{
			columns: []string{"id"},
			values:  [][]driver.Value{{"before"}, {"hidden"}, {"foreign"}, {"after"}},
		},
		searchMessageRows(t, "before", "owner"),
		{columns: []string{"message", "owner", "parts", "revision"}},
		searchMessageRows(t, "foreign", "foreign"),
		searchMessageRows(t, "after", "owner"),
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	docs, err := store.ReadSearchContext(t.Context(), "owner", "anchor", 2, 2)
	require.NoError(t, err)
	assert.Len(t, docs, 3)
	assert.Contains(t, script.queryTexts[1], "m.agent_id=$2")
	assert.Contains(t, script.queryTexts[1], "LIMIT $4")
	assert.True(t, script.committed)
}

func TestSearchContextGivenInvalidAnchorOrQueryFailureThenReturnsError(t *testing.T) {
	for _, queries := range [][]scriptedRows{
		{searchMessageRows(t, "anchor", "foreign")},
		{{columns: []string{"message", "owner", "parts", "revision"}}},
		{searchMessageRows(t, "anchor", "owner")},
		{searchMessageRows(t, "anchor", "owner"), {columns: []string{"id", "extra"}, values: [][]driver.Value{{"before", "unexpected"}}}},
		{searchMessageRows(t, "anchor", "owner"), {columns: []string{"id"}, values: [][]driver.Value{{"before"}}}},
	} {
		script := &scriptedPostgresScript{queries: queries}
		store, cleanup := scriptedPostgresStore(t, script)
		_, err := store.ReadSearchContext(t.Context(), "owner", "anchor", 1, 1)
		assert.Error(t, err)
		cleanup()
	}
	script := &scriptedPostgresScript{}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	for _, tc := range []struct {
		owner, id     string
		before, after int
	}{{"", "id", 0, 0}, {"owner", "", 0, 0}, {"owner", "id", -1, 0}, {"owner", "id", 0, 101}} {
		_, err := store.ReadSearchContext(t.Context(), tc.owner, tc.id, tc.before, tc.after)
		assert.ErrorIs(t, err, ErrNotFound)
	}
}

func TestSearchQueueGivenEmptyBackfillOrClaimThenSucceeds(t *testing.T) {
	script := &scriptedPostgresScript{
		queries: []scriptedRows{{columns: []string{"kind", "id", "revision", "token"}}},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	require.NoError(t, store.BackfillSearch(t.Context()))
	assert.Contains(t, script.execTexts[0], "ON CONFLICT (kind,source_id) DO NOTHING")
	work, err := store.ClaimSearchBatch(t.Context(), 0, 0)
	require.NoError(t, err)
	assert.Empty(t, work)
	assert.Equal(t, int64(100), script.queryArgs[0][0].Value)
	assert.Equal(t, int64(60000), script.queryArgs[0][2].Value)
}

func TestSearchQueueGivenLoadOrScanFailureThenRollsBackClaim(t *testing.T) {
	for _, queries := range [][]scriptedRows{
		nil,
		{{columns: []string{"kind", "id"}, values: [][]driver.Value{{"message", "id"}}}},
		{{columns: []string{"kind", "id", "revision", "token"}, values: [][]driver.Value{{"message", "id", int64(2), "token"}}}},
	} {
		script := &scriptedPostgresScript{queries: queries}
		store, cleanup := scriptedPostgresStore(t, script)
		_, err := store.ClaimSearchBatch(t.Context(), 1, time.Second)
		assert.Error(t, err)
		assert.False(t, script.committed)
		cleanup()
	}
}

func TestSearchQueueGivenInvalidLeaseOrLongErrorThenBoundsStoredError(t *testing.T) {
	script := &scriptedPostgresScript{}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	assert.Error(t, store.CompleteSearchWork(t.Context(), domain.SearchWork{}, nil))
	work := domain.SearchWork{
		Document:   domain.SearchDocument{Kind: "message", SourceID: "id", Revision: 2},
		LeaseToken: "token",
	}
	require.NoError(
		t,
		store.CompleteSearchWork(t.Context(), work, errors.New(string(make([]byte, 3000)))),
	)
	assert.Len(t, script.execArgs[0][3].Value, 2000)
}
