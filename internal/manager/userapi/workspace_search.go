package userapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	searchbackend "github.com/pax-beehive/pax-manager/internal/manager/search"
)

type WorkspaceSearchService struct {
	principal  PrincipalResolver
	sources    domain.SearchSourceStore
	summaries  domain.SummaryStore
	backend    domain.SearchBackend
	receiptKey []byte
}

func NewWorkspaceSearchService(principal PrincipalResolver, sources domain.SearchSourceStore,
	summaries domain.SummaryStore, backend domain.SearchBackend, receiptKey []byte,
) *WorkspaceSearchService {
	return &WorkspaceSearchService{principal: principal, sources: sources, summaries: summaries,
		backend: backend, receiptKey: append([]byte(nil), receiptKey...)}
}

type SearchRef struct {
	Kind                    string `json:"kind"`
	ID                      string `json:"id"`
	Revision                int64  `json:"revision,omitempty"`
	ContentHash             string `json:"content_hash,omitempty"`
	CanonicalizationVersion string `json:"canonicalization_version,omitempty"`
}

type WorkspaceSearchHit struct {
	Ref       SearchRef `json:"ref"`
	Title     string    `json:"title,omitempty"`
	Snippet   string    `json:"snippet"`
	AgentID   string    `json:"agent_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
}

type WorkspaceSearchPage struct {
	Hits       []WorkspaceSearchHit `json:"hits"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

type WorkspaceReadRequest struct {
	Ref    SearchRef `json:"ref"`
	View   string    `json:"view,omitempty"`
	Offset int       `json:"offset,omitempty"`
	Limit  int       `json:"limit,omitempty"`
	Before int       `json:"before,omitempty"`
	After  int       `json:"after,omitempty"`
}

type WorkspaceMessageRead struct {
	Ref            SearchRef              `json:"ref"`
	MessageID      string                 `json:"message_id"`
	AgentID        string                 `json:"agent_id"`
	SessionID      string                 `json:"session_id"`
	Role           string                 `json:"role"`
	SessionSeq     int64                  `json:"session_seq"`
	ConversationID string                 `json:"conversation_id,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
	Text           string                 `json:"text"`
	View           string                 `json:"view"`
	AvailableViews []string               `json:"available_views"`
	HasMore        bool                   `json:"has_more"`
	NextOffset     int                    `json:"next_offset,omitempty"`
	ReadReceipt    string                 `json:"read_receipt"`
	Context        []WorkspaceMessageRead `json:"context,omitempty"`
}

func (s *WorkspaceSearchService) Search(
	ctx context.Context,
	meta auth.RequestMetadata,
	query domain.SearchQuery,
) (WorkspaceSearchPage, error) {
	owner, err := s.owner(ctx, meta)
	if err != nil {
		return WorkspaceSearchPage{}, err
	}
	query.Query = strings.TrimSpace(query.Query)
	if err = validateWorkspaceQuery(query); err != nil {
		return WorkspaceSearchPage{}, err
	}
	if s.backend == nil || s.sources == nil {
		return WorkspaceSearchPage{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"search is unavailable",
		)
	}
	query.OwnerUserID = owner
	if query.Limit == 0 {
		query.Limit = 20
	}
	page, err := s.backend.Search(ctx, query)
	if errors.Is(err, searchbackend.ErrInvalidQuery) {
		return WorkspaceSearchPage{}, searchHTTPError(
			http.StatusBadRequest,
			"invalid search cursor or query",
		)
	}
	if err != nil {
		return WorkspaceSearchPage{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"search request failed",
		)
	}
	result := WorkspaceSearchPage{
		Hits:       make([]WorkspaceSearchHit, 0, len(page.Hits)),
		NextCursor: page.NextCursor,
	}
	for _, hit := range page.Hits {
		current, readErr := s.currentDocument(ctx, owner, hit.Kind, hit.SourceID)
		if errors.Is(readErr, domain.ErrNotFound) {
			continue
		}
		if readErr != nil {
			return WorkspaceSearchPage{}, searchSourceError(readErr)
		}
		if !searchMatchesCurrent(current, owner, query) {
			continue
		}
		text := current.Body
		if !literalContains(text, query.Query) {
			text = current.Title
		}
		result.Hits = append(
			result.Hits,
			WorkspaceSearchHit{Ref: documentRef(current), Title: current.Title,
				Snippet: searchSnippet(
					text,
					query.Query,
				), AgentID: current.AgentID, SessionID: current.SessionID},
		)
	}
	return result, nil
}

func (s *WorkspaceSearchService) Read(
	ctx context.Context,
	meta auth.RequestMetadata,
	request WorkspaceReadRequest,
) (any, error) {
	owner, err := s.owner(ctx, meta)
	if err != nil {
		return nil, err
	}
	if err = validateWorkspaceRead(request); err != nil {
		return nil, err
	}
	if request.Ref.Kind == "summary" {
		if s.summaries == nil {
			return nil, searchHTTPError(
				http.StatusServiceUnavailable,
				"summary storage is unavailable",
			)
		}
		version, readErr := s.summaries.GetSummary(ctx, owner, request.Ref.ID, request.Ref.Revision)
		return version, searchSourceError(readErr)
	}
	if request.Ref.Kind == "session" {
		return s.currentDocument(ctx, owner, "session", request.Ref.ID)
	}
	if request.Ref.Kind != "message" || s.sources == nil {
		return nil, searchHTTPError(http.StatusBadRequest, "invalid read kind")
	}
	document, err := s.readMessageVersion(ctx, owner, request.Ref)
	if err != nil {
		return nil, searchSourceError(err)
	}
	read, err := s.renderRead(owner, document, request)
	if err != nil {
		return nil, err
	}
	if request.Before > 0 || request.After > 0 {
		var contextDocs []domain.SearchDocument
		contextDocs, err = s.sources.ReadSearchContext(
			ctx,
			owner,
			request.Ref.ID,
			request.Before,
			request.After,
		)
		if err != nil {
			return nil, searchSourceError(err)
		}
		for _, contextDoc := range contextDocs {
			if contextDoc.SourceID == document.SourceID {
				continue
			}
			contextRead, renderErr := s.renderRead(
				owner,
				contextDoc,
				WorkspaceReadRequest{Limit: 2048},
			)
			if renderErr != nil {
				return nil, renderErr
			}
			read.Context = append(read.Context, contextRead)
		}
	}
	return read, nil
}

func (s *WorkspaceSearchService) Publish(
	ctx context.Context,
	meta auth.RequestMetadata,
	request domain.PublishSummaryRequest,
) (domain.SummaryVersion, error) {
	owner, err := s.owner(ctx, meta)
	if err != nil {
		return domain.SummaryVersion{}, err
	}
	if s.summaries == nil {
		return domain.SummaryVersion{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"summary storage is unavailable",
		)
	}
	if len(request.Coverage) > 512 || len(request.Claims) > 100 ||
		len(request.SummarySources) > 100 {
		return domain.SummaryVersion{}, searchHTTPError(
			http.StatusBadRequest,
			"summary exceeds source limits",
		)
	}
	if err = request.Validate(); err != nil {
		return domain.SummaryVersion{}, searchHTTPError(
			http.StatusBadRequest,
			"invalid summary content or sources",
		)
	}
	evidenceCount := 0
	for _, claim := range request.Claims {
		evidenceCount += len(claim.Evidence)
	}
	if evidenceCount > 512 {
		return domain.SummaryVersion{}, searchHTTPError(
			http.StatusBadRequest,
			"summary exceeds evidence limits",
		)
	}
	for _, ref := range request.Coverage {
		if _, err = s.verifyReceipt(ctx, owner, ref, ref.ReadReceipt); err != nil {
			return domain.SummaryVersion{}, err
		}
	}
	for _, claim := range request.Claims {
		for _, evidence := range claim.Evidence {
			token := evidence.ReadReceipt
			if token == "" {
				token = evidence.Message.ReadReceipt
			}
			read, proofErr := s.verifyReceipt(ctx, owner, evidence.Message, token)
			if proofErr != nil {
				return domain.SummaryVersion{}, proofErr
			}
			if evidence.Quote == "" || !strings.Contains(read.Text, evidence.Quote) ||
				evidence.View != "" && evidence.View != read.View {
				return domain.SummaryVersion{}, searchHTTPError(
					http.StatusBadRequest,
					"evidence quote was not exposed by this read receipt",
				)
			}
		}
	}
	version, err := s.summaries.PublishSummary(ctx, owner, request)
	return version, searchSourceError(err)
}

func (s *WorkspaceSearchService) owner(
	ctx context.Context,
	meta auth.RequestMetadata,
) (string, error) {
	if s.principal == nil {
		return "", domain.ErrUnauthorized
	}
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return "", err
	}
	if principal.User.UserID == "" {
		return "", domain.ErrUnauthorized
	}
	return principal.User.UserID, nil
}

func (s *WorkspaceSearchService) currentDocument(
	ctx context.Context,
	owner, kind, id string,
) (domain.SearchDocument, error) {
	if kind == "summary" {
		if s.summaries == nil {
			return domain.SearchDocument{}, domain.ErrNotFound
		}
		summary, err := s.summaries.GetSummary(ctx, owner, id, 0)
		if err != nil {
			return domain.SearchDocument{}, err
		}
		return domain.SearchDocument{
			Kind:            "summary",
			SourceID:        summary.SummaryID,
			OwnerUserID:     owner,
			Title:           summary.Title,
			Body:            summary.Body,
			SummaryRevision: summary.Revision,
			CreatedAt:       summary.CreatedAt,
		}, nil
	}
	if kind != "message" && kind != "session" || s.sources == nil {
		return domain.SearchDocument{}, domain.ErrNotFound
	}
	return s.sources.ReadSearchDocument(ctx, owner, kind, id)
}

func (s *WorkspaceSearchService) readMessageVersion(
	ctx context.Context,
	owner string,
	ref SearchRef,
) (domain.SearchDocument, error) {
	if ref.ContentHash == "" {
		return s.sources.ReadSearchDocument(ctx, owner, "message", ref.ID)
	}
	version := domain.MessageVersionRef{
		MessageID:               ref.ID,
		ContentHash:             ref.ContentHash,
		CanonicalizationVersion: ref.CanonicalizationVersion,
	}
	if s.summaries != nil {
		return s.summaries.ReadMessageVersion(ctx, owner, version)
	}
	document, err := s.sources.ReadSearchDocument(ctx, owner, "message", ref.ID)
	if err == nil &&
		(document.ContentHash != ref.ContentHash || document.CanonicalizationVersion != ref.CanonicalizationVersion) {
		return domain.SearchDocument{}, domain.ErrSourceVersionUnavailable
	}
	return document, err
}

func documentRef(document domain.SearchDocument) SearchRef {
	return SearchRef{Kind: document.Kind, ID: document.SourceID, Revision: document.SummaryRevision,
		ContentHash: document.ContentHash, CanonicalizationVersion: document.CanonicalizationVersion}
}

func literalContains(text, query string) bool {
	return strings.Contains(strings.ToLower(text), strings.ToLower(query))
}

func searchFilterContains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func validateWorkspaceQuery(query domain.SearchQuery) error {
	if query.Query == "" || utf8.RuneCountInString(query.Query) > 256 || query.Limit < 0 ||
		query.Limit > 50 || len(query.Kinds) > 3 || len(query.SessionIDs) > 50 {
		return searchHTTPError(http.StatusBadRequest, "invalid search query or limits")
	}
	for _, kind := range query.Kinds {
		if kind != "message" && kind != "session" && kind != "summary" {
			return searchHTTPError(http.StatusBadRequest, "invalid search kind")
		}
	}
	return nil
}

func validateWorkspaceRead(request WorkspaceReadRequest) error {
	invalidRef := request.Ref.ID == "" || request.Ref.Revision < 0
	invalidPage := request.Offset < 0 || request.Offset > 1_000_000_000 ||
		request.Limit < 0 || request.Limit > 16*1024
	invalidContext := request.Before < 0 || request.After < 0 || request.Before > 50 ||
		request.After > 50
	if invalidRef || invalidPage || invalidContext {
		return searchHTTPError(http.StatusBadRequest, "invalid read reference or limits")
	}
	if request.Offset > 0 && request.Ref.Kind == "message" && request.Ref.ContentHash == "" {
		return searchHTTPError(
			http.StatusBadRequest,
			"message version is required after the first page",
		)
	}
	return nil
}

func searchMatchesCurrent(
	document domain.SearchDocument,
	owner string,
	query domain.SearchQuery,
) bool {
	return !document.Deleted && document.OwnerUserID == owner &&
		(literalContains(document.Title, query.Query) || literalContains(document.Body, query.Query)) &&
		(len(query.SessionIDs) == 0 || searchFilterContains(query.SessionIDs, document.SessionID)) &&
		(len(query.Kinds) == 0 || searchFilterContains(query.Kinds, document.Kind))
}

func searchSnippet(text, query string) string {
	runes := []rune(text)
	index := strings.Index(strings.ToLower(text), strings.ToLower(query))
	start := 0
	if index >= 0 {
		start = utf8.RuneCountInString(strings.ToLower(text)[:index]) - 40
	}
	if start < 0 {
		start = 0
	}
	end := min(start+200, len(runes))
	return string(runes[start:end])
}

func searchHTTPError(status int, message string) error {
	return apperr.Error{Status: status, Message: message}
}

func searchSourceError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, domain.ErrSummaryVersionMismatch) ||
		errors.Is(err, domain.ErrSourceVersionUnavailable) {
		return searchHTTPError(http.StatusConflict, err.Error())
	}
	return err
}
