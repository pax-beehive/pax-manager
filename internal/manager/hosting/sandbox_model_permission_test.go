package hosting_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

func claimModelPermission(t *testing.T, s *hosting.Service) *hosting.Job {
	t.Helper()
	job, err := s.ClaimSandboxModel(t.Context(), &hosting.Claim{
		Kind: hosting.SandboxModelKind, HostID: "sandbox-host",
		Capacity: 1, SupportedHarnesses: []string{"codex", "pi"},
	})
	require.NoError(t, err)
	return job
}

func sleepModelSandbox(t *testing.T, s *hosting.Service, parent hosting.Update) {
	t.Helper()
	parent.State, parent.NodeID = "sleeping", ""
	_, err := s.Sandbox.Update(t.Context(), &parent, true)
	require.NoError(t, err)
}

func resumeModelSandbox(t *testing.T, s *hosting.Service, owner string) hosting.Update {
	t.Helper()
	ctx := t.Context()
	_, err := s.Sandbox.Status(ctx, owner, true)
	require.NoError(t, err)
	job, err := s.Sandbox.Claim(ctx, &hosting.Claim{
		Kind: hosting.SandboxKind, HostID: "sandbox-host", Capacity: 1,
	})
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, "sandbox-node", job.NodeID)
	parent := modelSandboxReady(job)
	_, err = s.Sandbox.Update(ctx, &parent, true)
	require.NoError(t, err)
	return parent
}

func TestSandboxModelPermissionSurvivesSandboxAndManagerRestart(t *testing.T) {
	ctx := t.Context()
	s, store, owner, _, policies := poolFixture(t)
	parent, _ := connectModelSandbox(t, s, store, owner)
	job := wakeSandboxModel(t, s, owner, "sandbox-codex")
	require.Contains(t, job.ModelToken, "sbd_")
	ready := modelSandboxReady(job)
	_, err := s.UpdateSandboxModel(ctx, &ready, true)
	require.NoError(t, err)
	sleepModelSandbox(t, s, parent)

	restarted := hosting.New(&s.Config, store, s.Now, s.ValidateBinding)
	restarted.Sandbox = s.Sandbox
	restarted.ModelAgentHarness = s.ModelAgentHarness
	_, err = restarted.RuntimePolicies(ctx, job.ModelToken)
	require.NoError(t, err)
	request := &hosting.PrepareRequest{
		Token: job.ModelToken, RequestID: "while-sleeping", Model: policies[0].ID,
		InputTokens: 100, Protocol: "responses",
	}
	prepared, err := restarted.Prepare(ctx, request)
	require.NoError(t, err)
	require.NoError(t, restarted.Finish(ctx, &hosting.FinishRequest{
		ReservationID: prepared.ReservationID, Outcome: "completed", Started: true,
		Known: true, InputTokens: 10, OutputTokens: 20,
		Moderation: atomicDecision(policies[0], "allowed"),
	}))
	require.Nil(t, claimModelPermission(t, restarted))
	assertPoolSpend(t, store, 50, 0)
	require.Empty(t, readSandboxModelAccess(t, restarted, owner).SandboxModelAccess.Harnesses)
	newParent := resumeModelSandbox(t, restarted, owner)
	resumed := claimModelPermission(t, restarted)
	require.NotNil(t, resumed)
	require.Equal(t, newParent.Generation, resumed.SandboxGeneration)
	require.Greater(t, resumed.Generation, job.Generation)
	require.NotEqual(t, job.LeaseToken, resumed.LeaseToken)
	require.Equal(t, job.EnvironmentID, resumed.EnvironmentID)
	require.Equal(t, job.ModelToken, resumed.ModelToken)
	_, err = restarted.UpdateSandboxModel(ctx, &ready, true)
	require.ErrorIs(t, err, hosting.ErrConflict)
	request.RequestID = "before-reprovisioned"
	_, err = restarted.Prepare(ctx, request)
	require.NoError(t, err)
}

func TestSandboxModelPermissionRevokeFencesCompatAndReissueWhileScopeRemains(t *testing.T) {
	ctx := t.Context()
	s, store, owner, now, policies := poolFixture(t)
	parent, allowed := connectModelSandbox(t, s, store, owner)
	job := wakeSandboxModel(t, s, owner, "sandbox-codex")
	ready := modelSandboxReady(job)
	_, err := s.UpdateSandboxModel(ctx, &ready, true)
	require.NoError(t, err)
	pi := wakeSandboxModel(t, s, owner, "sandbox-pi")
	piReady := modelSandboxReady(pi)
	_, err = s.UpdateSandboxModel(ctx, &piReady, true)
	require.NoError(t, err)
	target := &hosting.WakeTarget{NodeID: job.NodeID, AgentID: job.AgentID}
	require.ErrorIs(t, s.RevokeSandboxModel(ctx, "foreign", target), hosting.ErrNotFound)
	require.ErrorIs(t, s.RevokeSandboxModel(ctx, owner, &hosting.WakeTarget{
		NodeID: "other-node", AgentID: job.AgentID,
	}), hosting.ErrNotFound)
	checkHarness := s.ModelAgentHarness
	s.ModelAgentHarness = func(_ context.Context, _, _, _ string) (string, error) {
		return "pi", nil
	}
	require.ErrorIs(t, s.Authorize(ctx, job.ModelToken), hosting.ErrUnauthorized)
	s.ModelAgentHarness = checkHarness

	sleepModelSandbox(t, s, parent)
	*now = now.Add(32 * time.Second)
	startBeforeRevoke, err := s.ReserveModerated(ctx, job.ModelToken, "first-compat", 100)
	require.NoError(t, err)
	require.NoError(t, s.RecordModeration(ctx, startBeforeRevoke.ID,
		atomicDecision(policies[0], "allowed")))
	require.NoError(t, s.BeginGeneration(ctx, startBeforeRevoke.ID))
	err = s.Settle(ctx, startBeforeRevoke.ID, 10, 20, nil, "", true)
	require.NoError(t, err)

	pending, err := s.ReserveModerated(ctx, job.ModelToken, "second-compat", 100)
	require.NoError(t, err)
	require.NoError(t, s.RecordModeration(ctx, pending.ID, atomicDecision(policies[0], "allowed")))
	*allowed = false
	require.NoError(t, s.RevokeSandboxModel(ctx, owner, target))
	*allowed = true
	require.NoError(t, s.RevokeSandboxModel(ctx, owner, target))
	view := readSandboxModelAccess(t, s, owner).SandboxModelAccess
	require.Equal(t, []hosting.SandboxModelAgent{
		{AgentID: job.AgentID, State: "available", Enabled: false},
		{AgentID: pi.AgentID, State: "available", Enabled: true},
	}, view.Agents)
	require.Empty(t, view.Harnesses)
	require.ErrorIs(t, s.BeginGeneration(ctx, pending.ID), hosting.ErrUnauthorized)
	require.NoError(t, s.CancelBeforeGeneration(ctx, pending.ID))
	require.ErrorIs(t, s.Authorize(ctx, job.ModelToken), hosting.ErrUnauthorized)
	_, err = s.RuntimePolicies(ctx, job.ModelToken)
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	_, err = s.Prepare(ctx, &hosting.PrepareRequest{
		Token: job.ModelToken, RequestID: "after-revoked", Model: policies[0].ID,
		InputTokens: 100, Protocol: "responses",
	})
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	require.NoError(t, s.Authorize(ctx, pi.ModelToken))

	// Old Manager binaries ignore new JSON fields; the opt-out survives those writes.
	require.NoError(t, store.WithHostingState(ctx, func(state *hosting.State) error {
		state.SandboxModelGrants[owner+"/"+job.AgentID].Permission = nil
		return nil
	}))

	resumeModelSandbox(t, s, owner)
	resumedPi := claimModelPermission(t, s)
	require.NotNil(t, resumedPi)
	require.Equal(t, pi.AgentID, resumedPi.AgentID)
	piReady = modelSandboxReady(resumedPi)
	_, err = s.UpdateSandboxModel(ctx, &piReady, true)
	require.NoError(t, err)
	require.Nil(t, claimModelPermission(t, s))
	reissued := wakeSandboxModel(t, s, owner, job.AgentID)
	require.NotEqual(t, job.ModelToken, reissued.ModelToken)
	require.NoError(t, s.Authorize(ctx, reissued.ModelToken))
	require.ErrorIs(t, s.Authorize(ctx, job.ModelToken), hosting.ErrUnauthorized)
}

func TestSandboxModelLegacyGrantStaysUsableAndMigratesOnNextProvision(t *testing.T) {
	ctx := t.Context()
	s, store, owner, _, _ := poolFixture(t)
	parent, _ := connectModelSandbox(t, s, store, owner)
	job := wakeSandboxModel(t, s, owner, "sandbox-pi")
	ready := modelSandboxReady(job)
	_, err := s.UpdateSandboxModel(ctx, &ready, true)
	require.NoError(t, err)
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(
		"sandbox_model/%s/%s/%s/%s:%d", owner, job.NodeID, job.AgentID, job.JobID, job.Generation,
	)))
	mac := hmac.New(sha256.New, []byte(s.Config.TokenSecret))
	_, _ = mac.Write([]byte(payload))
	legacy := "sbm_" + payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	require.NoError(t, store.WithHostingState(ctx, func(state *hosting.State) error {
		grant := state.SandboxModelGrants[owner+"/"+job.AgentID]
		grant.Permission = nil
		grant.Job.ModelToken = legacy
		return nil
	}))
	require.NoError(t, s.Authorize(ctx, legacy))
	require.Nil(t, claimModelPermission(t, s))
	sleepModelSandbox(t, s, parent)
	require.ErrorIs(t, s.Authorize(ctx, legacy), hosting.ErrUnauthorized)

	resumeModelSandbox(t, s, owner)
	checkHarness := s.ModelAgentHarness
	s.ModelAgentHarness = func(_ context.Context, _, _, _ string) (string, error) {
		return "", hosting.ErrUnauthorized
	}
	require.Nil(t, claimModelPermission(t, s))
	s.ModelAgentHarness = checkHarness
	migrated := claimModelPermission(t, s)
	require.NotNil(t, migrated)
	require.Equal(t, job.AgentID, migrated.AgentID)
	require.Contains(t, migrated.ModelToken, "sbd_")
	require.NoError(t, s.Authorize(ctx, migrated.ModelToken))
	require.ErrorIs(t, s.Authorize(ctx, legacy), hosting.ErrUnauthorized)
}
