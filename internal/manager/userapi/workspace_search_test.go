package userapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/search"
)

type workspacePrincipal struct {
	owner string
	err   error
}

func (p workspacePrincipal) Principal(
	context.Context,
	auth.RequestMetadata,
) (domain.UserPrincipal, error) {
	return domain.UserPrincipal{User: domain.User{UserID: p.owner}}, p.err
}

type workspaceSources struct {
	docs         map[string]domain.SearchDocument
	versions     map[string]domain.SearchDocument
	summary      domain.SummaryVersion
	err          error
	published    int
	contextCalls int
	contextDocs  []domain.SearchDocument
}

func (f *workspaceSources) ReadSearchDocument(
	_ context.Context,
	owner, kind, id string,
) (domain.SearchDocument, error) {
	if f.err != nil {
		return domain.SearchDocument{}, f.err
	}
	doc, ok := f.docs[kind+":"+id]
	if !ok || doc.OwnerUserID != owner {
		return domain.SearchDocument{}, domain.ErrNotFound
	}
	return doc, nil
}

func (f *workspaceSources) ReadSearchContext(
	ctx context.Context,
	owner, id string,
	_, _ int,
) ([]domain.SearchDocument, error) {
	f.contextCalls++
	if f.contextDocs != nil {
		return f.contextDocs, nil
	}
	doc, err := f.ReadSearchDocument(ctx, owner, "message", id)
	return []domain.SearchDocument{doc}, err
}

func (f *workspaceSources) PublishSummary(
	_ context.Context,
	owner string,
	r domain.PublishSummaryRequest,
) (domain.SummaryVersion, error) {
	f.published++
	return domain.SummaryVersion{
		SummaryID:   r.SummaryID,
		Revision:    r.ExpectedRevision + 1,
		OwnerUserID: owner,
		Body:        r.Body,
	}, f.err
}

func (f *workspaceSources) GetSummary(
	_ context.Context,
	owner, id string,
	revision int64,
) (domain.SummaryVersion, error) {
	if f.err != nil {
		return domain.SummaryVersion{}, f.err
	}
	if f.summary.SummaryID != id || f.summary.OwnerUserID != owner ||
		revision != 0 && revision != f.summary.Revision {
		return domain.SummaryVersion{}, domain.ErrNotFound
	}
	return f.summary, nil
}

func (f *workspaceSources) ReadMessageVersion(
	ctx context.Context,
	owner string,
	ref domain.MessageVersionRef,
) (domain.SearchDocument, error) {
	current, err := f.ReadSearchDocument(ctx, owner, "message", ref.MessageID)
	if err != nil {
		return domain.SearchDocument{}, err
	}
	if current.ContentHash == ref.ContentHash &&
		current.CanonicalizationVersion == ref.CanonicalizationVersion {
		return current, nil
	}
	if saved, ok := f.versions[ref.Identity()]; ok {
		return saved, nil
	}
	return domain.SearchDocument{}, domain.ErrSourceVersionUnavailable
}

type workspaceBackend struct {
	page  domain.SearchPage
	query domain.SearchQuery
	err   error
}

func (b *workspaceBackend) EnsureIndex(context.Context) error                  { return nil }
func (b *workspaceBackend) Index(context.Context, domain.SearchDocument) error { return nil }

func (b *workspaceBackend) Search(
	_ context.Context,
	q domain.SearchQuery,
) (domain.SearchPage, error) {
	b.query = q
	return b.page, b.err
}

func workspaceFixture() (*WorkspaceSearchService, *workspaceSources, *workspaceBackend) {
	doc := domain.SearchDocument{
		Kind:                    "message",
		SourceID:                "m1",
		OwnerUserID:             "owner",
		AgentID:                 "agent",
		SessionID:               "session",
		Role:                    "assistant",
		SessionSeq:              10,
		Body:                    "first paragraph\n\nlast paragraph",
		CanonicalizationVersion: domain.SearchCanonicalizationVersion,
		CreatedAt:               time.Now(),
	}
	doc.ContentHash = domain.SearchContentHash(doc.Body)
	store := &workspaceSources{
		docs:     map[string]domain.SearchDocument{doc.DocumentID(): doc},
		versions: map[string]domain.SearchDocument{},
	}
	backend := &workspaceBackend{
		page: domain.SearchPage{Hits: []domain.SearchDocument{doc}, NextCursor: "next"},
	}
	service := NewWorkspaceSearchService(
		workspacePrincipal{owner: "owner"},
		store,
		store,
		backend,
		[]byte(strings.Repeat("k", 32)),
	)
	return service, store, backend
}

func requireWorkspaceStatus(t *testing.T, err error, status int) {
	t.Helper()
	var typed apperr.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, status, typed.Status)
}

func TestWorkspaceSearchGivenUntrustedIndexWhenSearchingThenRevalidatesOwnerAndText(t *testing.T) {
	s, store, backend := workspaceFixture()
	private := store.docs["message:m1"]
	private.SourceID = "private"
	private.OwnerUserID = "other"
	store.docs[private.DocumentID()] = private
	stale := store.docs["message:m1"]
	stale.SourceID = "stale"
	stale.Body = "changed"
	store.docs[stale.DocumentID()] = stale
	deleted := store.docs["message:m1"]
	deleted.SourceID = "deleted"
	deleted.Deleted = true
	store.docs[deleted.DocumentID()] = deleted
	backend.page.Hits = append(
		backend.page.Hits,
		private,
		stale,
		deleted,
		domain.SearchDocument{Kind: "message", SourceID: "gone"},
	)
	page, err := s.Search(
		t.Context(),
		auth.RequestMetadata{},
		domain.SearchQuery{Query: "PARAGRAPH", OwnerUserID: "spoof"},
	)
	require.NoError(t, err)
	require.Len(t, page.Hits, 1)
	assert.Equal(t, "owner", backend.query.OwnerUserID)
	assert.Equal(t, 20, backend.query.Limit)
	assert.Equal(t, "m1", page.Hits[0].Ref.ID)
	assert.Equal(t, "next", page.NextCursor)
	assert.Contains(t, page.Hits[0].Snippet, "paragraph")
	store.summary = domain.SummaryVersion{
		SummaryID:   "s1",
		OwnerUserID: "owner",
		Revision:    2,
		Title:       "paragraph title",
		Body:        "summary",
	}
	backend.page.Hits = []domain.SearchDocument{{Kind: "summary", SourceID: "s1"}}
	page, err = s.Search(t.Context(), auth.RequestMetadata{}, domain.SearchQuery{Query: "title"})
	require.NoError(t, err)
	require.Len(t, page.Hits, 1)
	assert.Equal(t, int64(2), page.Hits[0].Ref.Revision)
	assert.Equal(t, "paragraph title", page.Hits[0].Snippet)
}

func TestWorkspaceSearchGivenInvalidOrUnavailableRequestsThenReturnsBoundedErrors(t *testing.T) {
	s, store, backend := workspaceFixture()
	for _, query := range []domain.SearchQuery{{}, {Query: strings.Repeat("x", 257)}, {Query: "x", Limit: -1}, {Query: "x", Limit: 51}, {Query: "x", Kinds: []string{"unknown"}},
		{Query: "x", Kinds: []string{"message", "session", "summary", "message"}}, {Query: "x", SessionIDs: make([]string, 51)}} {
		_, err := s.Search(t.Context(), auth.RequestMetadata{}, query)
		requireWorkspaceStatus(t, err, http.StatusBadRequest)
	}
	backend.err = search.ErrInvalidQuery
	_, err := s.Search(t.Context(), auth.RequestMetadata{}, domain.SearchQuery{Query: "x"})
	requireWorkspaceStatus(t, err, http.StatusBadRequest)
	backend.err = errors.New("secret backend detail")
	_, err = s.Search(t.Context(), auth.RequestMetadata{}, domain.SearchQuery{Query: "x"})
	requireWorkspaceStatus(t, err, http.StatusServiceUnavailable)
	assert.NotContains(t, err.Error(), "secret")
	backend.err = nil
	store.err = errors.New("db failure")
	_, err = s.Search(t.Context(), auth.RequestMetadata{}, domain.SearchQuery{Query: "x"})
	assert.EqualError(t, err, "db failure")
	s.backend = nil
	_, err = s.Search(t.Context(), auth.RequestMetadata{}, domain.SearchQuery{Query: "x"})
	requireWorkspaceStatus(t, err, http.StatusServiceUnavailable)
	s.principal = workspacePrincipal{err: domain.ErrUnauthorized}
	_, err = s.Search(t.Context(), auth.RequestMetadata{}, domain.SearchQuery{Query: "x"})
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	s.principal = workspacePrincipal{}
	_, err = s.owner(t.Context(), auth.RequestMetadata{})
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	s.principal = nil
	_, err = s.owner(t.Context(), auth.RequestMetadata{})
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
}

func TestWorkspaceReadGivenMessageWhenExpandingThenKeepsVersionAndBindsReceiptToView(t *testing.T) {
	s, store, _ := workspaceFixture()
	value, err := s.Read(
		t.Context(),
		auth.RequestMetadata{},
		WorkspaceReadRequest{Ref: SearchRef{Kind: "message", ID: "m1"}, Before: 1, After: 1},
	)
	require.NoError(t, err)
	last := value.(WorkspaceMessageRead)
	assert.Equal(t, "last paragraph", last.Text)
	assert.Equal(t, 1, store.contextCalls)
	assert.NotEmpty(t, last.ReadReceipt)
	value, err = s.Read(
		t.Context(),
		auth.RequestMetadata{},
		WorkspaceReadRequest{Ref: last.Ref, View: "full", Limit: 5},
	)
	require.NoError(t, err)
	full := value.(WorkspaceMessageRead)
	assert.Equal(t, last.Ref, full.Ref)
	assert.Equal(t, "first", full.Text)
	assert.True(t, full.HasMore)
	assert.Equal(t, 5, full.NextOffset)
	value, err = s.Read(
		t.Context(),
		auth.RequestMetadata{},
		WorkspaceReadRequest{Ref: last.Ref, View: "first", Offset: 99, Limit: 3},
	)
	require.NoError(t, err)
	assert.Empty(t, value.(WorkspaceMessageRead).Text)
	assert.False(t, value.(WorkspaceMessageRead).HasMore)
	doc := store.docs["message:m1"]
	doc.Role = "user"
	store.docs[doc.DocumentID()] = doc
	value, err = s.Read(t.Context(), auth.RequestMetadata{}, WorkspaceReadRequest{Ref: last.Ref})
	require.NoError(t, err)
	assert.Equal(t, doc.Body, value.(WorkspaceMessageRead).Text)
}

func TestWorkspaceReadGivenInvalidVersionOrOptionsThenRejectsRead(t *testing.T) {
	s, store, _ := workspaceFixture()
	for _, request := range []WorkspaceReadRequest{{}, {Ref: SearchRef{Kind: "unknown", ID: "m1"}},
		{Ref: SearchRef{Kind: "message", ID: "m1"}, View: "unknown"}, {Ref: SearchRef{Kind: "message", ID: "m1"}, Offset: -1},
		{Ref: SearchRef{Kind: "message", ID: "m1"}, Limit: 16385}, {Ref: SearchRef{Kind: "message", ID: "m1"}, Before: 51}} {
		_, err := s.Read(t.Context(), auth.RequestMetadata{}, request)
		requireWorkspaceStatus(t, err, http.StatusBadRequest)
	}
	ref := documentRef(store.docs["message:m1"])
	ref.ContentHash = "stale"
	_, err := s.Read(t.Context(), auth.RequestMetadata{}, WorkspaceReadRequest{Ref: ref})
	requireWorkspaceStatus(t, err, http.StatusConflict)
	s.summaries = nil
	_, err = s.Read(t.Context(), auth.RequestMetadata{}, WorkspaceReadRequest{Ref: ref})
	requireWorkspaceStatus(t, err, http.StatusConflict)
	_, err = s.Read(
		t.Context(),
		auth.RequestMetadata{},
		WorkspaceReadRequest{Ref: SearchRef{Kind: "summary", ID: "s1"}},
	)
	requireWorkspaceStatus(t, err, http.StatusServiceUnavailable)
	s.receiptKey = nil
	_, err = s.Read(
		t.Context(),
		auth.RequestMetadata{},
		WorkspaceReadRequest{Ref: SearchRef{Kind: "message", ID: "m1"}},
	)
	requireWorkspaceStatus(t, err, http.StatusServiceUnavailable)
	s.principal = workspacePrincipal{err: domain.ErrUnauthorized}
	_, err = s.Read(t.Context(), auth.RequestMetadata{}, WorkspaceReadRequest{Ref: ref})
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
}

func TestWorkspacePublishGivenReadReceiptsThenOnlySeenQuotesCanBePublished(t *testing.T) {
	s, store, _ := workspaceFixture()
	value, err := s.Read(
		t.Context(),
		auth.RequestMetadata{},
		WorkspaceReadRequest{Ref: SearchRef{Kind: "message", ID: "m1"}},
	)
	require.NoError(t, err)
	last := value.(WorkspaceMessageRead)
	ref := domain.MessageVersionRef{
		MessageID:               last.Ref.ID,
		ContentHash:             last.Ref.ContentHash,
		CanonicalizationVersion: last.Ref.CanonicalizationVersion,
		ReadReceipt:             last.ReadReceipt,
	}
	request := domain.PublishSummaryRequest{
		SummaryID:   "s1",
		SubjectType: "topic",
		SubjectID:   "t1",
		RequestID:   "r1",
		Body:        "summary",
		Coverage:    []domain.MessageVersionRef{ref},
		Claims: []domain.SummaryClaim{
			{
				ID:   "claim",
				Text: "claim",
				Evidence: []domain.SummaryEvidence{
					{Message: ref, Quote: "last paragraph", View: "last"},
				},
			},
		},
	}
	version, err := s.Publish(t.Context(), auth.RequestMetadata{}, request)
	require.NoError(t, err)
	assert.Equal(t, int64(1), version.Revision)
	assert.Equal(t, 1, store.published)
	request.Claims[0].Evidence[0].Quote = "first paragraph"
	_, err = s.Publish(t.Context(), auth.RequestMetadata{}, request)
	requireWorkspaceStatus(t, err, http.StatusBadRequest)
	request.Claims[0].Evidence[0].Quote = "last"
	request.Claims[0].Evidence[0].View = "full"
	_, err = s.Publish(t.Context(), auth.RequestMetadata{}, request)
	requireWorkspaceStatus(t, err, http.StatusBadRequest)
	request.Claims[0].Evidence[0].View = "last"
	request.Claims[0].Evidence[0].ReadReceipt = "tampered"
	_, err = s.Publish(t.Context(), auth.RequestMetadata{}, request)
	requireWorkspaceStatus(t, err, http.StatusBadRequest)
	request.Claims = nil
	request.Coverage[0].ReadReceipt = "tampered"
	_, err = s.Publish(t.Context(), auth.RequestMetadata{}, request)
	requireWorkspaceStatus(t, err, http.StatusBadRequest)
	assert.Equal(t, 1, store.published)
}

func TestWorkspaceReceiptGivenForgedOrChangedSourceThenRejectsProof(t *testing.T) {
	s, store, _ := workspaceFixture()
	doc := store.docs["message:m1"]
	ref := domain.MessageVersionRef{
		MessageID:               doc.SourceID,
		ContentHash:             doc.ContentHash,
		CanonicalizationVersion: doc.CanonicalizationVersion,
	}
	valid := searchReadReceipt{Owner: "owner", Ref: documentRef(doc), View: "full", Limit: 10}
	for _, token := range []string{"", "a.b.c", "!.a", "e30.!", "e30.YQ", strings.Repeat("x", 4097) + ".a"} {
		_, err := s.verifyReceipt(t.Context(), "owner", ref, token)
		requireWorkspaceStatus(t, err, http.StatusBadRequest)
	}
	for _, modify := range []func(*searchReadReceipt){func(r *searchReadReceipt) { r.Owner = "other" }, func(r *searchReadReceipt) { r.Ref.Kind = "summary" },
		func(r *searchReadReceipt) { r.Ref.ID = "other" }, func(r *searchReadReceipt) { r.Ref.ContentHash = "other" }, func(r *searchReadReceipt) { r.Ref.CanonicalizationVersion = "other" },
		func(r *searchReadReceipt) { r.Offset = -1 }, func(r *searchReadReceipt) { r.Limit = 0 }} {
		r := valid
		modify(&r)
		_, err := s.verifyReceipt(t.Context(), "owner", ref, s.signReceipt(r))
		requireWorkspaceStatus(t, err, http.StatusBadRequest)
	}
	_, err := s.verifyReceipt(t.Context(), "owner", ref, s.signReceipt(valid))
	require.NoError(t, err)
	oldToken := s.signReceipt(valid)
	changed := doc
	changed.Body = "changed"
	changed.ContentHash = domain.SearchContentHash(changed.Body)
	store.docs[doc.DocumentID()] = changed
	_, err = s.verifyReceipt(t.Context(), "owner", ref, oldToken)
	requireWorkspaceStatus(t, err, http.StatusConflict)
	store.versions[ref.Identity()] = doc
	read, err := s.verifyReceipt(t.Context(), "owner", ref, oldToken)
	require.NoError(t, err)
	assert.Equal(t, "first para", read.Text)
	changed.OwnerUserID = "other"
	store.docs[doc.DocumentID()] = changed
	_, err = s.verifyReceipt(t.Context(), "owner", ref, oldToken)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestWorkspaceSummaryReadAndPublishErrors(t *testing.T) {
	s, store, _ := workspaceFixture()
	store.summary = domain.SummaryVersion{SummaryID: "s", OwnerUserID: "owner", Revision: 2}
	value, err := s.Read(
		t.Context(),
		auth.RequestMetadata{},
		WorkspaceReadRequest{Ref: SearchRef{Kind: "summary", ID: "s", Revision: 2}},
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), value.(domain.SummaryVersion).Revision)
	doc := store.docs["message:m1"]
	doc.Kind = "session"
	store.docs[doc.DocumentID()] = doc
	_, err = s.Read(
		t.Context(),
		auth.RequestMetadata{},
		WorkspaceReadRequest{Ref: SearchRef{Kind: "session", ID: "m1"}},
	)
	require.NoError(t, err)
	_, err = s.Publish(
		t.Context(),
		auth.RequestMetadata{},
		domain.PublishSummaryRequest{Coverage: make([]domain.MessageVersionRef, 1001)},
	)
	requireWorkspaceStatus(t, err, http.StatusBadRequest)
	s.summaries = nil
	_, err = s.Publish(t.Context(), auth.RequestMetadata{}, domain.PublishSummaryRequest{})
	requireWorkspaceStatus(t, err, http.StatusServiceUnavailable)
	s.principal = workspacePrincipal{err: domain.ErrUnauthorized}
	_, err = s.Publish(t.Context(), auth.RequestMetadata{}, domain.PublishSummaryRequest{})
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	assert.Equal(t, "\u6570\u636e\u5e93", searchSnippet("\u6570\u636e\u5e93", "\u6570\u636e"))
	assert.Len(t, []rune(searchSnippet(strings.Repeat("x", 500)+"needle", "needle")), 46)
}

func TestWorkspaceSearchGivenMovedSessionThenRechecksRequestedFilters(t *testing.T) {
	s, _, _ := workspaceFixture()
	page, err := s.Search(
		t.Context(),
		auth.RequestMetadata{},
		domain.SearchQuery{Query: "paragraph", SessionIDs: []string{"previous-session"}},
	)
	require.NoError(t, err)
	assert.Empty(t, page.Hits)
	page, err = s.Search(
		t.Context(),
		auth.RequestMetadata{},
		domain.SearchQuery{Query: "paragraph", Kinds: []string{"session"}},
	)
	require.NoError(t, err)
	assert.Empty(t, page.Hits)
	page, err = s.Search(
		t.Context(),
		auth.RequestMetadata{},
		domain.SearchQuery{
			Query:      "paragraph",
			Kinds:      []string{"message"},
			SessionIDs: []string{"session"},
		},
	)
	require.NoError(t, err)
	assert.Len(t, page.Hits, 1)
}

func TestWorkspaceReadGivenLargeContextThenRendersBoundedViewsWithIndependentReceipts(
	t *testing.T,
) {
	s, store, _ := workspaceFixture()
	anchor := store.docs["message:m1"]
	contextDoc := anchor
	contextDoc.SourceID = "context"
	contextDoc.Body = strings.Repeat("x", 9000)
	contextDoc.ContentHash = domain.SearchContentHash(contextDoc.Body)
	store.contextDocs = []domain.SearchDocument{anchor, contextDoc}
	value, err := s.Read(
		t.Context(),
		auth.RequestMetadata{},
		WorkspaceReadRequest{Ref: documentRef(anchor), Before: 1, Limit: 1},
	)
	require.NoError(t, err)
	read := value.(WorkspaceMessageRead)
	assert.Len(t, read.Text, 1)
	require.Len(t, read.Context, 1)
	assert.Equal(t, "context", read.Context[0].MessageID)
	assert.Len(t, read.Context[0].Text, 2048)
	assert.True(t, read.Context[0].HasMore)
	assert.NotEmpty(t, read.Context[0].ReadReceipt)
	_, err = s.Read(
		t.Context(),
		auth.RequestMetadata{},
		WorkspaceReadRequest{Ref: SearchRef{Kind: "message", ID: "m1"}, Offset: 1},
	)
	requireWorkspaceStatus(t, err, http.StatusBadRequest)
}

func TestWorkspacePublishGivenExcessEvidenceThenRejectsBeforeReadingSources(t *testing.T) {
	s, store, _ := workspaceFixture()
	doc := store.docs["message:m1"]
	ref := domain.MessageVersionRef{
		MessageID:               doc.SourceID,
		ContentHash:             doc.ContentHash,
		CanonicalizationVersion: doc.CanonicalizationVersion,
	}
	evidence := make([]domain.SummaryEvidence, 513)
	for i := range evidence {
		evidence[i] = domain.SummaryEvidence{Message: ref, Quote: "last"}
	}
	_, err := s.Publish(
		t.Context(),
		auth.RequestMetadata{},
		domain.PublishSummaryRequest{
			SummaryID:   "s",
			SubjectType: "topic",
			SubjectID:   "topic",
			RequestID:   "r",
			Body:        "summary",
			Coverage:    []domain.MessageVersionRef{ref},
			Claims:      []domain.SummaryClaim{{ID: "claim", Text: "claim", Evidence: evidence}},
		},
	)
	requireWorkspaceStatus(t, err, http.StatusBadRequest)
	assert.Zero(t, store.published)
}
