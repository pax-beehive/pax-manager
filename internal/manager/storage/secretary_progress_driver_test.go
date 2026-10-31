package storage

import (
	"context"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryProgressRows(t *testing.T, mode string) scriptedRows {
	t.Helper()
	message := domain.Message{
		MessageID:   "m1",
		AgentID:     "source",
		SessionID:   "session",
		Role:        "assistant",
		MessageType: "chat",
		TurnID:      "turn-1",
	}
	parts := []domain.MessagePart{{MessageID: "m1", PartIndex: 0, PartType: "text", Text: "body"}}
	hash, canonical, owner := domain.SearchContentHash(
		"body",
	), domain.SearchCanonicalizationVersion, "owner"
	switch mode {
	case "changed":
		hash = domain.SearchContentHash("old")
	case "canonicalization":
		canonical = "old-canonicalization"
	case "owner":
		owner = "other"
	case "reasoning":
		message.RawJSON = json.RawMessage(`{"channel":"reasoning"}`)
	case "tool":
		message.Role = "tool"
	case "duplicate-parts":
		parts = append(parts, parts[0])
	}
	rawMessage, err := json.Marshal(message)
	require.NoError(t, err)
	rawParts, err := json.Marshal(parts)
	require.NoError(t, err)
	if mode == "invalid-message" {
		rawMessage = []byte(`{`)
	}
	if mode == "invalid-parts" {
		rawParts = []byte(`{`)
	}
	return scriptedRows{
		columns: []string{
			"source",
			"agent",
			"session",
			"title",
			"completed",
			"message",
			"parts",
			"hash",
			"canonicalization",
			"owner",
		},
		values: [][]driver.Value{
			{
				"1",
				"source",
				"session",
				"Session title",
				time.Now(),
				rawMessage,
				rawParts,
				hash,
				canonical,
				owner,
			},
		},
	}
}

func TestSecretaryProgressPostgresDriverGivenCanonicalSourceThenCountsOnlyPublishedCurrentCoverage(
	t *testing.T,
) {
	for _, mode := range []string{"complete", "changed", "canonicalization", "owner", "reasoning", "tool", "duplicate-parts", "invalid-message", "invalid-parts", "invalid-scan", "empty"} {
		t.Run(mode, func(t *testing.T) {
			rows := secretaryProgressRows(t, mode)
			if mode == "invalid-scan" {
				rows.values[0][4] = "invalid-time"
			}
			if mode == "empty" {
				rows.values = nil
			}
			script := &scriptedPostgresScript{queries: []scriptedRows{
				{columns: []string{"as_of"}, values: [][]driver.Value{{time.Now()}}}, rows,
			}}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			page, err := store.ReadSecretaryProgress(
				t.Context(),
				"owner",
				domain.SecretaryProgressQuery{},
			)
			if strings.HasPrefix(mode, "invalid-") {
				assert.Error(t, err)
				assert.False(t, script.committed)
				return
			}
			require.NoError(t, err)
			assert.True(t, script.committed)
			assert.Equal(t, "owner", script.queryArgs[1][0].Value)
			assert.Contains(t, script.queryTexts[1], "a.owner_user_id = $1")
			assert.Contains(t, script.queryTexts[1], "secretary_internal_sessions")
			if mode == "reasoning" || mode == "tool" || mode == "duplicate-parts" ||
				mode == "empty" {
				assert.Zero(t, page.Counts.TotalMessages)
				return
			}
			assert.Equal(t, 1, page.Counts.TotalSessions)
			assert.Equal(t, 1, page.Counts.TotalTurns)
			if mode == "complete" {
				assert.Equal(t, 1, page.Counts.CompletedMessages)
				assert.Equal(t, 1, page.Counts.CompletedTurns)
			} else {
				assert.Equal(t, 1, page.Counts.PendingMessages)
				assert.Equal(t, 1, page.Counts.PendingTurns)
			}
		})
	}
}

func TestSecretaryProgressPostgresDriverGivenUnavailableDatabaseThenReturnsErrors(t *testing.T) {
	for _, mode := range []string{"closed", "time-query", "source-query", "anonymous", "bad-cursor"} {
		t.Run(mode, func(t *testing.T) {
			script := &scriptedPostgresScript{}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			owner, query := "owner", domain.SecretaryProgressQuery{}
			switch mode {
			case "closed":
				require.NoError(t, store.db.Close())
			case "source-query":
				script.queries = []scriptedRows{
					{columns: []string{"time"}, values: [][]driver.Value{{time.Now()}}},
				}
			case "anonymous":
				owner = ""
			case "bad-cursor":
				query.Cursor = "!"
			}
			_, err := store.ReadSecretaryProgress(t.Context(), owner, query)
			assert.Error(t, err)
		})
	}
}

func TestSecretaryProgressGivenStrictCursorThenRejectsInvalidVersionsAndFields(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"unknown":1}`, `{"v":1,"o":"wrong","a":"1"}`, `{"v":2,"o":"OWNER","a":"1"}`, `{"v":1,"o":"OWNER","a":"0"}`, `{"v":1,"o":"OWNER","a":"01"}`, `{"v":1,"o":"OWNER","a":"-1"}`, `{"v":1,"o":"OWNER","a":"memory:sha256:bad"}`, `{"v":1,"o":"OWNER","a":"memory:sha256:GGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGG"}`, `{"v":1,"o":"OWNER","a":"1"} {}`} {
		raw = strings.ReplaceAll(raw, "OWNER", domain.SearchContentHash("owner"))
		_, _, err := secretaryProgressQuery(
			"owner",
			domain.SecretaryProgressQuery{
				Cursor: base64.RawURLEncoding.EncodeToString([]byte(raw)),
			},
		)
		assert.ErrorIs(t, err, domain.ErrInvalidCursor, raw)
	}
	assert.True(t, secretaryProgressSourceID("memory:"+domain.SearchContentHash("source")))
	store, _ := secretaryMemoryFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := store.ReadSecretaryProgress(ctx, "owner", domain.SecretaryProgressQuery{})
	assert.ErrorIs(t, err, context.Canceled)
	page, err := store.ReadSecretaryProgress(t.Context(), "other", domain.SecretaryProgressQuery{})
	require.NoError(t, err)
	assert.NotNil(t, page.Sessions)
	assert.Empty(t, page.Sessions)
}
