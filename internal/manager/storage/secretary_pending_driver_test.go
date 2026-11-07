package storage

import (
	"database/sql/driver"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryPendingExecutionRows(sources ...string) scriptedRows {
	rows := scriptedRows{columns: []string{
		"source", "expected_agent", "expected_session", "agent", "session", "message",
		"current_hash", "expected_hash", "canonicalization", "execution",
	}}
	for _, source := range sources {
		rows.values = append(
			rows.values,
			[]driver.Value{source, "", "", "", "", []byte(`{}`), "", "", "", true},
		)
	}
	return rows
}

func TestSecretaryPendingDriverGivenCandidateThenMatchesCanonicalHashAndVisibility(t *testing.T) {
	for _, mode := range []string{"unchanged", "new", "canonicalization", "removed", "moved", "hidden", "hidden_processed", "duplicate", "empty", "invalid_json", "invalid_scan"} {
		t.Run(mode, func(t *testing.T) {
			rows := secretaryPendingExecutionRows()
			rows.values = [][]driver.Value{{"1", "source", "session", "source", "session",
				[]byte(`{"message_id":"m1","role":"assistant","message_type":"chat"}`),
				"hash", "hash", domain.SearchCanonicalizationVersion, false}}
			want := 0
			switch mode {
			case "new":
				rows.values[0][7], want = "", 1
			case "canonicalization":
				rows.values[0][8], want = "older-version", 1
			case "removed":
				rows.values[0][6], want = "", 1
			case "moved":
				rows.values[0][4], want = "another-session", 1
			case "hidden", "hidden_processed":
				rows.values[0][5] = []byte(
					`{"message_id":"m1","role":"assistant","message_type":"chat","raw_json":{"params":{"update":{"channel":"analysis"}}}}`,
				)
				if mode == "hidden" {
					rows.values[0][7] = ""
				} else {
					want = 1
				}
			case "duplicate":
				rows = secretaryPendingExecutionRows("1", "1", "2")
				want = 2
			case "empty":
				rows.values = nil
			case "invalid_json":
				rows.values[0][5] = []byte(`{`)
			case "invalid_scan":
				rows.values[0][9] = "not-a-boolean"
			}
			script := &scriptedPostgresScript{queries: []scriptedRows{rows}}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			count, err := store.secretaryPendingCount(t.Context(), "owner", 7)
			if mode == "invalid_json" || mode == "invalid_scan" {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, want, count)
			assert.Equal(t, "owner", script.queryArgs[0][0].Value)
			assert.Equal(t, int64(7), script.queryArgs[0][2].Value)
			assert.Empty(
				t,
				script.execTexts,
				"status inspection never acknowledges work or clears invalidations",
			)
		})
	}
}

func TestSecretaryPendingDriverGivenQueryFailureThenDoesNotReportAnEmptyQueue(t *testing.T) {
	store, cleanup := scriptedPostgresStore(t, &scriptedPostgresScript{})
	defer cleanup()
	_, err := store.secretaryPendingCount(t.Context(), "owner", 7)
	assert.ErrorContains(t, err, "unexpected query")
}
