package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryProjectionValue(column string, value driver.Value) scriptedRows {
	return scriptedRows{columns: []string{column}, values: [][]driver.Value{{value}}}
}

func secretaryProjectionDirtyRows() scriptedRows {
	return scriptedRows{columns: []string{"owner", "message", "generation"},
		values: [][]driver.Value{{"owner", "message", int64(7)}}}
}

func secretaryProjectionBootstrapRows(after, count int64, ids string) scriptedRows {
	return scriptedRows{columns: []string{"after", "count", "messages"},
		values: [][]driver.Value{{after, count, []byte(ids)}}}
}

func secretaryProjectionTx(t *testing.T, script *scriptedPostgresScript) *sql.Tx {
	t.Helper()
	store, cleanup := scriptedPostgresStore(t, script)
	t.Cleanup(cleanup)
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func TestSecretaryProgressProjectionDriverGivenAdvanceFailureThenNeverCommitsPartialWork(
	t *testing.T,
) {
	for _, scenario := range []string{"begin", "writer", "dirty_query", "dirty_scan", "bootstrap_owner", "ensure_owner", "bootstrap_page", "bootstrap_checkpoint", "commit"} {
		t.Run(scenario, func(t *testing.T) {
			script := &scriptedPostgresScript{}
			if scenario == "begin" {
				script.beginErr = assert.AnError
			} else if scenario != "writer" {
				script.queries = append(script.queries, secretaryProjectionValue("writer", true))
			}
			if scenario == "dirty_scan" {
				script.queries = append(
					script.queries,
					scriptedRows{
						columns: []string{"owner", "message", "generation"},
						values:  [][]driver.Value{{"owner", "message", "invalid"}},
					},
				)
			} else if scenario != "begin" && scenario != "writer" && scenario != "dirty_query" {
				script.queries = append(script.queries, scriptedRows{columns: []string{"owner", "message", "generation"}})
			}
			switch scenario {
			case "ensure_owner":
				script.queries = append(script.queries, secretaryProjectionValue("owner", "owner"))
				script.execErrors = []error{assert.AnError}
			case "bootstrap_page", "bootstrap_checkpoint":
				script.queries = append(
					script.queries,
					secretaryProjectionValue("owner", "owner"),
					secretaryProjectionValue("version", secretaryProgressProjectionVersion),
				)
				if scenario == "bootstrap_checkpoint" {
					script.queries = append(
						script.queries,
						secretaryProjectionBootstrapRows(0, 0, `[]`),
					)
					script.execErrors = []error{nil, assert.AnError}
				}
			case "commit":
				script.queries = append(script.queries, scriptedRows{columns: []string{"owner"}})
				script.commitErr = assert.AnError
			}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			_, err := store.AdvanceSecretaryProgress(t.Context(), 128)
			assert.Error(t, err)
			assert.Equal(t, scenario == "commit", script.committed)
			if scenario != "begin" && scenario != "commit" {
				assert.True(t, script.rolled)
			}
		})
	}
}

func TestSecretaryProgressProjectionDriverGivenLightLiveTrafficThenUsesRemainingBudgetForSparseHistory(
	t *testing.T,
) {
	script := &scriptedPostgresScript{queries: []scriptedRows{
		secretaryProjectionValue("writer", true),
		secretaryProjectionDirtyRows(),
		secretaryProjectionValue("version", secretaryProgressProjectionVersion),
		{columns: []string{"source"}},
		{columns: secretaryProjectionCanonicalRows(t).columns},
		secretaryProjectionValue("owner", "owner"),
		secretaryProjectionValue("version", secretaryProgressProjectionVersion),
		secretaryProjectionBootstrapRows(9, 1, `[]`),
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	more, err := store.AdvanceSecretaryProgress(t.Context(), 2)
	require.NoError(t, err)
	assert.True(t, more)
	assert.True(t, script.committed)
	assert.EqualValues(
		t,
		1,
		script.queryArgs[7][1].Value,
		"one live row leaves one raw historical row in this step",
	)
	assert.Len(
		t,
		script.queryTexts,
		8,
		"newly staged rows must wait for a committed queue before canonicalization",
	)
	last := script.execArgs[len(script.execArgs)-1]
	assert.EqualValues(t, 9, last[1].Value)
	assert.Equal(
		t,
		false,
		last[2].Value,
		"a full raw page is unfinished even if no rows belong to this owner",
	)
}

func TestSecretaryProgressProjectionDriverGivenAdvanceBoundsOrBusyWriterThenDoesNotRunUnboundedWork(
	t *testing.T,
) {
	for _, limit := range []int{-1, 0, 7, secretaryProgressReadBudget + 1} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			script := &scriptedPostgresScript{queries: []scriptedRows{
				secretaryProjectionValue("writer", true),
				{columns: []string{"owner", "message", "generation"}},
				{columns: []string{"owner"}},
			}}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			more, err := store.AdvanceSecretaryProgress(t.Context(), limit)
			require.NoError(t, err)
			assert.False(t, more)
			assert.True(t, script.committed)
			expected := int64(limit)
			if limit <= 0 || limit > secretaryProgressReadBudget {
				expected = secretaryProgressReadBudget
			}
			assert.Equal(t, expected, script.queryArgs[1][1].Value)
		})
	}
	script := &scriptedPostgresScript{
		queries: []scriptedRows{secretaryProjectionValue("writer", false)},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	more, err := store.AdvanceSecretaryProgress(t.Context(), 128)
	require.NoError(t, err)
	assert.False(t, more)
	assert.False(t, script.committed)
	assert.True(t, script.rolled)
	assert.Len(t, script.queryTexts, 1)
}

func TestSecretaryProgressProjectionDriverGivenPolicyResetFailureThenDoesNotAcknowledgeObsoleteCounts(
	t *testing.T,
) {
	for _, scenario := range []string{"insert", "version_read", "messages_delete", "sessions_delete", "reset", "success"} {
		t.Run(scenario, func(t *testing.T) {
			script := &scriptedPostgresScript{}
			if scenario != "version_read" {
				script.queries = []scriptedRows{secretaryProjectionValue("version", "obsolete")}
			}
			failureIndex := map[string]int{
				"insert":          0,
				"messages_delete": 1,
				"sessions_delete": 2,
				"reset":           3,
			}
			if index, failing := failureIndex[scenario]; failing {
				script.execErrors = make([]error, index+1)
				script.execErrors[index] = assert.AnError
			}
			err := ensureSecretaryProgressOwner(
				t.Context(),
				secretaryProjectionTx(t, script),
				"owner",
			)
			if scenario != "success" {
				assert.Error(t, err)
				assert.False(t, script.committed)
				return
			}
			require.NoError(t, err)
			require.Len(t, script.execTexts, 4)
			assert.Contains(t, script.execTexts[1], "DELETE FROM secretary_progress_messages")
			assert.Contains(t, script.execTexts[2], "DELETE FROM secretary_progress_sessions")
			assert.Contains(t, script.execTexts[3], "bootstrap_complete=FALSE")
			assert.Equal(t, secretaryProgressProjectionVersion, script.execArgs[3][1].Value)
		})
	}
}

func TestSecretaryProgressProjectionDriverGivenBootstrapPageFailureThenDoesNotAdvanceCursor(
	t *testing.T,
) {
	for _, scenario := range []string{"query", "scan", "json", "enqueue", "checkpoint", "full_page", "empty_page", "sparse_page"} {
		t.Run(scenario, func(t *testing.T) {
			script := &scriptedPostgresScript{}
			if scenario != "query" {
				rows := secretaryProjectionBootstrapRows(9, 1, `["message"]`)
				if scenario == "scan" {
					rows.values[0][0] = "invalid"
				}
				if scenario == "json" {
					rows.values[0][2] = []byte(`{`)
				}
				if scenario == "empty_page" {
					rows = secretaryProjectionBootstrapRows(0, 0, `[]`)
				}
				if scenario == "sparse_page" {
					rows = secretaryProjectionBootstrapRows(9, 1, `[]`)
				}
				script.queries = []scriptedRows{rows}
			}
			switch scenario {
			case "enqueue":
				script.execErrors = []error{assert.AnError}
			case "checkpoint":
				script.execErrors = []error{nil, assert.AnError}
			}
			err := stageSecretaryProgressBootstrap(
				t.Context(),
				secretaryProjectionTx(t, script),
				"owner",
				1,
			)
			if scenario != "full_page" && scenario != "empty_page" && scenario != "sparse_page" {
				assert.Error(t, err)
				assert.False(t, script.committed)
				return
			}
			require.NoError(t, err)
			last := script.execArgs[len(script.execArgs)-1]
			assert.Equal(t, scenario == "empty_page", last[2].Value)
			if scenario != "empty_page" {
				assert.EqualValues(t, 9, last[1].Value)
			}
		})
	}
}

func secretaryProjectionCanonicalRows(t *testing.T) scriptedRows {
	t.Helper()
	message, err := json.Marshal(
		domain.Message{
			MessageID:   "message",
			Role:        "assistant",
			MessageType: "chat",
			TurnID:      "turn",
		},
	)
	require.NoError(t, err)
	return scriptedRows{
		columns: []string{
			"message",
			"source",
			"agent",
			"session",
			"title",
			"completed",
			"raw",
			"current_hash",
			"processed_hash",
			"canonical",
			"owner",
		},
		values: [][]driver.Value{
			{
				"message",
				"1",
				"agent",
				"session",
				"title",
				nil,
				message,
				"hash",
				"hash",
				domain.SearchCanonicalizationVersion,
				"owner",
			},
		},
	}
}

func TestSecretaryProgressProjectionDriverGivenCanonicalRowsThenRejectsMalformedOrInvisibleContent(
	t *testing.T,
) {
	for _, scenario := range []string{"query", "scan", "json", "source", "hidden", "changed", "valid"} {
		t.Run(scenario, func(t *testing.T) {
			rows := secretaryProjectionCanonicalRows(t)
			switch scenario {
			case "scan":
				rows.values[0][5] = "invalid timestamp"
			case "json":
				rows.values[0][6] = []byte("{")
			case "source":
				rows.values[0][1] = "invalid"
			case "hidden":
				rows.values[0][6] = []byte(`{"role":"tool","message_type":"chat"}`)
			case "changed":
				rows.values[0][8] = "old hash"
			}
			script := &scriptedPostgresScript{}
			if scenario != "query" {
				script.queries = []scriptedRows{rows}
			}
			messages, err := secretaryProgressCurrentMessages(
				t.Context(),
				secretaryProjectionTx(t, script),
				"owner",
				`["message"]`,
			)
			if scenario == "query" || scenario == "scan" || scenario == "json" ||
				scenario == "source" {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if scenario == "hidden" {
				assert.Empty(t, messages)
				return
			}
			require.Len(t, messages, 1)
			assert.Equal(t, "turn", messages[0].turn)
			assert.EqualValues(t, 1, messages[0].source)
			assert.Equal(t, scenario == "valid", messages[0].completed)
		})
	}
}

func TestSecretaryProgressProjectionDriverGivenRefreshFailureThenDoesNotCommitHalfUpdatedSession(
	t *testing.T,
) {
	for _, scenario := range []string{"old_query", "old_scan", "current_query", "delete", "insert", "session_delete", "session_insert", "success"} {
		t.Run(scenario, func(t *testing.T) {
			script := &scriptedPostgresScript{}
			if scenario != "old_query" {
				value := driver.Value(int64(1))
				if scenario == "old_scan" {
					value = "invalid"
				}
				script.queries = []scriptedRows{secretaryProjectionValue("source", value)}
			}
			if scenario != "old_query" && scenario != "old_scan" && scenario != "current_query" {
				script.queries = append(script.queries, secretaryProjectionCanonicalRows(t))
			}
			failureIndex := map[string]int{
				"delete":         0,
				"insert":         1,
				"session_delete": 2,
				"session_insert": 3,
			}
			if index, failing := failureIndex[scenario]; failing {
				script.execErrors = make([]error, index+1)
				script.execErrors[index] = assert.AnError
			}
			err := refreshSecretaryProgressMessages(
				t.Context(),
				secretaryProjectionTx(t, script),
				"owner",
				[]string{"message"},
			)
			if scenario != "success" {
				assert.Error(t, err)
				assert.False(t, script.committed)
				return
			}
			require.NoError(t, err)
			require.Len(t, script.execTexts, 4)
			assert.Equal(t, true, script.execArgs[1][6].Value)
			assert.Contains(t, script.execTexts[3], "bool_and(completed)")
		})
	}
}

func TestSecretaryProgressProjectionDriverGivenDirtyRefreshFailureThenRetainsUnacknowledgedGeneration(
	t *testing.T,
) {
	for _, scenario := range []string{"query", "scan", "owner", "refresh", "ack", "success"} {
		t.Run(scenario, func(t *testing.T) {
			script := &scriptedPostgresScript{}
			if scenario != "query" {
				rows := secretaryProjectionDirtyRows()
				if scenario == "scan" {
					rows.values[0][2] = "invalid"
				}
				script.queries = []scriptedRows{rows}
			}
			if scenario == "owner" {
				script.execErrors = []error{assert.AnError}
			}
			if scenario == "refresh" || scenario == "ack" || scenario == "success" {
				script.queries = append(
					script.queries,
					secretaryProjectionValue("version", secretaryProgressProjectionVersion),
				)
			}
			if scenario == "ack" || scenario == "success" {
				script.queries = append(script.queries, scriptedRows{columns: []string{"source"}},
					scriptedRows{columns: secretaryProjectionCanonicalRows(t).columns})
			}
			if scenario == "ack" {
				script.execErrors = []error{nil, nil, assert.AnError}
			}
			count, err := drainSecretaryProgress(
				t.Context(),
				secretaryProjectionTx(t, script),
				"owner",
				1,
			)
			if scenario != "success" {
				assert.Error(t, err)
				assert.Zero(t, count)
				assert.False(t, script.committed)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 1, count)
			last := len(script.execTexts) - 1
			assert.Contains(t, script.execTexts[last], "generation=$3")
			assert.EqualValues(t, 7, script.execArgs[last][2].Value)
		})
	}
}

func secretaryProjectionCounterRows() scriptedRows {
	return scriptedRows{
		columns: []string{
			"as_of",
			"sessions",
			"completed_sessions",
			"partial",
			"unstarted",
			"messages",
			"completed_messages",
			"pending_messages",
			"turns",
			"completed_turns",
			"pending_turns",
			"ungrouped",
		},
		values: [][]driver.Value{
			{
				time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC),
				int64(2),
				int64(0),
				int64(2),
				int64(0),
				int64(4),
				int64(2),
				int64(2),
				int64(2),
				int64(0),
				int64(2),
				int64(0),
			},
		},
	}
}

func TestSecretaryProgressProjectionDriverGivenCommittedQueueRefreshFailureThenReturnsNoConfirmedCounts(
	t *testing.T,
) {
	for _, scenario := range []string{"begin", "writer", "busy", "dirty", "readiness", "preparing", "preparing_commit", "counters", "ready_commit", "ready"} {
		t.Run(scenario, func(t *testing.T) {
			script := &scriptedPostgresScript{queries: []scriptedRows{
				secretaryProjectionValue("writer", true),
				{columns: []string{"owner", "message", "generation"}},
				secretaryProjectionValue("ready", true),
				secretaryProjectionCounterRows(),
				{columns: []string{"source"}},
			}}
			switch scenario {
			case "begin":
				script.beginErr = assert.AnError
			case "writer":
				script.queries = nil
			case "busy":
				script.queries = []scriptedRows{secretaryProjectionValue("writer", false)}
			case "dirty":
				script.queries = script.queries[:1]
			case "readiness":
				script.queries = script.queries[:2]
			case "preparing", "preparing_commit":
				script.queries = script.queries[:3]
				script.queries[2] = secretaryProjectionValue("ready", false)
			case "counters":
				script.queries = script.queries[:3]
			}
			if scenario == "preparing_commit" || scenario == "ready_commit" {
				script.commitErr = assert.AnError
			}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			page, err := store.refreshSecretaryProgressOwner(t.Context(), "owner", "5", 1)
			if scenario == "ready" {
				require.NoError(t, err)
				assert.Equal(t, 2, page.Counts.TotalSessions)
			} else {
				assert.Error(t, err)
			}
			if scenario == "busy" || scenario == "preparing" {
				assert.ErrorIs(t, err, domain.ErrSecretaryProgressPreparing)
				assert.Empty(t, page)
			}
			commits := map[string]bool{
				"preparing":        true,
				"preparing_commit": true,
				"ready_commit":     true,
				"ready":            true,
			}
			assert.Equal(t, commits[scenario], script.committed)
		})
	}
}

func TestSecretaryProgressProjectionDriverGivenCounterFailureThenDoesNotPresentPartialPage(
	t *testing.T,
) {
	for _, scenario := range []string{"counts_query", "counts_scan", "sessions_query", "sessions_scan", "success"} {
		t.Run(scenario, func(t *testing.T) {
			script := &scriptedPostgresScript{}
			if scenario != "counts_query" {
				counts := secretaryProjectionCounterRows()
				if scenario == "counts_scan" {
					counts.values[0][1] = "invalid"
				}
				script.queries = []scriptedRows{counts}
			}
			if scenario == "sessions_scan" || scenario == "success" {
				rows := scriptedRows{
					columns: []string{
						"source",
						"agent",
						"session",
						"title",
						"state",
						"completed",
						"messages",
						"completed_messages",
						"pending_messages",
						"turns",
						"completed_turns",
						"pending_turns",
						"ungrouped",
					},
					values: [][]driver.Value{
						{
							"1",
							"agent",
							"session",
							"title",
							"partial",
							nil,
							int64(2),
							int64(1),
							int64(1),
							int64(1),
							int64(0),
							int64(1),
							int64(0),
						},
						{
							"2",
							"agent",
							"second",
							"second title",
							"partial",
							nil,
							int64(2),
							int64(1),
							int64(1),
							int64(1),
							int64(0),
							int64(1),
							int64(0),
						},
					},
				}
				if scenario == "sessions_scan" {
					rows.values[0][6] = "invalid"
				}
				script.queries = append(script.queries, rows)
			}
			page, err := readSecretaryProgressCounters(
				context.Background(),
				secretaryProjectionTx(t, script),
				"owner",
				"",
				1,
			)
			if scenario != "success" {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 2, page.Counts.TotalSessions)
			require.Len(t, page.Sessions, 1)
			after, limit, err := secretaryProgressQuery(
				"owner",
				domain.SecretaryProgressQuery{Cursor: page.NextCursor, Limit: 1},
			)
			require.NoError(t, err)
			assert.Equal(t, "1", after)
			assert.Equal(t, 1, limit)
			_, _, err = secretaryProgressQuery(
				"other",
				domain.SecretaryProgressQuery{Cursor: page.NextCursor},
			)
			assert.ErrorIs(t, err, domain.ErrInvalidCursor)
		})
	}
}
