package storage

import (
	"database/sql/driver"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryPostgresGivenMissingSettingsThenDefaultsDisabledWithoutPersistence(t *testing.T) {
	script := &scriptedPostgresScript{
		queries: []scriptedRows{
			{columns: []string{"owner", "agent", "enabled", "revision", "updated"}},
		},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	settings, err := store.GetSecretarySettings(t.Context(), "owner")
	require.NoError(t, err)
	assert.False(t, settings.Enabled)
	assert.Equal(t, "owner", settings.OwnerUserID)
	assert.Zero(t, settings.Revision)
	assert.Empty(t, script.execTexts)
}

func TestSecretaryPostgresGivenStorageFailureThenDoesNotEnableOrAcknowledgeWork(t *testing.T) {
	script := &scriptedPostgresScript{}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	_, err := store.GetSecretarySettings(t.Context(), "")
	assert.ErrorIs(t, err, ErrUnauthorized)
	_, err = store.UpdateSecretarySettings(t.Context(), "", domain.SecretarySettingsUpdate{})
	assert.ErrorIs(t, err, ErrUnauthorized)
	_, err = store.UpdateSecretarySettings(
		t.Context(),
		"owner",
		domain.SecretarySettingsUpdate{Enabled: true},
	)
	assert.ErrorIs(t, err, ErrConflict)
	_, err = store.GetSecretaryStatus(t.Context(), "owner")
	assert.ErrorContains(t, err, "unexpected query")
	_, err = store.UpdateSecretarySettings(
		t.Context(),
		"owner",
		domain.SecretarySettingsUpdate{AgentID: "selected", Enabled: true},
	)
	assert.ErrorContains(t, err, "unexpected query")
	_, err = store.UpdateSecretarySettings(t.Context(), "owner", domain.SecretarySettingsUpdate{})
	assert.ErrorContains(t, err, "unexpected query")
	_, err = store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	assert.ErrorContains(t, err, "unexpected query")
	assert.ErrorIs(t, store.RetrySecretaryJobs(t.Context(), ""), ErrUnauthorized)
	assert.ErrorContains(t, store.RetrySecretaryJobs(t.Context(), "owner"), "unexpected query")
	work := domain.SecretaryJob{
		SourceID:         "1",
		OwnerUserID:      "owner",
		AgentID:          "selected",
		LeaseToken:       "lease",
		SettingsRevision: 1,
		ThroughRevision:  1,
	}
	_, err = store.SecretaryJobActive(t.Context(), work)
	assert.ErrorContains(t, err, "unexpected query")
	assert.ErrorIs(
		t,
		store.RegisterSecretaryInternalSession(t.Context(), work, "wrong", "session"),
		ErrConflict,
	)
	assert.ErrorContains(
		t,
		store.RegisterSecretaryInternalSession(t.Context(), work, "selected", "session"),
		"unexpected query",
	)
	assert.ErrorIs(t, store.FailSecretaryJob(t.Context(), work, nil), nil)
	assert.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	require.NoError(t, store.db.Close())
	_, err = store.UpdateSecretarySettings(t.Context(), "owner", domain.SecretarySettingsUpdate{})
	assert.Error(t, err)
	_, err = store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	assert.Error(t, err)
	assert.Error(t, store.RetrySecretaryJobs(t.Context(), "owner"))
	assert.Error(t, store.FailSecretaryJob(t.Context(), work, assert.AnError))
	assert.Error(t, store.BackfillSecretaryJobs(t.Context()))
	work.Messages = []domain.SearchDocument{
		{SourceID: "m", ContentHash: "hash", CanonicalizationVersion: "v"},
	}
	secretaryPrepareJob(&work)
	_, err = store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	assert.Error(t, err)
}

func TestSecretaryPostgresGivenMissingOrStaleLeaseWhenPublishingThenRejectsBeforeInsert(
	t *testing.T,
) {
	for _, queries := range [][]scriptedRows{
		{},
		{{columns: []string{"owner"}}},
		{{columns: []string{"owner"}, values: [][]driver.Value{{"owner"}}}, {columns: []string{"revision"}}},
		{{columns: []string{"owner"}, values: [][]driver.Value{{"owner"}}}, {columns: []string{"revision"}, values: [][]driver.Value{{int64(1)}}}, {columns: []string{"active"}, values: [][]driver.Value{{false}}}},
	} {
		script := &scriptedPostgresScript{queries: queries}
		store, cleanup := scriptedPostgresStore(t, script)
		work := domain.SecretaryJob{
			SourceID:        "1",
			OwnerUserID:     "owner",
			ThroughRevision: 1,
			Messages: []domain.SearchDocument{
				{SourceID: "m", ContentHash: "hash", CanonicalizationVersion: "v"},
			},
		}
		secretaryPrepareJob(&work)
		_, err := store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
		assert.Error(t, err)
		assert.False(t, script.committed)
		cleanup()
	}
}
