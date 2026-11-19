package hosting_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

func TestScopedClaimDoesNotFallThroughToAnotherOwnerOrHost(t *testing.T) {
	s, store, owner, _ := fixture(t)
	ctx := t.Context()
	other, err := store.EnsureUser(ctx, "other@example.com", "Other", "user")
	require.NoError(t, err)
	unscoped, err := s.Wake(ctx, other.UserID)
	require.NoError(t, err)
	enrolled, err := s.EnrollNode(ctx, owner, "node-owned", "host-a")
	require.NoError(t, err)
	target := &hosting.WakeTarget{NodeID: "node-owned", AgentID: "agent-owned"}
	_, err = s.Wake(ctx, owner)
	require.ErrorIs(t, err, hosting.ErrConflict)
	_, err = s.WakeForAgent(ctx, other.UserID, target)
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	_, err = s.WakeForAgent(ctx, owner, target)
	require.NoError(t, err)

	scope := &hosting.Claim{
		HostID: "wrong-host", Capacity: 1,
		OwnerUserID: &owner, EnvironmentID: &enrolled.Starter.EnvironmentID,
	}
	job, err := s.Claim(ctx, scope)
	require.NoError(t, err)
	require.Nil(t, job)
	scope.HostID = "host-a"
	scope.EnvironmentID = &unscoped.Starter.EnvironmentID
	job, err = s.Claim(ctx, scope)
	require.NoError(t, err)
	require.Nil(t, job)
	scope.EnvironmentID = nil
	_, err = s.Claim(ctx, scope)
	require.ErrorIs(t, err, hosting.ErrConflict)
	legacyScope := &hosting.Claim{
		HostID: "host-a", Capacity: 1,
		OwnerUserID: &other.UserID, EnvironmentID: &unscoped.Starter.EnvironmentID,
	}
	job, err = s.Claim(ctx, legacyScope)
	require.NoError(t, err)
	require.Nil(t, job)

	job, err = s.Claim(ctx, &hosting.Claim{HostID: "host-a", Capacity: 1})
	require.NoError(t, err)
	require.Equal(t, other.UserID, job.OwnerUserID)
	require.Empty(t, job.NodeID)
	require.NotEmpty(t, job.RegistrationToken)

	scope.EnvironmentID = &enrolled.Starter.EnvironmentID
	job, err = s.Claim(ctx, scope)
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, owner, job.OwnerUserID)
	require.Equal(t, target.AgentID, job.AgentID)
	require.Empty(t, job.RegistrationToken)
	repeated, err := s.Claim(ctx, scope)
	require.NoError(t, err)
	require.Equal(t, job, repeated)
}

func TestEnrolledClaimCannotReportOtherOwnedAgentReady(t *testing.T) {
	s, _, owner, now := fixture(t)
	s.ValidateBinding = func(context.Context, string, string, string) error { return nil }
	ctx := t.Context()
	enrolled, err := s.EnrollNode(ctx, owner, "node-owned", "host-a")
	require.NoError(t, err)
	target := &hosting.WakeTarget{NodeID: "node-owned", AgentID: "agent-owned"}
	_, err = s.WakeForAgent(ctx, owner, target)
	require.NoError(t, err)
	job, err := s.Claim(ctx, &hosting.Claim{
		HostID: "host-a", Capacity: 1,
		OwnerUserID: &owner, EnvironmentID: &enrolled.Starter.EnvironmentID,
	})
	require.NoError(t, err)
	require.NotNil(t, job)
	update := &hosting.Update{
		HostID: "host-a", JobID: job.JobID, Generation: job.Generation,
		LeaseToken: job.LeaseToken, State: "ready", NodeID: job.NodeID, AgentID: "other-owned-agent",
	}
	_, err = s.Update(ctx, update, true)
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	update.AgentID = job.AgentID
	_, err = s.Update(ctx, update, true)
	require.NoError(t, err)
	ready, err := s.Status(ctx, owner, false)
	require.NoError(t, err)
	require.True(t, ready.EnrolledNode)
	require.Equal(t, "ready", ready.Starter.State)
	require.Equal(t, target.AgentID, ready.Starter.AgentID)
	_, err = s.Reserve(ctx, job.ModelToken, "live-request", 1000)
	require.NoError(t, err)

	*now = now.Add(4 * time.Minute)
	_, err = s.Reserve(ctx, job.ModelToken, "expired-request", 1000)
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	sleeping, err := s.Status(ctx, owner, false)
	require.NoError(t, err)
	require.True(t, sleeping.EnrolledNode)
	require.Equal(t, "sleeping", sleeping.Starter.State)
	require.Equal(t, target.NodeID, sleeping.Starter.NodeID)
}

func TestEnrollmentRebindRejectsLiveNodeAndConcurrentReplacement(t *testing.T) {
	s, _, owner, _ := fixture(t)
	ctx := t.Context()
	_, err := s.EnrollNode(ctx, owner, "old-node", "old-host")
	require.NoError(t, err)
	checks := 0
	s.PreviousNodeRemoved = func(_ context.Context, checkedOwner, checkedNode string) (bool, error) {
		require.Equal(t, owner, checkedOwner)
		require.Equal(t, "old-node", checkedNode)
		checks++
		return false, nil
	}
	_, err = s.EnrollNode(ctx, owner, "pending-node", "pending-host")
	require.ErrorIs(t, err, hosting.ErrConflict)
	require.Equal(t, 1, checks)

	s.PreviousNodeRemoved = func(_ context.Context, _, _ string) (bool, error) {
		checks++
		if checks == 2 {
			_, err := s.EnrollNode(ctx, owner, "winner-node", "winner-host")
			require.NoError(t, err)
		}
		return true, nil
	}
	_, err = s.EnrollNode(ctx, owner, "pending-node", "pending-host")
	require.ErrorIs(t, err, hosting.ErrConflict)
	status, err := s.Status(ctx, owner, false)
	require.NoError(t, err)
	require.Equal(t, "winner-node", status.Starter.NodeID)
}
