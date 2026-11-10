package storage

import (
	"database/sql/driver"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSummaryClaimsGivenCompactEncodingThenRestoresExactSourcesAndMetadata(t *testing.T) {
	request := summaryWithoutReceipts(summaryTestRequest("old body"))
	second := summaryTestRef("second", "different body")
	request.Coverage = append(request.Coverage, second)
	request.Claims[0].Kind = "decision"
	request.Claims[0].Basis = "user_explicit"
	request.Claims[0].State = "active"
	request.Claims[0].Supersedes = []string{"earlier"}
	request.Claims[0].Evidence[0].View = "last"
	request.Claims[0].Evidence = append(
		request.Claims[0].Evidence,
		domain.SummaryEvidence{Message: second, Quote: "different", View: "full"},
	)
	raw, err := domain.EncodeCompactSummaryClaims(request.Claims, request.Coverage)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "content_hash")
	assert.Contains(t, string(raw), `"source_key":0`)
	assert.Contains(t, string(raw), `"source_key":1`)
	claims, err := decodeSummaryClaims(raw, request.Coverage)
	require.NoError(t, err)
	assert.Equal(t, request.Claims, claims)
	legacy, err := json.Marshal(request.Claims)
	require.NoError(t, err)
	decoded, err := decodeSummaryClaims(legacy, request.Coverage)
	require.NoError(t, err)
	assert.Equal(t, claims, decoded)
	assert.Less(t, len(raw), len(legacy))
}

func TestSummaryClaimsGivenPublicationThenWritesCompactClaimsAndKeepsRequestHash(t *testing.T) {
	request := summaryTestRequest("old body")
	script := &scriptedPostgresScript{
		queries: []scriptedRows{
			summaryEmptyRows(),
			summaryEmptyRows(),
			summaryMessageRows("message_1", "old body", "owner_1"),
		},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	_, err := store.PublishSummary(t.Context(), "owner_1", request)
	require.NoError(t, err)
	expected, err := domain.EncodeCompactSummaryClaims(request.Claims, request.Coverage)
	require.NoError(t, err)
	assert.Equal(t, string(expected), string(script.execArgs[2][7].Value.([]byte)))
	original, err := json.Marshal(summaryWithoutReceipts(request))
	require.NoError(t, err)
	assert.Equal(t, domain.SearchContentHash(string(original)), script.execArgs[2][9].Value)
}

func TestSummaryClaimsGivenStoredCompactOrLegacyClaimsThenReadsFullReferences(t *testing.T) {
	request := summaryWithoutReceipts(summaryTestRequest("old body"))
	compact, err := domain.EncodeCompactSummaryClaims(request.Claims, request.Coverage)
	require.NoError(t, err)
	legacy, err := json.Marshal(request.Claims)
	require.NoError(t, err)
	for name, raw := range map[string][]byte{"compact": compact, "legacy": legacy} {
		t.Run(name, func(t *testing.T) {
			version := summaryVersionRows("summary_1", "owner_1", 1)
			version.values[0][7] = raw
			script := &scriptedPostgresScript{
				queries: []scriptedRows{
					version,
					summarySourceRows(),
					summaryAccessRows("owner_1", "message_1"),
					summaryMessageRows("message_1", "new body", "owner_1"),
				},
			}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			actual, err := store.GetSummary(t.Context(), "owner_1", "summary_1", 1)
			require.NoError(t, err)
			assert.Equal(t, request.Claims, actual.Claims)
			assert.Equal(t, request.Coverage[0], actual.Claims[0].Evidence[0].Message)
		})
	}
}

func TestSummaryClaimsGivenBrokenRevisionSourceOrderThenRejectsRatherThanRebinds(t *testing.T) {
	for _, mode := range []string{"gap", "message_after_child", "unknown_kind"} {
		t.Run(mode, func(t *testing.T) {
			version := summaryVersionRows("summary_1", "owner_1", 1)
			version.values[0][7] = []byte(
				`[{"id":"c","text":"fact","evidence":[{"source_key":0,"quote":"body"}]}]`,
			)
			sources := summarySourceRows()
			switch mode {
			case "gap":
				sources.values[0][6] = int64(1)
			case "message_after_child":
				sources.values = [][]driver.Value{sources.values[1], sources.values[0]}
				sources.values[0][6] = int64(0)
				sources.values[1][6] = int64(1)
			case "unknown_kind":
				sources.values[0][0] = "invalid"
			}
			script := &scriptedPostgresScript{queries: []scriptedRows{version, sources}}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			actual, err := store.GetSummary(t.Context(), "owner_1", "summary_1", 1)
			assert.ErrorIs(t, err, ErrConflict)
			assert.Empty(t, actual.SummaryID)
		})
	}
}

func TestSummaryClaimsGivenInvalidLocalSourceThenNeverReturnsPartialClaims(t *testing.T) {
	coverage := []domain.MessageVersionRef{summaryTestRef("message", "body")}
	for name, raw := range map[string]string{
		"missing_key":      `[{"id":"c","text":"fact","evidence":[{"quote":"body"}]}]`,
		"negative":         `[{"id":"c","text":"fact","evidence":[{"source_key":-1,"quote":"body"}]}]`,
		"outside_revision": `[{"id":"c","text":"fact","evidence":[{"source_key":1,"quote":"body"}]}]`,
		"fraction":         `[{"id":"c","text":"fact","evidence":[{"source_key":0.5,"quote":"body"}]}]`,
		"string":           `[{"id":"c","text":"fact","evidence":[{"source_key":"0","quote":"body"}]}]`,
		"null":             `[{"id":"c","text":"fact","evidence":[{"source_key":null,"quote":"body"}]}]`,
		"both_forms":       `[{"id":"c","text":"fact","evidence":[{"source_key":0,"message":{"message_id":"message","content_hash":"hash","canonicalization_version":"v1"},"quote":"body"}]}]`,
		"uncovered_legacy": `[{"id":"c","text":"fact","evidence":[{"message":{"message_id":"other","content_hash":"hash","canonicalization_version":"v1"},"quote":"body"}]}]`,
		"invalid_json":     `[`,
	} {
		t.Run(name, func(t *testing.T) {
			claims, err := decodeSummaryClaims([]byte(raw), coverage)
			assert.Error(t, err)
			assert.Nil(t, claims)
		})
	}
}
