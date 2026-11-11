package search

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestAgentSearchScopeNormalizesWithoutChangingCallerAndBoundsAgents(t *testing.T) {
	query := domain.SearchQuery{
		OwnerUserID: "owner", Query: "port", AgentIDs: []string{"z", "a"},
	}
	normalized, err := normalizeQuery(query)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "z"}, normalized.AgentIDs)
	assert.Equal(t, []string{"z", "a"}, query.AgentIDs)
	query.AgentIDs = make([]string, 101)
	_, err = normalizeQuery(query)
	assert.ErrorIs(t, err, ErrInvalidQuery)
}

func TestAgentSearchScopeConstrainsFlatGroupedAndGroupExpansionQueries(t *testing.T) {
	query := domain.SearchQuery{
		OwnerUserID: "owner", Query: "port", AgentIDs: []string{"agent"},
		SessionIDs: []string{"shared-session"},
	}
	for _, mode := range []string{"flat", "grouped", "expansion"} {
		t.Run(mode, func(t *testing.T) {
			current := query
			var body map[string]any
			if mode == "grouped" {
				current.GroupBy = "session"
				body = groupedSearchBody(current, groupCursor{PIT: "pit"})
			} else {
				if mode == "expansion" {
					current.GroupKey = "selected-group"
				}
				body = searchBody(current, cursor{PIT: "pit"})
			}
			filters := body["query"].(map[string]any)["bool"].(map[string]any)["filter"]
			assert.Contains(t, filters, map[string]any{
				"term": map[string]any{"owner_user_id": "owner"},
			})
			assert.Contains(t, filters, map[string]any{
				"terms": map[string]any{"agent_id": []string{"agent"}},
			})
			assert.Contains(t, filters, map[string]any{
				"terms": map[string]any{"session_id": []string{"shared-session"}},
			})
		})
	}
}

func TestAgentSearchScopeCannotReuseFlatOrGroupedCursorOutsideItsAgents(t *testing.T) {
	query := domain.SearchQuery{OwnerUserID: "owner", Query: "port", AgentIDs: []string{"agent"}}
	flat, err := json.Marshal(cursor{
		Fingerprint: queryFingerprint(query), PIT: "pit",
		After: []json.RawMessage{
			json.RawMessage(`2`),
			json.RawMessage(`12`),
			json.RawMessage(`"message:m"`),
			json.RawMessage(`1`),
		},
	})
	require.NoError(t, err)
	grouped, err := json.Marshal(
		groupCursor{Fingerprint: groupFingerprint(query), PIT: "pit", Offset: 1},
	)
	require.NoError(t, err)
	query.Cursor = base64.RawURLEncoding.EncodeToString(flat)
	_, err = readCursor(query)
	require.NoError(t, err)
	query.Cursor = base64.RawURLEncoding.EncodeToString(grouped)
	_, err = readGroupCursor(query)
	require.NoError(t, err)
	for _, agents := range [][]string{{"other-agent"}, {"agent", "other-agent"}, nil} {
		changed := query
		changed.AgentIDs = agents
		changed.Cursor = base64.RawURLEncoding.EncodeToString(flat)
		_, err = readCursor(changed)
		assert.ErrorIs(t, err, ErrInvalidQuery)
		changed.Cursor = base64.RawURLEncoding.EncodeToString(grouped)
		_, err = readGroupCursor(changed)
		assert.ErrorIs(t, err, ErrInvalidQuery)
	}
	query.AgentIDs = nil
	raw, err := json.Marshal(
		[]any{
			rankingVersion,
			query.OwnerUserID,
			domain.SearchTerms(query.Query),
			query.Kinds,
			query.SessionIDs,
		},
	)
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	assert.Equal(
		t,
		hex.EncodeToString(sum[:]),
		queryFingerprint(query),
		"unfiltered browser cursors retain their fingerprint",
	)
}
