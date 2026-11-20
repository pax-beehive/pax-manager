package hosting_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func connectModelSandbox(
	t *testing.T,
	s *hosting.Service,
	store *storage.MemoryStore,
	owner string,
) (hosting.Update, *bool) {
	t.Helper()
	ctx := t.Context()
	allowed := true
	config := hosting.SandboxConfig{
		HostKey: strings.Repeat(
			"a",
			32,
		), HostID: "sandbox-host", CloudURL: "https://node.example.com",
	}
	s.Sandbox = hosting.NewSandbox(store, config, s.Now,
		func(_ context.Context, candidate string) bool { return allowed && candidate == owner },
		func(_ context.Context, candidate, node string) error {
			if candidate != owner || node != "sandbox-node" {
				return hosting.ErrUnauthorized
			}
			return nil
		},
	)
	s.ModelAgentHarness = func(_ context.Context, candidate, node, agent string) (string, error) {
		if candidate != owner || node != "sandbox-node" {
			return "", hosting.ErrUnauthorized
		}
		switch agent {
		case "sandbox-pi":
			return "pi", nil
		case "sandbox-codex":
			return "codex", nil
		default:
			return "", hosting.ErrUnauthorized
		}
	}
	_, err := s.Sandbox.Status(ctx, owner, true)
	require.NoError(t, err)
	job, _, err := s.Sandbox.Claim(ctx, &hosting.Claim{
		Kind: hosting.SandboxKind, HostID: config.HostID, Capacity: 1,
	})
	require.NoError(t, err)
	require.NotNil(t, job)
	update := hosting.Update{
		Kind: job.Kind, HostID: config.HostID, JobID: job.JobID,
		LeaseToken: job.LeaseToken, Generation: job.Generation, State: "ready", NodeID: "sandbox-node",
	}
	_, err = s.Sandbox.Update(ctx, &update, true)
	require.NoError(t, err)
	return update, &allowed
}

func readSandboxModelAccess(t *testing.T, s *hosting.Service, owner string) *hosting.Status {
	t.Helper()
	status, err := s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.NoError(t, s.IncludeSandboxModelAccess(t.Context(), owner, status))
	return status
}

func wakeSandboxModel(
	t *testing.T, s *hosting.Service, owner, agent string,
) *hosting.Job {
	t.Helper()
	claim := hosting.Claim{
		Kind: hosting.SandboxModelKind, HostID: "sandbox-host",
		Capacity: 1, SupportedHarnesses: []string{"codex", "pi"},
	}
	job, err := s.ClaimSandboxModel(t.Context(), &claim)
	require.NoError(t, err)
	require.Nil(t, job)
	matched, err := s.WakeSandboxModel(
		t.Context(), owner, &hosting.WakeTarget{NodeID: "sandbox-node", AgentID: agent},
	)
	require.True(t, matched)
	require.NoError(t, err)
	job, err = s.ClaimSandboxModel(t.Context(), &claim)
	require.NoError(t, err)
	require.NotNil(t, job)
	return job
}

func modelSandboxReady(job *hosting.Job) hosting.Update {
	return hosting.Update{
		Kind: job.Kind, HostID: "sandbox-host", JobID: job.JobID,
		LeaseToken: job.LeaseToken, Generation: job.Generation, State: "ready",
		NodeID: job.NodeID, AgentID: job.AgentID,
	}
}

func TestSandboxModelSharesStarterQuotaAndFencesNewSpendAfterParentStops(t *testing.T) {
	ctx := t.Context()
	s, store, owner, now, policies := poolFixture(t)
	_, err := s.Status(ctx, owner, true)
	require.NoError(t, err)
	legacy := poolJob(t, s, owner)
	legacyUsage := prepareAtomic(t, s, legacy, policies[0], "chat")
	require.NoError(t, s.Finish(ctx, &hosting.FinishRequest{
		ReservationID: legacyUsage.ReservationID, Outcome: "completed", Started: true,
		Known: true, InputTokens: 10, OutputTokens: 20,
		Moderation: atomicDecision(policies[0], "allowed"),
	}))
	parent, _ := connectModelSandbox(t, s, store, owner)
	require.Nil(t, readSandboxModelAccess(t, s, owner).SandboxModelAccess)
	job := wakeSandboxModel(t, s, owner, "sandbox-codex")
	require.Equal(t, "codex", job.Harness)
	require.NotEqual(t, legacy.EnvironmentID, job.EnvironmentID)
	require.True(t, strings.HasPrefix(job.EnvironmentID, "smodel-"))
	require.Equal(t, parent.Generation, job.SandboxGeneration)
	require.True(t, strings.HasPrefix(job.SandboxEnvironmentID, "snode-"))
	require.Equal(t, "free", job.Plan)
	require.Equal(t, "https://node.example.com", job.CloudURL)
	require.Equal(t, []hosting.SandboxModelAgent{{AgentID: job.AgentID, State: "starting"}},
		readSandboxModelAccess(t, s, owner).SandboxModelAccess.Agents)
	require.NoError(t, s.Authorize(ctx, job.ModelToken))
	_, err = s.RuntimePolicies(ctx, job.ModelToken)
	require.NoError(t, err)
	request := &hosting.PrepareRequest{
		Token: job.ModelToken, RequestID: "aws-responses", Model: policies[0].ID,
		InputTokens: 100, Protocol: "responses",
	}
	_, err = s.Prepare(ctx, request)
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	_, err = s.Reserve(ctx, job.ModelToken, "aws-before-ready", 1000)
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	ready := modelSandboxReady(job)
	ready.AgentID = "another-agent"
	_, err = s.UpdateSandboxModel(ctx, &ready, true)
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	ready.AgentID = job.AgentID
	_, err = s.UpdateSandboxModel(ctx, &ready, true)
	require.NoError(t, err)
	prepared, err := s.Prepare(ctx, request)
	require.NoError(t, err)
	status := readSandboxModelAccess(t, s, owner)
	require.Equal(t, []hosting.SandboxModelAgent{{AgentID: job.AgentID, State: "ready"}},
		status.SandboxModelAccess.Agents)
	require.False(t, status.EnrolledNode)
	require.EqualValues(t, 50, status.Starter.Quota.SpentMicrousd)
	require.Positive(t, status.Starter.Quota.ReservedMicrousd)
	second := wakeSandboxModel(t, s, owner, "sandbox-pi")
	require.NotEqual(t, job.EnvironmentID, second.EnvironmentID)
	require.NotEqual(t, job.ModelToken, second.ModelToken)
	require.Equal(t, job.SandboxEnvironmentID, second.SandboxEnvironmentID)
	secondReady := modelSandboxReady(second)
	_, err = s.UpdateSandboxModel(ctx, &secondReady, true)
	require.NoError(t, err)
	require.NoError(t, s.Authorize(ctx, job.ModelToken))
	require.NoError(t, s.Authorize(ctx, second.ModelToken))
	require.Equal(t, []hosting.SandboxModelAgent{
		{AgentID: job.AgentID, State: "ready"}, {AgentID: second.AgentID, State: "ready"},
	}, readSandboxModelAccess(t, s, owner).SandboxModelAccess.Agents)
	secondRequest := &hosting.PrepareRequest{
		Token: second.ModelToken, RequestID: "aws-chat", Model: policies[0].ID,
		InputTokens: 100, Protocol: "chat",
	}
	_, err = s.Prepare(ctx, secondRequest)
	require.ErrorIs(t, err, hosting.ErrConflict)
	require.NoError(t, s.Finish(ctx, &hosting.FinishRequest{
		ReservationID: prepared.ReservationID, Outcome: "completed", Started: true,
		Known: true, InputTokens: 10, OutputTokens: 20,
		Moderation: atomicDecision(policies[0], "allowed"),
	}))
	secondHold, err := s.Prepare(ctx, secondRequest)
	require.NoError(t, err)
	require.Positive(t, monthlyQuota(t, s, owner).ReservedMicrousd)

	*now = now.Add(80 * time.Second)
	parent.State, parent.NodeID = "", ""
	parentExpiry, err := s.Sandbox.Update(ctx, &parent, false)
	require.NoError(t, err)
	heartbeat := ready
	heartbeat.State, heartbeat.NodeID, heartbeat.AgentID = "", "", ""
	renewed, err := s.UpdateSandboxModel(ctx, &heartbeat, false)
	require.NoError(t, err)
	require.Equal(t, parentExpiry, renewed)
	parent.State = "sleeping"
	_, err = s.Sandbox.Update(ctx, &parent, true)
	require.NoError(t, err)
	require.ErrorIs(t, s.Authorize(ctx, job.ModelToken), hosting.ErrUnauthorized)
	require.ErrorIs(t, s.Authorize(ctx, second.ModelToken), hosting.ErrUnauthorized)
	_, err = s.UpdateSandboxModel(ctx, &heartbeat, false)
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	require.NoError(t, s.Authorize(ctx, legacy.ModelToken))
	require.NoError(t, s.Finish(ctx, &hosting.FinishRequest{
		ReservationID: secondHold.ReservationID, Outcome: "completed", Started: true,
		Known: true, InputTokens: 10, OutputTokens: 20,
		Moderation: atomicDecision(policies[0], "allowed"),
	}))
	assertPoolSpend(t, store, 150, 0)
	require.EqualValues(t, 150, monthlyQuota(t, s, owner).SpentMicrousd)
}

func TestSandboxModelRejectsFalseParentAndRevokedGate(t *testing.T) {
	ctx := t.Context()
	s, store, owner, _, _ := poolFixture(t)
	_, allowed := connectModelSandbox(t, s, store, owner)
	claim := hosting.Claim{
		Kind: hosting.SandboxModelKind, HostID: "sandbox-host", Capacity: 1,
		SupportedHarnesses: []string{"pi", "gemini"},
	}
	_, err := s.ClaimSandboxModel(ctx, &claim)
	require.ErrorIs(t, err, hosting.ErrConflict)
	require.Nil(t, readSandboxModelAccess(t, s, owner).SandboxModelAccess)
	claim.SupportedHarnesses = []string{"opencode", "pi"}
	_, err = s.ClaimSandboxModel(ctx, &claim)
	require.NoError(t, err)
	status := readSandboxModelAccess(t, s, owner)
	require.Nil(t, status.Starter)
	require.True(t, status.UserBudgetAvailable)
	require.Equal(t, []string{"opencode", "pi"}, status.SandboxModelAccess.Harnesses)
	matched, err := s.WakeSandboxModel(
		ctx, owner, &hosting.WakeTarget{NodeID: "ordinary-node", AgentID: "sandbox-pi"},
	)
	require.False(t, matched)
	require.NoError(t, err)
	matched, err = s.WakeSandboxModel(
		ctx, owner, &hosting.WakeTarget{NodeID: "sandbox-node", AgentID: "sandbox-codex"},
	)
	require.True(t, matched)
	require.ErrorIs(t, err, hosting.ErrDisabled)
	claim.Capacity, claim.SupportedHarnesses = 0, []string{}
	_, err = s.ClaimSandboxModel(ctx, &claim)
	require.NoError(t, err)
	require.Nil(t, readSandboxModelAccess(t, s, owner).SandboxModelAccess)
	job := wakeSandboxModel(t, s, owner, "sandbox-pi")
	ready := modelSandboxReady(job)
	_, err = s.UpdateSandboxModel(ctx, &ready, true)
	require.NoError(t, err)
	*allowed = false
	require.Nil(t, readSandboxModelAccess(t, s, owner).SandboxModelAccess)
	require.ErrorIs(t, s.Authorize(ctx, job.ModelToken), hosting.ErrUnauthorized)
	_, err = s.UpdateSandboxModel(ctx, &ready, true)
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	ready.State, ready.NodeID, ready.AgentID = "sleeping", "", ""
	_, err = s.UpdateSandboxModel(ctx, &ready, true)
	require.NoError(t, err)
	*allowed = true
	retry := wakeSandboxModel(t, s, owner, "sandbox-pi")
	require.NotEqual(t, job.ModelToken, retry.ModelToken)
	require.ErrorIs(t, s.Authorize(ctx, job.ModelToken), hosting.ErrUnauthorized)
}
