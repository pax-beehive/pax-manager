package manager

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pax-beehive/paxkit/reliablemq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

func TestSecretaryTerminalGivenLateReliableFailureThroughServerStoreThenReclaimsOriginalAttempt(
	t *testing.T,
) {
	for name, payload := range map[string]string{
		"cancelled": `{"jsonrpc":"2.0","id":41,"result":{"stopReason":"cancelled"}}`,
		"rpc_error": `{"jsonrpc":"2.0","id":41,"error":{"code":-32603,"message":"failed"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f, now := newSecretaryTerminalWakeAcceptanceFixture(t)
			executions := f.service.secretaryStore.(domain.SecretaryExecutionStore)
			require.NoError(t, executions.FailSecretaryAttempt(
				t.Context(), f.job, f.job.AttemptID, domain.ErrSecretaryExecutionUnknown,
			))
			require.NoError(t, f.service.secretaryStore.FailSecretaryJob(
				t.Context(), f.job, domain.ErrSecretaryExecutionUnknown,
			))
			*now = now.Add(6 * time.Second)
			jobs, err := f.service.secretaryStore.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
			require.NoError(t, err)
			require.Empty(t, jobs, "unconfirmed execution must remain blocked")

			f.frame.Payload = json.RawMessage(payload)
			for range 2 {
				require.NoError(t, f.agent.dispatchReliableACPFrame(
					t.Context(), f.service.agentACPFramePipeline(), f.frame,
				))
			}
			jobs, err = f.service.secretaryStore.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
			require.NoError(t, err)
			require.Len(
				t,
				jobs,
				1,
				"late reliable terminal must wake the blocked job through the server store",
			)
			recovered, err := executions.PrepareSecretaryBatch(t.Context(), jobs[0])
			require.NoError(t, err)
			assert.Equal(t, f.job.BatchID, recovered.BatchID)
			assert.Equal(t, f.job.AttemptID, recovered.AttemptID)
			require.NotNil(t, recovered.Attempt)
			assert.Equal(t, "unknown", recovered.Attempt.State)
			_, completed, err := f.service.recoverSecretaryAgent(
				t.Context(),
				secretary.ExecutionRequest{
					OwnerID: recovered.OwnerUserID, AgentID: recovered.AgentID,
					RunID: recovered.RunID, Job: recovered,
				},
			)
			assert.ErrorIs(t, err, domain.ErrSecretaryExecutionFailed)
			assert.False(t, completed)
			detail, err := f.service.secretaryStore.(domain.SecretaryTaskStore).GetSecretaryTask(
				t.Context(), f.job.OwnerUserID, f.job.SourceID, domain.SecretaryTaskQuery{},
			)
			require.NoError(t, err)
			require.Len(t, detail.Attempts, 1, "waking recovery must not dispatch another attempt")
			assert.Equal(t, f.job.AttemptID, detail.Attempts[0].AttemptID)
			messages, err := f.service.store.ListMessages(
				t.Context(),
				f.job.AgentID,
				f.sessionID,
				100,
			)
			require.NoError(t, err)
			terminals := 0
			for _, message := range messages {
				if message.MessageType == secretaryExecutionFailedType {
					terminals++
				}
			}
			assert.Equal(t, 1, terminals, "reliable replay must retain exactly one terminal")
		})
	}
}

func newSecretaryTerminalWakeAcceptanceFixture(
	t *testing.T,
) (secretaryTerminalFixture, *time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	srv := newServer(
		Config{LocalUserID: "local@example.test", AllowLocalUserHeader: true},
		NewMemoryStore(clock),
	)
	srv.clock = clock
	t.Cleanup(func() { require.NoError(t, srv.CloseTransportStore(context.Background())) })
	fixture := testNodeAgent(t, srv, "late-terminal@example.test")
	owner := testUserPrincipal(t, srv, fixture.userEmail).User.UserID
	createConversationTestSession(t, srv, fixture, "late-terminal-source", "late-terminal-native")
	message := domain.Message{
		MessageID: "late-terminal-source-message", AgentID: fixture.agentID, SessionID: "late-terminal-source",
		Role: "user", Status: "complete", MessageType: "user_message", SessionSeq: 1,
		Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionUserToAgent,
	}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &message))
	require.NoError(t, srv.store.UpsertMessagePart(t.Context(), &domain.MessagePart{
		MessageID: message.MessageID, PartType: domain.MessagePartText, Text: "A settled source message.",
	}))
	_, err := srv.secretaryStore.UpdateSecretarySettings(t.Context(), owner,
		domain.SecretarySettingsUpdate{AgentID: fixture.agentID, Enabled: true})
	require.NoError(t, err)
	jobs, err := srv.secretaryStore.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	executions := srv.secretaryStore.(domain.SecretaryExecutionStore)
	job, err := executions.PrepareSecretaryBatch(t.Context(), jobs[0])
	require.NoError(t, err)
	attempt, err := executions.BeginSecretaryAttempt(t.Context(), job)
	require.NoError(t, err)
	job.AttemptID, job.RunID, job.Attempt = attempt.ID, attempt.ID, &attempt
	sessionID := secretary.ExecutionSessionID(owner, attempt.ID)
	turnID := secretary.ExecutionTurnID(owner, attempt.ID)
	createConversationTestSession(t, srv, fixture, sessionID, "late-terminal-execution-native")
	require.NoError(
		t,
		srv.secretaryStore.RegisterSecretaryInternalSession(
			t.Context(),
			job,
			fixture.agentID,
			sessionID,
		),
	)
	agent := &ACPTunnelAgent{agentID: fixture.agentID, ownerUserID: owner,
		nodeID: fixture.nodeID, connectionID: "terminal-queue", store: srv.store}
	require.NoError(
		t,
		persistSecretaryPromptCorrelation(
			t.Context(),
			agent,
			sessionID,
			turnID,
			json.RawMessage("41"),
			&job,
		),
	)
	return secretaryTerminalFixture{
		service: srv, agent: agent, job: job, sessionID: sessionID, turnID: turnID,
		frame: reliablemq.Frame{
			Key: reliablemq.FrameKey{QueueID: "terminal-queue", Stream: reliablemq.StreamACP,
				Direction: reliablemq.DirectionInbound, Seq: 1},
			Kind: reliablemq.FrameKindData,
			Metadata: reliablemq.Metadata{"agent_id": fixture.agentID,
				"manager_session_id": sessionID, "turn_id": turnID},
			CreatedAt: now,
		},
	}, &now
}
