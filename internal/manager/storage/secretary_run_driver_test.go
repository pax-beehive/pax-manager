package storage

import (
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func secretaryRunReceiptRows(value any) scriptedRows {
	rows := scriptedRows{columns: []string{"accepted_at"}}
	if value != nil {
		rows.values = [][]driver.Value{{value}}
	}
	return rows
}

func secretaryRunSettingsRows(enabled bool, revision int64) scriptedRows {
	return scriptedRows{
		columns: []string{"enabled", "revision"},
		values:  [][]driver.Value{{enabled, revision}},
	}
}

func TestSecretaryRunDriverGivenAcceptanceOrReplayThenReturnsDurableReceiptWithoutSourceReads(
	t *testing.T,
) {
	accepted := time.Date(2026, 11, 11, 22, 0, 0, 0, time.UTC)
	for _, mode := range []string{"new", "replay", "conflict", "disabled-replay", "disabled", "missing-settings"} {
		t.Run(mode, func(t *testing.T) {
			queries := []scriptedRows{
				secretaryRunReceiptRows(nil),
				secretaryRunSettingsRows(true, 3),
				secretaryRunReceiptRows(accepted),
			}
			switch mode {
			case "replay":
				queries = []scriptedRows{secretaryRunReceiptRows(accepted)}
			case "conflict":
				queries = append(
					queries[:2],
					secretaryRunReceiptRows(nil),
					secretaryRunReceiptRows(accepted),
				)
			case "disabled-replay":
				queries[1] = secretaryRunSettingsRows(false, 4)
			case "disabled":
				queries[1], queries[2] = secretaryRunSettingsRows(
					false,
					4,
				), secretaryRunReceiptRows(
					nil,
				)
			case "missing-settings":
				queries[1], queries[2] = scriptedRows{
					columns: []string{"enabled", "revision"},
				}, secretaryRunReceiptRows(
					nil,
				)
			}
			script := &scriptedPostgresScript{queries: queries}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			receipt, err := store.RequestSecretaryRun(t.Context(), "owner", "stable-id")
			if mode == "disabled" || mode == "missing-settings" {
				assert.ErrorIs(t, err, ErrConflict)
				assert.True(t, script.rolled)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, accepted, receipt.AcceptedAt)
			assert.Equal(t, "accepted", receipt.State)
			assert.Equal(t, "stable-id", receipt.RequestID)
			assert.True(t, script.committed)
			all := strings.Join(append(script.queryTexts, script.execTexts...), "\n")
			for _, forbidden := range []string{"FROM messages", "message_parts", "secretary_message_state", "UPDATE secretary_jobs", "pending_jobs"} {
				assert.NotContains(t, all, forbidden)
			}
			assert.Contains(t, all, "lock_timeout='250ms'")
		})
	}
}

func TestSecretaryRunDriverGivenAcceptancePersistenceFailureThenDoesNotReturnReceipt(t *testing.T) {
	accepted := time.Now()
	base := []scriptedRows{
		secretaryRunReceiptRows(nil),
		secretaryRunSettingsRows(true, 3),
		secretaryRunReceiptRows(accepted),
	}
	for _, mode := range []string{"begin", "timeout-setting", "receipt-read", "settings-read", "insert", "invalid-receipt", "commit", "disabled-read", "duplicate-read"} {
		t.Run(mode, func(t *testing.T) {
			script := &scriptedPostgresScript{queries: append([]scriptedRows(nil), base...)}
			switch mode {
			case "begin":
				script.beginErr = errors.New("begin failed")
			case "timeout-setting":
				script.execErrors = []error{errors.New("setting failed")}
			case "receipt-read":
				script.queries = nil
			case "settings-read":
				script.queries = script.queries[:1]
			case "insert":
				script.queries = script.queries[:2]
			case "invalid-receipt":
				script.queries[0] = secretaryRunReceiptRows("invalid-time")
			case "commit":
				script.commitErr = errors.New("commit failed")
			case "disabled-read":
				script.queries = []scriptedRows{
					secretaryRunReceiptRows(nil),
					secretaryRunSettingsRows(false, 3),
				}
			case "duplicate-read":
				script.queries[2] = secretaryRunReceiptRows(nil)
			}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			_, err := store.RequestSecretaryRun(t.Context(), "owner", "request")
			assert.Error(t, err)
			if mode != "begin" && mode != "commit" {
				assert.True(t, script.rolled)
			}
		})
	}
}

func secretaryRunAdvanceRows() []scriptedRows {
	return []scriptedRows{
		{columns: []string{"owner", "request"}, values: [][]driver.Value{{"owner", "request"}}},
		{columns: []string{"locked"}, values: [][]driver.Value{{true}}},
		secretaryRunSettingsRows(true, 3),
		{
			columns: []string{"settings", "version", "upper", "after"},
			values:  [][]driver.Value{{int64(3), secretaryRunCommandVersion, int64(10), int64(0)}},
		},
		{
			columns: []string{"after", "visited", "queued"},
			values:  [][]driver.Value{{int64(10), int64(10), int64(3)}},
		},
	}
}

func TestSecretaryRunDriverGivenBoundedMetadataStepThenCommitsCursorAndJobsTogether(t *testing.T) {
	for _, mode := range []string{"advance", "empty", "busy", "disabled", "settings-missing", "revision", "version", "command-missing"} {
		t.Run(mode, func(t *testing.T) {
			queries := secretaryRunAdvanceRows()
			switch mode {
			case "empty":
				queries = []scriptedRows{{columns: []string{"owner", "request"}}}
			case "busy":
				queries[1].values[0][0] = false
			case "disabled":
				queries[2] = secretaryRunSettingsRows(false, 3)
			case "settings-missing":
				queries[2] = scriptedRows{columns: []string{"enabled", "revision"}}
			case "revision":
				queries[2] = secretaryRunSettingsRows(true, 4)
			case "version":
				queries[3].values[0][1] = "old-policy"
			case "command-missing":
				queries[3].values = nil
			}
			script := &scriptedPostgresScript{queries: queries}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			worked, err := store.AdvanceSecretaryRuns(t.Context(), 10000)
			require.NoError(t, err)
			assert.Equal(t, mode != "empty" && mode != "busy" && mode != "command-missing", worked)
			assert.True(t, script.committed)
			if mode == "advance" {
				assert.EqualValues(t, 128, script.queryArgs[4][3].Value)
				assert.Contains(
					t,
					script.execTexts[1],
					"source_after=$3,queued_count=queued_count+$4",
				)
				assert.EqualValues(t, 10, script.execArgs[1][2].Value)
				assert.EqualValues(t, 3, script.execArgs[1][3].Value)
				assert.Equal(t, true, script.execArgs[1][4].Value)
			}
			assert.NotContains(t, secretaryRunPageSQL, "SKIP LOCKED")
			assert.NotContains(t, secretaryRunPageSQL, "message_parts")
			assert.NotContains(t, secretaryRunPageSQL, "desired_revision=")
		})
	}
}

func TestSecretaryRunDriverGivenMetadataStepFailureThenRollsBackCursor(t *testing.T) {
	for _, mode := range []string{"begin", "timeout-setting", "candidates", "candidate-scan", "lock", "lock-scan", "settings", "command", "page", "page-scan", "cursor", "cancel", "commit"} {
		t.Run(mode, func(t *testing.T) {
			script := &scriptedPostgresScript{queries: secretaryRunAdvanceRows()}
			switch mode {
			case "begin":
				script.beginErr = errors.New("begin failed")
			case "timeout-setting":
				script.execErrors = []error{errors.New("setting failed")}
			case "candidates":
				script.queries = nil
			case "candidate-scan":
				script.queries[0].values[0][0] = nil
			case "lock":
				script.queries = script.queries[:1]
			case "lock-scan":
				script.queries[1].values[0][0] = "invalid-bool"
			case "settings":
				script.queries = script.queries[:2]
			case "command":
				script.queries = script.queries[:3]
			case "page":
				script.queries = script.queries[:4]
			case "page-scan":
				script.queries[4].values[0][0] = "invalid-number"
			case "cursor":
				script.execErrors = []error{nil, errors.New("cursor failed")}
			case "cancel":
				script.queries[2] = secretaryRunSettingsRows(false, 3)
				script.execErrors = []error{nil, errors.New("cancel failed")}
			case "commit":
				script.commitErr = errors.New("commit failed")
			}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			_, err := store.AdvanceSecretaryRuns(t.Context(), 2)
			assert.Error(t, err)
			if mode != "begin" && mode != "commit" {
				assert.True(t, script.rolled)
			}
		})
	}
}
