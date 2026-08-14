package storage

import (
	"context"
	"database/sql/driver"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresSchemaGivenBrowserKeyDistributionThenManagerPersistsOnlyOpaquePackages(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)
	schema := string(raw)
	pairingStart := strings.Index(schema, "CREATE TABLE IF NOT EXISTS e2ee_pairing_requests")
	packageStart := strings.Index(schema, "CREATE TABLE IF NOT EXISTS e2ee_key_packages")
	require.NotEqual(t, -1, pairingStart)
	require.NotEqual(t, -1, packageStart)

	pairingSchema := schema[pairingStart:packageStart]
	packageSchema := schema[packageStart:]
	assert.Contains(t, pairingSchema, "recipient_public_key BYTEA NOT NULL")
	assert.Contains(t, pairingSchema, "secret_commitment BYTEA NOT NULL")
	assert.NotContains(t, pairingSchema, "pairing_secret")
	assert.Contains(t, packageSchema, "sender_ephemeral_public_key BYTEA NOT NULL")
	assert.Contains(t, packageSchema, "ciphertext BYTEA NOT NULL")
	assert.NotContains(t, packageSchema, "root_key")
}

func TestMemoryE2EEKeysGivenBrowserPairingWhenPaxdPublishesPackageThenDeviceCanLoadOpaqueRoot(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 2, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	ctx := context.Background()
	request := E2EEPairingRequest{
		PairingID: "pair_1", OwnerUserID: "user_1", NodeID: "node_1",
		AgentID: "agent_1", DeviceID: "device_1", DeviceName: "Chrome on Mac",
		KeyEpoch: 1, RecipientPublicKey: []byte("recipient-public"),
		SecretCommitment: []byte("commitment"), ExpiresAt: now.Add(5 * time.Minute),
	}

	created, err := store.CreateE2EEPairingRequest(ctx, request)
	require.NoError(t, err)
	assert.Equal(t, now, created.CreatedAt)
	pending, err := store.ListPendingE2EEPairingRequests(ctx, "user_1", "agent_1", 20)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "device_1", pending[0].DeviceID)

	keyPackage := E2EEKeyPackage{
		PairingID: "pair_1", OwnerUserID: "user_1", NodeID: "node_1",
		AgentID: "agent_1", DeviceID: "device_1", KeyEpoch: 1,
		RecipientPublicKey:       []byte("recipient-public"),
		SenderEphemeralPublicKey: []byte("sender-public"),
		Nonce:                    []byte("123456789012"), Ciphertext: []byte("opaque-root-ciphertext"),
	}
	stored, err := store.CompleteE2EEPairing(ctx, request, keyPackage)
	require.NoError(t, err)
	assert.Equal(t, "opaque-root-ciphertext", string(stored.Ciphertext))
	loaded, err := store.GetE2EEKeyPackage(ctx, "user_1", "agent_1", "device_1", 1)
	require.NoError(t, err)
	assert.Equal(t, keyPackage.Ciphertext, loaded.Ciphertext)
	pending, err = store.ListPendingE2EEPairingRequests(ctx, "user_1", "agent_1", 20)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestMemoryE2EEKeysGivenExpiredOrMismatchedPairingWhenCompletedThenRejectsIt(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 2, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	request := E2EEPairingRequest{
		PairingID: "pair_1", OwnerUserID: "user_1", NodeID: "node_1",
		AgentID: "agent_1", DeviceID: "device_1", KeyEpoch: 1,
		RecipientPublicKey: []byte("recipient-public"), SecretCommitment: []byte("commitment"),
		ExpiresAt: now.Add(-time.Second),
	}
	_, err := store.CreateE2EEPairingRequest(context.Background(), request)
	require.NoError(t, err)
	_, err = store.CompleteE2EEPairing(context.Background(), request, E2EEKeyPackage{
		PairingID: "pair_1", OwnerUserID: "user_1", NodeID: "node_1",
		AgentID: "agent_1", DeviceID: "other_device", KeyEpoch: 1,
	})
	require.ErrorIs(t, err, ErrConflict)
}

func TestMemoryE2EEKeysGivenAmbiguousRetriesThenCreationAndCompletionStayIdempotent(t *testing.T) {
	now := time.Date(2026, 8, 8, 2, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	request := testE2EEPairingRequest(now)
	_, err := store.CreateE2EEPairingRequest(context.Background(), request)
	require.NoError(t, err)
	retry := request
	retry.ExpiresAt = retry.ExpiresAt.Add(time.Minute)
	_, err = store.CreateE2EEPairingRequest(context.Background(), retry)
	require.NoError(t, err)
	keyPackage := testE2EEKeyPackage(now)
	_, err = store.CompleteE2EEPairing(context.Background(), request, keyPackage)
	require.NoError(t, err)
	now = now.Add(time.Hour)
	_, err = store.CompleteE2EEPairing(context.Background(), request, keyPackage)
	require.NoError(t, err)
}

func TestMemoryE2EEKeysGivenRequestsWhenReadAndLimitedThenOnlyOwnedPendingRequestsReturn(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 2, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	ctx := context.Background()
	first := testE2EEPairingRequest(now)
	first.PairingID = "pair_first"
	first.CreatedAt = now.Add(-time.Minute)
	second := testE2EEPairingRequest(now)
	second.PairingID = "pair_second"
	_, err := store.CreateE2EEPairingRequest(ctx, second)
	require.NoError(t, err)
	_, err = store.CreateE2EEPairingRequest(ctx, first)
	require.NoError(t, err)

	loaded, err := store.GetE2EEPairingRequest(ctx, "user_1", "agent_1", "pair_first")
	require.NoError(t, err)
	assert.Equal(t, "pair_first", loaded.PairingID)
	limited, err := store.ListPendingE2EEPairingRequests(ctx, "user_1", "agent_1", 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	assert.Equal(t, "pair_first", limited[0].PairingID)
	_, err = store.GetE2EEPairingRequest(ctx, "other_user", "agent_1", "pair_first")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.GetE2EEKeyPackage(ctx, "user_1", "agent_1", "missing", 1)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryE2EEKeysGivenInvalidOrConflictingRecordsWhenStoredThenTheyFailClosed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 2, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	ctx := context.Background()
	_, err := store.CreateE2EEPairingRequest(ctx, E2EEPairingRequest{})
	assert.ErrorIs(t, err, ErrConflict)
	request := testE2EEPairingRequest(now)
	_, err = store.CreateE2EEPairingRequest(ctx, request)
	require.NoError(t, err)
	conflicting := request
	conflicting.DeviceID = "other_device"
	_, err = store.CreateE2EEPairingRequest(ctx, conflicting)
	assert.ErrorIs(t, err, ErrConflict)
	_, err = store.CompleteE2EEPairing(ctx, E2EEPairingRequest{PairingID: "missing"}, testE2EEKeyPackage(now))
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestPostgresE2EEKeysGivenBrowserPairingWhenPackagePublishedThenOpaquePackageIsDurable(t *testing.T) {
	now := time.Date(2026, 8, 8, 2, 0, 0, 0, time.UTC)
	request := testE2EEPairingRequest(now)
	keyPackage := testE2EEKeyPackage(now)
	script := &scriptedPostgresScript{queries: []scriptedRows{
		{columns: []string{"created_at"}, values: [][]driver.Value{{now}}},
		{columns: e2eePairingRequestColumns(), values: [][]driver.Value{e2eePairingRequestValues(request)}},
		{columns: e2eePairingRequestColumns(), values: [][]driver.Value{e2eePairingRequestValues(request)}},
		{columns: []string{"created_at"}, values: [][]driver.Value{{now}}},
		{columns: e2eeKeyPackageColumns(), values: [][]driver.Value{e2eeKeyPackageValues(keyPackage)}},
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	store.now = func() time.Time { return now }
	ctx := context.Background()

	created, err := store.CreateE2EEPairingRequest(ctx, request)
	require.NoError(t, err)
	assert.Equal(t, now, created.CreatedAt)
	pending, err := store.ListPendingE2EEPairingRequests(ctx, "user_1", "agent_1", 20)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "device_1", pending[0].DeviceID)
	stored, err := store.CompleteE2EEPairing(ctx, request, keyPackage)
	require.NoError(t, err)
	assert.Equal(t, keyPackage.Ciphertext, stored.Ciphertext)
	loaded, err := store.GetE2EEKeyPackage(ctx, "user_1", "agent_1", "device_1", 1)
	require.NoError(t, err)
	assert.Equal(t, keyPackage, loaded)
	assert.True(t, script.committed)
	require.Len(t, script.execTexts, 1)
	assert.Contains(t, script.execTexts[0], "completed_at")
}

func TestPostgresE2EEKeysGivenExpiredRequestWhenPackagePublishedThenTransactionRollsBack(t *testing.T) {
	now := time.Date(2026, 8, 8, 2, 0, 0, 0, time.UTC)
	request := testE2EEPairingRequest(now)
	request.ExpiresAt = now.Add(-time.Second)
	script := &scriptedPostgresScript{queries: []scriptedRows{
		{columns: e2eePairingRequestColumns(), values: [][]driver.Value{e2eePairingRequestValues(request)}},
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	store.now = func() time.Time { return now }

	_, err := store.CompleteE2EEPairing(context.Background(), request, testE2EEKeyPackage(now))
	require.ErrorIs(t, err, ErrConflict)
	assert.True(t, script.rolled)
	assert.False(t, script.committed)
}

func TestPostgresE2EEKeysGivenCreationRetryWhenRecordMatchesThenReturnsExistingRequest(t *testing.T) {
	now := time.Date(2026, 8, 8, 2, 0, 0, 0, time.UTC)
	request := testE2EEPairingRequest(now)
	script := &scriptedPostgresScript{queries: []scriptedRows{
		{columns: []string{"created_at"}},
		{columns: e2eePairingRequestColumns(), values: [][]driver.Value{e2eePairingRequestValues(request)}},
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()

	created, err := store.CreateE2EEPairingRequest(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, request, created)
}

func TestPostgresE2EEKeysGivenExistingRecordsWhenReadThenOwnedValuesAreReturned(t *testing.T) {
	now := time.Date(2026, 8, 8, 2, 0, 0, 0, time.UTC)
	request := testE2EEPairingRequest(now)
	keyPackage := testE2EEKeyPackage(now)
	script := &scriptedPostgresScript{queries: []scriptedRows{
		{columns: e2eePairingRequestColumns(), values: [][]driver.Value{e2eePairingRequestValues(request)}},
		{columns: e2eeKeyPackageColumns(), values: [][]driver.Value{e2eeKeyPackageValues(keyPackage)}},
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()

	loadedRequest, err := store.GetE2EEPairingRequest(context.Background(), "user_1", "agent_1", "pair_1")
	require.NoError(t, err)
	assert.Equal(t, request, loadedRequest)
	loadedPackage, err := store.GetE2EEKeyPackage(context.Background(), "user_1", "agent_1", "device_1", 1)
	require.NoError(t, err)
	assert.Equal(t, keyPackage, loadedPackage)
}

func TestPostgresE2EEKeysGivenCompletedPairingRetryWhenPackageMatchesThenCommitIsIdempotent(t *testing.T) {
	now := time.Date(2026, 8, 8, 2, 0, 0, 0, time.UTC)
	request := testE2EEPairingRequest(now)
	request.CompletedAt = &now
	keyPackage := testE2EEKeyPackage(now)
	script := &scriptedPostgresScript{queries: []scriptedRows{
		{columns: e2eePairingRequestColumns(), values: [][]driver.Value{e2eePairingRequestValues(request)}},
		{columns: e2eeKeyPackageColumns(), values: [][]driver.Value{e2eeKeyPackageValues(keyPackage)}},
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()

	stored, err := store.CompleteE2EEPairing(context.Background(), request, keyPackage)
	require.NoError(t, err)
	assert.Equal(t, keyPackage, stored)
	assert.True(t, script.committed)
}

func testE2EEPairingRequest(now time.Time) E2EEPairingRequest {
	return E2EEPairingRequest{
		PairingID: "pair_1", OwnerUserID: "user_1", NodeID: "node_1",
		AgentID: "agent_1", DeviceID: "device_1", DeviceName: "Chrome on Mac",
		KeyEpoch: 1, RecipientPublicKey: []byte("recipient-public"),
		SecretCommitment: []byte("commitment"), CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}
}

func testE2EEKeyPackage(now time.Time) E2EEKeyPackage {
	return E2EEKeyPackage{
		PairingID: "pair_1", OwnerUserID: "user_1", NodeID: "node_1",
		AgentID: "agent_1", DeviceID: "device_1", KeyEpoch: 1,
		RecipientPublicKey:       []byte("recipient-public"),
		SenderEphemeralPublicKey: []byte("sender-public"),
		Nonce:                    []byte("123456789012"),
		Ciphertext:               []byte("opaque-root-ciphertext"),
		CreatedAt:                now,
	}
}

func e2eePairingRequestColumns() []string {
	return []string{
		"pairing_id", "owner_user_id", "node_id", "agent_id", "device_id", "device_name",
		"key_epoch", "recipient_public_key", "secret_commitment", "created_at", "expires_at", "completed_at",
	}
}

func e2eePairingRequestValues(request E2EEPairingRequest) []driver.Value {
	var completedAt driver.Value
	if request.CompletedAt != nil {
		completedAt = *request.CompletedAt
	}
	return []driver.Value{
		request.PairingID, request.OwnerUserID, request.NodeID, request.AgentID,
		request.DeviceID, request.DeviceName, request.KeyEpoch, request.RecipientPublicKey,
		request.SecretCommitment, request.CreatedAt, request.ExpiresAt, completedAt,
	}
}

func e2eeKeyPackageColumns() []string {
	return []string{
		"pairing_id", "owner_user_id", "node_id", "agent_id", "device_id", "key_epoch",
		"recipient_public_key", "sender_ephemeral_public_key", "nonce", "ciphertext", "created_at",
	}
}

func e2eeKeyPackageValues(keyPackage E2EEKeyPackage) []driver.Value {
	return []driver.Value{
		keyPackage.PairingID, keyPackage.OwnerUserID, keyPackage.NodeID, keyPackage.AgentID,
		keyPackage.DeviceID, keyPackage.KeyEpoch, keyPackage.RecipientPublicKey,
		keyPackage.SenderEphemeralPublicKey, keyPackage.Nonce, keyPackage.Ciphertext,
		keyPackage.CreatedAt,
	}
}
