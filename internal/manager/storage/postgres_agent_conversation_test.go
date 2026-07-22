package storage

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestPostgresAgentConversationGivenSameOwnerAgentsWhenStartedThenWritesGraphAndPrompt(
	t *testing.T,
) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	script := &scriptedPostgresScript{
		queries: []scriptedRows{
			scriptedRow(postgresAgentValues("agent_source", "user_1", now)...),
			scriptedRow(
				postgresRepresentativeValues(
					"rep_source",
					"profile_source",
					"agent_source",
					"user_1",
					now,
				)...),
			scriptedRow(
				postgresRepresentativeValues(
					"rep_target",
					"profile_target",
					"agent_target",
					"user_1",
					now,
				)...),
			scriptedRow(postgresAgentValues("agent_target", "user_1", now)...),
			scriptedRow(true),
			scriptedRow(
				"conv_1",
				domain.ConversationTypeAgentThread,
				"personal",
				"user_1",
				domain.ConversationHistoryFullHistory,
				domain.ConversationStatusActive,
				now,
				nil,
			),
			scriptedRow(
				"bind_source",
				"conv_1",
				"rep_source",
				domain.ConversationAgentRelationshipParticipant,
				"user_1",
				domain.ConversationAgentAccessFromBinding,
				domain.ConversationAgentBindingStatusActive,
				now,
				nil,
			),
			scriptedRow(
				"bind_target",
				"conv_1",
				"rep_target",
				domain.ConversationAgentRelationshipAssistant,
				"user_1",
				domain.ConversationAgentAccessFromBinding,
				domain.ConversationAgentBindingStatusActive,
				now,
				nil,
			),
			scriptedRow("user_1"),
			scriptedRow(true),
			scriptedRow("user_1"),
			scriptedRow("user_1"),
			scriptedRow(
				postgresSessionValues(
					"agent_source",
					"sess_source",
					"conv_1",
					"profile_source",
					"rep_source",
					now,
				)...),
			scriptedRow("user_1"),
			scriptedRow(true),
			scriptedRow("user_1"),
			scriptedRow("user_1"),
			scriptedRow(
				postgresSessionValues(
					"agent_target",
					"sess_target",
					"conv_1",
					"profile_target",
					"rep_target",
					now,
				)...),
			scriptedRow(
				"inv_1",
				"conv_1",
				"",
				"rep_source",
				"agent_source",
				"sess_source",
				"rep_target",
				"agent_target",
				"sess_target",
				"",
				"user_1",
				domain.ConversationAgentAccessCurrentTurn,
				[]byte(`[]`),
				10,
				10,
				domain.ConversationAgentInvocationStatusActive,
				now,
				nil,
				nil,
			),
			scriptedRow(
				int64(1),
				"msg_prompt",
				"conv_1",
				"user_1",
				"node_1",
				"agent_source",
				"sess_source",
				domain.MessageSourceACPTunnel,
				domain.MessageDirectionUserToAgent,
				"user",
				"sent",
				domain.MessageTypePaxUser,
				"",
				"",
				"",
				"agent_conversation:msg_prompt",
				[]byte(`{"input":"Review this"}`),
				now,
				now,
			),
			scriptedRow(
				int64(1),
				"msg_prompt",
				0,
				domain.MessagePartText,
				"Review this",
				[]byte(`{"input":"Review this"}`),
				"",
				now,
				now,
			),
			scriptedRow(
				int64(1),
				"msg_prompt",
				"conv_1",
				"user_1",
				"node_1",
				"agent_source",
				"sess_source",
				domain.MessageSourceACPTunnel,
				domain.MessageDirectionUserToAgent,
				"user",
				"sent",
				domain.MessageTypePaxUser,
				"",
				"",
				"",
				"agent_conversation:msg_prompt",
				[]byte(`{"input":"Review this"}`),
				now,
				now,
			),
			scriptedRow(
				int64(2),
				"msg_display",
				"conv_1",
				"user_1",
				"node_1",
				"agent_source",
				"sess_source",
				domain.MessageSourceACPTunnel,
				domain.MessageDirectionUserToAgent,
				"user",
				"sent",
				domain.MessageTypePaxInvocation,
				"msg_prompt",
				"",
				"",
				"agent_conversation:inv_1:inquiry:source:display",
				[]byte(
					`{"invocation_id":"inv_1","invocation_type":"agent_conversation","phase":"inquiry","side":"source","replaces_message_ids":["msg_prompt"],"sender":{"representative_agent_id":"rep_source","agent_id":"agent_source","session_id":"sess_source"},"receiver":{"representative_agent_id":"rep_target","agent_id":"agent_target","session_id":"sess_target"},"content":{"display_text":"Asked agent_target for input.","original_text":"Review this"}}`,
				),
				now,
				now,
			),
			scriptedRow(
				int64(2),
				"msg_display",
				0,
				domain.MessagePartText,
				"Asked agent_target for input.",
				[]byte(
					`{"invocation_id":"inv_1","invocation_type":"agent_conversation","phase":"inquiry","side":"source","replaces_message_ids":["msg_prompt"],"sender":{"representative_agent_id":"rep_source","agent_id":"agent_source","session_id":"sess_source"},"receiver":{"representative_agent_id":"rep_target","agent_id":"agent_target","session_id":"sess_target"},"content":{"display_text":"Asked agent_target for input.","original_text":"Review this"}}`,
				),
				"",
				now,
				now,
			),
		},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()

	start, err := store.StartAgentConversation(
		context.Background(),
		Node{NodeID: "node_1"},
		domain.StartAgentConversationRequest{
			FromRuntimeAgentID:      "agent_source",
			ToRepresentativeAgentID: "rep_target",
			ConversationID:          "conv_1",
			Input:                   "Review this",
			MaxTurns:                99,
		},
	)

	require.NoError(t, err)
	require.Equal(t, "conv_1", start.Conversation.ConversationID)
	require.Equal(t, "rep_source", start.SourceRepresentative.RepresentativeAgentID)
	require.Equal(t, "rep_target", start.TargetRepresentative.RepresentativeAgentID)
	require.Equal(t, "sess_source", start.SourceSession.SessionID)
	require.Equal(t, "sess_target", start.TargetSession.SessionID)
	require.Empty(t, start.Invocation.ParentInvocationID)
	require.Equal(t, "rep_source", start.Invocation.SourceRepresentativeAgentID)
	require.Equal(t, "agent_source", start.Invocation.SourceRuntimeAgentID)
	require.Equal(t, "sess_source", start.Invocation.SourceSessionID)
	require.Equal(t, "rep_target", start.Invocation.TargetRepresentativeAgentID)
	require.Equal(t, "agent_target", start.Invocation.TargetRuntimeAgentID)
	require.Equal(t, "sess_target", start.Invocation.TargetSessionID)
	require.Empty(t, start.Invocation.ReceiptTokenHash)
	require.Equal(t, 10, start.Invocation.MaxTurns)
	require.Equal(t, 10, start.Invocation.RemainingTurns)
	require.Equal(t, "Review this", start.PromptMessage.Parts[0].Text)
	assert.Equal(t, 5, script.execCount)
	assert.Empty(t, script.queries)
}

func TestPostgresRepresentativeAgentGivenOwnedRuntimeAgentWhenUpsertedThenWritesProfileAndRep(
	t *testing.T,
) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	script := &scriptedPostgresScript{
		queries: []scriptedRows{
			scriptedRow(postgresAgentValues("agent_source", "user_1", now)...),
			scriptedRow(
				"prof_1",
				"user",
				"user_1",
				"Source reviewer",
				"Reviews work",
				[]byte(`{}`),
				"",
				"",
				[]byte(`{}`),
				[]byte(`{}`),
				domain.ConversationStatusActive,
				"user_1",
				now,
				now,
				nil,
			),
			scriptedRow(
				postgresRepresentativeValues("rep_1", "prof_1", "agent_source", "user_1", now)...),
		},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()

	rep, profile, err := store.UpsertRepresentativeAgent(
		context.Background(),
		UserPrincipal{User: User{UserID: "user_1"}},
		domain.UpsertRepresentativeAgentRequest{
			RuntimeAgentID: "agent_source",
			ProfileID:      "prof_1",
			DisplayName:    "Source reviewer",
			Description:    "Reviews work",
		},
	)

	require.NoError(t, err)
	require.Equal(t, "rep_1", rep.RepresentativeAgentID)
	require.Equal(t, "prof_1", profile.ProfileID)
	require.Equal(t, "Source reviewer", profile.DisplayName)
	assert.Empty(t, script.queries)
}

func TestPostgresRepresentativeAgentGivenVisibleRuntimeAgentWhenListedThenReturnsRepresentatives(
	t *testing.T,
) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	script := &scriptedPostgresScript{
		queries: []scriptedRows{
			scriptedRow(
				postgresRepresentativeValues("rep_1", "prof_1", "agent_source", "user_1", now)...),
		},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()

	reps, err := store.ListRepresentativeAgents(
		context.Background(),
		UserPrincipal{User: User{UserID: "user_1"}},
		"agent_source",
	)

	require.NoError(t, err)
	require.Len(t, reps, 1)
	require.Equal(t, "rep_1", reps[0].RepresentativeAgentID)
	assert.Empty(t, script.queries)
}

func TestPostgresAgentConversationGivenInvocationWhenCompletingThenUpdatesStatus(t *testing.T) {
	script := &scriptedPostgresScript{execResults: []int64{1}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()

	err := store.CompleteAgentConversationInvocation(context.Background(), "inv_1")

	require.NoError(t, err)
	assert.Equal(t, 1, script.execCount)
}

func TestPostgresAgentConversationGivenMissingInvocationWhenCompletingThenReturnsNotFound(
	t *testing.T,
) {
	script := &scriptedPostgresScript{execResults: []int64{0}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()

	err := store.CompleteAgentConversationInvocation(context.Background(), "inv_1")

	require.ErrorIs(t, err, ErrNotFound)
	assert.Equal(t, 1, script.execCount)
}

func TestPostgresAgentConversationGivenEmptyInvocationWhenCompletingThenReturnsNotFound(
	t *testing.T,
) {
	script := &scriptedPostgresScript{}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()

	err := store.CompleteAgentConversationInvocation(context.Background(), "")

	require.ErrorIs(t, err, ErrNotFound)
	assert.Equal(t, 0, script.execCount)
}

func TestPostgresAgentConversationGivenUserPairsWhenCheckingInteractionThenUsesOwnerOrSharedTeam(
	t *testing.T,
) {
	t.Run(
		"Given an empty source user then interaction is rejected without querying",
		func(t *testing.T) {
			script := &scriptedPostgresScript{}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()

			ok := store.agentConversationUsersCanInteract(context.Background(), "", "user_2")

			require.False(t, ok)
			assert.Empty(t, script.queries)
		},
	)

	t.Run("Given the same user then interaction is allowed without querying", func(t *testing.T) {
		script := &scriptedPostgresScript{}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()

		ok := store.agentConversationUsersCanInteract(context.Background(), "user_1", "user_1")

		require.True(t, ok)
		assert.Empty(t, script.queries)
	})

	t.Run("Given users sharing an active team then interaction is allowed", func(t *testing.T) {
		script := &scriptedPostgresScript{
			queries: []scriptedRows{scriptedRow(true)},
		}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()

		ok := store.agentConversationUsersCanInteract(context.Background(), "user_1", "user_2")

		require.True(t, ok)
		assert.Empty(t, script.queries)
	})
}

func scriptedRow(values ...driver.Value) scriptedRows {
	columns := make([]string, len(values))
	for i := range values {
		columns[i] = "col"
	}
	return scriptedRows{columns: columns, values: [][]driver.Value{values}}
}

func postgresAgentValues(agentID string, ownerUserID string, now time.Time) []driver.Value {
	return []driver.Value{
		agentID,
		"node_1",
		ownerUserID,
		agentID + " name",
		"",
		[]byte(`{}`),
		"studio",
		"codex",
		"workstation",
		"darwin",
		"0.1.0",
		"http://localhost:8642",
		"online",
		"online",
		nil,
		now,
		[]byte(`{}`),
		[]byte(`{}`),
	}
}

func postgresRepresentativeValues(
	representativeAgentID string,
	profileID string,
	runtimeAgentID string,
	representsID string,
	now time.Time,
) []driver.Value {
	return []driver.Value{
		representativeAgentID,
		profileID,
		runtimeAgentID,
		"user",
		representsID,
		"",
		domain.ConversationStatusActive,
		representsID,
		now,
		now,
		nil,
	}
}

func postgresSessionValues(
	agentID string,
	sessionID string,
	conversationID string,
	profileID string,
	representativeAgentID string,
	now time.Time,
) []driver.Value {
	return []driver.Value{
		int64(1),
		"node_1",
		agentID,
		sessionID,
		conversationID,
		profileID,
		representativeAgentID,
		"user_1",
		"",
		"codex",
		"",
		"",
		"",
		[]byte(`[]`),
		domain.MessageSourceACPTunnel,
		"idle",
		"",
		nil,
		nil,
		int64(0),
		int64(0),
		int64(0),
		int64(0),
		int64(0),
		int64(0),
		int64(0),
		int64(0),
		float64(0),
		float64(0),
		float64(0),
		"",
		"",
		"",
		now,
		now,
		[]byte(`{}`),
	}
}
