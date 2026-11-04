package manager

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

type secretarySummaryToolStore struct {
	domain.SecretaryStore
	items   []domain.SummaryVersion
	err     error
	queries []domain.SummaryListQuery
	owner   string
	source  string
}

func (*secretarySummaryToolStore) PublishSecretaryBatch(
	context.Context,
	domain.SecretaryJob,
	domain.SecretaryBatchPublication,
) ([]domain.SummaryVersion, error) {
	return nil, nil
}

func (s *secretarySummaryToolStore) ListSecretaryNodeSummaries(
	_ context.Context,
	owner, source string,
	query domain.SummaryListQuery,
) (domain.SecretarySummaryPage, error) {
	s.owner, s.source = owner, source
	s.queries = append(s.queries, query)
	items := s.items
	cursor := ""
	if len(items) > query.Limit {
		items, cursor = items[:query.Limit], "next-page"
	}
	return domain.SecretarySummaryPage{Items: items, NextCursor: cursor}, s.err
}

func TestSecretarySummaryToolGivenCurrentSessionThenUsesBoundedAuthorizedPagination(t *testing.T) {
	store := &secretarySummaryToolStore{
		items: []domain.SummaryVersion{
			{SummaryID: "a", Revision: 2},
			{SummaryID: "b", Revision: 1},
		},
	}
	srv := &Service{secretaryStore: store}
	grant := &secretaryToolGrant{
		job: domain.SecretaryJob{
			OwnerUserID:     "owner",
			SourceID:        "source",
			SourceSessionID: "source-session",
		},
	}
	page, err := srv.secretaryToolSummaries(
		t.Context(),
		grant,
		`{"session_id":"source-session","cursor":"previous","limit":1}`,
	)
	require.NoError(t, err)
	assert.Equal(t, "owner", store.owner)
	assert.Equal(t, "source", store.source)
	assert.Equal(t, domain.SummaryListQuery{Cursor: "previous", Limit: 1}, store.queries[0])
	assert.Equal(t, "next-page", page.NextCursor)
	require.Len(t, page.Items, 1)
	_, err = srv.secretaryToolSummaries(t.Context(), grant, `{"session_id":"source-session"}`)
	require.NoError(t, err)
	assert.Equal(t, 20, store.queries[1].Limit)
}

func TestSecretarySummaryToolGivenInvalidArgumentsOrMissingCapabilityThenRejects(t *testing.T) {
	srv := &Service{}
	grant := &secretaryToolGrant{job: domain.SecretaryJob{SourceSessionID: "source-session"}}
	for _, arguments := range []string{"not json", `{}`, `{"session_id":"source-session","limit":-1}`, `{"session_id":"source-session","limit":51}`, `{"source_id":"other"}`} {
		_, err := srv.secretaryToolSummaries(t.Context(), grant, arguments)
		assert.Error(t, err)
	}
	_, err := srv.secretaryToolSummaries(t.Context(), grant, `{"session_id":"other-session"}`)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = srv.secretaryToolSummaries(t.Context(), grant, `{"session_id":"source-session"}`)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	store := &secretarySummaryToolStore{err: domain.ErrNotFound}
	srv.secretaryStore = store
	_, err = srv.secretaryToolSummaries(t.Context(), grant, `{"session_id":"source-session"}`)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestSecretarySummaryToolGivenLargePageThenShrinksPageWithExplicitCursor(t *testing.T) {
	store := &secretarySummaryToolStore{}
	for range 5 {
		store.items = append(
			store.items,
			domain.SummaryVersion{
				SummaryID: "summary",
				Revision:  1,
				Body:      strings.Repeat("x", 30*1024),
			},
		)
	}
	srv := &Service{secretaryStore: store}
	grant := &secretaryToolGrant{job: domain.SecretaryJob{SourceSessionID: "source-session"}}
	page, err := srv.secretaryToolSummaries(
		t.Context(),
		grant,
		`{"session_id":"source-session","limit":5}`,
	)
	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
	assert.Equal(t, "next-page", page.NextCursor)
	assert.Equal(t, []domain.SummaryListQuery{{Limit: 5}, {Limit: 3}, {Limit: 2}}, store.queries)
	store.items[0].Body = strings.Repeat("x", 70*1024)
	page, err = srv.secretaryToolSummaries(
		t.Context(),
		grant,
		`{"session_id":"source-session","limit":1}`,
	)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.True(t, page.Items[0].RequiresRead)
	assert.Empty(t, page.Items[0].Body)
}

func TestSecretarySummaryLedgerGivenPageThenPreservesExactSummaryContentAndBudgets(t *testing.T) {
	grant := &secretaryToolGrant{summaries: map[domain.SummaryRef]bool{}}
	page := domain.SecretarySummaryPage{
		Items: []domain.SummaryVersion{
			{SummaryID: "b", Revision: 2, Body: "Second"},
			{SummaryID: "a", Revision: 1, Body: "First"},
		},
	}
	require.NoError(t, grant.record(page))
	bytes := grant.readBytes
	require.NoError(t, grant.record(page))
	assert.Equal(t, bytes, grant.readBytes)
	srv := &Service{}
	srv.secretaryTools.Store("cap", grant)
	reads := srv.secretaryToolsReads("cap")
	require.Len(t, reads.SummaryVersions, 2)
	assert.Equal(t, "a", reads.SummaryVersions[0].SummaryID)
	assert.Equal(t, "First", reads.SummaryVersions[0].Body)
	grant.readBytes = 4 * 1024 * 1024
	err := grant.record(
		domain.SecretarySummaryPage{
			Items: []domain.SummaryVersion{{SummaryID: "new", Revision: 1, Body: "exceeds"}},
		},
	)
	assert.Error(t, err)
	assert.Len(t, grant.summaries, 2)
}

func TestSecretarySummaryToolGivenRunDispatchThenUsesSummaryCapability(t *testing.T) {
	srv, _, job, _, _ := secretaryToolsFixture(t)
	store := &secretarySummaryToolStore{err: errors.New("summary lookup")}
	srv.secretaryStore = store
	_, err := srv.runSecretaryTool(
		t.Context(),
		&secretaryToolGrant{job: job},
		secretaryToolRequest{Tool: "summaries", ArgumentsJSON: `{"session_id":"source-session"}`},
	)
	assert.ErrorContains(t, err, "summary lookup")
}

func TestSecretarySummaryToolGivenFullMCPArgumentsThenAuthenticatesAndPinsOnlyCurrentSession(
	t *testing.T,
) {
	srv, fixture, job, _, capability := secretaryToolsFixture(t)
	store := &secretarySummaryToolStore{
		SecretaryStore: srv.secretaryStore,
		items: []domain.SummaryVersion{
			{
				SummaryID:   "summary",
				OwnerUserID: job.OwnerUserID,
				Revision:    2,
				Body:        "Supported decision",
			},
		},
	}
	srv.secretaryStore = store
	result, err := secretaryToolCall(
		t,
		srv,
		fixture,
		capability,
		"summaries",
		`{"session_id":"source-session","cursor":"opaque-page","limit":20}`,
	)
	require.NoError(t, err)
	page := result.(secretarySummaryToolPage)
	require.Len(t, page.Items, 1)
	assert.Equal(t, job.OwnerUserID, store.owner)
	assert.Equal(t, job.SourceID, store.source)
	assert.Equal(t, domain.SummaryListQuery{Cursor: "opaque-page", Limit: 20}, store.queries[0])
	assert.Equal(
		t,
		[]domain.SummaryVersion{page.Items[0].SummaryVersion},
		srv.secretaryToolsReads(capability).SummaryVersions,
	)
	_, err = secretaryToolCall(
		t,
		srv,
		fixture,
		capability,
		"summaries",
		`{"session_id":"another-session","limit":20}`,
	)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	assert.Len(t, store.queries, 1, "foreign session arguments must fail before any storage read")
}

func TestSecretarySummaryToolGivenLargeLegacyCoverageThenHeaderRequiresExactReadBeforeRevision(
	t *testing.T,
) {
	srv, fixture, job, _, capability := secretaryToolsFixture(t)
	legacy := domain.SummaryVersion{
		SummaryID:   "legacy-summary",
		Revision:    4,
		OwnerUserID: job.OwnerUserID,
		Title:       "Legacy decisions",
		Body:        "Preserved legacy conclusions",
	}
	for index := range 4096 {
		legacy.Coverage = append(
			legacy.Coverage,
			domain.MessageVersionRef{
				MessageID:               "old-" + strconv.Itoa(index),
				ContentHash:             domain.SearchContentHash("old source"),
				CanonicalizationVersion: domain.SearchCanonicalizationVersion,
			},
		)
	}
	store := &secretarySummaryToolStore{
		SecretaryStore: srv.secretaryStore,
		items:          []domain.SummaryVersion{legacy},
	}
	srv.secretaryStore = store
	readStore := &developerSummaryHTTPStore{summary: legacy}
	srv.workspaceSearch = userapi.NewWorkspaceSearchService(srv.auth, nil, readStore, nil, nil)
	result, err := secretaryToolCall(
		t,
		srv,
		fixture,
		capability,
		"summaries",
		`{"session_id":"source-session"}`,
	)
	require.NoError(t, err)
	page := result.(secretarySummaryToolPage)
	require.Len(t, page.Items, 1)
	assert.True(t, page.Items[0].RequiresRead)
	encoded, err := json.Marshal(page)
	require.NoError(t, err)
	assert.Less(t, len(encoded), 64*1024)
	assert.JSONEq(
		t,
		`{"items":[{"summary_id":"legacy-summary","revision":4,"title":"Legacy decisions","requires_read":true}]}`,
		string(encoded),
	)
	assert.Empty(t, srv.secretaryToolsReads(capability).SummaryVersions)
	assert.Empty(t, srv.secretaryToolsReads(capability).Summaries)
	job.SourceID, job.BatchID = "1", "batch"
	job.Messages = []domain.SearchDocument{
		{
			Kind:                    "message",
			SourceID:                "new-message",
			OwnerUserID:             job.OwnerUserID,
			AgentID:                 job.SourceAgentID,
			SessionID:               job.SourceSessionID,
			Body:                    "New conclusion",
			ContentHash:             domain.SearchContentHash("New conclusion"),
			CanonicalizationVersion: domain.SearchCanonicalizationVersion,
		},
	}
	output := `{"summaries":[{"action":"revise","summary_id":"legacy-summary","expected_revision":4,"title":"Compacted decisions","body":"Explicitly compacted conclusions","source_message_ids":["new-message"]}],"outcomes":[{"message_id":"new-message","action":"summarized","summary_keys":["legacy-summary"]}]}`
	_, err = secretary.BuildNodePublication(job, output, srv.secretaryToolsReads(capability))
	assert.ErrorContains(t, err, "exact read baseline")
	_, err = secretaryToolCall(
		t,
		srv,
		fixture,
		capability,
		"read",
		`{"ref":{"kind":"summary","id":"legacy-summary","revision":4}}`,
	)
	require.NoError(t, err)
	reads := srv.secretaryToolsReads(capability)
	require.Len(t, reads.SummaryVersions, 1)
	assert.Len(t, reads.SummaryVersions[0].Coverage, 4096)
	assert.Equal(t, job.OwnerUserID, readStore.owner)
	assert.EqualValues(t, 4, readStore.revision)
	publication, err := secretary.BuildNodePublication(job, output, reads)
	require.NoError(t, err)
	require.Len(t, publication.Outputs, 1)
	assert.Len(
		t,
		publication.Outputs[0].Request.Coverage,
		1,
		"compaction is explicit; old coverage is never silently appended",
	)
}

func TestSecretarySummaryLedgerGivenHeaderThenCountsBudgetWithoutGrantingReadProvenance(
	t *testing.T,
) {
	grant := &secretaryToolGrant{summaries: map[domain.SummaryRef]bool{}}
	page := secretarySummaryToolPage{
		Items: []secretarySummaryToolItem{
			{
				SummaryVersion: domain.SummaryVersion{
					SummaryID: "large",
					Revision:  1,
					Title:     "Large summary",
				},
				RequiresRead: true,
			},
		},
	}
	require.NoError(t, grant.record(page))
	assert.Positive(t, grant.readBytes)
	assert.Empty(t, grant.summaries)
	assert.Empty(t, grant.summaryVersions)
	grant.readBytes = 4 * 1024 * 1024
	assert.Error(t, grant.record(page))
	assert.Empty(t, grant.summaries)
	grant.readBytes = 0
	for index := range 100 {
		grant.summaries[domain.SummaryRef{SummaryID: "old", Revision: int64(index)}] = true
	}
	page.Items = append(
		page.Items,
		secretarySummaryToolItem{
			SummaryVersion: domain.SummaryVersion{
				SummaryID: "new",
				Revision:  1,
				Body:      "new summary",
			},
		},
	)
	assert.Error(t, grant.record(page))
	assert.Zero(t, grant.readBytes, "a rejected page must not partially consume the read ledger")
}
