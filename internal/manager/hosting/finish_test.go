package hosting_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

func TestAtomicFinishAcceptsUsageAfterRevocationPolicyChangeAndBudgetOvershoot(t *testing.T) {
	s, store, owner, now, policies := poolFixture(t)
	job := poolJob(t, s, owner)
	prepared := prepareAtomic(t, s, job, policies[0], "chat")
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		record := state.Environments[owner]
		record.Generation++
		record.Job.Active, record.Starter.AccessPaused = false, true
		record.Starter.Quota.GrantedMicrousd = 1
		state.Admin.PoolMicrousd = number(1)
		return nil
	}))
	s.Config.Models[0].Enabled = false
	*now = now.Add(4 * time.Minute)
	finish := &hosting.FinishRequest{
		ReservationID: prepared.ReservationID, Outcome: "completed", Started: true, Known: true,
		InputTokens: 1000, OutputTokens: 3000, Moderation: atomicDecision(policies[0], "allowed"),
	}
	require.NoError(t, s.Finish(t.Context(), finish))
	state := readAtomicState(t, store)
	require.EqualValues(t, 7000, state.SpentMicrousd)
	require.Zero(t, state.ReservedMicrousd)
	require.EqualValues(t, 7000, state.Environments[owner].Starter.Quota.SpentMicrousd)
	r := state.Requests[prepared.ReservationID]
	require.Equal(t, policies[0], *r.Policy)
	require.Equal(t, "settled", r.Status)
	require.True(t, r.UsageKnown)
	require.False(t, r.ReconciliationRequired)
	finish.InputTokens, finish.OutputTokens = 1, 1
	require.NoError(t, s.Finish(t.Context(), finish))
	require.EqualValues(t, 7000, readAtomicState(t, store).SpentMicrousd)
}

func TestAtomicFinishReleasesBlockedAndUnstartedRequests(t *testing.T) {
	for _, outcome := range []string{"blocked", "unavailable", "cancelled", "invalid", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			s, store, owner, _, policies := poolFixture(t)
			prepared := prepareAtomic(t, s, poolJob(t, s, owner), policies[0], "chat")
			finish := &hosting.FinishRequest{
				ReservationID: prepared.ReservationID,
				Outcome:       outcome,
			}
			if outcome == "blocked" || outcome == "unavailable" {
				finish.Moderation = atomicDecision(policies[0], outcome)
			}
			require.NoError(t, s.Finish(t.Context(), finish))
			require.NoError(t, s.Finish(t.Context(), finish))
			state := readAtomicState(t, store)
			require.Zero(t, state.SpentMicrousd)
			require.Zero(t, state.ReservedMicrousd)
			require.Zero(t, state.Environments[owner].Starter.Quota.ReservedTokens)
			r := state.Requests[prepared.ReservationID]
			require.False(t, r.GenerationStarted)
			require.False(t, r.ReconciliationRequired)
			require.Equal(t, outcome, r.Outcome)
		})
	}
}

func TestAtomicFinishUnknownCancellationIsConservative(t *testing.T) {
	s, store, owner, _, policies := poolFixture(t)
	prepared := prepareAtomic(t, s, poolJob(t, s, owner), policies[0], "messages")
	require.NoError(t, s.Finish(t.Context(), &hosting.FinishRequest{
		ReservationID: prepared.ReservationID, Outcome: "cancelled", Started: true,
		Moderation: atomicDecision(policies[0], "allowed"), ErrorCode: "request_cancelled",
	}))
	state := readAtomicState(t, store)
	r := state.Requests[prepared.ReservationID]
	require.Equal(t, "conservative", r.Status)
	require.Equal(t, "cancelled", r.Outcome)
	require.False(t, r.UsageKnown)
	require.True(t, r.ReconciliationRequired)
	require.Zero(t, r.InputTokens)
	require.Zero(t, r.OutputTokens)
	require.Equal(t, r.Microusd, r.ActualMicrousd)
	require.Equal(t, r.Tokens, r.ActualTokens)
	require.Equal(t, r.Microusd, state.SpentMicrousd)
	require.Zero(t, state.ReservedMicrousd)
}

func TestAtomicFinishRequiresMatchingModerationBeforeStartedUsage(t *testing.T) {
	for _, test := range []struct {
		name     string
		policy   int
		decision string
		started  bool
		outcome  string
	}{
		{"missing decision", 0, "", true, "completed"},
		{"blocked generation", 0, "blocked", true, "completed"},
		{"invented skipped policy", 0, "skipped_by_policy", true, "completed"},
		{"required skipped policy", 1, "allowed", true, "completed"},
		{"missing blocked verdict", 0, "", false, "blocked"},
		{"allowed blocked outcome", 0, "allowed", false, "blocked"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, store, owner, _, policies := poolFixture(t)
			policy := policies[test.policy]
			prepared := prepareAtomic(t, s, poolJob(t, s, owner), policy, "chat")
			finish := &hosting.FinishRequest{
				ReservationID: prepared.ReservationID, Started: test.started, Outcome: test.outcome,
			}
			if test.decision != "" {
				finish.Moderation = atomicDecision(policy, test.decision)
			}
			require.ErrorIs(t, s.Finish(t.Context(), finish), hosting.ErrConflict)
			require.Equal(
				t,
				"reserved",
				readAtomicState(t, store).Requests[prepared.ReservationID].Status,
			)
		})
	}
	s, store, owner, _, policies := poolFixture(t)
	prepared := prepareAtomic(t, s, poolJob(t, s, owner), policies[1], "chat")
	require.NoError(t, s.Finish(t.Context(), &hosting.FinishRequest{
		ReservationID: prepared.ReservationID, Started: true, Known: true, Outcome: "completed",
		InputTokens: 1, OutputTokens: 2,
		Moderation: atomicDecision(policies[1], "skipped_by_policy"),
	}))
	require.Equal(t, "settled", readAtomicState(t, store).Requests[prepared.ReservationID].Status)
}

func TestAtomicReservationCannotUseLegacyGenerationOrRefundActions(t *testing.T) {
	s, store, owner, _, policies := poolFixture(t)
	prepared := prepareAtomic(t, s, poolJob(t, s, owner), policies[0], "chat")
	id := prepared.ReservationID
	require.ErrorIs(
		t,
		s.RecordModeration(t.Context(), id, atomicDecision(policies[0], "allowed")),
		hosting.ErrConflict,
	)
	require.ErrorIs(t, s.BeginGeneration(t.Context(), id), hosting.ErrConflict)
	require.ErrorIs(t, s.CancelBeforeGeneration(t.Context(), id), hosting.ErrConflict)
	require.ErrorIs(t, s.Settle(t.Context(), id, 0, 0, nil, "", true), hosting.ErrConflict)
	require.Equal(t, "reserved", readAtomicState(t, store).Requests[id].Status)
}

func TestAtomicCrashConservesHoldAndLateFinishReconcilesOnce(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "never started", true: "actual usage"}[started],
			func(t *testing.T) {
				s, store, owner, now, policies := poolFixture(t)
				prepared := prepareAtomic(t, s, poolJob(t, s, owner), policies[0], "responses")
				*now = now.Add(4 * time.Minute)
				_, err := s.Status(t.Context(), owner, false)
				require.NoError(t, err)
				r := readAtomicState(t, store).Requests[prepared.ReservationID]
				require.Equal(t, "conservative", r.Status)
				require.True(t, r.ReconciliationRequired)
				require.True(t, r.FinishedAt.IsZero())
				require.Equal(t, r.Microusd, r.ActualMicrousd)
				finish := &hosting.FinishRequest{ReservationID: r.ID, Outcome: "cancelled"}
				if started {
					finish.Started, finish.Known, finish.Outcome = true, true, "completed"
					finish.InputTokens, finish.OutputTokens = 10, 20
					finish.Moderation = atomicDecision(policies[0], "allowed")
				}
				require.NoError(t, s.Finish(t.Context(), finish))
				require.NoError(t, s.Finish(t.Context(), finish))
				state := readAtomicState(t, store)
				expected := int64(0)
				if started {
					expected = 50
				}
				require.Equal(t, expected, state.SpentMicrousd)
				require.Equal(t, expected, state.Environments[owner].Starter.Quota.SpentMicrousd)
				require.Zero(t, state.ReservedMicrousd)
				require.False(t, state.Requests[r.ID].ReconciliationRequired)
			},
		)
	}
}

func TestAtomicLateFinishDoesNotChargeOrRefundNewMonth(t *testing.T) {
	s, store, owner, now, policies := poolFixture(t)
	prepared := prepareAtomic(t, s, poolJob(t, s, owner), policies[0], "chat")
	*now = now.Add(4 * time.Minute)
	_, err := s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	*now = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	_, err = s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	before := readAtomicState(t, store)
	require.Zero(t, before.Environments[owner].Starter.Quota.SpentMicrousd)
	require.Equal(t, before.SpentMicrousd, before.PoolSpentBaselineMicrousd)
	require.NoError(t, s.Finish(t.Context(), &hosting.FinishRequest{
		ReservationID: prepared.ReservationID, Started: true, Known: true, Outcome: "completed",
		InputTokens: 10, OutputTokens: 20, Moderation: atomicDecision(policies[0], "allowed"),
	}))
	after := readAtomicState(t, store)
	require.EqualValues(t, 50, after.SpentMicrousd)
	require.Equal(t, after.SpentMicrousd, after.PoolSpentBaselineMicrousd)
	require.Equal(
		t,
		before.Environments[owner].Starter.Quota,
		after.Environments[owner].Starter.Quota,
	)
}
