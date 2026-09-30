package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestMemoryE2EEPairingLatestRequestWins(t *testing.T) {
	now := time.Now().UTC()
	testE2EEPairingLifecycle(t, NewMemoryStore(func() time.Time { return now }), &now)
}

// Both stores must retain the old authorization until the latest request completes.
func testE2EEPairingLifecycle(t *testing.T, store domain.E2EEKeyDistributionStore, now *time.Time) {
	t.Helper()
	ctx := context.Background()
	a := testE2EEPairingRequest(*now)
	packageA := testE2EEKeyPackage(*now)
	_, err := store.CreateE2EEPairingRequest(ctx, a)
	require.NoError(t, err)
	_, err = store.CompleteE2EEPairing(ctx, a, packageA)
	require.NoError(t, err)

	b := a
	b.PairingID = "pair_b"
	// Equal timestamps must not prevent a newly created request superseding A.
	_, err = store.CreateE2EEPairingRequest(ctx, b)
	require.NoError(t, err)
	_, err = store.GetE2EEPairingRequest(ctx, a.OwnerUserID, a.AgentID, a.PairingID)
	require.ErrorIs(t, err, domain.ErrE2EEPairingSuperseded)
	_, err = store.CompleteE2EEPairing(ctx, a, packageA)
	require.ErrorIs(t, err, domain.ErrE2EEPairingSuperseded)
	_, err = store.CreateE2EEPairingRequest(ctx, a)
	require.ErrorIs(t, err, domain.ErrE2EEPairingSuperseded)
	loaded, err := store.GetE2EEKeyPackage(ctx, a.OwnerUserID, a.AgentID, a.DeviceID, a.KeyEpoch)
	require.NoError(t, err)
	require.Equal(t, a.PairingID, loaded.PairingID)

	packageB := packageA
	packageB.PairingID = b.PairingID
	packageB.Ciphertext = []byte("new-browser-wrapped-root-key")
	_, err = store.CompleteE2EEPairing(ctx, b, packageB)
	require.NoError(t, err)
	loaded, err = store.GetE2EEKeyPackage(ctx, a.OwnerUserID, a.AgentID, a.DeviceID, a.KeyEpoch)
	require.NoError(t, err)
	require.Equal(t, b.PairingID, loaded.PairingID)
	require.Equal(t, packageB.Ciphertext, loaded.Ciphertext)

	// paxd re-wraps on every retry. Successful retries must return the saved package.
	retry := packageB
	retry.Ciphertext = []byte("different-random-wrapping-of-the-same-key")
	*now = now.Add(time.Hour)
	completed, err := store.CompleteE2EEPairing(ctx, b, retry)
	require.NoError(t, err)
	require.Equal(t, loaded, completed)

	c := b
	c.PairingID = "pair_c"
	c.CreatedAt = *now
	c.ExpiresAt = now.Add(time.Minute)
	_, err = store.CreateE2EEPairingRequest(ctx, c)
	require.NoError(t, err)
	_, err = store.CompleteE2EEPairing(ctx, b, packageB)
	require.ErrorIs(t, err, domain.ErrE2EEPairingSuperseded)
	*now = now.Add(time.Minute)
	packageC := packageB
	packageC.PairingID = c.PairingID
	_, err = store.CompleteE2EEPairing(ctx, c, packageC)
	require.ErrorIs(t, err, domain.ErrE2EEPairingExpired)
	loaded, err = store.GetE2EEKeyPackage(ctx, a.OwnerUserID, a.AgentID, a.DeviceID, a.KeyEpoch)
	require.NoError(t, err)
	require.Equal(t, b.PairingID, loaded.PairingID)
	_, err = store.CompleteE2EEPairing(ctx, b, packageB)
	require.ErrorIs(t, err, domain.ErrE2EEPairingSuperseded)

	d := c
	d.PairingID = "pair_d"
	d.ExpiresAt = now.Add(time.Minute)
	_, err = store.CreateE2EEPairingRequest(ctx, d)
	require.NoError(t, err)
	packageD := packageB
	packageD.PairingID = d.PairingID
	_, err = store.CompleteE2EEPairing(ctx, d, packageD)
	require.NoError(t, err)
}

func TestMemoryE2EEPairingScopesAndCreationRetries(t *testing.T) {
	now := time.Now().UTC()
	store := NewMemoryStore(func() time.Time { return now })
	ctx := context.Background()
	a := testE2EEPairingRequest(now)
	_, err := store.CreateE2EEPairingRequest(ctx, a)
	require.NoError(t, err)
	for _, scope := range []string{"device", "agent", "epoch"} {
		other := a
		other.PairingID = "pair_" + scope
		switch scope {
		case "device":
			other.DeviceID = "other_device"
		case "agent":
			other.AgentID = "other_agent"
		case "epoch":
			other.KeyEpoch++
		}
		_, err = store.CreateE2EEPairingRequest(ctx, other)
		require.NoError(t, err)
	}
	b := a
	b.PairingID = "pair_b"
	_, err = store.CreateE2EEPairingRequest(ctx, b)
	require.NoError(t, err)
	retry := b
	retry.ExpiresAt = now.Add(time.Hour)
	created, err := store.CreateE2EEPairingRequest(ctx, retry)
	require.NoError(t, err)
	require.Equal(t, b.ExpiresAt, created.ExpiresAt)
	pending, err := store.ListPendingE2EEPairingRequests(ctx, a.OwnerUserID, a.AgentID, 20)
	require.NoError(t, err)
	require.Len(t, pending, 3)
	for _, request := range pending {
		require.NotEqual(t, a.PairingID, request.PairingID)
	}
	// A stale caller that fetched A before B was generated is fenced at completion.
	_, err = store.CompleteE2EEPairing(ctx, a, testE2EEKeyPackage(now))
	require.ErrorIs(t, err, domain.ErrE2EEPairingSuperseded)
}
