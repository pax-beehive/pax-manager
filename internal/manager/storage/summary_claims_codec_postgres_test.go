package storage

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSummaryClaimsPostgresGivenNewRevisionsThenStoresLocalKeysAndReadsExactHistoricalSources(
	t *testing.T,
) {
	store := summaryPostgresFixture(t)
	request := summaryTestRequest("old body")
	second := summaryTestRef("message_2", "another message")
	request.Coverage = append(request.Coverage, second)
	request.Claims[0].Kind = "decision"
	request.Claims[0].Basis = "user_explicit"
	request.Claims[0].State = "active"
	request.Claims[0].Supersedes = []string{"old_claim"}
	request.Claims[0].Evidence[0].View = "last"
	request.Claims[0].Evidence = append(
		request.Claims[0].Evidence,
		domain.SummaryEvidence{Message: second, Quote: "another", View: "full"},
	)
	first, err := store.PublishSummary(t.Context(), "owner_1", request)
	require.NoError(t, err)
	expected := summaryWithoutReceipts(request)
	assert.Equal(t, expected.Claims, first.Claims)
	var stored []byte
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT claims_json FROM summary_versions WHERE summary_id=$1 AND revision=1`, request.SummaryID).
			Scan(&stored),
	)
	compact, err := domain.EncodeCompactSummaryClaims(request.Claims, request.Coverage)
	require.NoError(t, err)
	assert.JSONEq(t, string(compact), string(stored))
	assert.NotContains(t, string(stored), "message_id")
	assert.NotContains(t, string(stored), "content_hash")
	assert.NotContains(t, string(stored), "read_receipt")
	legacy, err := json.Marshal(expected.Claims)
	require.NoError(t, err)
	assert.Less(t, len(compact), len(legacy))

	request.RequestID = "request_2"
	request.ExpectedRevision = 1
	request.Body = "Updated summary"
	request.Coverage[0], request.Coverage[1] = request.Coverage[1], request.Coverage[0]
	_, err = store.PublishSummary(t.Context(), "owner_1", request)
	require.NoError(t, err)
	for _, revision := range []int64{1, 2} {
		version, readErr := store.GetSummary(t.Context(), "owner_1", request.SummaryID, revision)
		require.NoError(t, readErr)
		assert.Equal(t, expected.Claims, version.Claims)
		if revision == 1 {
			assert.Equal(t, expected.Coverage, version.Coverage)
		} else {
			assert.Equal(t, []domain.MessageVersionRef{second, expected.Coverage[0]}, version.Coverage)
		}
	}
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT claims_json FROM summary_versions WHERE summary_id=$1 AND revision=2`, request.SummaryID).
			Scan(&stored),
	)
	var keys []struct {
		Evidence []struct {
			SourceKey int `json:"source_key"`
		} `json:"evidence"`
	}
	require.NoError(t, json.Unmarshal(stored, &keys))
	assert.Equal(t, 1, keys[0].Evidence[0].SourceKey)
	assert.Equal(t, 0, keys[0].Evidence[1].SourceKey)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text='new body' WHERE message_id='message_1'`,
	)
	require.NoError(t, err)
	snapshot, err := store.ReadMessageVersion(t.Context(), "owner_1", expected.Coverage[0])
	require.NoError(t, err)
	assert.Equal(t, "old body", snapshot.Body)
	_, err = store.GetSummary(t.Context(), "other_owner", request.SummaryID, 1)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE agent_sessions SET archived_at=now() WHERE session_id='session_1'`,
	)
	require.NoError(t, err)
	_, err = store.GetSummary(t.Context(), "owner_1", request.SummaryID, 1)
	assert.ErrorIs(t, err, ErrNotFound)
}

func insertClaimsCodecFixture(
	t *testing.T,
	store *PostgresStore,
	request domain.PublishSummaryRequest,
	claims []byte,
	order int,
) {
	t.Helper()
	raw, err := json.Marshal(summaryWithoutReceipts(request))
	require.NoError(t, err)
	_, err = store.db.ExecContext(
		t.Context(),
		`INSERT INTO summary_versions(summary_id,revision,owner_user_id,subject_type,subject_id,title,body,claims_json,request_id,request_hash,created_at) VALUES($1,1,'owner_1',$2,$3,$4,$5,$6,$7,$8,now())`,
		request.SummaryID,
		request.SubjectType,
		request.SubjectID,
		request.Title,
		request.Body,
		claims,
		request.RequestID,
		domain.SearchContentHash(string(raw)),
	)
	require.NoError(t, err)
	ref := request.Coverage[0]
	_, err = store.db.ExecContext(
		t.Context(),
		`INSERT INTO message_snapshots(message_id,canonicalization_version,content_hash,owner_user_id,agent_id,session_id,message_created_at,body,created_at) VALUES($1,$2,$3,'owner_1','agent_1','session_1',now(),'old body',now())`,
		ref.MessageID,
		ref.CanonicalizationVersion,
		ref.ContentHash,
	)
	require.NoError(t, err)
	_, err = store.db.ExecContext(
		t.Context(),
		`INSERT INTO summary_sources(summary_id,revision,source_order,source_kind,message_id,canonicalization_version,content_hash) VALUES($1,1,$2,'message',$3,$4,$5)`,
		request.SummaryID,
		order,
		ref.MessageID,
		ref.CanonicalizationVersion,
		ref.ContentHash,
	)
	require.NoError(t, err)
}

func TestSummaryClaimsPostgresGivenLegacyRevisionThenReadsAndReplaysWithoutRewriting(t *testing.T) {
	store := summaryPostgresFixture(t)
	request := summaryWithoutReceipts(summaryTestRequest("old body"))
	request.SummaryID = "legacy"
	request.RequestID = "legacy_request"
	legacy, err := json.Marshal(request.Claims)
	require.NoError(t, err)
	insertClaimsCodecFixture(t, store, request, legacy, 0)
	version, err := store.GetSummary(t.Context(), "owner_1", request.SummaryID, 1)
	require.NoError(t, err)
	assert.Equal(t, request.Claims, version.Claims)
	replayed, err := store.PublishSummary(t.Context(), "owner_1", request)
	require.NoError(t, err)
	assert.Equal(t, version, replayed)
	var stored []byte
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT claims_json FROM summary_versions WHERE summary_id=$1`, request.SummaryID).
			Scan(&stored),
	)
	assert.JSONEq(t, string(legacy), string(stored))
	assert.NotContains(t, string(stored), "source_key")
}

func TestSummaryClaimsPostgresGivenBadLocalReferenceThenFailsClosed(t *testing.T) {
	for _, mode := range []string{"outside_revision", "source_order_gap", "conflicting_forms"} {
		t.Run(mode, func(t *testing.T) {
			store := summaryPostgresFixture(t)
			request := summaryWithoutReceipts(summaryTestRequest("old body"))
			raw := []byte(
				`[{"id":"claim_1","text":"Fact","evidence":[{"source_key":1,"quote":"body","view":"full"}]}]`,
			)
			order := 0
			if mode == "source_order_gap" {
				raw = []byte(
					`[{"id":"claim_1","text":"Fact","evidence":[{"source_key":0,"quote":"body"}]}]`,
				)
				order = 1
			}
			if mode == "conflicting_forms" {
				raw = []byte(
					`[{"id":"claim_1","text":"Fact","evidence":[{"source_key":0,"message":null,"quote":"body"}]}]`,
				)
			}
			insertClaimsCodecFixture(t, store, request, raw, order)
			version, err := store.GetSummary(t.Context(), "owner_1", request.SummaryID, 1)
			assert.ErrorIs(t, err, ErrConflict)
			assert.Empty(t, version.SummaryID)
		})
	}
}
