package storage

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestShortPairingGivenRotatedCodeThenPreviousExpiresAtNinetySeconds(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	req := testE2EEPairingRequest(now)
	req.ProtocolVersion = "short-code-v2"
	req.RecipientCapabilityHash = make([]byte, 32)
	_, err := store.CreateE2EEPairingRequest(t.Context(), req)
	require.NoError(t, err)
	now = now.Add(89 * time.Second)
	_, err = store.CreateShortPairingAttempt(
		t.Context(),
		req,
		domain.ShortPairingAttempt{
			AttemptID:              "attempt1",
			Generation:             0,
			ApproverCapabilityHash: make([]byte, 32),
			ClientHello:            "opaque",
		},
	)
	require.NoError(t, err)
	now = now.Add(time.Second)
	_, err = store.CreateShortPairingAttempt(
		t.Context(),
		req,
		domain.ShortPairingAttempt{
			AttemptID:              "attempt2",
			Generation:             0,
			ApproverCapabilityHash: make([]byte, 32),
			ClientHello:            "opaque",
		},
	)
	require.Error(t, err)
}

type shortTestStore interface {
	domain.ShortPairingStore
	CreateE2EEPairingRequest(context.Context, E2EEPairingRequest) (E2EEPairingRequest, error)
	CompleteE2EEPairing(context.Context, E2EEPairingRequest, E2EEKeyPackage) (E2EEKeyPackage, error)
	GetE2EEKeyPackage(context.Context, string, string, string, int64) (E2EEKeyPackage, error)
	ListPendingE2EEPairingRequests(
		context.Context,
		string,
		string,
		int,
	) ([]E2EEPairingRequest, error)
}

func TestShortPairingLifecycle(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			for _, scenario := range []string{"confirmation", "expired_confirmation", "cancel", "reject", "completed_is_terminal", "rate_limit", "concurrent_approval"} {
				t.Run(scenario, func(t *testing.T) {
					now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
					var store shortTestStore = NewMemoryStore(func() time.Time { return now })
					if backend == "postgres" {
						store = NewPostgresStore(
							pairingPostgresFixture(t)(),
							func() time.Time { return now },
						)
					}
					req := testE2EEPairingRequest(now)
					req.ProtocolVersion = domain.ShortPairingProtocol
					req.RecipientCapabilityHash = bytes.Repeat([]byte{1}, 32)
					_, err := store.CreateE2EEPairingRequest(t.Context(), req)
					require.NoError(t, err)
					cap := bytes.Repeat([]byte{2}, 32)
					a, err := store.CreateShortPairingAttempt(
						t.Context(),
						req,
						domain.ShortPairingAttempt{
							AttemptID:              "attempt_1",
							ApproverCapabilityHash: cap,
							ClientHello:            "client",
						},
					)
					require.NoError(t, err)
					req.ApprovalAttemptID = a.AttemptID
					req.ApprovalCapabilityHash = cap
					// Neither the login cookie nor the wrong capability can advance or approve.
					_, err = store.AdvanceShortPairingAttempt(
						t.Context(),
						req,
						a.AttemptID,
						cap,
						1,
						"server",
					)
					require.ErrorIs(t, err, ErrNotFound)
					_, err = store.CompleteE2EEPairing(t.Context(), req, testE2EEKeyPackage(now))
					require.Error(t, err)
					if scenario == "rate_limit" {
						for i := 1; i < 30; i++ {
							_, err = store.CreateShortPairingAttempt(
								t.Context(),
								req,
								domain.ShortPairingAttempt{
									AttemptID:              fmt.Sprintf("attempt_%d", i+1),
									ApproverCapabilityHash: cap,
									ClientHello:            "client",
								},
							)
							require.NoError(t, err)
						}
						// A fresh pairing and a new generation do not reset the account budget.
						next := req
						next.PairingID = "pair_next"
						next.DeviceID = "device_next"
						_, err = store.CreateE2EEPairingRequest(t.Context(), next)
						require.NoError(t, err)
						now = now.Add(time.Minute)
						_, err = store.CreateShortPairingAttempt(
							t.Context(),
							next,
							domain.ShortPairingAttempt{
								AttemptID:              "over_budget",
								Generation:             1,
								ApproverCapabilityHash: cap,
								ClientHello:            "client",
							},
						)
						require.ErrorIs(t, err, domain.ErrShortPairingLimited)
						return
					}
					_, err = store.AdvanceShortPairingAttempt(
						t.Context(),
						req,
						a.AttemptID,
						req.RecipientCapabilityHash,
						1,
						"server",
					)
					require.NoError(t, err)
					_, err = store.AdvanceShortPairingAttempt(
						t.Context(),
						req,
						a.AttemptID,
						cap,
						2,
						"finish",
					)
					require.NoError(t, err)
					a, err = store.AdvanceShortPairingAttempt(
						t.Context(),
						req,
						a.AttemptID,
						req.RecipientCapabilityHash,
						3,
						"encrypted_secret",
					)
					require.NoError(t, err)
					require.WithinDuration(t, now.Add(30*time.Second), *a.ConfirmationExpiresAt, 0)
					now = now.Add(time.Second)
					replay, err := store.AdvanceShortPairingAttempt(
						t.Context(),
						req,
						a.AttemptID,
						req.RecipientCapabilityHash,
						3,
						"encrypted_secret",
					)
					require.NoError(t, err)
					require.WithinDuration(
						t,
						*a.ConfirmationExpiresAt,
						*replay.ConfirmationExpiresAt,
						0,
					)
					switch scenario {
					case "cancel", "reject":
						reason := "cancelled"
						callerCap := req.RecipientCapabilityHash
						if scenario == "reject" {
							reason = "rejected"
							callerCap = cap
						}
						require.NoError(
							t,
							store.EndShortPairing(t.Context(), req, callerCap, reason),
						)
						require.NoError(
							t,
							store.EndShortPairing(t.Context(), req, callerCap, reason),
						)
						_, err = store.CompleteE2EEPairing(
							t.Context(),
							req,
							testE2EEKeyPackage(now),
						)
						require.ErrorIs(t, err, domain.ErrShortPairingEnded)
						pending, err := store.ListPendingE2EEPairingRequests(
							t.Context(),
							req.OwnerUserID,
							req.AgentID,
							20,
						)
						require.NoError(t, err)
						require.Empty(t, pending)
						return
					case "expired_confirmation":
						now = *a.ConfirmationExpiresAt
						_, err = store.CompleteE2EEPairing(
							t.Context(),
							req,
							testE2EEKeyPackage(now),
						)
						require.ErrorIs(t, err, domain.ErrE2EEPairingExpired)
						return
					case "concurrent_approval":
						results := make(chan error, 8)
						for range 8 {
							go func() { _, e := store.CompleteE2EEPairing(t.Context(), req, testE2EEKeyPackage(now)); results <- e }()
						}
						for range 8 {
							require.NoError(t, <-results)
						}
					default:
						_, err = store.CompleteE2EEPairing(
							t.Context(),
							req,
							testE2EEKeyPackage(now),
						)
						require.NoError(t, err)
					}
					if scenario == "completed_is_terminal" {
						require.Error(t, store.EndShortPairing(t.Context(), req, cap, "rejected"))
						require.Error(
							t,
							store.EndShortPairing(
								t.Context(),
								req,
								req.RecipientCapabilityHash,
								"cancelled",
							),
						)
					}
					now = now.Add(20 * time.Minute)
					_, err = store.CompleteE2EEPairing(t.Context(), req, testE2EEKeyPackage(now))
					require.NoError(t, err)
					loaded, err := store.GetE2EEKeyPackage(
						t.Context(),
						req.OwnerUserID,
						req.AgentID,
						req.DeviceID,
						req.KeyEpoch,
					)
					require.NoError(t, err)
					require.Equal(t, req.PairingID, loaded.PairingID)
				})
			}
		})
	}
}

func TestShortPairingPostgresBudgetSharedAcrossInstances(t *testing.T) {
	open := pairingPostgresFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	const count = 36
	stores := make([]*PostgresStore, count)
	requests := make([]E2EEPairingRequest, count)
	for i := range count {
		stores[i] = NewPostgresStore(open(), func() time.Time { return now })
		requests[i] = testE2EEPairingRequest(now)
		requests[i].PairingID = fmt.Sprintf("pair_%d", i)
		requests[i].DeviceID = fmt.Sprintf("device_%d", i)
		requests[i].ProtocolVersion = domain.ShortPairingProtocol
		requests[i].RecipientCapabilityHash = bytes.Repeat([]byte{1}, 32)
		_, err := stores[i].CreateE2EEPairingRequest(t.Context(), requests[i])
		require.NoError(t, err)
	}
	results := make(chan error, count)
	for i := range count {
		go func() {
			_, err := stores[i].CreateShortPairingAttempt(
				t.Context(),
				requests[i],
				domain.ShortPairingAttempt{
					AttemptID:              fmt.Sprintf("attempt_%d", i),
					ClientHello:            "hello",
					ApproverCapabilityHash: bytes.Repeat([]byte{2}, 32),
				},
			)
			results <- err
		}()
	}
	accepted := 0
	for range count {
		err := <-results
		if err == nil {
			accepted++
		} else {
			require.ErrorIs(t, err, domain.ErrShortPairingLimited)
		}
	}
	require.Equal(t, 30, accepted)
}

func TestShortPairingPostgresMigrationPreservesLegacyAccess(t *testing.T) {
	open := pairingPostgresFixture(t)
	db := open()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := NewPostgresStore(db, func() time.Time { return now })
	req := testE2EEPairingRequest(now)
	_, err := store.CreateE2EEPairingRequest(t.Context(), req)
	require.NoError(t, err)
	_, err = store.CompleteE2EEPairing(t.Context(), req, testE2EEKeyPackage(now))
	require.NoError(t, err)
	_, err = db.ExecContext(
		t.Context(),
		`DROP TABLE e2ee_pairing_attempts; DROP TABLE e2ee_pairing_rate_limits;
 ALTER TABLE e2ee_pairing_requests DROP COLUMN protocol_version,DROP COLUMN recipient_capability_hash,DROP COLUMN cancelled_at,DROP COLUMN rejected_at;`,
	)
	require.NoError(t, err)
	for range 2 {
		_, err = db.ExecContext(t.Context(), pairingSchemaSQL(t))
		require.NoError(t, err)
	}
	migrated, err := store.GetE2EEPairingRequest(
		t.Context(),
		req.OwnerUserID,
		req.AgentID,
		req.PairingID,
	)
	require.NoError(t, err)
	require.Equal(t, "legacy-v1", migrated.ProtocolVersion)
	now = now.Add(time.Hour)
	loaded, err := store.GetE2EEKeyPackage(
		t.Context(),
		req.OwnerUserID,
		req.AgentID,
		req.DeviceID,
		req.KeyEpoch,
	)
	require.NoError(t, err)
	require.Equal(t, req.PairingID, loaded.PairingID)
}
