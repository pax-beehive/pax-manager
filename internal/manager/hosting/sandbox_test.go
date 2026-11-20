package hosting_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func sandboxFixture(t *testing.T) (
	*hosting.SandboxService,
	*storage.MemoryStore,
	hosting.SandboxConfig,
	string,
	*time.Time,
	*bool,
) {
	t.Helper()
	now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	store := storage.NewMemoryStore(func() time.Time { return now })
	user, err := store.EnsureUser(t.Context(), "sandbox@example.com", "Sandbox", "user")
	require.NoError(t, err)
	config := hosting.SandboxConfig{
		HostKey:  strings.Repeat("s", 32),
		HostID:   "aws-sandbox-pilot",
		CloudURL: "https://node.example.com/",
	}
	allowed := true
	service := hosting.NewSandbox(store, config, func() time.Time { return now },
		func(_ context.Context, ownerID string) bool {
			return allowed && ownerID == user.UserID
		},
		func(_ context.Context, ownerID, nodeID string) error {
			if ownerID != user.UserID || (nodeID != "owned-node" && nodeID != "other-owned-node") {
				return hosting.ErrUnauthorized
			}
			return nil
		},
	)
	return service, store, config, user.UserID, &now, &allowed
}

func TestSandboxQueuesNodeOnlyJobAndReplaysUnderSameLease(t *testing.T) {
	s, store, config, owner, now, _ := sandboxFixture(t)
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		state.Environments[owner] = &hosting.EnvironmentRecord{Starter: hosting.Starter{
			EnvironmentID: "starter-home",
			NodeID:        "node-home",
		}}
		return nil
	}))
	require.True(t, s.HostAuthorized(config.HostKey))
	require.False(t, s.HostAuthorized(strings.Repeat("x", 32)))
	status, err := s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.Nil(t, status.Sandbox)
	_, err = s.Status(t.Context(), "uninvited-user", true)
	require.ErrorIs(t, err, hosting.ErrNotFound)
	for range 2 {
		status, err = s.Status(t.Context(), owner, true)
		require.NoError(t, err)
		require.Equal(t, "queued", status.Sandbox.Status)
	}

	claim := hosting.Claim{Kind: hosting.SandboxKind, HostID: config.HostID}
	job, _, err := s.Claim(t.Context(), &claim)
	require.NoError(t, err)
	require.Nil(t, job)
	claim.Capacity = 1
	job, _, err = s.Claim(t.Context(), &claim)
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, int64(1), job.Generation)
	require.Equal(t, hosting.SandboxKind, job.Kind)
	require.Equal(t, owner, job.OwnerUserID)
	require.Equal(t, strings.TrimSuffix(config.CloudURL, "/"), job.CloudURL)
	require.Equal(t, now.Add(180*time.Second), job.LeaseExpiresAt)
	require.True(t, strings.HasPrefix(job.EnvironmentID, "snode-"))
	data, err := json.Marshal(job)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"model_`)
	require.NotContains(t, string(data), `"agent_id"`)
	registration, err := store.ResolveRegistrationToken(
		t.Context(),
		hashTest(job.RegistrationToken),
	)
	require.NoError(t, err)
	require.Equal(t, owner, registration.UserID)
	_, err = store.ResolveRegistrationToken(t.Context(), hashTest(job.RegistrationToken))
	require.Error(t, err)

	claim.Capacity = 0
	replayed, _, err := s.Claim(t.Context(), &claim)
	require.NoError(t, err)
	require.Equal(t, job, replayed)
	claim.HostID = "other-host"
	_, _, err = s.Claim(t.Context(), &claim)
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	claim.HostID, claim.Kind = config.HostID, ""
	_, _, err = s.Claim(t.Context(), &claim)
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		require.Equal(t, "node-home", state.Environments[owner].Starter.NodeID)
		return nil
	}))
}

func TestSandboxClaimCountsEligibleRemainingStarts(t *testing.T) {
	ctx := t.Context()
	_, store, config, owner, now, _ := sandboxFixture(t)
	other, err := store.EnsureUser(ctx, "another@example.com", "Another", "user")
	require.NoError(t, err)
	newOwner, existingOwner := min(owner, other.UserID), max(owner, other.UserID)
	denied := ""
	s := hosting.NewSandbox(store, config, func() time.Time { return *now },
		func(_ context.Context, owner string) bool { return owner != denied },
		func(context.Context, string, string) error { return nil },
	)
	for _, owner := range []string{newOwner, existingOwner, "foreign-host", "denied", "invalid"} {
		_, err := s.Status(ctx, owner, true)
		require.NoError(t, err)
	}
	denied = "denied"
	require.NoError(t, store.WithSandboxState(ctx, func(state *hosting.State) error {
		state.Environments[existingOwner].Starter.NodeID = "owned-node"
		state.Environments["foreign-host"].HostID = "other-host"
		state.Environments["invalid"].Starter.OwnerUserID = "wrong-owner"
		return nil
	}))

	claim := hosting.Claim{Kind: hosting.SandboxKind, HostID: config.HostID}
	job, count, err := s.Claim(ctx, &claim)
	require.NoError(t, err)
	require.Nil(t, job)
	require.Equal(t, 2, count)
	claim.ExistingOnly = true
	job, count, err = s.Claim(ctx, &claim)
	require.NoError(t, err)
	require.Nil(t, job)
	require.Equal(t, 1, count)

	claim.Capacity = 1
	job, count, err = s.Claim(ctx, &claim)
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, newOwner, job.OwnerUserID, "the count filter must not change job selection")
	require.Equal(t, 1, count)
	claim.Capacity = 0
	replayed, count, err := s.Claim(ctx, &claim)
	require.NoError(t, err)
	require.Equal(t, job, replayed)
	require.Equal(t, 1, count)

	*now = job.LeaseExpiresAt
	claim.Capacity = 1
	job, count, err = s.Claim(ctx, &claim)
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, existingOwner, job.OwnerUserID)
	require.Equal(t, 0, count)
	status, err := s.Status(ctx, newOwner, false)
	require.NoError(t, err)
	require.Equal(t, "sleeping", status.Sandbox.Status)
}

func TestSandboxReportsWaitingOnlyForFreshZeroCapacity(t *testing.T) {
	ctx := t.Context()
	s, store, config, owner, now, allowed := sandboxFixture(t)
	claim := hosting.Claim{Kind: hosting.SandboxKind, HostID: config.HostID}
	job, _, err := s.Claim(ctx, &claim)
	require.NoError(t, err)
	require.Nil(t, job)
	status, err := s.Status(ctx, owner, true)
	require.NoError(t, err)
	require.Equal(t, &hosting.SandboxView{
		Status: "queued", ReasonCode: "waiting_for_capacity",
	}, status.Sandbox)

	*now = now.Add(21 * time.Second)
	status, err = s.Status(ctx, owner, false)
	require.NoError(t, err)
	require.Equal(t, &hosting.SandboxView{Status: "queued"}, status.Sandbox)
	job, _, err = s.Claim(ctx, &claim)
	require.NoError(t, err)
	require.Nil(t, job)
	successorConfig := config
	successorConfig.HostID = "new-sandbox-host"
	successor := hosting.NewSandbox(store, successorConfig, func() time.Time { return *now },
		func(context.Context, string) bool { return true },
		func(context.Context, string, string) error { return nil },
	)
	status, err = successor.Status(ctx, owner, false)
	require.NoError(t, err)
	require.Equal(t, &hosting.SandboxView{Status: "queued"}, status.Sandbox)

	// A later positive sample supersedes the zero even if another claim cannot run yet.
	*allowed = false
	claim.Capacity = 1
	job, _, err = s.Claim(ctx, &claim)
	require.NoError(t, err)
	require.Nil(t, job)
	*allowed = true
	status, err = s.Status(ctx, owner, false)
	require.NoError(t, err)
	require.Equal(t, &hosting.SandboxView{Status: "queued"}, status.Sandbox)
}

func TestSandboxStartupFailureReasons(t *testing.T) {
	for _, tc := range []struct {
		workerCode string
		status     string
		reason     string
	}{
		{"host_environment_limit", "failed", "host_environment_limit"},
		{"container_exited", "failed", "container_exited"},
		{"node_connection_timeout", "failed", "node_connection_timeout"},
		{"startup_failed", "failed", "host_start_failed"},
		{"unknown private diagnostic", "failed", "host_start_failed"},
		{"host_capacity", "queued", ""},
	} {
		t.Run(tc.workerCode, func(t *testing.T) {
			ctx := t.Context()
			s, store, config, owner, _, _ := sandboxFixture(t)
			_, err := s.Status(ctx, owner, true)
			require.NoError(t, err)
			claim := hosting.Claim{Kind: hosting.SandboxKind, HostID: config.HostID, Capacity: 1}
			job, _, err := s.Claim(ctx, &claim)
			require.NoError(t, err)
			require.NotNil(t, job)
			update := hosting.Update{
				Kind: hosting.SandboxKind, HostID: config.HostID, JobID: job.JobID,
				LeaseToken: job.LeaseToken, Generation: job.Generation, State: "failed",
				ErrorCode: tc.workerCode,
			}
			_, err = s.Update(ctx, &update, true)
			require.NoError(t, err)
			status, err := s.Status(ctx, owner, false)
			require.NoError(t, err)
			require.Equal(t, &hosting.SandboxView{Status: tc.status, ReasonCode: tc.reason},
				status.Sandbox)
			require.NoError(t, store.WithSandboxState(ctx, func(state *hosting.State) error {
				record := state.Environments[owner]
				if tc.workerCode == "host_capacity" {
					require.Nil(t, record.Job)
					require.Equal(t, job.Generation+1, record.Generation)
				} else {
					require.Equal(t, tc.reason, record.Starter.ErrorCode)
				}
				return nil
			}))
			status, err = s.Status(ctx, owner, true)
			require.NoError(t, err)
			require.Equal(t, &hosting.SandboxView{Status: "queued"}, status.Sandbox)
		})
	}
}

func TestSandboxSanitizesHistoricalFailureReason(t *testing.T) {
	ctx := t.Context()
	s, store, _, owner, _, _ := sandboxFixture(t)
	for _, historicalCode := range []string{"", "host_start_failed", "private diagnostic"} {
		require.NoError(t, store.WithSandboxState(ctx, func(state *hosting.State) error {
			state.Environments[owner] = &hosting.EnvironmentRecord{
				Starter: hosting.Starter{State: "failed", ErrorCode: historicalCode},
			}
			return nil
		}))
		status, err := s.Status(ctx, owner, false)
		require.NoError(t, err)
		require.Equal(t, &hosting.SandboxView{
			Status: "failed", ReasonCode: "host_start_failed",
		}, status.Sandbox)
	}
}

func TestSandboxReadyChecksOwnerAndKeepsNodeAcrossWake(t *testing.T) {
	s, _, config, owner, now, _ := sandboxFixture(t)
	_, err := s.Status(t.Context(), owner, true)
	require.NoError(t, err)
	claim := hosting.Claim{Kind: hosting.SandboxKind, HostID: config.HostID, Capacity: 1}
	job, _, err := s.Claim(t.Context(), &claim)
	require.NoError(t, err)
	require.NotNil(t, job)
	update := hosting.Update{
		Kind: hosting.SandboxKind, HostID: config.HostID, JobID: job.JobID,
		LeaseToken: job.LeaseToken, Generation: job.Generation, State: "ready", NodeID: "foreign-node",
	}
	_, err = s.Update(t.Context(), &update, true)
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	update.NodeID, update.AgentID = "owned-node", "unrequested-agent"
	_, err = s.Update(t.Context(), &update, true)
	require.ErrorIs(t, err, hosting.ErrConflict)
	update.AgentID = ""
	_, err = s.Update(t.Context(), &update, false)
	require.ErrorIs(t, err, hosting.ErrConflict)
	for range 2 {
		_, err = s.Update(t.Context(), &update, true)
		require.NoError(t, err)
	}
	status, err := s.Status(t.Context(), owner, true)
	require.NoError(t, err)
	require.Equal(t, &hosting.SandboxView{Status: "ready", NodeID: "owned-node"}, status.Sandbox)

	update.NodeID = "other-owned-node"
	_, err = s.Update(t.Context(), &update, true)
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	update.State, update.NodeID = "", ""
	*now = now.Add(time.Minute)
	renewed, err := s.Update(t.Context(), &update, false)
	require.NoError(t, err)
	require.Equal(t, now.Add(180*time.Second), renewed)
	*now = renewed.Add(time.Second)
	_, err = s.Update(t.Context(), &update, false)
	require.ErrorIs(t, err, hosting.ErrConflict)
	status, err = s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.Equal(t, &hosting.SandboxView{Status: "sleeping", NodeID: "owned-node"}, status.Sandbox)
	status, err = s.Status(t.Context(), owner, true)
	require.NoError(t, err)
	require.Equal(t, "queued", status.Sandbox.Status)
	resumed, _, err := s.Claim(t.Context(), &claim)
	require.NoError(t, err)
	require.NotNil(t, resumed)
	require.Equal(t, job.EnvironmentID, resumed.EnvironmentID)
	require.Equal(t, "owned-node", resumed.NodeID)
	require.Empty(t, resumed.RegistrationToken)
	require.Greater(t, resumed.Generation, job.Generation)
	_, err = s.Update(t.Context(), &update, false)
	require.ErrorIs(t, err, hosting.ErrNotFound)
}

func TestSandboxGateChangeControlsQueueAndRevokesActiveLease(t *testing.T) {
	ctx := t.Context()
	s, _, config, owner, _, allowed := sandboxFixture(t)
	claim := hosting.Claim{Kind: hosting.SandboxKind, HostID: config.HostID, Capacity: 1}

	*allowed = false
	require.True(t, s.HostAuthorized(config.HostKey))
	_, err := s.Status(ctx, owner, true)
	require.ErrorIs(t, err, hosting.ErrNotFound)
	job, _, err := s.Claim(ctx, &claim)
	require.NoError(t, err)
	require.Nil(t, job)

	*allowed = true
	status, err := s.Status(ctx, owner, true)
	require.NoError(t, err)
	require.Equal(t, "queued", status.Sandbox.Status)
	*allowed = false
	job, _, err = s.Claim(ctx, &claim)
	require.NoError(t, err)
	require.Nil(t, job)

	*allowed = true
	job, _, err = s.Claim(ctx, &claim)
	require.NoError(t, err)
	require.NotNil(t, job)
	update := hosting.Update{
		Kind: hosting.SandboxKind, HostID: config.HostID, JobID: job.JobID,
		LeaseToken: job.LeaseToken, Generation: job.Generation, State: "ready", NodeID: "owned-node",
	}
	_, err = s.Update(ctx, &update, true)
	require.NoError(t, err)

	*allowed = false
	update.State, update.NodeID = "", ""
	expiry, err := s.Update(ctx, &update, false)
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	require.True(t, expiry.IsZero())
	require.True(t, s.HostAuthorized(config.HostKey))

	*allowed = true
	status, err = s.Status(ctx, owner, false)
	require.NoError(t, err)
	require.Equal(t, &hosting.SandboxView{Status: "sleeping", NodeID: "owned-node"}, status.Sandbox)
	_, err = s.Update(ctx, &update, false)
	require.ErrorIs(t, err, hosting.ErrConflict)
	_, err = s.Status(ctx, owner, true)
	require.NoError(t, err)
	resumed, _, err := s.Claim(ctx, &claim)
	require.NoError(t, err)
	require.NotNil(t, resumed)
	require.Equal(t, "owned-node", resumed.NodeID)
	require.Greater(t, resumed.Generation, job.Generation)
}

func TestSandboxGateDeniesUnfinishedCreationButAllowsValidTerminalUpdate(t *testing.T) {
	for _, finish := range []string{"ready", "sleeping"} {
		t.Run(finish, func(t *testing.T) {
			ctx := t.Context()
			s, _, config, owner, _, allowed := sandboxFixture(t)
			_, err := s.Status(ctx, owner, true)
			require.NoError(t, err)
			claim := hosting.Claim{Kind: hosting.SandboxKind, HostID: config.HostID, Capacity: 1}
			job, _, err := s.Claim(ctx, &claim)
			require.NoError(t, err)
			require.NotNil(t, job)
			*allowed = false
			claim.Capacity = 0
			replayed, _, err := s.Claim(ctx, &claim)
			require.NoError(t, err)
			require.Nil(t, replayed)

			update := hosting.Update{
				Kind: hosting.SandboxKind, HostID: config.HostID, JobID: job.JobID,
				LeaseToken: "wrong-lease", Generation: job.Generation, State: finish,
			}
			if finish == "ready" {
				update.NodeID = "owned-node"
			}
			_, err = s.Update(ctx, &update, true)
			require.ErrorIs(t, err, hosting.ErrConflict)
			update.LeaseToken = job.LeaseToken
			_, err = s.Update(ctx, &update, true)
			if finish == "ready" {
				require.ErrorIs(t, err, hosting.ErrUnauthorized)
			} else {
				require.NoError(t, err)
			}
			*allowed = true
			status, err := s.Status(ctx, owner, false)
			require.NoError(t, err)
			require.Equal(t, &hosting.SandboxView{Status: "sleeping"}, status.Sandbox)
		})
	}
}
