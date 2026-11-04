package manager

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

func TestSecretaryAgentGivenNodeAttemptWithoutDurableRunningFenceThenRejectsBeforeNativeCall(
	t *testing.T,
) {
	for name, attempt := range map[string]*domain.SecretaryAttempt{
		"missing":     nil,
		"unknown":     {ID: "attempt", BatchID: "batch", State: "unknown"},
		"other id":    {ID: "other", BatchID: "batch", State: "running"},
		"other batch": {ID: "attempt", BatchID: "other", State: "running"},
	} {
		t.Run(name, func(t *testing.T) {
			request := secretary.ExecutionRequest{
				OwnerID: "owner",
				AgentID: "agent",
				RunID:   "attempt",
				Prompt:  "Summarize",
				Job: domain.SecretaryJob{
					BatchID:   "batch",
					AttemptID: "attempt",
					Attempt:   attempt,
				},
			}
			_, err := (&Service{}).executeSecretaryAgent(t.Context(), request)
			assert.ErrorIs(t, err, domain.ErrSecretaryExecutionUnknown)
		})
	}
}

func TestSecretaryAgentRecoveryGivenCompletedPersistedTurnThenReverifiesSignedSourceReads(
	t *testing.T,
) {
	srv, fixture, job, _, _ := secretaryToolsFixture(t)
	job.SourceID, job.BatchID, job.AttemptID, job.RunID = "1", "batch", "attempt", "attempt"
	job.Attempt = &domain.SecretaryAttempt{ID: "attempt", BatchID: "batch", State: "running"}
	request := secretary.ExecutionRequest{
		OwnerID: job.OwnerUserID,
		AgentID: job.AgentID,
		RunID:   job.RunID,
		Job:     job,
	}
	sessionID := secretary.ExecutionSessionID(request.OwnerID, request.RunID)
	_, completed, err := srv.recoverSecretaryAgent(t.Context(), request)
	require.NoError(t, err)
	assert.False(t, completed)
	createConversationTestSession(t, srv, fixture, sessionID, "persisted-native")
	putSecretaryToolMessage(
		t,
		srv,
		fixture,
		"related-proof",
		"assistant",
		"Earlier paragraph.\n\nLast decision.",
	)
	readData, err := srv.workspaceSearch.WithPrincipal(secretaryToolPrincipal{owner: job.OwnerUserID}).
		Read(t.Context(), auth.RequestMetadata{}, userapi.WorkspaceReadRequest{Ref: userapi.SearchRef{Kind: "message", ID: "related-proof"}, View: "last"})
	require.NoError(t, err)
	read := readData.(userapi.WorkspaceMessageRead)
	ref := domain.MessageVersionRef{
		MessageID:               read.MessageID,
		ContentHash:             read.Ref.ContentHash,
		CanonicalizationVersion: read.Ref.CanonicalizationVersion,
		ReadReceipt:             read.ReadReceipt,
	}
	output, err := json.Marshal(
		map[string]any{
			"summaries": []any{
				map[string]any{
					"action":          "create",
					"proposal_key":    "decision",
					"body":            "Last decision",
					"related_sources": []domain.MessageVersionRef{ref},
				},
			},
			"outcomes": []any{},
		},
	)
	require.NoError(t, err)
	turnID := secretary.ExecutionTurnID(job.OwnerUserID, job.RunID)
	message := domain.Message{
		MessageID:   "persisted-output",
		AgentID:     fixture.agentID,
		SessionID:   sessionID,
		Role:        "assistant",
		MessageType: "agent_message_chunk",
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionAgentToUser,
		TurnID:      turnID,
		SessionSeq:  1,
	}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &message))
	require.NoError(
		t,
		srv.store.UpsertMessagePart(
			t.Context(),
			&domain.MessagePart{
				MessageID: message.MessageID,
				PartType:  domain.MessagePartText,
				Text:      string(output),
			},
		),
	)
	_, completed, err = srv.secretaryExecutor().(secretary.RecoveryExecutor).Recover(
		t.Context(),
		request,
	)
	require.NoError(t, err)
	assert.False(t, completed, "visible output without a durable completion is still unknown")
	done := domain.Message{
		MessageID:   "persisted-done",
		AgentID:     fixture.agentID,
		SessionID:   sessionID,
		Role:        "assistant",
		MessageType: "turn_done",
		Status:      "complete",
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionAgentToUser,
		TurnID:      turnID,
		SessionSeq:  2,
	}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &done))
	result, completed, err := srv.recoverSecretaryAgent(t.Context(), request)
	require.NoError(t, err)
	assert.True(t, completed)
	assert.Equal(t, string(output), result.Text)
	require.Len(t, result.Reads.Messages, 1)
	assert.Equal(t, "Last decision.", result.Reads.Messages[0].Text)
	assert.Equal(t, ref, result.Reads.Messages[0].Ref)
}

func TestSecretaryAgentRecoveryGivenInvalidIdentityThenNeverTouchesNativeExecution(t *testing.T) {
	_, completed, err := (&Service{}).recoverSecretaryAgent(
		t.Context(),
		secretary.ExecutionRequest{},
	)
	assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
	assert.False(t, completed)
	srv, _, job, _, _ := secretaryToolsFixture(t)
	job.BatchID, job.AttemptID, job.RunID = "batch", "attempt", "attempt"
	job.Attempt = &domain.SecretaryAttempt{ID: "attempt", BatchID: "batch", State: "unknown"}
	request := secretary.ExecutionRequest{
		OwnerID: job.OwnerUserID,
		AgentID: "missing",
		RunID:   job.RunID,
		Job:     job,
	}
	request.Job.AgentID = request.AgentID
	_, completed, err = srv.recoverSecretaryAgent(t.Context(), request)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	assert.False(t, completed)
	request.OwnerID, request.Job.OwnerUserID = "missing", "missing"
	_, completed, err = srv.recoverSecretaryAgent(t.Context(), request)
	assert.Error(t, err)
	assert.False(t, completed)
}

func TestSecretaryAgentGivenNodeNoopOutputThenAcceptsNewProtocolWithoutReceiptLookups(
	t *testing.T,
) {
	request := secretary.ExecutionRequest{
		OwnerID: "owner",
		Job:     domain.SecretaryJob{BatchID: "batch", SourceID: "source", OwnerUserID: "owner"},
	}
	result := secretary.ExecutionResult{
		Text: `{"summaries":[],"outcomes":[{"message_id":"m","action":"no_summary","reason":"No new conclusion"}]}`,
	}
	finished, err := (&Service{}).finishSecretaryExecution(t.Context(), request, result)
	assert.NoError(t, err)
	assert.Equal(t, result.Text, finished.Text)
	result.Text = `{"body":"old protocol"}`
	_, err = (&Service{}).finishSecretaryExecution(t.Context(), request, result)
	assert.Error(t, err)
}
