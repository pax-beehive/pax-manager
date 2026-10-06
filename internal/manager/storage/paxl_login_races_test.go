package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func loginRaceStore(t *testing.T, postgres bool, clock *atomic.Int64) (domain.Store, *sql.DB) {
	t.Helper()
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	if !postgres {
		return NewMemoryStore(now), nil
	}
	dsn := os.Getenv("PAX_MANAGER_LOGIN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAX_MANAGER_LOGIN_TEST_DATABASE_URL to isolated PostgreSQL")
	}
	cfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*cfg)
	schema := fmt.Sprintf("paxl_login_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close()
	})
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = db.Close() })
	ddl, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), string(ddl))
	require.NoError(t, err)
	return NewPostgresStore(db, now), db
}

func TestPaxlLoginGivenConcurrentApprovalsAndCommitsThenOneIdentityAndCredentialWins(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(fmt.Sprintf("postgres=%t", postgres), func(t *testing.T) {
			var clock atomic.Int64
			clock.Store(time.Now().UnixNano())
			store, db := loginRaceStore(t, postgres, &clock)
			first, err := store.EnsureUser(t.Context(), "first@example.com", "", "user")
			require.NoError(t, err)
			second, err := store.EnsureUser(t.Context(), "second@example.com", "", "user")
			require.NoError(t, err)
			start := domain.PaxlDeviceLoginSession{
				LoginID:       "login",
				UserCode:      "ABC123",
				PollTokenHash: "poll",
				Protocol:      domain.PaxlDeviceLoginProtocolClientCommit,
				Status:        "pending",
				ExpiresAt:     time.Now().Add(time.Hour),
				CreatedAt:     time.Now(),
			}
			require.NoError(t, store.CreatePaxlDeviceLoginSession(t.Context(), start))
			var wg sync.WaitGroup
			gate := make(chan struct{})
			for i := range 16 {
				wg.Go(func() {
					<-gate
					user := first
					if i%2 == 1 {
						user = second
					}
					_, err := store.ApprovePaxlDeviceLoginSession(
						t.Context(),
						domain.UserPrincipal{User: user},
						start.UserCode,
						"unused",
						"unused",
						"unused",
					)
					if err != nil {
						assert.ErrorIs(t, err, ErrConflict)
					}
				})
			}
			close(gate)
			wg.Wait()
			bound, err := store.PollPaxlDeviceLoginSession(t.Context(), start.LoginID, "poll")
			require.NoError(t, err)
			assert.Equal(t, "confirmed", bound.Status)
			assert.Empty(t, bound.APIKey)
			owner, err := store.GetUser(t.Context(), bound.OwnerUserID)
			require.NoError(t, err)
			keys, err := store.ListUserAPIKeys(t.Context(), domain.UserPrincipal{User: owner})
			require.NoError(t, err)
			assert.Empty(t, keys)
			update := domain.PaxlDeviceLoginUpdate{
				LoginID:        "login",
				PollTokenHash:  "poll",
				ExpectedUserID: owner.UserID,
				Action:         "commit",
				KeyHash:        "hash",
				KeyPrefix:      "prefix",
				APIKey:         "credential",
			}
			if db != nil {
				_, err = db.ExecContext(
					t.Context(),
					`CREATE FUNCTION fail_login_node() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'injected node failure'; END; $$ LANGUAGE plpgsql; CREATE TRIGGER fail_login_node BEFORE INSERT ON nodes FOR EACH ROW EXECUTE FUNCTION fail_login_node()`,
				)
				require.NoError(t, err)
				_, err = store.UpdatePaxlDeviceLoginSession(t.Context(), update)
				require.Error(t, err)
				keys, err = store.ListUserAPIKeys(t.Context(), domain.UserPrincipal{User: owner})
				require.NoError(t, err)
				assert.Empty(t, keys, "node failure rolls back the credential")
				_, err = db.ExecContext(t.Context(), `DROP TRIGGER fail_login_node ON nodes`)
				require.NoError(t, err)
			}
			results := make(chan domain.PaxlDeviceLoginSession, 16)
			for i := range 16 {
				wg.Go(func() {
					copy := update
					copy.KeyHash, copy.APIKey = fmt.Sprintf("hash-%d", i), fmt.Sprintf("key-%d", i)
					result, err := store.UpdatePaxlDeviceLoginSession(t.Context(), copy)
					assert.NoError(t, err)
					results <- result
				})
			}
			wg.Wait()
			close(results)
			var issued domain.PaxlDeviceLoginSession
			for result := range results {
				if issued.LoginID == "" {
					issued = result
				}
				assert.Equal(t, issued.UserAPIKeyID, result.UserAPIKeyID)
				assert.Equal(t, issued.NodeID, result.NodeID)
				assert.Equal(t, issued.APIKey, result.APIKey)
			}
			keys, err = store.ListUserAPIKeys(t.Context(), domain.UserPrincipal{User: owner})
			require.NoError(t, err)
			assert.Len(t, keys, 1)
			update.ExpectedUserID = "another-user"
			_, err = store.UpdatePaxlDeviceLoginSession(t.Context(), update)
			assert.ErrorIs(t, err, ErrConflict)
			update.ExpectedUserID, update.Action = owner.UserID, "ack"
			acked, err := store.UpdatePaxlDeviceLoginSession(t.Context(), update)
			require.NoError(t, err)
			assert.Empty(t, acked.APIKey)
			require.NoError(t, store.DeleteStalePaxlDeviceLoginSessions(t.Context(), time.Now()))
			_, err = store.UpdatePaxlDeviceLoginSession(t.Context(), update)
			require.NoError(t, err, "cleanup must preserve acknowledgement retries")
			update.Action = "commit"
			final, err := store.UpdatePaxlDeviceLoginSession(t.Context(), update)
			require.NoError(t, err)
			assert.Equal(t, "consumed", final.Status)
			assert.Empty(t, final.APIKey)
			clock.Store(time.Now().Add(2 * time.Hour).UnixNano())
			_, err = store.UpdatePaxlDeviceLoginSession(t.Context(), update)
			assert.ErrorIs(t, err, ErrUnauthorized)
		})
	}
}

func TestPaxlLoginGivenLegacyApprovalRetriesThenOneCredentialAndNoFailedRequestOrphans(
	t *testing.T,
) {
	for _, postgres := range []bool{false, true} {
		t.Run(fmt.Sprintf("postgres=%t", postgres), func(t *testing.T) {
			var clock atomic.Int64
			clock.Store(time.Now().UnixNano())
			store, _ := loginRaceStore(t, postgres, &clock)
			user, err := store.EnsureUser(t.Context(), "legacy@example.com", "", "user")
			require.NoError(t, err)
			principal := domain.UserPrincipal{User: user}
			_, err = store.ApprovePaxlDeviceLoginSession(
				t.Context(),
				principal,
				"MISSING",
				"hash",
				"prefix",
				"key",
			)
			require.Error(t, err)
			session := domain.PaxlDeviceLoginSession{
				LoginID:       "legacy",
				UserCode:      "ABC123",
				PollTokenHash: "poll",
				Status:        "pending",
				ExpiresAt:     time.Now().Add(time.Minute),
				CreatedAt:     time.Now(),
			}
			require.NoError(t, store.CreatePaxlDeviceLoginSession(t.Context(), session))
			first, err := store.ApprovePaxlDeviceLoginSession(
				t.Context(),
				principal,
				session.UserCode,
				"hash",
				"prefix",
				"key",
			)
			require.NoError(t, err)
			second, err := store.ApprovePaxlDeviceLoginSession(
				t.Context(),
				principal,
				session.UserCode,
				"another-hash",
				"prefix",
				"another-key",
			)
			require.NoError(t, err)
			assert.Equal(t, first.UserAPIKeyID, second.UserAPIKeyID)
			assert.Equal(t, first.NodeID, second.NodeID)
			keys, err := store.ListUserAPIKeys(t.Context(), principal)
			require.NoError(t, err)
			assert.Len(t, keys, 1)
			_, err = store.ConsumePaxlDeviceLoginSession(t.Context(), session.LoginID, "poll")
			require.NoError(t, err)
			require.NoError(t, store.DeleteStalePaxlDeviceLoginSessions(t.Context(), time.Now()))
			_, err = store.PollPaxlDeviceLoginSession(t.Context(), session.LoginID, "poll")
			assert.ErrorIs(t, err, ErrUnauthorized)
			session.LoginID, session.UserCode = "expired", "DEF456"
			require.NoError(t, store.CreatePaxlDeviceLoginSession(t.Context(), session))
			clock.Store(time.Now().Add(time.Hour).UnixNano())
			_, err = store.ApprovePaxlDeviceLoginSession(
				t.Context(),
				principal,
				session.UserCode,
				"expired-hash",
				"prefix",
				"expired-key",
			)
			assert.ErrorIs(t, err, ErrUnauthorized)
			keys, err = store.ListUserAPIKeys(t.Context(), principal)
			require.NoError(t, err)
			assert.Len(t, keys, 1)
		})
	}
}

func TestPaxlLoginGivenUnacknowledgedCredentialWhenExpiredThenCleanupRevokesIt(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(fmt.Sprintf("postgres=%t", postgres), func(t *testing.T) {
			var clock atomic.Int64
			clock.Store(time.Now().UnixNano())
			store, _ := loginRaceStore(t, postgres, &clock)
			user, err := store.EnsureUser(t.Context(), "owner@example.com", "", "user")
			require.NoError(t, err)
			now := time.Unix(0, clock.Load())
			start := domain.PaxlDeviceLoginSession{
				LoginID:       "login",
				UserCode:      "ABC123",
				PollTokenHash: "poll",
				Protocol:      domain.PaxlDeviceLoginProtocolClientCommit,
				Status:        "pending",
				CreatedAt:     now,
				ExpiresAt:     now.Add(time.Minute),
			}
			require.NoError(t, store.CreatePaxlDeviceLoginSession(t.Context(), start))
			_, err = store.ApprovePaxlDeviceLoginSession(
				t.Context(),
				domain.UserPrincipal{User: user},
				start.UserCode,
				"unused",
				"unused",
				"unused",
			)
			require.NoError(t, err)
			_, err = store.UpdatePaxlDeviceLoginSession(
				t.Context(),
				domain.PaxlDeviceLoginUpdate{
					LoginID:        "login",
					PollTokenHash:  "poll",
					ExpectedUserID: user.UserID,
					Action:         "commit",
					KeyHash:        "hash",
					KeyPrefix:      "prefix",
					APIKey:         "key",
				},
			)
			require.NoError(t, err)
			clock.Store(now.Add(2 * time.Minute).UnixNano())
			_, err = store.AuthenticateUserAPIKey(t.Context(), "hash")
			assert.ErrorIs(t, err, ErrUnauthorized, "expiry must apply before background cleanup")
			require.NoError(
				t,
				store.DeleteStalePaxlDeviceLoginSessions(t.Context(), time.Unix(0, clock.Load())),
			)
			_, err = store.AuthenticateUserAPIKey(t.Context(), "hash")
			assert.ErrorIs(t, err, ErrUnauthorized)
			_, err = store.PollPaxlDeviceLoginSession(t.Context(), "login", "poll")
			assert.ErrorIs(t, err, ErrUnauthorized)
		})
	}
}

func TestPaxlLoginGivenCancelRacingCommitThenOnlyOneTerminalOutcome(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(fmt.Sprintf("postgres=%t", postgres), func(t *testing.T) {
			var clock atomic.Int64
			clock.Store(time.Now().UnixNano())
			store, _ := loginRaceStore(t, postgres, &clock)
			user, err := store.EnsureUser(t.Context(), "owner@example.com", "", "user")
			require.NoError(t, err)
			for i := range 12 {
				id := fmt.Sprintf("login-%d", i)
				code := fmt.Sprintf("A%05d", i)
				require.NoError(
					t,
					store.CreatePaxlDeviceLoginSession(
						t.Context(),
						domain.PaxlDeviceLoginSession{
							LoginID:       id,
							UserCode:      code,
							PollTokenHash: "poll",
							Protocol:      domain.PaxlDeviceLoginProtocolClientCommit,
							Status:        "pending",
							ExpiresAt:     time.Now().Add(time.Hour),
							CreatedAt:     time.Now(),
						},
					),
				)
				_, err := store.ApprovePaxlDeviceLoginSession(
					t.Context(),
					domain.UserPrincipal{User: user},
					code,
					"unused",
					"unused",
					"unused",
				)
				require.NoError(t, err)
				var wg sync.WaitGroup
				gate := make(chan struct{})
				results := make(chan error, 2)
				for _, action := range []string{"commit", "cancel"} {
					wg.Go(func() {
						<-gate
						_, err := store.UpdatePaxlDeviceLoginSession(
							t.Context(),
							domain.PaxlDeviceLoginUpdate{
								LoginID:        id,
								PollTokenHash:  "poll",
								ExpectedUserID: user.UserID,
								Action:         action,
								KeyHash:        id,
								KeyPrefix:      "prefix",
								APIKey:         "key",
							},
						)
						results <- err
					})
				}
				close(gate)
				wg.Wait()
				close(results)
				var failures int
				for err := range results {
					if err != nil {
						assert.ErrorIs(t, err, ErrConflict)
						failures++
					}
				}
				assert.Equal(t, 1, failures)
				state, err := store.PollPaxlDeviceLoginSession(t.Context(), id, "poll")
				require.NoError(t, err)
				if state.Status == "cancelled" {
					assert.Empty(t, state.APIKey)
					assert.Empty(t, state.NodeID)
					_, err = store.ApprovePaxlDeviceLoginSession(
						t.Context(),
						domain.UserPrincipal{User: user},
						code,
						"unused",
						"unused",
						"unused",
					)
					assert.ErrorIs(t, err, ErrConflict)
				} else {
					assert.Equal(t, "approved", state.Status)
					assert.NotEmpty(t, state.APIKey)
				}
			}
		})
	}
}

func TestPaxlLoginGivenInvalidMutationThenNoCredentialCanBeCreated(t *testing.T) {
	now := time.Now()
	store := NewMemoryStore(func() time.Time { return now })
	user, err := store.EnsureUser(t.Context(), "owner@example.com", "", "user")
	require.NoError(t, err)
	for _, tc := range []struct {
		status, action, owner, hash string
		protocol                    bool
		want                        error
	}{
		{"pending", "commit", "usr_bad", "poll", true, ErrConflict},
		{"pending", "ack", "", "poll", true, ErrConflict},
		{"pending", "cancel", "", "poll", true, nil},
		{"confirmed", "commit", "", "poll", true, ErrConflict},
		{"confirmed", "commit", user.UserID, "poll", true, ErrUnauthorized},
		{"confirmed", "ack", user.UserID, "poll", true, ErrConflict},
		{"confirmed", "invalid", user.UserID, "poll", true, ErrConflict},
		{"confirmed", "cancel", "", "wrong", true, ErrUnauthorized},
		{"confirmed", "cancel", "", "poll", false, ErrUnauthorized},
		{"cancelled", "cancel", "", "poll", true, nil},
		{"cancelled", "commit", user.UserID, "poll", true, ErrConflict},
		{"approved", "cancel", "", "poll", true, ErrConflict},
		{"consumed", "ack", user.UserID, "poll", true, nil},
	} {
		t.Run(
			fmt.Sprintf("%s-%s-%s-%t", tc.status, tc.action, tc.hash, tc.protocol),
			func(t *testing.T) {
				id := t.Name()
				protocol := ""
				if tc.protocol {
					protocol = domain.PaxlDeviceLoginProtocolClientCommit
				}
				require.NoError(
					t,
					store.CreatePaxlDeviceLoginSession(
						t.Context(),
						domain.PaxlDeviceLoginSession{
							LoginID:       id,
							UserCode:      id,
							PollTokenHash: "poll",
							Protocol:      protocol,
							Status:        tc.status,
							OwnerUserID:   user.UserID,
							ExpiresAt:     now.Add(time.Hour),
						},
					),
				)
				_, err := store.UpdatePaxlDeviceLoginSession(
					t.Context(),
					domain.PaxlDeviceLoginUpdate{
						LoginID:        id,
						PollTokenHash:  tc.hash,
						ExpectedUserID: tc.owner,
						Action:         tc.action,
					},
				)
				assert.ErrorIs(t, err, tc.want)
			},
		)
	}
	_, err = store.UpdatePaxlDeviceLoginSession(
		t.Context(),
		domain.PaxlDeviceLoginUpdate{LoginID: "missing", PollTokenHash: "poll"},
	)
	assert.ErrorIs(t, err, ErrUnauthorized)
	keys, err := store.ListUserAPIKeys(t.Context(), domain.UserPrincipal{User: user})
	require.NoError(t, err)
	assert.Empty(t, keys)
}
