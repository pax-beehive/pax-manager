package storage

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSummaryGivenAllCoveredMessagesWhenPublishedThenSnapshotsAndVersionCommitTogether(
	t *testing.T,
) {
	request := summaryTestRequest("old body")
	second := summaryTestRef("message_2", "second body")
	request.Coverage = append(request.Coverage, second)
	script := &scriptedPostgresScript{queries: []scriptedRows{
		summaryEmptyRows(), summaryEmptyRows(), summaryMessageRows("message_1", "old body", "owner_1"),
		summaryMessageRows("message_2", "second body", "owner_1"),
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	version, err := store.PublishSummary(t.Context(), "owner_1", request)
	require.NoError(t, err)
	assert.EqualValues(t, 1, version.Revision)
	assert.Len(t, version.Coverage, 2)
	assert.Empty(t, version.Coverage[0].ReadReceipt)
	assert.Empty(t, version.Claims[0].Evidence[0].ReadReceipt)
	assert.Equal(t, "read-proof", request.Coverage[0].ReadReceipt)
	assert.True(t, script.committed)
	assert.False(t, script.rolled)
	require.Len(t, script.execTexts, 7)
	assert.Contains(t, script.execTexts[2], "INSERT INTO summary_versions")
	assert.Contains(t, script.execTexts[3], "INSERT INTO message_snapshots")
	assert.Equal(t, "old body", script.execArgs[3][10].Value)
	assert.Contains(t, script.execTexts[5], "INSERT INTO message_snapshots")
	assert.Equal(t, "second body", script.execArgs[5][10].Value)
	assert.Contains(t, script.execTexts[4], "INSERT INTO summary_sources")
	assert.EqualValues(t, 0, script.execArgs[4][2].Value)
	assert.EqualValues(t, 1, script.execArgs[6][2].Value)
}

func TestSummaryGivenRetryWhenPayloadMatchesThenReturnsPublishedVersion(t *testing.T) {
	request := summaryTestRequest("old body")
	clean := summaryWithoutReceipts(request)
	raw, err := json.Marshal(clean)
	require.NoError(t, err)
	for _, matching := range []bool{true, false} {
		t.Run(
			map[bool]string{true: "same_payload", false: "different_payload"}[matching],
			func(t *testing.T) {
				hash := domain.SearchContentHash(string(raw))
				if !matching {
					hash = "different"
				}
				script := &scriptedPostgresScript{queries: []scriptedRows{
					{
						columns: []string{"summary_id", "revision", "request_hash"},
						values:  [][]driver.Value{{"summary_1", int64(1), hash}},
					},
				}}
				if matching {
					script.queries = append(
						script.queries,
						summaryVersionRows("summary_1", "owner_1", 1),
						summarySourceRows(),
						summaryAccessRows("owner_1", "message_1"),
						summaryMessageRows("message_1", "new body", "owner_1"),
					)
				}
				store, cleanup := scriptedPostgresStore(t, script)
				defer cleanup()
				version, publishErr := store.PublishSummary(t.Context(), "owner_1", request)
				if matching {
					require.NoError(t, publishErr)
					assert.EqualValues(t, 1, version.Revision)
					assert.True(t, script.committed)
				} else {
					assert.ErrorIs(t, publishErr, ErrConflict)
					assert.True(t, script.rolled)
				}
				assert.Len(t, script.execTexts, 2)
			},
		)
	}
}

func TestSummaryGivenChangedOrUnauthorizedSourcesWhenPublishedThenRejectsWithoutVersion(
	t *testing.T,
) {
	for _, tc := range []struct {
		name   string
		rows   []scriptedRows
		mutate func(*domain.PublishSummaryRequest)
		want   error
	}{
		{"changed_unfrozen_body", []scriptedRows{summaryEmptyRows(), summaryEmptyRows(), summaryMessageRows("message_1", "changed", "owner_1"), summaryEmptyRows()}, nil, domain.ErrSourceVersionUnavailable},
		{"other_owner", []scriptedRows{summaryEmptyRows(), summaryEmptyRows(), summaryMessageRows("message_1", "old body", "other")}, nil, ErrNotFound},
		{"deleted_message", []scriptedRows{summaryEmptyRows(), summaryEmptyRows(), summaryEmptyRows()}, nil, ErrNotFound},
		{"unsupported_quote", []scriptedRows{summaryEmptyRows(), summaryEmptyRows(), summaryMessageRows("message_1", "old body", "owner_1")}, func(r *domain.PublishSummaryRequest) { r.Claims[0].Evidence[0].Quote = "not present" }, ErrConflict},
		{"stale_revision", []scriptedRows{summaryEmptyRows(), summaryHeadRows("owner_1", "topic", "topic_1", 2)}, nil, domain.ErrSummaryVersionMismatch},
		{"unknown_expected_revision", []scriptedRows{summaryEmptyRows(), summaryEmptyRows()}, func(r *domain.PublishSummaryRequest) { r.ExpectedRevision = 1 }, domain.ErrSummaryVersionMismatch},
		{"summary_other_owner", []scriptedRows{summaryEmptyRows(), summaryHeadRows("other", "topic", "topic_1", 1)}, nil, ErrNotFound},
		{"subject_changed", []scriptedRows{summaryEmptyRows(), summaryHeadRows("owner_1", "topic", "other_topic", 1)}, nil, ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := summaryTestRequest("old body")
			if tc.mutate != nil {
				tc.mutate(&request)
			}
			script := &scriptedPostgresScript{queries: tc.rows}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			_, err := store.PublishSummary(t.Context(), "owner_1", request)
			assert.ErrorIs(t, err, tc.want)
			assert.False(t, script.committed)
			assert.True(t, script.rolled)
			assert.Len(t, script.execTexts, 2)
		})
	}
}

func TestSummaryGivenNextVersionAndFixedChildWhenPublishedThenKeepsExactReferences(t *testing.T) {
	request := summaryTestRequest("old body")
	request.ExpectedRevision = 1
	request.SummarySources = []domain.SummaryRef{{SummaryID: "child_1", Revision: 3}}
	script := &scriptedPostgresScript{queries: []scriptedRows{
		summaryEmptyRows(), summaryHeadRows("owner_1", "topic", "topic_1", 1),
		summaryMessageRows("message_1", "old body", "owner_1"),
		summaryVersionRows("child_1", "owner_1", 3), summarySourceRows(),
		summaryAccessRows(
			"owner_1",
			"message_1",
		), summaryMessageRows("message_1", "new body", "owner_1"),
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	result, err := store.PublishSummary(t.Context(), "owner_1", request)
	require.NoError(t, err)
	assert.EqualValues(t, 2, result.Revision)
	assert.Equal(t, request.SummarySources, result.SummarySources)
	assert.Equal(t, "child_1", script.execArgs[5][3].Value)
	assert.EqualValues(t, 3, script.execArgs[5][4].Value)
}

func TestSummaryGivenHistoricalSnapshotWhenSourceChangesThenExactBodyCanStillBeRead(t *testing.T) {
	for _, tc := range []struct {
		name, currentOwner, snapshotOwner, snapshotBody string
		missing, publish                                bool
		want                                            error
	}{
		{name: "old_body", currentOwner: "owner_1", snapshotOwner: "owner_1", snapshotBody: "old body"},
		{name: "freeze_old_body", currentOwner: "owner_1", snapshotOwner: "owner_1", snapshotBody: "old body", publish: true},
		{name: "snapshot_missing", currentOwner: "owner_1", missing: true, want: domain.ErrSourceVersionUnavailable},
		{name: "snapshot_corrupted", currentOwner: "owner_1", snapshotOwner: "owner_1", snapshotBody: "bad body", want: domain.ErrSourceVersionUnavailable},
		{name: "snapshot_other_owner", currentOwner: "owner_1", snapshotOwner: "other", snapshotBody: "old body", want: ErrNotFound},
		{name: "access_revoked", currentOwner: "other", want: ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rows []scriptedRows
			if tc.publish {
				rows = append(rows, summaryEmptyRows(), summaryEmptyRows())
			}
			rows = append(rows, summaryMessageRows("message_1", "new body", tc.currentOwner))
			if tc.currentOwner == "owner_1" {
				if tc.missing {
					rows = append(rows, summaryEmptyRows())
				} else {
					rows = append(rows, summarySnapshotRows(tc.snapshotOwner, tc.snapshotBody))
				}
			}
			script := &scriptedPostgresScript{queries: rows}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			if tc.publish {
				_, err := store.PublishSummary(
					t.Context(),
					"owner_1",
					summaryTestRequest("old body"),
				)
				require.NoError(t, err)
				assert.Equal(t, "old body", script.execArgs[3][10].Value)
				return
			}
			doc, err := store.ReadMessageVersion(
				t.Context(),
				"owner_1",
				summaryTestRef("message_1", "old body"),
			)
			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "old body", doc.Body)
			assert.Equal(t, "assistant", doc.Role)
			assert.EqualValues(t, 7, doc.SessionSeq)
			assert.True(t, script.committed)
		})
	}
}

func TestSummaryGivenCurrentVersionWhenReadThenNoHistoricalSnapshotRequired(t *testing.T) {
	script := &scriptedPostgresScript{
		queries: []scriptedRows{summaryMessageRows("message_1", "old body", "owner_1")},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	doc, err := store.ReadMessageVersion(
		t.Context(),
		"owner_1",
		summaryTestRef("message_1", "old body"),
	)
	require.NoError(t, err)
	assert.Equal(t, "old body", doc.Body)
	assert.Len(t, script.queryTexts, 1)
}

func TestSummaryGivenNestedSourcesWhenReadThenCurrentAccessToEveryLeafIsRequired(t *testing.T) {
	for _, owner := range []string{"owner_1", "other"} {
		t.Run(owner, func(t *testing.T) {
			script := &scriptedPostgresScript{queries: []scriptedRows{
				summaryVersionRows("summary_1", "owner_1", 1), summarySourceRows(),
				summaryAccessRows(
					"owner_1",
					"message_1",
				), summaryMessageRows("message_1", "new body", owner),
			}}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			version, err := store.GetSummary(t.Context(), "owner_1", "summary_1", 1)
			if owner == "other" {
				assert.ErrorIs(t, err, ErrNotFound)
				assert.True(t, script.rolled)
				return
			}
			require.NoError(t, err)
			assert.EqualValues(t, 1, version.Revision)
			require.Len(t, version.Coverage, 1)
			assert.Equal(t, "message_1", version.Coverage[0].MessageID)
			require.Len(t, version.SummarySources, 1)
			assert.EqualValues(t, 3, version.SummarySources[0].Revision)
			assert.Contains(t, script.queryTexts[2], "WITH RECURSIVE")
		})
	}
}

func TestSummaryGivenTruncatedDependencySetThenRejectsBeforeReadingAnyLeaf(t *testing.T) {
	access := summaryAccessRows("owner_1", "")
	for i := range 4096 {
		access.values = append(
			access.values,
			[]driver.Value{"owner_1", fmt.Sprintf("message_%d", i)},
		)
	}
	script := &scriptedPostgresScript{queries: []scriptedRows{
		summaryVersionRows("summary_1", "owner_1", 1), summarySourceRows(), access,
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	_, err := store.GetSummary(t.Context(), "owner_1", "summary_1", 1)
	assert.ErrorIs(t, err, ErrConflict)
	assert.Len(t, script.queryTexts, 3)
	assert.False(t, script.committed)
	assert.True(t, script.rolled)
}

func TestSummaryGivenMissingDependencyVersionThenRejectsAccess(t *testing.T) {
	script := &scriptedPostgresScript{queries: []scriptedRows{
		summaryVersionRows(
			"summary_1",
			"owner_1",
			1,
		), summarySourceRows(), summaryAccessRows("", ""),
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	_, err := store.GetSummary(t.Context(), "owner_1", "summary_1", 1)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.False(t, script.committed)
	assert.True(t, script.rolled)
}

func TestSummaryGivenCurrentSummaryWhenIndexedThenReturnsCurrentTextAfterAccessCheck(t *testing.T) {
	script := &scriptedPostgresScript{
		queries: []scriptedRows{summaryVersionRows("summary_1", "owner_1", 2),
			summaryAccessRows(
				"owner_1",
				"message_1",
			), summaryMessageRows("message_1", "new body", "owner_1")},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	doc, err := loadSummarySearchDocument(t.Context(), tx, "summary_1")
	require.NoError(t, err)
	assert.Equal(t, "summary", doc.Kind)
	assert.EqualValues(t, 2, doc.SummaryRevision)
	assert.Equal(t, domain.SearchContentHash("Summary body"), doc.ContentHash)
}

func TestSummaryGivenInvalidInputsAndStorageFailuresThenErrorsRemainExplicit(t *testing.T) {
	script := &scriptedPostgresScript{}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	_, err := store.PublishSummary(t.Context(), "", summaryTestRequest("old body"))
	assert.ErrorIs(t, err, ErrUnauthorized)
	_, err = store.PublishSummary(t.Context(), "owner_1", domain.PublishSummaryRequest{})
	assert.ErrorIs(t, err, ErrConflict)
	_, err = store.GetSummary(t.Context(), "", "s", 1)
	assert.ErrorIs(t, err, ErrUnauthorized)
	_, err = store.GetSummary(t.Context(), "owner_1", "", 1)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.GetSummary(t.Context(), "owner_1", "s", -1)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.ReadMessageVersion(t.Context(), "", summaryTestRef("m", "b"))
	assert.ErrorIs(t, err, ErrUnauthorized)
	_, err = store.ReadMessageVersion(t.Context(), "owner_1", domain.MessageVersionRef{})
	assert.ErrorIs(t, err, domain.ErrSourceVersionUnavailable)
	_, err = store.PublishSummary(t.Context(), "owner_1", summaryTestRequest("old body"))
	assert.ErrorContains(t, err, "unexpected query")
	_, err = store.GetSummary(t.Context(), "owner_1", "s", 1)
	assert.ErrorContains(t, err, "unexpected query")
	_, err = store.ReadMessageVersion(t.Context(), "owner_1", summaryTestRef("m", "b"))
	assert.ErrorContains(t, err, "unexpected query")
	assert.True(t, summaryRetryable(&pgconn.PgError{Code: "40001"}))
	assert.True(t, summaryRetryable(&pgconn.PgError{Code: "23505"}))
	assert.False(t, summaryRetryable(errors.New("other")))
	assert.False(t, summaryRetryable(nil))
}

func TestSummaryGivenMissingMalformedOrRevokedDependencyWhenReadThenFailsClosed(t *testing.T) {
	for _, rows := range [][]scriptedRows{
		{summaryEmptyRows()},
		{{columns: []string{"summary_id"}, values: [][]driver.Value{{"short"}}}},
		{summaryVersionRows("s", "owner_1", 1)},
		{summaryVersionRows("s", "owner_1", 1), summarySourceRows()},
		{summaryVersionRows("s", "owner_1", 1), summarySourceRows(), summaryAccessRows("other", "message_1")},
		{summaryVersionRows("s", "owner_1", 1), summarySourceRows(), summaryAccessRows("owner_1", "message_1"), summaryEmptyRows()},
	} {
		script := &scriptedPostgresScript{queries: rows}
		store, cleanup := scriptedPostgresStore(t, script)
		_, err := store.GetSummary(t.Context(), "owner_1", "s", 1)
		assert.Error(t, err)
		assert.False(t, script.committed)
		cleanup()
	}
}

func summaryTestRequest(body string) domain.PublishSummaryRequest {
	ref := summaryTestRef("message_1", body)
	ref.ReadReceipt = "read-proof"
	return domain.PublishSummaryRequest{
		SummaryID:   "summary_1",
		SubjectType: "topic",
		SubjectID:   "topic_1",
		RequestID:   "request_1",
		Title:       "Summary title",
		Body:        "Summary body",
		Coverage:    []domain.MessageVersionRef{ref},
		Claims: []domain.SummaryClaim{
			{
				ID:   "claim_1",
				Text: "Summary body",
				Evidence: []domain.SummaryEvidence{
					{Message: ref, Quote: "body", ReadReceipt: "quote-proof"},
				},
			},
		},
	}
}

func summaryTestRef(id, body string) domain.MessageVersionRef {
	return domain.MessageVersionRef{
		MessageID:               id,
		ContentHash:             domain.SearchContentHash(body),
		CanonicalizationVersion: domain.SearchCanonicalizationVersion,
	}
}

func summaryEmptyRows() scriptedRows { return scriptedRows{columns: []string{"empty"}} }

func summaryHeadRows(owner, subjectType, subjectID string, revision int64) scriptedRows {
	return scriptedRows{
		columns: []string{"owner", "type", "id", "revision"},
		values:  [][]driver.Value{{owner, subjectType, subjectID, revision}},
	}
}

func summaryMessageRows(id, body, owner string) scriptedRows {
	message, _ := json.Marshal(
		domain.Message{
			MessageID:  id,
			AgentID:    "agent_1",
			SessionID:  "session_1",
			Role:       "assistant",
			SessionSeq: 7,
			CreatedAt:  time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
		},
	)
	parts, _ := json.Marshal(
		[]domain.MessagePart{
			{MessageID: id, PartIndex: 0, PartType: domain.MessagePartText, Text: body},
		},
	)
	return scriptedRows{
		columns: []string{"message", "owner", "parts", "revision"},
		values:  [][]driver.Value{{message, owner, parts, int64(5)}},
	}
}

func summarySnapshotRows(owner, body string) scriptedRows {
	return scriptedRows{
		columns: []string{
			"owner",
			"agent",
			"session",
			"conversation",
			"role",
			"seq",
			"created",
			"body",
		},
		values: [][]driver.Value{
			{
				owner,
				"agent_1",
				"session_1",
				"conv_1",
				"assistant",
				int64(7),
				time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
				body,
			},
		},
	}
}

func summaryVersionRows(id, owner string, revision int64) scriptedRows {
	return scriptedRows{
		columns: []string{
			"id",
			"revision",
			"owner",
			"type",
			"subject",
			"title",
			"body",
			"claims",
			"created",
		},
		values: [][]driver.Value{
			{
				id,
				revision,
				owner,
				"topic",
				"topic_1",
				"Summary title",
				"Summary body",
				[]byte("[]"),
				time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
			},
		},
	}
}

func summarySourceRows() scriptedRows {
	ref := summaryTestRef("message_1", "old body")
	return scriptedRows{
		columns: []string{"kind", "message", "canonical", "hash", "child", "revision"},
		values: [][]driver.Value{
			{"message", ref.MessageID, ref.CanonicalizationVersion, ref.ContentHash, "", int64(0)},
			{"summary", "", "", "", "child_1", int64(3)},
		},
	}
}

func summaryAccessRows(owner, message string) scriptedRows {
	return scriptedRows{
		columns: []string{"owner", "message"},
		values:  [][]driver.Value{{owner, message}},
	}
}
