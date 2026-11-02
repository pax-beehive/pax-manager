package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

func putSecretaryTurnMessage(
	t *testing.T, srv *Service, fixture conversationTestFixture,
	id, sessionID, turnID, role, kind, text string,
) {
	t.Helper()
	message := domain.Message{
		MessageID: id, AgentID: fixture.agentID, SessionID: sessionID, TurnID: turnID,
		Role: role, MessageType: kind, Source: domain.MessageSourceACPTunnel,
		Direction: domain.MessageDirectionAgentToUser,
	}
	if role == "user" {
		message.Direction = domain.MessageDirectionUserToAgent
	}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &message))
	require.NoError(t, srv.store.UpsertMessagePart(t.Context(), &domain.MessagePart{
		MessageID: id, PartType: domain.MessagePartText, Text: text,
	}))
}

func TestSecretaryHistoryGivenExactTurnAndFullViewThenPagesOnlyItsVisibleMessages(t *testing.T) {
	srv, fixture, job, _, capability := secretaryToolsFixture(t)
	createConversationTestSession(t, srv, fixture, "other-session", "other-native")
	for _, message := range []struct{ id, session, turn, role, kind, text string }{
		{"question", "source-session", "turn-1", "user", "user_message", "Complete user question.\n\nMore context."},
		{"reply-first", "source-session", "turn-1", "assistant", "agent_message_chunk", "First detail.\n\nFirst ending."},
		{"reply-last", "source-session", "turn-1", "assistant", "agent_message_chunk", "Final detail.\n\nFinal ending."},
		{"tool", "source-session", "turn-1", "tool", "tool_call", "Private tool output"},
		{"reasoning", "source-session", "turn-1", "assistant", "agent_thought_chunk", "Private reasoning"},
		{"unrelated", "source-session", "turn-2", "assistant", "agent_message_chunk", "Another turn"},
		{"ungrouped", "source-session", "", "user", "user_message", "No invented turn"},
		{"other-session", "other-session", "turn-1", "assistant", "agent_message_chunk", "Other session"},
	} {
		putSecretaryTurnMessage(
			t,
			srv,
			fixture,
			message.id,
			message.session,
			message.turn,
			message.role,
			message.kind,
			message.text,
		)
	}
	var collected []userapi.WorkspaceMessageRead
	before := int64(0)
	for pageNumber := 0; pageNumber < 5; pageNumber++ {
		arguments, err := json.Marshal(map[string]any{
			"session_id": "source-session", "turn_id": "turn-1", "view": "full", "limit": 2, "before_seq": before,
		})
		require.NoError(t, err)
		data, err := secretaryToolCall(t, srv, fixture, capability, "history", string(arguments))
		require.NoError(t, err)
		page := data.(secretaryHistoryPage)
		assert.LessOrEqual(t, len(page.Messages), 2)
		collected = append(page.Messages, collected...)
		for _, message := range page.Messages {
			assert.Equal(t, "turn-1", message.TurnID)
			assert.Equal(t, "source-session", message.SessionID)
			assert.Equal(t, "full", message.View)
			verified, verifyErr := srv.workspaceSearch.VerifyReadReceipt(
				t.Context(),
				job.OwnerUserID,
				domain.MessageVersionRef{
					MessageID:               message.MessageID,
					ContentHash:             message.Ref.ContentHash,
					CanonicalizationVersion: message.Ref.CanonicalizationVersion,
				},
				message.ReadReceipt,
			)
			require.NoError(t, verifyErr)
			assert.Equal(t, message.Text, verified.Text)
		}
		if !page.Pagination.HasOlder {
			break
		}
		require.Greater(t, page.Pagination.NextBeforeSeq, int64(0))
		assert.NotEqual(t, before, page.Pagination.NextBeforeSeq)
		before = page.Pagination.NextBeforeSeq
	}
	require.Len(t, collected, 3)
	assert.Equal(t, []string{"question", "reply-first", "reply-last"}, []string{
		collected[0].MessageID, collected[1].MessageID, collected[2].MessageID,
	})
	assert.Equal(t, "First detail.\n\nFirst ending.", collected[1].Text)
	assert.Equal(t, "Final detail.\n\nFinal ending.", collected[2].Text)
	data, err := secretaryToolCall(t, srv, fixture, capability, "history",
		`{"session_id":"source-session","turn_id":"missing","view":"full"}`)
	require.NoError(t, err)
	assert.Empty(t, data.(secretaryHistoryPage).Messages)
}

func TestSecretaryHistoryGivenDefaultAndFullViewsThenPreservesVersionAndExplicitContinuation(
	t *testing.T,
) {
	srv, fixture, _, _, capability := secretaryToolsFixture(t)
	body := strings.Repeat("a", 17000) + "\n\nLast paragraph."
	putSecretaryTurnMessage(t, srv, fixture, "long-reply", "source-session", "turn-long",
		"assistant", "agent_message_chunk", body)
	data, err := secretaryToolCall(t, srv, fixture, capability, "history",
		`{"session_id":"source-session","turn_id":"turn-long"}`)
	require.NoError(t, err)
	last := data.(secretaryHistoryPage).Messages[0]
	assert.Equal(t, "Last paragraph.", last.Text)
	assert.Equal(t, "last", last.View)
	data, err = secretaryToolCall(t, srv, fixture, capability, "history",
		`{"session_id":"source-session","turn_id":"turn-long","view":"full"}`)
	require.NoError(t, err)
	full := data.(secretaryHistoryPage).Messages[0]
	assert.Equal(t, last.Ref, full.Ref)
	assert.Equal(t, domain.SearchContentHash(body), full.Ref.ContentHash)
	assert.NotEqual(t, last.ReadReceipt, full.ReadReceipt)
	assert.Equal(t, strings.Repeat("a", 16384), full.Text)
	assert.True(t, full.HasMore)
	assert.Equal(t, 16384, full.NextOffset)
	arguments, err := json.Marshal(
		userapi.WorkspaceReadRequest{Ref: full.Ref, View: "full", Offset: full.NextOffset},
	)
	require.NoError(t, err)
	data, err = secretaryToolCall(t, srv, fixture, capability, "read", string(arguments))
	require.NoError(t, err)
	continuation := data.(userapi.WorkspaceMessageRead)
	assert.Equal(t, body, full.Text+continuation.Text)
	assert.False(t, continuation.HasMore)
	for _, arguments := range []string{
		`{"session_id":"source-session","turn_id":" "}`,
		`{"session_id":"source-session","view":"unsupported"}`,
	} {
		_, err = secretaryToolCall(t, srv, fixture, capability, "history", arguments)
		assert.Error(t, err)
	}
}

type secretaryExpandedHistoryStore struct {
	domain.Store
	page domain.MessageHistoryPage
}

func (s *secretaryExpandedHistoryStore) ListTurnSummaryPage(
	context.Context, string, string, string, int64, int64, int,
) (domain.MessageHistoryPage, error) {
	return s.page, nil
}

func (*secretaryExpandedHistoryStore) LatestSessionMessage(
	context.Context, string, string,
) (domain.Message, error) {
	return domain.Message{}, nil
}

func (s *secretaryExpandedHistoryStore) ListMessageSummaryPage(
	context.Context,
	string,
	string,
	int64,
	int64,
	int,
) (domain.MessageHistoryPage, error) {
	return s.page, nil
}

func (s *secretaryExpandedHistoryStore) ListMessageSummaryParts(
	context.Context,
	[]string,
) (map[string][]domain.MessagePart, error) {
	return nil, nil
}

type secretaryCountingSources struct {
	agentID string
	reads   []string
	turns   map[string]string
}

func (s *secretaryCountingSources) ReadSearchDocument(
	_ context.Context,
	owner, kind, id string,
) (domain.SearchDocument, error) {
	s.reads = append(s.reads, id)
	return domain.SearchDocument{
		Kind:                    kind,
		SourceID:                id,
		OwnerUserID:             owner,
		AgentID:                 s.agentID,
		SessionID:               "source-session",
		TurnID:                  s.turns[id],
		Role:                    "user",
		Body:                    id,
		ContentHash:             domain.SearchContentHash(id),
		CanonicalizationVersion: domain.SearchCanonicalizationVersion,
	}, nil
}

func TestSecretaryHistoryGivenChangedTurnMembershipThenNeverReturnsAnotherTurn(t *testing.T) {
	srv, fixture, _, _, capability := secretaryToolsFixture(t)
	page := domain.MessageHistoryPage{HeadSeq: 3, NextBeforeSeq: 1, NextAfterSeq: 3}
	for index, turn := range []string{"another-turn", "selected-turn", "selected-turn"} {
		page.Messages = append(page.Messages, domain.Message{
			MessageID: fmt.Sprintf("m%d", index+1), SessionSeq: int64(index + 1), TurnID: turn,
			Role: "user", MessageType: "user_message", Source: domain.MessageSourceACPTunnel,
			Direction: domain.MessageDirectionUserToAgent,
		})
	}
	srv.store = &secretaryExpandedHistoryStore{Store: srv.store, page: page}
	sources := &secretaryCountingSources{agentID: fixture.agentID,
		turns: map[string]string{"m2": "selected-turn", "m3": "changed-turn"}}
	srv.workspaceSearch = userapi.NewWorkspaceSearchService(nil, sources, nil, nil,
		[]byte(strings.Repeat("k", 32)))
	data, err := secretaryToolCall(t, srv, fixture, capability, "history",
		`{"session_id":"source-session","turn_id":"selected-turn","view":"full"}`)
	require.NoError(t, err)
	assert.Equal(t, []string{"m2", "m3"}, sources.reads)
	result := data.(secretaryHistoryPage)
	require.Len(t, result.Messages, 1)
	assert.Equal(t, "m2", result.Messages[0].MessageID)
}

func (*secretaryCountingSources) ReadSearchContext(
	context.Context,
	string,
	string,
	int,
	int,
) ([]domain.SearchDocument, error) {
	return nil, nil
}

func TestSecretaryToolsGivenThousandTurnContextRowsThenOnlyReadsServerWindow(t *testing.T) {
	srv, fixture, _, _, capability := secretaryToolsFixture(t)
	page := domain.MessageHistoryPage{
		HeadSeq:       1000,
		NextBeforeSeq: 999,
		NextAfterSeq:  1000,
		HasOlder:      true,
		HasMore:       true,
	}
	for seq := int64(1); seq <= 1000; seq++ {
		page.Messages = append(
			page.Messages,
			domain.Message{
				MessageID:   fmt.Sprintf("m%d", seq),
				SessionSeq:  seq,
				TurnID:      "same-turn",
				Role:        "user",
				MessageType: "user_message",
				Source:      domain.MessageSourceACPTunnel,
				Direction:   domain.MessageDirectionUserToAgent,
			},
		)
	}
	// Duplicate context does not consume another canonical read or alter cursors.
	page.Messages = append(page.Messages, page.Messages[998])
	srv.store = &secretaryExpandedHistoryStore{Store: srv.store, page: page}
	sources := &secretaryCountingSources{agentID: fixture.agentID}
	srv.workspaceSearch = userapi.NewWorkspaceSearchService(
		nil,
		sources,
		nil,
		nil,
		[]byte(strings.Repeat("k", 32)),
	)
	data, err := secretaryToolCall(
		t,
		srv,
		fixture,
		capability,
		"history",
		`{"session_id":"source-session","limit":2}`,
	)
	require.NoError(t, err)
	history := data.(secretaryHistoryPage)
	require.Len(t, history.Messages, 2)
	assert.Equal(t, []string{"m999", "m1000"}, sources.reads)
	assert.Equal(
		t,
		domain.MessageHistoryPagination{
			HeadSeq:       1000,
			NextBeforeSeq: 999,
			NextAfterSeq:  1000,
			HasOlder:      true,
			HasMore:       true,
		},
		history.Pagination,
	)
	assert.True(
		t,
		secretaryHistoryInWindow(
			domain.MessageWithParts{Message: domain.Message{SessionSeq: 999}},
			secretaryHistoryRequest{AfterSeq: 998},
			history.Pagination,
		),
	)
	assert.False(
		t,
		secretaryHistoryInWindow(
			domain.MessageWithParts{Message: domain.Message{SessionSeq: 999}},
			secretaryHistoryRequest{AfterSeq: 999},
			history.Pagination,
		),
	)
	assert.False(
		t,
		secretaryHistoryInWindow(
			domain.MessageWithParts{Message: domain.Message{SessionSeq: 1000}},
			secretaryHistoryRequest{BeforeSeq: 1000},
			history.Pagination,
		),
	)
}
