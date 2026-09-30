package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// An abandoned request may remain stored, but must neither remain pending nor
// prevent its device from creating and completing a replacement.
func testE2EEAbandonedRequestRecovery(
	t *testing.T,
	store domain.E2EEKeyDistributionStore,
	now *time.Time,
) {
	t.Helper()
	ctx := t.Context()
	abandoned := testE2EEPairingRequest(*now)
	_, err := store.CreateE2EEPairingRequest(ctx, abandoned)
	require.NoError(t, err)
	*now = abandoned.ExpiresAt.Add(time.Minute)
	pending, err := store.ListPendingE2EEPairingRequests(
		ctx,
		abandoned.OwnerUserID,
		abandoned.AgentID,
		50,
	)
	require.NoError(t, err)
	require.Empty(t, pending)
	_, err = store.CompleteE2EEPairing(ctx, abandoned, testE2EEKeyPackage(*now))
	require.ErrorIs(t, err, domain.ErrE2EEPairingExpired)

	replacement := testE2EEPairingRequest(*now)
	replacement.PairingID = "pair_recovered"
	_, err = store.CreateE2EEPairingRequest(ctx, replacement)
	require.NoError(t, err)
	pending, err = store.ListPendingE2EEPairingRequests(
		ctx,
		replacement.OwnerUserID,
		replacement.AgentID,
		50,
	)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, replacement.PairingID, pending[0].PairingID)
	keyPackage := testE2EEKeyPackage(*now)
	keyPackage.PairingID = replacement.PairingID
	_, err = store.CompleteE2EEPairing(ctx, replacement, keyPackage)
	require.NoError(t, err)
	loaded, err := store.GetE2EEKeyPackage(
		ctx,
		replacement.OwnerUserID,
		replacement.AgentID,
		replacement.DeviceID,
		replacement.KeyEpoch,
	)
	require.NoError(t, err)
	require.Equal(t, replacement.PairingID, loaded.PairingID)
}

// A successful approval can outlive its request's approval deadline: a browser
// closed during approval must still retrieve its package on the next visit.
func testE2EEApprovedPackageSurvivesDeadline(
	t *testing.T,
	store domain.E2EEKeyDistributionStore,
	now *time.Time,
) {
	t.Helper()
	ctx := t.Context()
	request := testE2EEPairingRequest(*now)
	_, err := store.CreateE2EEPairingRequest(ctx, request)
	require.NoError(t, err)
	saved, err := store.CompleteE2EEPairing(ctx, request, testE2EEKeyPackage(*now))
	require.NoError(t, err)
	*now = now.Add(48 * time.Hour)
	pending, err := store.ListPendingE2EEPairingRequests(
		ctx,
		request.OwnerUserID,
		request.AgentID,
		50,
	)
	require.NoError(t, err)
	require.Empty(t, pending)
	loaded, err := store.GetE2EEKeyPackage(
		ctx,
		request.OwnerUserID,
		request.AgentID,
		request.DeviceID,
		request.KeyEpoch,
	)
	require.NoError(t, err)
	require.True(t, saved.CreatedAt.Equal(loaded.CreatedAt))
	loaded.CreatedAt = saved.CreatedAt
	require.Equal(t, saved, loaded)
}

func TestMemoryE2EEOrphanRecovery(t *testing.T) {
	for name, scenario := range map[string]func(*testing.T, domain.E2EEKeyDistributionStore, *time.Time){
		"abandoned_request": testE2EEAbandonedRequestRecovery,
		"approved_package":  testE2EEApprovedPackageSurvivesDeadline,
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Microsecond)
			scenario(t, NewMemoryStore(func() time.Time { return now }), &now)
		})
	}
}

func TestPostgresE2EEOrphanRecovery(t *testing.T) {
	for name, scenario := range map[string]func(*testing.T, domain.E2EEKeyDistributionStore, *time.Time){
		"abandoned_request": testE2EEAbandonedRequestRecovery,
		"approved_package":  testE2EEApprovedPackageSurvivesDeadline,
	} {
		t.Run(name, func(t *testing.T) {
			open := pairingPostgresFixture(t)
			now := time.Now().UTC().Truncate(time.Microsecond)
			scenario(t, NewPostgresStore(open(), func() time.Time { return now }), &now)
		})
	}
}
