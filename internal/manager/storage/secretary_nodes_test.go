package storage

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryNodesPublication(
	job domain.SecretaryJob,
	keys ...string,
) domain.SecretaryBatchPublication {
	publication := domain.SecretaryBatchPublication{RequestID: job.BatchID}
	for _, key := range keys {
		request := secretaryPublication(job)
		request.SummaryID, request.RequestID = "", ""
		publication.Outputs = append(
			publication.Outputs,
			domain.SecretarySummaryOutput{ProposalKey: key, Request: request},
		)
	}
	for _, message := range job.Messages {
		publication.Outcomes = append(publication.Outcomes, domain.SecretaryMessageOutcome{
			MessageID: message.SourceID, Action: "summarized", SummaryKeys: keys,
		})
	}
	return publication
}

func secretaryNodesReadyJob(t *testing.T, store interface {
	domain.SecretaryStore
	domain.SecretaryExecutionStore
}) domain.SecretaryJob {
	t.Helper()
	return secretaryNodesReadyWork(t, store, secretaryClaim(t, store))
}

func secretaryNodesReadyWork(
	t *testing.T,
	store domain.SecretaryExecutionStore,
	work domain.SecretaryJob,
) domain.SecretaryJob {
	t.Helper()
	job, err := store.PrepareSecretaryBatch(t.Context(), work)
	require.NoError(t, err)
	attempt, err := store.BeginSecretaryAttempt(t.Context(), job)
	require.NoError(t, err)
	job.AttemptID = attempt.ID
	require.NoError(
		t,
		store.SaveSecretaryAttemptOutput(
			t.Context(),
			job,
			attempt.ID,
			"{}",
			domain.SecretaryToolReads{},
		),
	)
	return job
}

func TestSecretaryNodesMemoryGivenTwoIndependentSummariesThenPublishesAtomicallyAndCountsMessageOnce(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	job := secretaryNodesReadyJob(t, store)
	publication := secretaryNodesPublication(job, "search", "deploy")
	results, err := store.PublishSecretaryBatch(t.Context(), job, publication)
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.NotEqual(t, results[0].SummaryID, results[1].SummaryID)
	assert.Equal(t, domain.SecretaryNodeID(job.BatchID, "search"), results[0].SummaryID)
	assert.Equal(t, "1", store.secretaryState.nodes[results[0].SummaryID].OriginSourceID)
	assert.Equal(t, "summary", store.secretaryState.nodes[results[0].SummaryID].Type)
	progress, err := store.ReadSecretaryProgress(
		t.Context(),
		"owner",
		domain.SecretaryProgressQuery{},
	)
	require.NoError(t, err)
	assert.Equal(t, 1, progress.Counts.CompletedMessages)
	status, err := store.GetSecretaryStatus(t.Context(), "owner")
	require.NoError(t, err)
	assert.Zero(t, status.PendingCount)
	replayed, err := store.PublishSecretaryBatch(t.Context(), job, publication)
	require.NoError(t, err)
	assert.Equal(t, results, replayed)
	assert.Len(t, store.secretaryState.summaries[results[0].SummaryID], 1)
	publication.Outputs[0].Request.Body = "changed"
	_, err = store.PublishSecretaryBatch(t.Context(), job, publication)
	assert.ErrorIs(t, err, ErrConflict)
}

func TestSecretaryNodesMemoryGivenSecondOutputInvalidThenNoPartialPublicationOrCheckpoint(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	job := secretaryNodesReadyJob(t, store)
	publication := secretaryNodesPublication(job, "one", "two")
	publication.Outputs[1].Request.Claims = []domain.SummaryClaim{
		{ID: "bad", Text: "wrong", Evidence: []domain.SummaryEvidence{
			{Message: publication.Outputs[1].Request.Coverage[0], Quote: "absent quote"},
		}},
	}
	_, err := store.PublishSecretaryBatch(t.Context(), job, publication)
	assert.Error(t, err)
	assert.Empty(t, store.secretaryState.summaries)
	assert.Empty(t, store.secretaryState.nodes)
	assert.Empty(t, store.secretaryState.requests)
	assert.Empty(t, store.secretaryState.snapshots)
	assert.Empty(t, store.secretaryState.jobs[job.SourceID].checkpoint)
	assert.NotEmpty(t, store.secretaryState.jobs[job.SourceID].work.LeaseToken)
}

func TestSecretaryNodesMemoryGivenNoSummaryThenProcessesWithoutCreatingOrRequeuingOnRestart(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	job := secretaryNodesReadyJob(t, store)
	publication := domain.SecretaryBatchPublication{
		RequestID: job.BatchID,
		Outcomes: []domain.SecretaryMessageOutcome{
			{MessageID: "m1", Action: "no_summary", Reason: "No durable new information"},
		},
	}
	results, err := store.PublishSecretaryBatch(t.Context(), job, publication)
	require.NoError(t, err)
	assert.Empty(t, results)
	assert.Empty(t, store.secretaryState.nodes)
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	status, err := store.GetSecretaryStatus(t.Context(), "owner")
	require.NoError(t, err)
	assert.Zero(t, status.PendingCount)
}

func TestSecretaryNodePublicationGivenMalformedOutputOrMissingOutcomeThenRejects(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	job := secretaryClaim(t, store)
	job.BatchID = "batch-validation"
	for _, mode := range []string{"wrong-batch", "agent-create-id", "duplicate-key", "missing-outcome", "unknown-message", "missing-coverage", "unknown-action", "no-summary-without-reason", "unknown-key", "bad-source", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			publication := secretaryNodesPublication(job, "one")
			switch mode {
			case "wrong-batch":
				publication.RequestID = "other"
			case "agent-create-id":
				publication.Outputs[0].Request.SummaryID = "chosen-by-agent"
			case "duplicate-key":
				publication.Outputs = append(publication.Outputs, publication.Outputs[0])
			case "missing-outcome":
				publication.Outcomes = nil
			case "unknown-message":
				publication.Outcomes[0].MessageID = "absent"
			case "missing-coverage":
				publication.Outputs[0].Request.Coverage = nil
			case "unknown-action":
				publication.Outcomes[0].Action = "delete"
			case "no-summary-without-reason":
				publication.Outcomes[0].Action, publication.Outcomes[0].SummaryKeys = "no_summary", nil
			case "unknown-key":
				publication.Outcomes[0].SummaryKeys = []string{"missing"}
			case "bad-source":
				publication.Outputs[0].Request.Coverage[0].MessageID = "other"
			case "oversized":
				publication.Outputs[0].Request.Body = strings.Repeat("x", secretaryNodeStateBytes)
			}
			_, _, err := secretaryNodePublication(job, publication)
			assert.Error(t, err)
		})
	}
}

func TestSecretaryNodesMemoryGivenIncrementalRevisionThenCanDropOldCoverageWithoutLosingProgress(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	job := secretaryNodesReadyJob(t, store)
	first, err := store.PublishSecretaryBatch(
		t.Context(),
		job,
		secretaryNodesPublication(job, "one", "two"),
	)
	require.NoError(t, err)
	secretaryMemoryMessage(store, "m2", "A new conclusion", 2)
	next := secretaryNodesReadyJob(t, store)
	require.Len(t, next.CurrentSummaries, 2)
	request := secretaryPublication(next)
	request.SummaryID, request.ExpectedRevision = first[0].SummaryID, first[0].Revision
	publication := domain.SecretaryBatchPublication{RequestID: next.BatchID,
		Outputs: []domain.SecretarySummaryOutput{{Request: request}},
		Outcomes: []domain.SecretaryMessageOutcome{
			{MessageID: "m2", Action: "summarized", SummaryKeys: []string{first[0].SummaryID}},
		},
	}
	results, err := store.PublishSecretaryBatch(t.Context(), next, publication)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.EqualValues(t, 2, results[0].Revision)
	require.Len(t, results[0].Coverage, 1)
	assert.Equal(t, "m2", results[0].Coverage[0].MessageID)
	assert.Len(t, store.secretaryState.summaries[first[1].SummaryID], 1)
	historical, err := store.GetSummary(t.Context(), "owner", first[0].SummaryID, 1)
	require.NoError(t, err)
	assert.Equal(t, "m1", historical.Coverage[0].MessageID)
	progress, err := store.ReadSecretaryProgress(
		t.Context(),
		"owner",
		domain.SecretaryProgressQuery{},
	)
	require.NoError(t, err)
	assert.Equal(t, 2, progress.Counts.CompletedMessages)
	page, err := store.ListSecretaryNodeSummaries(
		t.Context(),
		"owner",
		next.SourceID,
		domain.SummaryListQuery{Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NotEmpty(t, page.NextCursor)
	page, err = store.ListSecretaryNodeSummaries(
		t.Context(),
		"owner",
		next.SourceID,
		domain.SummaryListQuery{Limit: 1, Cursor: page.NextCursor},
	)
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
	assert.Empty(t, page.NextCursor)
	_, err = store.ListSecretaryNodeSummaries(
		t.Context(),
		"other",
		next.SourceID,
		domain.SummaryListQuery{},
	)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestSummaryRevisionsMemoryGivenSeveralVersionsThenPagesNewestFirstWithScopedCursor(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	job := secretaryClaim(t, store)
	request := secretaryPublication(job)
	for revision := range 3 {
		request.ExpectedRevision = int64(revision)
		request.RequestID = string(rune('a' + revision))
		_, err := store.PublishSummary(t.Context(), "owner", request)
		require.NoError(t, err)
	}
	page, err := store.ListSummaryRevisions(
		t.Context(),
		"owner",
		request.SummaryID,
		domain.SummaryListQuery{Limit: 2},
	)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.EqualValues(t, 3, page.Items[0].Revision)
	assert.EqualValues(t, 2, page.Items[1].Revision)
	require.NotEmpty(t, page.NextCursor)
	last, err := store.ListSummaryRevisions(
		t.Context(),
		"owner",
		request.SummaryID,
		domain.SummaryListQuery{Limit: 2, Cursor: page.NextCursor},
	)
	require.NoError(t, err)
	require.Len(t, last.Items, 1)
	assert.EqualValues(t, 1, last.Items[0].Revision)
	assert.Empty(t, last.NextCursor)
	_, err = store.ListSummaryRevisions(
		t.Context(),
		"owner",
		"other",
		domain.SummaryListQuery{Cursor: page.NextCursor},
	)
	assert.ErrorIs(t, err, domain.ErrInvalidCursor)
	_, err = store.ListSummaryRevisions(
		t.Context(),
		"other",
		request.SummaryID,
		domain.SummaryListQuery{},
	)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestSecretaryNodesMemoryGivenExternalSourceEditThenQueuesOriginRevisionWithoutConsumingExternalMessages(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	store.sessions[sessionKey("secretary", "related")] = AgentSession{
		ID:            2,
		AgentID:       "secretary",
		SessionID:     "related",
		RuntimeStatus: domain.RuntimeStatusIdle,
	}
	secretaryMemoryMessage(store, "external", "External fact", 1)
	message := store.messages["external"]
	message.AgentID, message.SessionID = "secretary", "related"
	store.messages["external"] = message
	secretaryEnable(t, store)
	job := secretaryNodesReadyJob(t, store)
	ref := summaryTestRef("external", "External fact")
	job.AllowedRelatedMessages = []domain.MessageVersionRef{ref}
	publication := secretaryNodesPublication(job, "one")
	publication.Outputs[0].Request.Coverage = append(publication.Outputs[0].Request.Coverage, ref)
	versions, err := store.PublishSecretaryBatch(t.Context(), job, publication)
	require.NoError(t, err)
	part := store.messageParts[messagePartKey{MessageID: "external", Index: 0}]
	part.Text = "A corrected external fact"
	store.messageParts[messagePartKey{MessageID: "external", Index: 0}] = part
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	store.secretaryState.jobs["2"].available = store.now().Add(time.Hour)
	stale, err := store.GetSummary(t.Context(), "owner", versions[0].SummaryID, 0)
	require.NoError(t, err)
	assert.Equal(t, "stale", stale.NodeStatus)
	revision := secretaryNodesReadyJob(t, store)
	assert.Empty(t, revision.Messages)
	assert.Empty(t, revision.CurrentSummaries)
	require.Equal(
		t,
		[]domain.SummaryRef{{SummaryID: versions[0].SummaryID, Revision: 1}},
		revision.InvalidatedSummaries,
	)
	request := domain.PublishSummaryRequest{SummaryID: versions[0].SummaryID, ExpectedRevision: 1,
		Title: "Unavailable summary", Body: domain.SecretaryEmptySummaryBody}
	results, err := store.PublishSecretaryBatch(
		t.Context(),
		revision,
		domain.SecretaryBatchPublication{RequestID: revision.BatchID,
			Outputs: []domain.SecretarySummaryOutput{{Request: request}}},
	)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "empty", results[0].NodeStatus)
	historical, err := store.GetSummary(t.Context(), "owner", versions[0].SummaryID, 1)
	require.NoError(t, err)
	assert.Equal(t, "active", historical.NodeStatus)
	assert.Empty(t, store.secretaryState.jobs["2"].checkpoint)
	assert.Empty(t, store.secretaryState.invalidations)
}

func TestSecretaryNodesMemoryGivenUnfinishedRelatedSourceThenRejectsWithoutPublishing(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	store.sessions[sessionKey("secretary", "related")] = AgentSession{
		ID:            2,
		AgentID:       "secretary",
		SessionID:     "related",
		RuntimeStatus: domain.RuntimeStatusIdle,
	}
	secretaryMemoryMessage(store, "external", "Unfinished fact", 1)
	message := store.messages["external"]
	message.AgentID, message.SessionID, message.Status = "secretary", "related", "streaming"
	store.messages["external"] = message
	secretaryEnable(t, store)
	job := secretaryNodesReadyJob(t, store)
	ref := summaryTestRef("external", "Unfinished fact")
	job.AllowedRelatedMessages = []domain.MessageVersionRef{ref}
	publication := secretaryNodesPublication(job, "one")
	publication.Outputs[0].Request.Coverage = append(publication.Outputs[0].Request.Coverage, ref)
	_, err := store.PublishSecretaryBatch(t.Context(), job, publication)
	assert.ErrorIs(t, err, domain.ErrSecretarySourceIncomplete)
	assert.Empty(t, store.secretaryState.summaries)
	assert.Empty(t, store.secretaryState.jobs["1"].checkpoint)
}

func TestSecretaryNodesMemoryGivenInputChangedOrLeaseRevokedThenCannotCommit(t *testing.T) {
	for _, mode := range []string{"changed", "deleted", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			store, _ := secretaryMemoryFixture(t)
			settings := secretaryEnable(t, store)
			job := secretaryNodesReadyJob(t, store)
			publication := secretaryNodesPublication(job, "one")
			switch mode {
			case "changed":
				secretaryMemoryMessage(store, "m1", "Changed input", 1)
			case "deleted":
				delete(store.messages, "m1")
			case "disabled":
				_, err := store.UpdateSecretarySettings(
					t.Context(),
					"owner",
					domain.SecretarySettingsUpdate{ExpectedRevision: settings.Revision},
				)
				require.NoError(t, err)
			}
			_, err := store.PublishSecretaryBatch(t.Context(), job, publication)
			assert.Error(t, err)
			assert.Empty(t, store.secretaryState.summaries)
			assert.Empty(t, store.secretaryState.jobs["1"].checkpoint)
		})
	}
}

func TestSecretaryNodesMemoryGivenLargeLegacySummaryThenDefersBodyWithoutResettingProgress(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	var coverage []domain.MessageVersionRef
	for index := range 512 {
		id, text := fmt.Sprintf("legacy_%04d", index), fmt.Sprintf("Fact %d", index)
		secretaryMemoryMessage(store, id, text, int64(index+2))
		coverage = append(coverage, summaryTestRef(id, text))
	}
	secretaryEnable(t, store)
	job := store.secretaryState.jobs["1"]
	job.processed = job.desired
	for id, hash := range job.seen {
		job.checkpoint[id] = hash
	}
	store.secretaryState.summaries["legacy-large"] = []domain.SummaryVersion{
		{SummaryID: "legacy-large", OwnerUserID: "owner",
			SubjectType: "fragment", SubjectID: secretarySubject("1"), Revision: 1, Title: "Legacy conclusion", Body: "Preserved historical content", Coverage: coverage},
	}
	secretaryMemoryMessage(store, "increment", "A new completed turn", 514)
	work := secretaryClaim(t, store)
	require.Len(t, work.Messages, 1)
	assert.Equal(t, "increment", work.Messages[0].SourceID)
	assert.Empty(t, work.CurrentSummaries)
	assert.True(t, work.SummariesHasMore)
	assert.Empty(t, work.SummariesCursor)
	assert.False(t, work.ResetRequired)
	assert.Len(t, job.checkpoint, 513)
	prepared, err := store.PrepareSecretaryBatch(t.Context(), work)
	require.NoError(t, err)
	assert.True(t, prepared.SummariesHasMore)
	assert.Empty(t, store.secretaryExecutionsLocked()[prepared.BatchID].attempts)
	assert.Len(t, store.secretaryState.summaries["legacy-large"], 1)
}
