package storage

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type workspaceApprovalTestStore interface {
	CreateApproval(context.Context, Node, CreateApprovalRequest) (AgentApproval, error)
	GetApproval(context.Context, UserPrincipal, string) (AgentApproval, error)
	DecideApproval(
		context.Context,
		UserPrincipal,
		string,
		ApprovalDecisionRequest,
	) (AgentApproval, error)
	ExpireWorkspaceTurnApprovals(context.Context, string, string, string, string, string) error
	CancelWorkspaceApproval(
		context.Context,
		string,
		string,
		string,
		string,
		string,
		string,
	) (AgentApproval, error)
}

func workspaceApprovalFixture(
	t *testing.T, backend string,
) (workspaceApprovalTestStore, string, *atomic.Int64) {
	t.Helper()
	clock := &atomic.Int64{}
	clock.Store(time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC).UnixNano())
	store, owner := approvalNotificationFixture(
		t,
		backend,
		func() time.Time { return time.Unix(0, clock.Load()).UTC() },
	)
	return store.(workspaceApprovalTestStore), owner, clock
}

func createWorkspaceApproval(
	t *testing.T,
	store workspaceApprovalTestStore,
	owner string,
	deadline *time.Time,
) AgentApproval {
	t.Helper()
	approval, err := store.CreateApproval(
		t.Context(),
		Node{NodeID: "node", OwnerUserID: owner},
		CreateApprovalRequest{
			AgentID: "agent", SessionID: "session", Domain: "workspace", Operation: "search", ResourceType: "turn",
			ResourceRef: "turn-one", ActionFingerprint: "workspace-test", ExpiresAt: deadline,
			RequestBody: json.RawMessage(
				`{"turn_id":"turn-one","scope":{"queries":["*"],"agent_ids":["*"],"session_ids":["*"]}}`,
			),
		},
	)
	require.NoError(t, err)
	return approval
}

func TestWorkspaceApprovalStorageSeparatesPendingDeadlineAndGrantLifetime(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		for _, unlimited := range []bool{false, true} {
			name := "bounded"
			if unlimited {
				name = "unlimited"
			}
			t.Run(backend+"/"+name, func(t *testing.T) {
				store, owner, clock := workspaceApprovalFixture(t, backend)
				created := time.Unix(0, clock.Load()).UTC()
				deadline := created.Add(600 * time.Second)
				var expiresAt *time.Time
				if !unlimited {
					expiresAt = &deadline
				}
				approval := createWorkspaceApproval(t, store, owner, expiresAt)
				assert.Equal(t, expiresAt, approval.ExpiresAt)
				decisionAt := created.Add(599 * time.Second)
				clock.Store(decisionAt.UnixNano())
				approved, err := store.DecideApproval(
					t.Context(),
					UserPrincipal{User: User{UserID: owner}},
					approval.ApprovalID,
					ApprovalDecisionRequest{DecisionOption: "allow_once"},
				)
				require.NoError(t, err)
				require.NotNil(t, approved.ExpiresAt)
				assert.Equal(
					t,
					decisionAt.Add(domain.WorkspaceSearchGrantLifetime),
					*approved.ExpiresAt,
				)
				clock.Store(created.Add(601 * time.Second).UnixNano())
				current, err := store.GetApproval(
					t.Context(),
					UserPrincipal{User: User{UserID: owner}},
					approval.ApprovalID,
				)
				require.NoError(t, err)
				assert.True(
					t,
					current.ExpiresAt.After(time.Unix(0, clock.Load())),
					"request deadline does not expire an approved grant",
				)
				assert.JSONEq(t, string(approval.RequestBody), string(current.RequestBody))
				require.NoError(
					t,
					store.ExpireWorkspaceTurnApprovals(
						t.Context(),
						owner,
						"node",
						"agent",
						"session",
						"turn-one",
					),
				)
				current, err = store.GetApproval(
					t.Context(),
					UserPrincipal{User: User{UserID: owner}},
					approval.ApprovalID,
				)
				require.NoError(t, err)
				assert.False(
					t,
					current.ExpiresAt.After(time.Unix(0, clock.Load())),
					"turn end remains a hard bound",
				)
			})
		}
	}
}

func TestWorkspaceApprovalStorageRejectsLateCancelledOrBroadDecisions(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		for _, scenario := range []string{"request_expired", "turn_expired", "cancelled", "broad_decision"} {
			t.Run(backend+"/"+scenario, func(t *testing.T) {
				store, owner, clock := workspaceApprovalFixture(t, backend)
				deadline := time.Unix(0, clock.Load()).UTC().Add(600 * time.Second)
				approval := createWorkspaceApproval(t, store, owner, &deadline)
				decision := "allow_once"
				switch scenario {
				case "request_expired":
					clock.Store(deadline.UnixNano())
				case "turn_expired":
					require.NoError(
						t,
						store.ExpireWorkspaceTurnApprovals(
							t.Context(),
							owner,
							"node",
							"agent",
							"session",
							"turn-one",
						),
					)
				case "cancelled":
					cancelled, err := store.CancelWorkspaceApproval(
						t.Context(),
						owner,
						"node",
						"agent",
						"session",
						"turn-one",
						approval.ApprovalID,
					)
					require.NoError(t, err)
					require.NotNil(t, cancelled.GrantRevokedAt)
				case "broad_decision":
					decision = "allow_always_on_all_agents"
				}
				_, err := store.DecideApproval(
					t.Context(),
					UserPrincipal{User: User{UserID: owner}},
					approval.ApprovalID,
					ApprovalDecisionRequest{DecisionOption: decision},
				)
				assert.ErrorIs(t, err, ErrConflict)
			})
		}
	}
}

func TestWorkspaceApprovalStorageCancellationIsBoundAndIdempotent(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		for _, approved := range []bool{false, true} {
			name := "pending"
			if approved {
				name = "approved"
			}
			t.Run(backend+"/"+name, func(t *testing.T) {
				store, owner, clock := workspaceApprovalFixture(t, backend)
				approval := createWorkspaceApproval(t, store, owner, nil)
				if approved {
					_, err := store.DecideApproval(
						t.Context(),
						UserPrincipal{User: User{UserID: owner}},
						approval.ApprovalID,
						ApprovalDecisionRequest{DecisionOption: "allow_once"},
					)
					require.NoError(t, err)
				}
				contextIDs := []string{
					owner,
					"node",
					"agent",
					"session",
					"turn-one",
					approval.ApprovalID,
				}
				for index := range contextIDs {
					wrong := append([]string(nil), contextIDs...)
					wrong[index] = "foreign"
					_, err := store.CancelWorkspaceApproval(
						t.Context(),
						wrong[0],
						wrong[1],
						wrong[2],
						wrong[3],
						wrong[4],
						wrong[5],
					)
					assert.ErrorIs(t, err, ErrNotFound)
				}
				cancelled, err := store.CancelWorkspaceApproval(
					t.Context(),
					owner,
					"node",
					"agent",
					"session",
					"turn-one",
					approval.ApprovalID,
				)
				require.NoError(t, err)
				require.NotNil(t, cancelled.GrantRevokedAt)
				assert.Equal(
					t,
					"session",
					cancelled.RequestSessionID,
					"cancellation returns canonical session identity",
				)
				clock.Add(int64(time.Minute))
				again, err := store.CancelWorkspaceApproval(
					t.Context(),
					owner,
					"node",
					"agent",
					"session",
					"turn-one",
					approval.ApprovalID,
				)
				require.NoError(t, err)
				assert.Equal(t, cancelled.GrantRevokedAt, again.GrantRevokedAt)
			})
		}
	}
}

func TestWorkspaceApprovalStorageCancellationPreservesDeniedAndExpiredRecords(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		for _, scenario := range []string{"denied", "expired"} {
			t.Run(backend+"/"+scenario, func(t *testing.T) {
				store, owner, clock := workspaceApprovalFixture(t, backend)
				deadline := time.Unix(0, clock.Load()).UTC().Add(time.Second)
				approval := createWorkspaceApproval(t, store, owner, &deadline)
				if scenario == "denied" {
					_, err := store.DecideApproval(
						t.Context(),
						UserPrincipal{User: User{UserID: owner}},
						approval.ApprovalID,
						ApprovalDecisionRequest{DecisionOption: "deny"},
					)
					require.NoError(t, err)
				} else {
					clock.Store(deadline.UnixNano())
				}
				cancelled, err := store.CancelWorkspaceApproval(
					t.Context(),
					owner,
					"node",
					"agent",
					"session",
					"turn-one",
					approval.ApprovalID,
				)
				require.NoError(t, err)
				assert.Nil(t, cancelled.GrantRevokedAt)
				if scenario == "denied" {
					assert.Equal(t, "deny", cancelled.Decision)
				}
			})
		}
	}
}

func TestWorkspaceApprovalStorageConcurrentCancelCannotLeaveAnApprovedGrant(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			store, owner, _ := workspaceApprovalFixture(t, backend)
			for range 8 {
				approval := createWorkspaceApproval(t, store, owner, nil)
				var group sync.WaitGroup
				group.Go(func() {
					_, err := store.DecideApproval(
						t.Context(),
						UserPrincipal{User: User{UserID: owner}},
						approval.ApprovalID,
						ApprovalDecisionRequest{DecisionOption: "allow_once"},
					)
					if err != nil {
						assert.ErrorIs(t, err, ErrConflict)
					}
				})
				group.Go(func() {
					_, err := store.CancelWorkspaceApproval(
						t.Context(),
						owner,
						"node",
						"agent",
						"session",
						"turn-one",
						approval.ApprovalID,
					)
					assert.NoError(t, err)
				})
				group.Wait()
				final, err := store.GetApproval(
					t.Context(),
					UserPrincipal{User: User{UserID: owner}},
					approval.ApprovalID,
				)
				require.NoError(t, err)
				assert.NotNil(t, final.GrantRevokedAt)
			}
		})
	}
}

func TestWorkspaceApprovalStorageDoesNotChangeOtherApprovalDomains(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			store, owner, clock := workspaceApprovalFixture(t, backend)
			expired := time.Unix(0, clock.Load()).UTC().Add(-time.Second)
			approval, err := store.CreateApproval(
				t.Context(),
				Node{NodeID: "node", OwnerUserID: owner},
				CreateApprovalRequest{
					AgentID: "agent", SessionID: "session", Domain: "agent_action", Operation: "execute", ResourceType: "tool",
					ResourceRef: "command", ActionFingerprint: "non-workspace", ExpiresAt: &expired,
				},
			)
			require.NoError(t, err)
			decided, err := store.DecideApproval(
				t.Context(),
				UserPrincipal{User: User{UserID: owner}},
				approval.ApprovalID,
				ApprovalDecisionRequest{DecisionOption: "allow_for_this_agent"},
			)
			require.NoError(t, err)
			assert.Equal(t, "agent", decided.DecisionScope)
			assert.Equal(t, &expired, decided.ExpiresAt)
		})
	}
}
