package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryBatchGivenCompactViewsThenBudgetsFullSnapshotsIndependently(t *testing.T) {
	work := domain.SecretaryJob{
		OwnerUserID:     "owner",
		SourceAgentID:   "agent",
		SourceSessionID: "session",
	}
	batch := newSecretaryBatch(&work)
	for i, id := range []string{"first", "second", "third"} {
		doc := domain.SearchDocument{
			SourceID:    id,
			OwnerUserID: "owner",
			AgentID:     "agent",
			SessionID:   "session",
			Role:        "assistant",
			Body:        strings.Repeat("x", domain.SecretaryMaxSourceBytes/2-16) + "\n\nFinal.",
		}
		doc.ContentHash = domain.SearchContentHash(doc.Body)
		consumed, err := batch.consume(secretaryChange{id: id, revision: int64(i + 1)}, doc, nil)
		require.NoError(t, err)
		assert.Equal(t, i < 2, consumed)
	}
	assert.Len(t, work.Messages, 2)
	assert.EqualValues(t, 2, work.ThroughRevision)
	assert.NoError(t, secretaryWithinBounds(work.Messages))
	assert.Equal(t, 2*len("Final."), batch.bytes)
	work.Messages[0].Role = "user"
	assert.NoError(t, secretaryWithinBounds(work.Messages))
}

func TestSecretarySourceGivenOversizedPartsThenRejectsBeforeJSONAggregation(t *testing.T) {
	script := &scriptedPostgresScript{queries: []scriptedRows{{
		columns: []string{"message", "owner", "parts", "revision"},
		values:  [][]driver.Value{{nil, "owner", nil, int64(1)}},
	}}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	tx, err := store.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = loadSearchMessageWithByteLimit(
		t.Context(),
		tx,
		"large",
		domain.SecretaryMaxSourceBytes,
	)
	assert.ErrorContains(t, err, "full-source byte limit")
	require.Len(t, script.queryTexts, 1)
	assert.Contains(t, script.queryTexts[0], "octet_length")
	assert.Contains(t, script.queryTexts[0], "THEN COALESCE((SELECT jsonb_agg")
	assert.Contains(t, script.queryTexts[0], "<= $2")
}
