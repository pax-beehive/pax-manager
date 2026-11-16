package userapi

import (
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestWorkspaceSearchNodeScopeRechecksCurrentSourcesInEveryMode(t *testing.T) {
	for _, mode := range []string{"flat", "grouped", "expansion"} {
		t.Run(mode, func(t *testing.T) {
			service, sources, backend, allowed := groupedFixture()
			allowed.NodeID = "node"
			sources.docs[allowed.DocumentID()] = allowed
			backend.page.Hits = []domain.SearchDocument{allowed}
			for _, node := range []string{"other-node", ""} {
				other := allowed
				other.SourceID, other.NodeID = "private-"+node, node
				sources.docs[other.DocumentID()] = other
				stale := other
				stale.NodeID = allowed.NodeID
				backend.page.Hits = append(backend.page.Hits, stale)
				backend.pages[0].Groups[0].Matches = append(
					backend.pages[0].Groups[0].Matches,
					stale,
				)
			}
			query := domain.SearchQuery{Query: "port", NodeIDs: []string{"node"}}
			switch mode {
			case "grouped":
				query.GroupBy = "session"
			case "expansion":
				query.GroupKey = domain.SearchDocumentGroupKey(allowed)
			}
			page, err := service.Search(t.Context(), auth.RequestMetadata{}, query)
			require.NoError(t, err)
			if mode == "grouped" {
				require.Len(t, page.Groups, 1)
				require.Len(t, page.Groups[0].Matches, 1)
				assert.Equal(t, allowed.SourceID, page.Groups[0].Matches[0].Ref.ID)
				assert.Equal(t, "node", page.Groups[0].Matches[0].NodeID)
				assert.Equal(t, query.NodeIDs, backend.queries[0].NodeIDs)
			} else {
				require.Len(t, page.Hits, 1)
				assert.Equal(t, allowed.SourceID, page.Hits[0].Ref.ID)
				assert.Equal(t, "node", page.Hits[0].NodeID)
				assert.Equal(t, query.NodeIDs, backend.query.NodeIDs)
			}
		})
	}
}

func TestWorkspaceSearchNodeScopeRejectsOversizedFilter(t *testing.T) {
	service, _, backend := workspaceFixture()
	_, err := service.Search(t.Context(), auth.RequestMetadata{}, domain.SearchQuery{
		Query: "port", NodeIDs: make([]string, 51),
	})
	requireWorkspaceStatus(t, err, http.StatusBadRequest)
	assert.Empty(t, backend.query)
}

func TestWorkspaceSearchNodeScopeRechecksGroupMetadataAfterSourceRead(t *testing.T) {
	for _, metadataNode := range []string{"other-node", ""} {
		service, sources, _, doc := groupedFixture()
		doc.NodeID = "node"
		sources.docs[doc.DocumentID()] = doc
		sources.metadata = domain.SearchGroupMetadata{Key: domain.SearchDocumentGroupKey(doc),
			NodeID: metadataNode, Title: "Private title", AgentID: doc.AgentID, SessionID: doc.SessionID}
		page, err := service.Search(t.Context(), auth.RequestMetadata{}, domain.SearchQuery{
			Query: "port", NodeIDs: []string{"node"}, GroupBy: "session",
		})
		require.NoError(t, err)
		assert.Empty(t, page.Groups)
	}
}

func TestWorkspaceSearchNodeScopeOnlyReleasesCursorForVerifiedBoundary(t *testing.T) {
	for _, staleFirst := range []bool{false, true} {
		service, sources, backend, allowed := groupedFixture()
		allowed.NodeID = "node"
		sources.docs[allowed.DocumentID()] = allowed
		stale := allowed
		stale.SourceID, stale.NodeID = "private-message", "other-node"
		sources.docs[stale.DocumentID()] = stale
		stale.NodeID = "node"
		backend.page.Hits = []domain.SearchDocument{allowed, stale}
		boundary := stale.SourceID
		if staleFirst {
			backend.page.Hits = []domain.SearchDocument{stale, allowed}
			boundary = allowed.SourceID
		}
		backend.page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(
			`{"pit":"pit","after":[2,12,"message:` + boundary + `",1]}`,
		))
		page, err := service.Search(t.Context(), auth.RequestMetadata{}, domain.SearchQuery{
			Query: "port", NodeIDs: []string{"node"},
		})
		require.NoError(t, err)
		require.Len(t, page.Hits, 1)
		if staleFirst {
			assert.Equal(
				t,
				backend.page.NextCursor,
				page.NextCursor,
				"a verified boundary keeps pagination",
			)
		} else {
			assert.Empty(t, page.NextCursor, "refused boundary sort values must not leak through the cursor")
		}
	}
}

func TestWorkspaceSearchNodeScopeReadsCanonicalSummaryAttributionInEveryMode(t *testing.T) {
	for _, mode := range []string{"flat", "grouped", "expansion"} {
		for _, node := range []string{"node", "other-node", "", "unavailable"} {
			t.Run(mode+"/"+node, func(t *testing.T) {
				service, sources, backend, doc := groupedFixture()
				doc.Kind, doc.SourceID, doc.NodeID = "summary", "summary", node
				doc.SummaryRevision, doc.Body = 7, "port canonical summary"
				sources.docs[doc.DocumentID()] = doc
				if node == "unavailable" {
					delete(sources.docs, doc.DocumentID())
				}
				sources.summary = domain.SummaryVersion{
					SummaryID:   "summary",
					OwnerUserID: "owner",
					Body:        "port legacy summary",
				}
				hit := doc
				hit.NodeID = "node"
				backend.page.Hits = []domain.SearchDocument{hit}
				backend.pages[0].Groups = []domain.SearchGroup{
					{
						Key:     domain.SearchDocumentGroupKey(doc),
						Matches: []domain.SearchDocument{hit},
					},
				}
				query := domain.SearchQuery{Query: "port", NodeIDs: []string{"node"}}
				switch mode {
				case "grouped":
					query.GroupBy = "session"
				case "expansion":
					query.GroupKey = domain.SearchDocumentGroupKey(doc)
				}
				page, err := service.Search(t.Context(), auth.RequestMetadata{}, query)
				require.NoError(t, err)
				hits := page.Hits
				if mode == "grouped" && len(page.Groups) > 0 {
					hits = page.Groups[0].Matches
				}
				if node == "node" {
					require.Len(t, hits, 1)
					assert.Equal(t, int64(7), hits[0].Ref.Revision)
					assert.Equal(t, "node", hits[0].NodeID)
					assert.Equal(t, "port canonical summary", hits[0].Snippet)
				} else {
					assert.Empty(t, hits)
					assert.Empty(t, page.Groups)
				}
			})
		}
	}
}
