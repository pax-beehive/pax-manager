package manager

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

func TestSecretaryAgentGivenCompletedBaselineClaimEchoThenRecoversWithoutAnotherPrompt(
	t *testing.T,
) {
	srv, _ := testServer(t, "schema-recovery@example.test")
	fixture := testNodeAgent(t, srv, "schema-recovery@example.test")
	owner := testUserPrincipal(t, srv, fixture.userEmail).User.UserID
	ref := domain.MessageVersionRef{
		MessageID: "earlier-message", ContentHash: domain.SearchContentHash("Earlier decision"),
		CanonicalizationVersion: domain.SearchCanonicalizationVersion,
	}
	claim := domain.SummaryClaim{ID: "old-claim", Text: "Earlier decision",
		Evidence: []domain.SummaryEvidence{{Message: ref, Quote: "Earlier decision", View: "full"}}}
	output, err := json.Marshal(
		map[string]any{"body": "Earlier decision", "claims": []domain.SummaryClaim{claim}},
	)
	require.NoError(t, err)
	job := domain.SecretaryJob{
		SourceID: "source", OwnerUserID: owner, SourceAgentID: fixture.agentID,
		SourceSessionID: "source-session", AgentID: fixture.agentID, RunID: "completed-run",
		TargetSummaryID: "summary", ExpectedRevision: 1,
		CurrentSummary: &domain.SummaryVersion{
			SummaryID: "summary", Revision: 1, OwnerUserID: owner,
			Body: "Earlier decision", Claims: []domain.SummaryClaim{claim}, Coverage: []domain.MessageVersionRef{ref},
		},
		Messages: []domain.SearchDocument{{
			Kind: "message", SourceID: "delta-message", OwnerUserID: owner,
			AgentID: fixture.agentID, SessionID: "source-session", Body: "New update",
			ContentHash: domain.SearchContentHash(
				"New update",
			), CanonicalizationVersion: domain.SearchCanonicalizationVersion,
		}},
	}
	request := secretary.ExecutionRequest{
		Job: job, OwnerID: owner, AgentID: fixture.agentID, RunID: job.RunID, Prompt: "Summarize.",
		BeforePrompt: func(context.Context, string, string) error {
			t.Fatal(
				"a completed response must recover without creating or prompting a native session",
			)
			return domain.ErrConflict
		},
	}
	sessionID := secretary.ExecutionSessionID(owner, job.RunID)
	turnID := secretary.ExecutionTurnID(owner, job.RunID)
	createConversationTestSession(t, srv, fixture, sessionID, "completed-native")
	for _, message := range []domain.Message{
		{MessageID: "completed-output", MessageType: "agent_message_chunk"},
		{MessageID: "completed-end", MessageType: "turn_done", Status: "complete"},
	} {
		message.AgentID, message.SessionID, message.TurnID = fixture.agentID, sessionID, turnID
		message.Role, message.Source, message.Direction = "assistant", domain.MessageSourceACPTunnel, domain.MessageDirectionAgentToUser
		require.NoError(t, srv.store.UpsertMessage(t.Context(), &message))
	}
	require.NoError(t, srv.store.UpsertMessagePart(t.Context(), &domain.MessagePart{
		MessageID: "completed-output", PartType: domain.MessagePartText, Text: string(output),
	}))
	result, err := srv.executeSecretaryAgent(t.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, sessionID, result.SessionID)
	assert.Equal(t, string(output), result.Text)
	publication, err := secretary.BuildPublicationWithReads(job, result.Text, result.Reads)
	require.NoError(t, err)
	assert.Equal(t, job.RunID, publication.RequestID)
	require.Len(t, publication.Claims, 1)
	assert.Equal(t, "claim_1", publication.Claims[0].ID)
	assert.Equal(t, claim.Evidence, publication.Claims[0].Evidence)
}
