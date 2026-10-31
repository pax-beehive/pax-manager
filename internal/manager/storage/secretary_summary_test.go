package storage

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryMemorySummaryGivenExactChildReferencesWhenPublishedThenSnapshotsStayImmutable(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	request := secretaryPublication(work)
	request.Claims = []domain.SummaryClaim{
		{
			ID:       "claim",
			Text:     "original",
			Evidence: []domain.SummaryEvidence{{Message: request.Coverage[0], Quote: "original"}},
		},
	}
	child, err := store.PublishSecretaryJob(t.Context(), work, request)
	require.NoError(t, err)
	parentRequest := domain.PublishSummaryRequest{
		SummaryID:      "parent",
		SubjectType:    "project",
		SubjectID:      "project",
		RequestID:      "parent-request",
		Body:           "Project progress",
		SummarySources: []domain.SummaryRef{{SummaryID: child.SummaryID, Revision: 1}},
	}
	parent, err := store.PublishSummary(t.Context(), "owner", parentRequest)
	require.NoError(t, err)
	parent.SummarySources[0].Revision = 99
	retried, err := store.PublishSummary(t.Context(), "owner", parentRequest)
	require.NoError(t, err)
	assert.EqualValues(t, 1, retried.SummarySources[0].Revision)
	parentRequest.Body = "different body"
	_, err = store.PublishSummary(t.Context(), "owner", parentRequest)
	assert.ErrorIs(t, err, ErrConflict)
	secretaryMemoryMessage(store, "m1", "changed", 1)
	historical, err := store.ReadMessageVersion(t.Context(), "owner", child.Coverage[0])
	require.NoError(t, err)
	assert.Equal(t, "original", historical.Body)
	_, err = store.GetSummary(t.Context(), "other", child.SummaryID, 1)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.GetSummary(t.Context(), "owner", child.SummaryID, 99)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.GetSummary(t.Context(), "", child.SummaryID, 1)
	assert.ErrorIs(t, err, ErrUnauthorized)
	_, err = store.ReadMessageVersion(t.Context(), "", child.Coverage[0])
	assert.ErrorIs(t, err, ErrUnauthorized)
	_, err = store.ReadMessageVersion(t.Context(), "owner", domain.MessageVersionRef{})
	assert.ErrorIs(t, err, domain.ErrSourceVersionUnavailable)
	_, err = store.PublishSummary(t.Context(), "", request)
	assert.ErrorIs(t, err, ErrUnauthorized)
	_, err = store.PublishSummary(t.Context(), "owner", domain.PublishSummaryRequest{})
	assert.ErrorIs(t, err, ErrConflict)
	for _, mode := range []string{"other-owner", "changed-subject", "stale-revision", "bad-quote", "missing-child"} {
		t.Run(mode, func(t *testing.T) {
			r := request
			r.RequestID = mode
			r.ExpectedRevision = 1
			r.Coverage = append([]domain.MessageVersionRef(nil), request.Coverage...)
			owner := "owner"
			want := error(ErrConflict)
			switch mode {
			case "other-owner":
				owner = "other"
				want = ErrNotFound
			case "changed-subject":
				r.SubjectID = "different"
			case "stale-revision":
				r.ExpectedRevision = 4
				want = domain.ErrSummaryVersionMismatch
			case "bad-quote":
				r.Claims = []domain.SummaryClaim{
					{
						ID:   "bad",
						Text: "bad",
						Evidence: []domain.SummaryEvidence{
							{Message: r.Coverage[0], Quote: "not in original"},
						},
					},
				}
			case "missing-child":
				r.SummarySources = []domain.SummaryRef{{SummaryID: "missing", Revision: 1}}
				want = ErrNotFound
			}
			_, publishErr := store.PublishSummary(t.Context(), owner, r)
			assert.ErrorIs(t, publishErr, want)
		})
	}
	delete(store.messages, "m1")
	_, err = store.GetSummary(t.Context(), "owner", "parent", 1)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestSecretaryJobsGivenOwnerTransferWhenClaimedThenOldCheckpointsAndLeasesCannotCrossOwners(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	first := secretaryClaim(t, store)
	_, err := store.PublishSecretaryJob(t.Context(), first, secretaryPublication(first))
	require.NoError(t, err)
	agent := store.agents["source"]
	agent.OwnerUserID = "other"
	store.agents["source"] = agent
	_, err = store.UpdateSecretarySettings(
		t.Context(),
		"other",
		domain.SecretarySettingsUpdate{AgentID: "other", Enabled: true},
	)
	require.NoError(t, err)
	work := secretaryClaim(t, store)
	assert.Equal(t, "other", work.OwnerUserID)
	require.Len(t, work.Messages, 1)
	assert.Equal(t, "m1", work.Messages[0].SourceID)
	assert.NotEqual(t, first.RunID, work.RunID)
	active, err := store.SecretaryJobActive(t.Context(), first)
	require.NoError(t, err)
	assert.False(t, active)
}

func TestSecretaryHelpersGivenInvalidCoverageOrLongErrorsThenRejectsOrBounds(t *testing.T) {
	assert.Empty(t, secretaryErrorText(nil))
	assert.Len(t, secretaryErrorText(assert.AnError), len(assert.AnError.Error()))
	assert.Len(t, secretaryErrorText(longSecretaryError{}), 1000)
	bounded := secretaryErrorText(unicodeSecretaryError{})
	assert.True(t, utf8.ValidString(bounded))
	assert.LessOrEqual(t, len(bounded), 1000)
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	request := secretaryPublication(work)
	request.SubjectType = "invalid"
	assert.ErrorIs(t, validateSecretaryPublication(work, request), ErrConflict)
	request = secretaryPublication(work)
	request.Body = ""
	assert.ErrorIs(t, validateSecretaryPublication(work, request), ErrConflict)
	assert.ErrorIs(
		t,
		secretaryWithinBounds(make([]domain.SearchDocument, domain.SecretaryMaxMessages+1)),
		ErrConflict,
	)
	assert.NoError(t, secretaryWithinBounds(nil))
	_, err := store.readMemoryMessageVersionLocked(
		"owner",
		domain.MessageVersionRef{
			MessageID:               "m1",
			ContentHash:             "not-frozen",
			CanonicalizationVersion: "visible-text-v1",
		},
	)
	assert.ErrorIs(t, err, domain.ErrSourceVersionUnavailable)
	assert.NoError(
		t,
		store.FailSecretaryJob(
			t.Context(),
			domain.SecretaryJob{SourceID: "absent"},
			assert.AnError,
		),
	)
	jobs, err := store.ClaimSecretaryJobs(t.Context(), 99, 31*time.Minute, -time.Second)
	require.NoError(t, err)
	assert.Empty(t, jobs)
}

type longSecretaryError struct{}
type unicodeSecretaryError struct{}

func (unicodeSecretaryError) Error() string { return strings.Repeat("\u9519", 1001) }

func (longSecretaryError) Error() string { return strings.Repeat("x", 1001) }
