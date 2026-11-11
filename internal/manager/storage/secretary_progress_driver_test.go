package storage

import (
	"context"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgconn"
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
	}
	rawMessage, err := json.Marshal(message)
	require.NoError(t, err)
	current, err := domain.CanonicalSearchMessage(domain.MessageWithParts{Message: domain.Message{
		MessageID: "m1", Role: "assistant", MessageType: "chat",
	}, Parts: parts})
	require.NoError(t, err)
	if mode == "invalid-message" {
		rawMessage = []byte(`{`)
	}
	return scriptedRows{
		columns: []string{
			"source",
			"agent",
			"session",
			"title",
			"completed",
			"message",
			"current_hash",
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
				current.ContentHash,
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
	for _, mode := range []string{"complete", "changed", "canonicalization", "owner", "reasoning", "tool", "invalid-message", "invalid-scan", "empty"} {
		t.Run(mode, func(t *testing.T) {
			rows := secretaryProgressRows(t, mode)
			if mode == "invalid-scan" {
				rows.values[0][4] = "invalid-time"
			}
			if mode == "empty" {
				rows.values = nil
			}
			script := &scriptedPostgresScript{queries: []scriptedRows{rows}}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			result, err := store.db.QueryContext(
				t.Context(),
				secretaryProgressSQL,
				"owner",
				secretaryProgressWhitespace,
			)
			require.NoError(t, err)
			progress, err := scanSecretaryProgress(result, "owner")
			if strings.HasPrefix(mode, "invalid-") {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			page := progress.page("owner", "", 20, time.Now())
			assert.Equal(t, "owner", script.queryArgs[0][0].Value)
			assert.Contains(t, script.queryTexts[0], "a.owner_user_id = $1")
			assert.Contains(t, script.queryTexts[0], "secretary_internal_sessions")
			assert.Contains(t, script.queryTexts[0], "sha256(convert_to(parts.body,'UTF8'))")
			if mode == "reasoning" || mode == "tool" ||
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
	for _, mode := range []string{"closed", "readiness-query", "readiness-scan", "anonymous", "bad-cursor"} {
		t.Run(mode, func(t *testing.T) {
			script := &scriptedPostgresScript{}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			owner, query := "owner", domain.SecretaryProgressQuery{}
			switch mode {
			case "closed":
				require.NoError(t, store.db.Close())
			case "readiness-scan":
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

func TestSecretaryProgressPostgresDriverGivenConcurrentRefreshThenReturnsPreparing(t *testing.T) {
	for _, code := range []string{"40001", "40P01", "23503"} {
		t.Run(code, func(t *testing.T) {
			script := &scriptedPostgresScript{
				queries: []scriptedRows{
					secretaryProjectionValue("ready", false),
					secretaryProjectionValue("writer", true),
				},
				execErrors: []error{&pgconn.PgError{Code: code}},
			}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			_, err := store.ReadSecretaryProgress(
				t.Context(),
				"owner",
				domain.SecretaryProgressQuery{},
			)
			if code == "23503" {
				assert.Error(t, err)
				assert.NotErrorIs(t, err, domain.ErrSecretaryProgressPreparing)
			} else {
				assert.ErrorIs(t, err, domain.ErrSecretaryProgressPreparing)
			}
			assert.False(t, script.committed)
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

func TestSecretaryProgressGivenSQLWhitespaceThenMatchesCanonicalUnicodeTrimming(t *testing.T) {
	var mismatches []rune
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if strings.ContainsRune(secretaryProgressWhitespace, r) != unicode.IsSpace(r) {
			mismatches = append(mismatches, r)
		}
	}
	assert.Empty(t, mismatches)
}
