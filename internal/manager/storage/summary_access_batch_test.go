package storage

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSummaryAccessGivenTwentySummariesWithSixtyFourLeavesThenUsesEightyOneQueries(
	t *testing.T,
) {
	refs := scriptedRows{columns: []string{"summary_id", "revision", "created_at"}}
	script := &scriptedPostgresScript{queries: []scriptedRows{refs}}
	for summary := range 20 {
		id := fmt.Sprintf("summary_%02d", summary)
		script.queries[0].values = append(
			script.queries[0].values,
			[]driver.Value{id, int64(1), time.Now()},
		)
		access := summaryAccessRows("owner_1", "")
		access.values = nil
		messages := summaryMessageRows("", "", "")
		messages.values = nil
		for message := range 64 {
			messageID := fmt.Sprintf("message_%d_%d", summary, message)
			access.values = append(access.values, []driver.Value{"owner_1", messageID, false})
			messages.values = append(
				messages.values,
				summaryMessageRows(messageID, "body", "owner_1").values...)
		}
		script.queries = append(
			script.queries,
			summaryVersionRows(id, "owner_1", 1),
			summarySourceRows(),
			access,
			messages,
		)
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	page, err := store.ListRecentSummaries(t.Context(), "owner_1", domain.SummaryListQuery{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 20)
	assert.Len(t, script.queryTexts, 81)
}

func TestSummaryAccessGivenManyLeafMessagesThenUsesOneCanonicalVisibilityQuery(t *testing.T) {
	access := summaryAccessRows("owner_1", "message_1")
	messages := summaryMessageRows("message_1", "visible body", "owner_1")
	for i := 2; i <= 64; i++ {
		id := fmt.Sprintf("message_%d", i)
		access.values = append(access.values, []driver.Value{"owner_1", id, false})
		messages.values = append(
			messages.values,
			summaryMessageRows(id, "visible body", "owner_1").values...)
	}
	// The same leaf can be reached through multiple pinned child summaries.
	access.values = append(access.values, []driver.Value{"owner_1", "message_1", false})
	script := &scriptedPostgresScript{queries: []scriptedRows{
		summaryVersionRows("summary_1", "owner_1", 1), summarySourceRows(), access, messages,
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	_, err := store.GetSummary(t.Context(), "owner_1", "summary_1", 1)
	require.NoError(t, err)
	require.Len(t, script.queryTexts, 4)
	assert.Contains(t, script.queryTexts[3], "jsonb_array_elements_text($1::jsonb)")
	assert.Contains(t, script.queryTexts[3], "s.archived_at IS NULL")
	assert.Contains(t, script.queryTexts[3], "replaces_message_ids")
	assert.NotContains(t, script.queryTexts[3], "desired_revision")
	var ids []string
	require.NoError(t, json.Unmarshal([]byte(script.queryArgs[3][0].Value.(string)), &ids))
	assert.Len(t, ids, 64)
}

func TestSummaryAccessGivenHiddenMalformedBlankOrMissingLeafThenNeverReturnsSummary(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*scriptedRows)
	}{
		{"missing", func(rows *scriptedRows) { rows.values = nil }},
		{"foreign owner", func(rows *scriptedRows) { rows.values[0][1] = "other" }},
		{"invalid metadata", func(rows *scriptedRows) { rows.values[0][0] = []byte(`{"message_id":"message_1","role":"tool"}`) }},
		{"hidden envelope", func(rows *scriptedRows) {
			rows.values[0][0] = []byte(`{"message_id":"message_1","role":"assistant","raw_json":{"params":{"update":{"channel":"analysis"}}}}`)
		}},
		{"invalid raw envelope", func(rows *scriptedRows) {
			rows.values[0][0] = []byte(`{"message_id":"message_1","role":"assistant","raw_json":[]}`)
		}},
		{"blank text", func(rows *scriptedRows) {
			rows.values[0][2] = []byte(`[{"part_index":0,"part_type":"text","text":"\u3000\u00a0 \n"}]`)
		}},
		{"negative index", func(rows *scriptedRows) {
			rows.values[0][2] = []byte(`[{"part_index":-1,"part_type":"text","text":"body"}]`)
		}},
		{"duplicate index", func(rows *scriptedRows) {
			rows.values[0][2] = []byte(`[{"part_index":0,"part_type":"text","text":"body"},{"part_index":0,"part_type":"text","text":"body"}]`)
		}},
		{"invalid message JSON", func(rows *scriptedRows) { rows.values[0][0] = []byte(`invalid`) }},
		{"invalid parts JSON", func(rows *scriptedRows) { rows.values[0][2] = []byte(`invalid`) }},
		{"invalid scan", func(rows *scriptedRows) { rows.values[0][3] = "invalid" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			messages := summaryMessageRows("message_1", "current body", "owner_1")
			test.edit(&messages)
			script := &scriptedPostgresScript{queries: []scriptedRows{
				summaryVersionRows(
					"summary_1",
					"owner_1",
					1,
				), summarySourceRows(), summaryAccessRows("owner_1", "message_1"), messages,
			}}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			_, err := store.GetSummary(t.Context(), "owner_1", "summary_1", 1)
			require.Error(t, err)
			assert.False(t, script.committed)
			assert.True(t, script.rolled)
			if test.name != "invalid message JSON" && test.name != "invalid parts JSON" &&
				test.name != "invalid scan" {
				assert.ErrorIs(t, err, domain.ErrNotFound)
			}
		})
	}
}
