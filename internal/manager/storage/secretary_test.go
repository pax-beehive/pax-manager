package storage

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryMemoryFixture(t *testing.T) (*MemoryStore, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	store.agents["source"] = Agent{AgentID: "source", OwnerUserID: "owner"}
	store.agents["secretary"] = Agent{AgentID: "secretary", OwnerUserID: "owner"}
	store.agents["other"] = Agent{AgentID: "other", OwnerUserID: "other"}
	store.sessions[sessionKey("source", "session")] = AgentSession{
		ID:            1,
		AgentID:       "source",
		SessionID:     "session",
		RuntimeStatus: domain.RuntimeStatusIdle,
	}
	secretaryMemoryMessage(store, "m1", "original", 1)
	return store, &now
}

func secretaryMemoryMessage(store *MemoryStore, id, text string, seq int64) {
	store.messages[id] = Message{
		MessageID:   id,
		AgentID:     "source",
		SessionID:   "session",
		Role:        "assistant",
		MessageType: "agent_message_chunk",
		SessionSeq:  seq,
		CreatedAt:   store.now(),
	}
	store.messageParts[messagePartKey{MessageID: id, Index: 0}] = MessagePart{
		MessageID: id,
		PartIndex: 0,
		PartType:  "text",
		Text:      text,
	}
}

func secretaryEnable(t *testing.T, store domain.SecretaryStore) domain.SecretarySettings {
	t.Helper()
	settings, err := store.UpdateSecretarySettings(
		t.Context(),
		"owner",
		domain.SecretarySettingsUpdate{AgentID: "secretary", Enabled: true},
	)
	require.NoError(t, err)
	return settings
}

func secretaryClaim(t *testing.T, store domain.SecretaryStore) domain.SecretaryJob {
	t.Helper()
	jobs, err := store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	return jobs[0]
}

func secretaryPublication(work domain.SecretaryJob) domain.PublishSummaryRequest {
	request := domain.PublishSummaryRequest{
		SummaryID:        work.TargetSummaryID,
		ExpectedRevision: work.ExpectedRevision,
		RequestID:        work.RunID,
		SubjectType:      "fragment",
		SubjectID:        secretarySubject(work.SourceID),
		Title:            "Progress",
		Body:             "Summarized source",
	}
	for _, doc := range work.Messages {
		request.Coverage = append(
			request.Coverage,
			domain.MessageVersionRef{
				MessageID:               doc.SourceID,
				ContentHash:             doc.ContentHash,
				CanonicalizationVersion: doc.CanonicalizationVersion,
			},
		)
	}
	return request
}

func TestSecretarySettingsGivenDefaultWhenNotEnabledThenNothingIsScheduled(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	cfg, err := store.GetSecretarySettings(t.Context(), "owner")
	require.NoError(t, err)
	assert.False(t, cfg.Enabled)
	assert.Zero(t, cfg.Revision)
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	jobs, err := store.ClaimSecretaryJobs(t.Context(), 0, 0, 0)
	require.NoError(t, err)
	assert.Empty(t, jobs)
	_, err = store.GetSecretarySettings(t.Context(), "")
	assert.ErrorIs(t, err, ErrUnauthorized)
	_, err = store.GetSecretaryStatus(t.Context(), "")
	assert.ErrorIs(t, err, ErrUnauthorized)
	_, err = store.UpdateSecretarySettings(t.Context(), "", domain.SecretarySettingsUpdate{})
	assert.ErrorIs(t, err, ErrUnauthorized)
	_, err = store.UpdateSecretarySettings(
		t.Context(),
		"owner",
		domain.SecretarySettingsUpdate{Enabled: true},
	)
	assert.ErrorIs(t, err, ErrConflict)
	_, err = store.UpdateSecretarySettings(
		t.Context(),
		"owner",
		domain.SecretarySettingsUpdate{AgentID: "other"},
	)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.UpdateSecretarySettings(
		t.Context(),
		"owner",
		domain.SecretarySettingsUpdate{ExpectedRevision: -1},
	)
	assert.ErrorIs(t, err, ErrConflict)
	assert.ErrorIs(t, store.RetrySecretaryJobs(t.Context(), ""), ErrUnauthorized)
	assert.ErrorIs(t, store.RetrySecretaryJobs(t.Context(), "owner"), ErrConflict)
	secretaryEnable(t, store)
	_, err = store.UpdateSecretarySettings(
		t.Context(),
		"owner",
		domain.SecretarySettingsUpdate{AgentID: "secretary", Enabled: true},
	)
	assert.ErrorIs(t, err, ErrConflict)
	status, err := store.GetSecretaryStatus(t.Context(), "owner")
	require.NoError(t, err)
	assert.Equal(t, 1, status.PendingCount)
}

func TestSecretaryJobsGivenStreamingAndBusySessionWhenClaimedThenWaitsForSettledIdle(t *testing.T) {
	store, now := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	jobs, err := store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 30*time.Second)
	require.NoError(t, err)
	assert.Empty(t, jobs)
	*now = now.Add(time.Minute)
	session := store.sessions[sessionKey("source", "session")]
	session.RuntimeStatus = domain.RuntimeStatusRunning
	store.sessions[sessionKey("source", "session")] = session
	jobs, err = store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	assert.Empty(t, jobs)
	session.RuntimeStatus = domain.RuntimeStatusIdle
	store.sessions[sessionKey("source", "session")] = session
	work := secretaryClaim(t, store)
	assert.Equal(t, "original", work.Messages[0].Body)
	jobs, err = store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	assert.Empty(t, jobs)
	status, err := store.GetSecretaryStatus(t.Context(), "owner")
	require.NoError(t, err)
	assert.True(t, status.Processing)
	require.NoError(t, store.FailSecretaryJob(t.Context(), work, errors.New("offline")))
	status, err = store.GetSecretaryStatus(t.Context(), "owner")
	require.NoError(t, err)
	assert.Equal(t, "offline", status.LastError)
	*now = now.Add(6 * time.Second)
	retry := secretaryClaim(t, store)
	assert.Equal(t, work.RunID, retry.RunID)
	assert.NotEqual(t, work.LeaseToken, retry.LeaseToken)
	require.NoError(
		t,
		store.FailSecretaryJob(t.Context(), retry, errors.New("invalid model output")),
	)
	require.NoError(t, store.RetrySecretaryJobs(t.Context(), "owner"))
	explicit := secretaryClaim(t, store)
	assert.NotEqual(t, retry.RunID, explicit.RunID)
	assert.Equal(t, retry.TargetSummaryID, explicit.TargetSummaryID)
	active, err := store.SecretaryJobActive(t.Context(), work)
	require.NoError(t, err)
	assert.False(t, active)
}

func TestSecretaryJobsGivenNewWorkDuringGenerationWhenPublishedThenOnlyClaimedCheckpointAdvances(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	secretaryMemoryMessage(store, "m2", "next message", 2)
	version, err := store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	require.NoError(t, err)
	assert.EqualValues(t, 1, version.Revision)
	status, err := store.GetSecretaryStatus(t.Context(), "owner")
	require.NoError(t, err)
	assert.Equal(t, 1, status.PendingCount)
	assert.NotNil(t, status.LastCompletedAt)
	next := secretaryClaim(t, store)
	require.Len(t, next.Messages, 1)
	assert.Equal(t, "m2", next.Messages[0].SourceID)
	assert.NotEqual(t, work.TargetSummaryID, next.TargetSummaryID)
	assert.Len(t, next.PreviousSummaries, 1)
	_, err = store.PublishSecretaryJob(t.Context(), next, secretaryPublication(next))
	require.NoError(t, err)
	jobs, err := store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	assert.Empty(t, jobs)
}

func TestSecretaryJobsGivenLateEditWhenProcessedThenRegeneratesOnlyItsExistingFragment(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	first, err := store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	require.NoError(t, err)
	secretaryMemoryMessage(store, "m1", "edited", 1)
	updated := secretaryClaim(t, store)
	assert.Equal(t, first.SummaryID, updated.TargetSummaryID)
	assert.EqualValues(t, 1, updated.ExpectedRevision)
	second, err := store.PublishSecretaryJob(t.Context(), updated, secretaryPublication(updated))
	require.NoError(t, err)
	assert.EqualValues(t, 2, second.Revision)
	old, err := store.ReadMessageVersion(t.Context(), "owner", first.Coverage[0])
	require.NoError(t, err)
	assert.Equal(t, "original", old.Body)
	historical, err := store.GetSummary(t.Context(), "owner", first.SummaryID, 1)
	require.NoError(t, err)
	assert.Equal(t, first.Body, historical.Body)
	secretaryMemoryMessage(store, "m1", "original", 1)
	reverted := secretaryClaim(t, store)
	assert.NotEqual(t, work.RunID, reverted.RunID)
	assert.Equal(t, first.SummaryID, reverted.TargetSummaryID)
	assert.EqualValues(t, 2, reverted.ExpectedRevision)
	third, publishErr := store.PublishSecretaryJob(
		t.Context(),
		reverted,
		secretaryPublication(reverted),
	)
	require.NoError(t, publishErr)
	assert.EqualValues(t, 3, third.Revision)
	delete(store.messages, "m1")
	_, err = store.GetSummary(t.Context(), "owner", first.SummaryID, 1)
	assert.ErrorIs(t, err, ErrNotFound)
	jobs, err := store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	assert.Empty(t, jobs)
}

func TestSecretaryJobsGivenDisabledOrChangedSourceWhenPublishedThenNoCheckpointOrVersionLeaks(
	t *testing.T,
) {
	for _, mode := range []string{"disable", "expired", "changed", "invalid_coverage"} {
		t.Run(mode, func(t *testing.T) {
			store, now := secretaryMemoryFixture(t)
			cfg := secretaryEnable(t, store)
			work := secretaryClaim(t, store)
			request := secretaryPublication(work)
			want := domain.ErrSecretaryFenced
			switch mode {
			case "disable":
				_, err := store.UpdateSecretarySettings(
					t.Context(),
					"owner",
					domain.SecretarySettingsUpdate{
						AgentID:          "secretary",
						ExpectedRevision: cfg.Revision,
					},
				)
				require.NoError(t, err)
			case "expired":
				*now = now.Add(2 * time.Minute)
			case "changed":
				secretaryMemoryMessage(store, "m1", "changed before publication", 1)
				want = domain.ErrSourceVersionUnavailable
			case "invalid_coverage":
				request.Coverage[0].ContentHash = "wrong"
				want = ErrConflict
			}
			_, err := store.PublishSecretaryJob(t.Context(), work, request)
			assert.ErrorIs(t, err, want)
			assert.Empty(t, store.secretaryState.summaries)
			assert.Zero(t, store.secretaryState.jobs[work.SourceID].processed)
			assert.Empty(t, store.secretaryState.jobs[work.SourceID].checkpoint)
		})
	}
}

func TestSecretaryJobsGivenInternalSessionsAndConcurrentClaimsThenNeverCreatesFeedbackLoop(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	var group sync.WaitGroup
	var mu sync.Mutex
	var claimed []domain.SecretaryJob
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			jobs, err := store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
			assert.NoError(t, err)
			mu.Lock()
			claimed = append(claimed, jobs...)
			mu.Unlock()
		}()
	}
	group.Wait()
	require.Len(t, claimed, 1)
	work := claimed[0]
	assert.ErrorIs(
		t,
		store.RegisterSecretaryInternalSession(t.Context(), work, "other", "internal"),
		ErrConflict,
	)
	require.NoError(
		t,
		store.RegisterSecretaryInternalSession(t.Context(), work, "secretary", "internal"),
	)
	store.sessions[sessionKey("secretary", "internal")] = AgentSession{
		ID:        2,
		AgentID:   "secretary",
		SessionID: "internal",
	}
	store.messages["internal"] = Message{
		MessageID:   "internal",
		AgentID:     "secretary",
		SessionID:   "internal",
		Role:        "assistant",
		MessageType: "agent_message_chunk",
	}
	store.messageParts[messagePartKey{MessageID: "internal"}] = MessagePart{
		MessageID: "internal",
		PartType:  "text",
		Text:      "Do not summarize this",
	}
	require.NoError(
		t,
		store.RegisterSecretaryInternalSession(t.Context(), work, "secretary", "internal"),
	)
	assert.Equal(
		t,
		domain.SecretaryInternalSource,
		store.sessions[sessionKey("secretary", "internal")].Source,
	)
	_, hiddenErr := store.ReadSearchDocument(t.Context(), "owner", "message", "internal")
	assert.ErrorIs(t, hiddenErr, ErrNotFound)
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	assert.Len(t, store.secretaryState.jobs, 1)
	_, err := store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	require.NoError(t, err)
	assert.ErrorIs(
		t,
		store.RegisterSecretaryInternalSession(t.Context(), work, "secretary", "late"),
		domain.ErrSecretaryFenced,
	)
}

func TestSecretaryJobsGivenOversizedMessageWhenClaimedThenReportsWithoutTruncation(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	secretaryMemoryMessage(store, "m1", strings.Repeat("x", domain.SecretaryMaxBytes+1), 1)
	secretaryEnable(t, store)
	jobs, err := store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	assert.Empty(t, jobs)
	status, err := store.GetSecretaryStatus(t.Context(), "owner")
	require.NoError(t, err)
	assert.Contains(t, status.LastError, "bounded batch")
	assert.Equal(t, 1, status.PendingCount)
	secretaryMemoryMessage(store, "m1", "fixed", 1)
	require.NoError(t, store.RetrySecretaryJobs(t.Context(), "owner"))
	work := secretaryClaim(t, store)
	assert.Equal(t, "fixed", work.Messages[0].Body)
}
