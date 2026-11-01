package storage

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryRollingGivenAppendThenKeepsOneHeadAndOnlyPassesDelta(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	session := store.sessions[sessionKey("source", "session")]
	session.SessionName = "Workspace conversation"
	session.PrimaryProjectID = "project-1"
	session.WorkspaceRoots = []string{"/workspace"}
	session.PaxConfig.CWD = "/workspace/app"
	store.sessions[sessionKey("source", "session")] = session
	secretaryEnable(t, store)
	firstJob := secretaryClaim(t, store)
	first, err := store.PublishSecretaryJob(t.Context(), firstJob, secretaryPublication(firstJob))
	require.NoError(t, err)
	secretaryMemoryMessage(store, "m2", "new content", 2)
	next := secretaryClaim(t, store)
	assert.Equal(t, first.SummaryID, next.TargetSummaryID)
	assert.EqualValues(t, 1, next.ExpectedRevision)
	require.NotNil(t, next.CurrentSummary)
	assert.Equal(t, first.Body, next.CurrentSummary.Body)
	assert.Empty(t, next.PreviousSummaries)
	require.Len(t, next.Messages, 1)
	assert.Equal(t, "m2", next.Messages[0].SourceID)
	assert.Equal(t, "Workspace conversation", next.SourceMetadata.Title)
	assert.Equal(t, "project-1", next.SourceMetadata.PrimaryProjectID)
	assert.Equal(t, "/workspace/app", next.SourceMetadata.CWD)
	assert.Equal(t, []string{"/workspace"}, next.SourceMetadata.WorkspaceRoots)
	second, err := store.PublishSecretaryJob(t.Context(), next, secretaryPublication(next))
	require.NoError(t, err)
	assert.EqualValues(t, 2, second.Revision)
	assert.Len(t, second.Coverage, 2)
	assert.Empty(t, second.SummarySources)
	old, err := store.GetSummary(t.Context(), "owner", first.SummaryID, 1)
	require.NoError(t, err)
	assert.Len(t, old.Coverage, 1)
}

func TestSecretaryRollingGivenInheritedClaimThenKeepsEvidenceUntilItsSourceChanges(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	request := secretaryPublication(work)
	request.Claims = []domain.SummaryClaim{
		{
			ID:       "stable-claim",
			Text:     "Original fact",
			Evidence: []domain.SummaryEvidence{{Message: request.Coverage[0], Quote: "original"}},
		},
	}
	first, err := store.PublishSecretaryJob(t.Context(), work, request)
	require.NoError(t, err)
	secretaryMemoryMessage(store, "m2", "new content", 2)
	appendJob := secretaryClaim(t, store)
	request = secretaryPublication(appendJob)
	request.Claims = first.Claims
	second, err := store.PublishSecretaryJob(t.Context(), appendJob, request)
	require.NoError(t, err)
	assert.Equal(t, "stable-claim", second.Claims[0].ID)
	secretaryMemoryMessage(store, "m1", "corrected", 1)
	edit := secretaryClaim(t, store)
	require.Len(t, edit.Messages, 1)
	request = secretaryPublication(edit)
	request.Claims = first.Claims
	_, err = store.PublishSecretaryJob(t.Context(), edit, request)
	assert.ErrorIs(t, err, ErrConflict)
	assert.EqualValues(t, 2, len(store.secretaryState.summaries[first.SummaryID]))
}

func TestSecretaryRollingGivenRelatedProofThenLinksSourceWithoutProcessingItsJob(t *testing.T) {
	for _, mode := range []string{"allowed", "missing_proof", "other_owner"} {
		t.Run(mode, func(t *testing.T) {
			store, _ := secretaryMemoryFixture(t)
			secretaryEnable(t, store)
			work := secretaryClaim(t, store)
			store.sessions[sessionKey("secretary", "related")] = AgentSession{
				ID:        2,
				AgentID:   "secretary",
				SessionID: "related",
			}
			secretaryMemoryMessage(store, "related", "related fact", 1)
			message := store.messages["related"]
			message.AgentID, message.SessionID = "secretary", "related"
			if mode == "other_owner" {
				message.AgentID = "other"
				store.sessions[sessionKey("other", "related")] = AgentSession{
					ID:        3,
					AgentID:   "other",
					SessionID: "related",
				}
			}
			store.messages["related"] = message
			ref := summaryTestRef("related", "related fact")
			if mode != "missing_proof" {
				work.AllowedRelatedMessages = []domain.MessageVersionRef{ref}
			}
			request := secretaryPublication(work)
			request.Coverage = append(request.Coverage, ref)
			version, err := store.PublishSecretaryJob(t.Context(), work, request)
			if mode != "allowed" {
				require.Error(t, err)
				assert.Empty(t, store.secretaryState.summaries)
				return
			}
			require.NoError(t, err)
			assert.Len(t, version.Coverage, 2)
			assert.Empty(t, store.secretaryState.jobs[work.SourceID].checkpoint["related"])
		})
	}
}

func TestSecretaryRollingGivenResetRetryThenKeepsIdentityAndPublishesEmptyState(t *testing.T) {
	store, now := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	first, err := store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	require.NoError(t, err)
	delete(store.messages, "m1")
	reset := secretaryClaim(t, store)
	require.True(t, reset.ResetRequired)
	assert.Empty(t, reset.Messages)
	require.NoError(t, store.FailSecretaryJob(t.Context(), reset, errors.New("transient")))
	*now = now.Add(time.Minute)
	retry := secretaryClaim(t, store)
	assert.Equal(t, reset.RunID, retry.RunID)
	assert.Equal(t, reset.ThroughRevision, retry.ThroughRevision)
	request := secretaryPublication(retry)
	request.Title = "Session summary"
	request.Body = "This session has no remaining readable messages."
	version, err := store.PublishSecretaryJob(t.Context(), retry, request)
	require.NoError(t, err)
	assert.EqualValues(t, 2, version.Revision)
	assert.Empty(t, version.Coverage)
	assert.Empty(t, version.SummarySources)
	assert.Empty(t, store.secretaryState.jobs[work.SourceID].checkpoint)
	_, err = store.PublishSummary(t.Context(), "owner", request)
	assert.ErrorIs(t, err, ErrConflict)
	_, err = store.GetSummary(t.Context(), "owner", first.SummaryID, 1)
	assert.ErrorIs(t, err, ErrNotFound)
	current, err := store.GetSummary(t.Context(), "owner", version.SummaryID, 0)
	require.NoError(t, err)
	assert.Equal(t, request.Body, current.Body)
}

func TestSecretaryRollingGivenManyPublishedMessagesThenCarriesCoverageBeyondPublicLimit(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	for i := 2; i <= 520; i++ {
		secretaryMemoryMessage(store, fmt.Sprintf("m%04d", i), "body", int64(i))
	}
	var latest domain.SummaryVersion
	for {
		jobs, err := store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		if len(jobs) == 0 {
			break
		}
		assert.LessOrEqual(t, len(jobs[0].Messages), domain.SecretaryMaxMessages)
		latest, err = store.PublishSecretaryJob(t.Context(), jobs[0], secretaryPublication(jobs[0]))
		require.NoError(t, err)
	}
	assert.Len(t, latest.Coverage, 520)
	assert.Greater(t, latest.Revision, int64(1))
	assert.Empty(t, latest.SummarySources)
}

func TestSecretaryRollingGivenProcessedLegacySourceThenBackfillsWithoutLosingHistory(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	legacy := secretaryPublication(work)
	legacy.SummaryID = "old-fragment"
	_, err := store.PublishSummary(t.Context(), "owner", legacy)
	require.NoError(t, err)
	job := store.secretaryState.jobs[work.SourceID]
	job.processed = job.desired
	job.checkpoint["m1"] = work.Messages[0].ContentHash
	job.work.LeaseToken = ""
	job.leaseUntil = time.Time{}
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	rebuild := secretaryClaim(t, store)
	require.True(t, rebuild.ResetRequired)
	assert.Equal(t, "rolling_summary_rebuild", rebuild.ResetReason)
	assert.Nil(t, rebuild.CurrentSummary)
	require.Len(t, rebuild.Messages, 1)
	_, err = store.PublishSecretaryJob(t.Context(), rebuild, secretaryPublication(rebuild))
	require.NoError(t, err)
	old, err := store.GetSummary(t.Context(), "owner", "old-fragment", 1)
	require.NoError(t, err)
	assert.Equal(t, legacy.Body, old.Body)
}

func TestSecretaryRollingGivenSourceMetadataThenUsesReportedCWDAndCustomTitle(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	session := store.sessions[sessionKey("source", "session")]
	session.CustomSessionName = "Custom title"
	session.Metadata = []byte(`{"initial_cwd":"/old","pax_config":{"cwd":"/current"}}`)
	store.sessions[sessionKey("source", "session")] = session
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	assert.Equal(t, "Custom title", work.SourceMetadata.Title)
	assert.Equal(t, "/current", work.SourceMetadata.CWD)
}

func TestSecretaryRollingGivenInvalidPublicationThenRejectsBeforeWriting(t *testing.T) {
	for _, mode := range []string{"target", "owner", "baseline_revision", "duplicate", "missing", "unproved_child"} {
		t.Run(mode, func(t *testing.T) {
			work := secretaryDriverWork()
			request := secretaryPublication(work)
			switch mode {
			case "target":
				request.SummaryID = "different"
			case "owner":
				work.CurrentSummary = &domain.SummaryVersion{
					SummaryID:   work.TargetSummaryID,
					OwnerUserID: "other",
				}
			case "baseline_revision":
				work.CurrentSummary = &domain.SummaryVersion{
					SummaryID:   work.TargetSummaryID,
					OwnerUserID: work.OwnerUserID,
					Revision:    99,
				}
			case "duplicate":
				request.Coverage = append(request.Coverage, request.Coverage[0])
			case "missing":
				request.Coverage = nil
			case "unproved_child":
				request.SummarySources = []domain.SummaryRef{{SummaryID: "child", Revision: 1}}
			}
			_, err := secretaryComposePublication(work, request)
			assert.ErrorIs(t, err, ErrConflict)
		})
	}
}

func TestSecretaryRollingGivenExpandedCoverageThenKeepsEvidenceChecksAndGraphBudget(t *testing.T) {
	request := secretaryPublication(secretaryDriverWork())
	request.Coverage = nil
	for i := 0; i < 513; i++ {
		request.Coverage = append(request.Coverage, summaryTestRef(fmt.Sprintf("m%d", i), "body"))
	}
	ref := request.Coverage[500]
	request.Claims = []domain.SummaryClaim{
		{
			ID:   "claim",
			Text: "Fact",
			Evidence: []domain.SummaryEvidence{
				{Message: ref, Quote: "body"},
				{Message: ref, Quote: "body"},
			},
		},
	}
	require.NoError(t, validateSecretaryExpanded(request, false))
	request.Claims[0].Evidence[0].Message = summaryTestRef("unknown", "body")
	assert.ErrorIs(t, validateSecretaryExpanded(request, false), ErrConflict)
	request.Claims = nil
	request.Coverage[0].MessageID = ""
	assert.ErrorIs(t, validateSecretaryExpanded(request, false), ErrConflict)
	request.Coverage = make([]domain.MessageVersionRef, secretaryCoverageLimit+1)
	assert.ErrorIs(t, validateSecretaryExpanded(request, false), ErrConflict)
}

func TestSecretaryRollingGivenRelatedSummaryThenChecksTransitiveBudgetBeforeCommit(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	request := secretaryPublication(secretaryDriverWork())
	child := domain.SummaryVersion{SummaryID: "child", Revision: 1}
	for i := 0; i < secretaryCoverageLimit; i++ {
		child.Coverage = append(child.Coverage, summaryTestRef(fmt.Sprintf("child-%d", i), "body"))
	}
	store.secretaryLocked().summaries["child"] = []domain.SummaryVersion{child}
	request.SummarySources = []domain.SummaryRef{{SummaryID: "child", Revision: 1}}
	assert.ErrorIs(t, store.validateMemorySecretaryGraphLocked(request), ErrConflict)
	request.Coverage = nil
	require.NoError(t, store.validateMemorySecretaryGraphLocked(request))
	request.SummarySources = append(request.SummarySources, request.SummarySources[0])
	require.NoError(t, store.validateMemorySecretaryGraphLocked(request))
	request.SummarySources[0].Revision = 2
	assert.ErrorIs(t, store.validateMemorySecretaryGraphLocked(request), ErrNotFound)
}

func TestSecretaryRollingGivenManyRemovedSourcesThenKeepsBoundedDeterministicNotice(t *testing.T) {
	ids := []string{"", "a", "a"}
	for i := 0; i < 100; i++ {
		ids = append(ids, fmt.Sprintf("removed-%03d", i))
	}
	bounded := secretaryBoundRemoved(ids)
	assert.Len(t, bounded, domain.SecretaryMaxMessages)
	assert.Equal(t, "a", bounded[0])
	assert.Equal(t, bounded, secretaryBoundRemoved(ids))
}

func TestSecretaryRollingGivenChangedContextThenChangesExecutionButNotSummaryIdentity(
	t *testing.T,
) {
	work := secretaryDriverWork()
	first := work.RunID
	work.SourceMetadata.CWD = "/changed"
	secretaryPrepareJob(&work)
	assert.NotEqual(t, first, work.RunID)
	assert.Equal(
		t,
		domain.SecretarySummaryID(work.OwnerUserID, work.SourceID),
		work.TargetSummaryID,
	)
}

func TestSecretaryRollingGivenDeletedCoverageThenRebuildsWithoutExposingOldBody(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	secretaryMemoryMessage(store, "m2", "retained content", 2)
	secretaryEnable(t, store)
	firstJob := secretaryClaim(t, store)
	first, err := store.PublishSecretaryJob(t.Context(), firstJob, secretaryPublication(firstJob))
	require.NoError(t, err)
	delete(store.messages, "m1")
	work := secretaryClaim(t, store)
	assert.True(t, work.ResetRequired)
	assert.Nil(t, work.CurrentSummary)
	assert.Contains(t, work.RemovedMessageIDs, "m1")
	require.Len(t, work.Messages, 1)
	assert.Equal(t, "m2", work.Messages[0].SourceID)
	assert.Equal(t, first.SummaryID, work.TargetSummaryID)
	second, err := store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	require.NoError(t, err)
	require.Len(t, second.Coverage, 1)
	assert.Equal(t, "m2", second.Coverage[0].MessageID)
	_, err = store.GetSummary(t.Context(), "owner", first.SummaryID, 1)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestSecretaryRollingGivenMessageMovesToAnotherOwnedSourceThenRemovesOnlyOldMembership(
	t *testing.T,
) {
	for _, partial := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "all_moved", true: "some_retained"}[partial],
			func(t *testing.T) {
				store, _ := secretaryMemoryFixture(t)
				if partial {
					secretaryMemoryMessage(store, "m2", "retained", 2)
				}
				secretaryEnable(t, store)
				work := secretaryClaim(t, store)
				_, err := store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
				require.NoError(t, err)
				store.sessions[sessionKey("secretary", "destination")] = AgentSession{
					ID:        2,
					AgentID:   "secretary",
					SessionID: "destination",
				}
				message := store.messages["m1"]
				message.AgentID = "secretary"
				message.SessionID = "destination"
				store.messages["m1"] = message
				removal := secretaryClaim(t, store)
				assert.Empty(t, removal.Messages)
				assert.Contains(t, removal.RemovedMessageIDs, "m1")
				assert.Equal(t, !partial, removal.ResetRequired)
				request := secretaryPublication(removal)
				version, err := store.PublishSecretaryJob(t.Context(), removal, request)
				require.NoError(t, err)
				if partial {
					require.Len(t, version.Coverage, 1)
					assert.Equal(t, "m2", version.Coverage[0].MessageID)
				} else {
					assert.Empty(t, version.Coverage)
					assert.Nil(t, removal.CurrentSummary)
				}
				assert.Empty(t, store.secretaryState.jobs[work.SourceID].checkpoint["m1"])
				destination := secretaryClaim(t, store)
				require.Len(t, destination.Messages, 1)
				assert.Equal(t, "m1", destination.Messages[0].SourceID)
				assert.Equal(t, "destination", destination.SourceSessionID)
			},
		)
	}
}
