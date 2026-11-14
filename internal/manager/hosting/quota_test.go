package hosting_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func monthlyFixture(t *testing.T) (*hosting.Service, *storage.MemoryStore, string, *time.Time) {
	t.Helper()
	s, store, owner, now := fixture(t)
	s.Config.GrantMicrousd, s.Config.GrantTokens = 5_000_000, 1_000_000
	return s, store, owner, now
}

func monthlyQuota(t *testing.T, s *hosting.Service, owner string) hosting.Quota {
	t.Helper()
	status, err := s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.NotNil(t, status.Starter)
	return status.Starter.Quota
}

func assertPoolSpend(t *testing.T, store *storage.MemoryStore, spent, reserved int64) {
	t.Helper()
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		require.Equal(t, spent, state.SpentMicrousd)
		require.Equal(t, reserved, state.ReservedMicrousd)
		return nil
	}))
}

func TestMonthlyQuotaUsesUTCAndExpiresUnusedCreditOnce(t *testing.T) {
	s, store, owner, now := monthlyFixture(t)
	*now = time.Date(2026, 11, 30, 23, 30, 0, 0, time.FixedZone("fixture-west", -8*3600))
	job := poolJob(t, s, owner)
	quota := monthlyQuota(t, s, owner)
	require.Equal(t, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), quota.PeriodStart)
	require.Equal(t, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), quota.ResetsAt)
	r, err := s.Reserve(t.Context(), job.ModelToken, "monthly-december-spend", 50000)
	require.NoError(t, err)
	require.NoError(t, s.Settle(t.Context(), r.ID, 10000, 1000, nil, "fixture", true))
	for range 3 {
		_, err = s.Status(t.Context(), owner, true)
		require.NoError(t, err)
		quota = monthlyQuota(t, s, owner)
		require.Equal(t, int64(5_000_000), quota.GrantedMicrousd)
		require.Equal(t, int64(15000), quota.SpentMicrousd)
		require.Equal(t, int64(4_985_000), quota.RemainingMicrousd)
	}
	*now = time.Date(2027, 1, 1, 0, 0, 1, 0, time.UTC)
	quota = monthlyQuota(t, s, owner)
	require.Equal(t, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), quota.PeriodStart)
	require.Equal(t, int64(5_000_000), quota.GrantedMicrousd)
	require.Equal(t, int64(5_000_000), quota.RemainingMicrousd)
	require.Zero(t, quota.SpentMicrousd)
	require.Zero(t, quota.SpentTokens)
	require.Equal(t, int64(1_000_000), quota.RemainingTokens)
	assertPoolSpend(t, store, 15000, 0)
}

func TestMonthlyQuotaSkipsMissedGrantsAndClockRollbackCannotRefill(t *testing.T) {
	s, store, owner, now := monthlyFixture(t)
	_, err := s.Status(t.Context(), owner, true)
	require.NoError(t, err)
	*now = time.Date(2027, 3, 15, 12, 0, 0, 0, time.UTC)
	quota := monthlyQuota(t, s, owner)
	require.Equal(t, int64(5_000_000), quota.GrantedMicrousd)
	require.Equal(t, time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC), quota.PeriodStart)
	job := poolJob(t, s, owner)
	r, err := s.Reserve(t.Context(), job.ModelToken, "monthly-march-spend", 1000)
	require.NoError(t, err)
	require.NoError(t, s.Settle(t.Context(), r.ID, 100, 20, nil, "fixture", true))
	for _, shifted := range []time.Time{
		time.Date(2027, 2, 28, 23, 59, 0, 0, time.UTC),
		time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2027, 3, 31, 23, 59, 59, 0, time.UTC),
	} {
		*now = shifted
		quota = monthlyQuota(t, s, owner)
		require.Equal(t, time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC), quota.PeriodStart)
		require.Equal(t, int64(200), quota.SpentMicrousd)
		require.Equal(t, int64(4_999_800), quota.RemainingMicrousd)
	}
	assertPoolSpend(t, store, 200, 0)
}

func TestMonthlyQuotaAdoptsLegacyBalanceWithoutNewGrant(t *testing.T) {
	s, store, owner, now := monthlyFixture(t)
	_, err := s.Status(t.Context(), owner, true)
	require.NoError(t, err)
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		state.Environments[owner].Starter.Quota = hosting.Quota{
			GrantedMicrousd: 1_000_000, SpentMicrousd: 400_000,
			GrantedTokens: 200_000, SpentTokens: 8000,
		}
		state.SpentMicrousd = 400_000
		return nil
	}))
	restarted := hosting.New(&s.Config, store, s.Now, s.ValidateBinding)
	quota := monthlyQuota(t, restarted, owner)
	require.Equal(t, int64(1_000_000), quota.GrantedMicrousd)
	require.Equal(t, int64(600_000), quota.RemainingMicrousd)
	require.Equal(t, int64(192_000), quota.RemainingTokens)
	require.Equal(t, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), quota.PeriodStart)
	quota = monthlyQuota(t, hosting.New(&s.Config, store, s.Now, s.ValidateBinding), owner)
	require.Equal(t, int64(600_000), quota.RemainingMicrousd)
	*now = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	quota = monthlyQuota(t, restarted, owner)
	require.Equal(t, int64(5_000_000), quota.RemainingMicrousd)
	assertPoolSpend(t, store, 400_000, 0)
}

func TestMonthlyQuotaDefersRolloverUntilPreviousRequestFinishes(t *testing.T) {
	for _, finish := range []string{"known", "cancel", "started_orphan", "pending_orphan"} {
		t.Run(finish, func(t *testing.T) {
			s, store, owner, now := monthlyFixture(t)
			*now = time.Date(2026, 11, 30, 23, 59, 30, 0, time.UTC)
			job := poolJob(t, s, owner)
			r, err := s.ReserveModerated(
				t.Context(),
				job.ModelToken,
				"monthly-cross-boundary",
				1000,
			)
			require.NoError(t, err)
			if finish == "known" || finish == "started_orphan" {
				require.NoError(t, s.RecordModeration(t.Context(), r.ID,
					&hosting.ModerationDecision{Verdict: "allowed"}))
				require.NoError(t, s.BeginGeneration(t.Context(), r.ID))
			}
			*now = now.Add(45 * time.Second)
			quota := monthlyQuota(t, s, owner)
			require.Equal(t, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), quota.PeriodStart)
			require.Equal(t, r.Microusd, quota.ReservedMicrousd)
			assertPoolSpend(t, store, 0, r.Microusd)
			_, err = s.Reserve(t.Context(), job.ModelToken, "monthly-no-second-hold", 1000)
			require.ErrorIs(t, err, hosting.ErrPlatformBudget)
			var spent int64
			switch finish {
			case "known":
				spent = 200
				require.NoError(t, s.Settle(t.Context(), r.ID, 100, 20, nil, "fixture", true))
			case "cancel":
				require.NoError(t, s.CancelBeforeGeneration(t.Context(), r.ID))
			case "started_orphan", "pending_orphan":
				*now = r.CreatedAt.Add(3*time.Minute + time.Second)
				if finish == "started_orphan" {
					spent = r.Microusd
				}
			}
			if finish == "known" || finish == "cancel" {
				require.NoError(
					t,
					store.WithHostingState(t.Context(), func(state *hosting.State) error {
						old := state.Environments[owner].Starter.Quota
						require.Equal(
							t,
							time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
							old.PeriodStart,
						)
						require.Equal(t, spent, old.SpentMicrousd)
						require.Zero(t, old.ReservedMicrousd)
						return nil
					}),
				)
			}
			quota = monthlyQuota(t, s, owner)
			require.Equal(t, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), quota.PeriodStart)
			require.Equal(t, int64(5_000_000), quota.RemainingMicrousd)
			require.Zero(t, quota.SpentMicrousd)
			require.Zero(t, quota.ReservedMicrousd)
			assertPoolSpend(t, store, spent, 0)
			require.NoError(
				t,
				store.WithHostingState(t.Context(), func(state *hosting.State) error {
					stored := state.Requests[r.ID]
					require.Equal(t, spent, stored.ActualMicrousd)
					require.NotEqual(t, "reserved", stored.Status)
					return nil
				}),
			)
		})
	}
}

func TestMonthlyPoolRefreshesWithoutResettingLifetimeSpend(t *testing.T) {
	s, store, owner, now := monthlyFixture(t)
	s.Config.GlobalMicrousd = 5000
	job := poolJob(t, s, owner)
	r, err := s.Reserve(t.Context(), job.ModelToken, "consume-platform-pool", 1000)
	require.NoError(t, err)
	require.Equal(t, int64(5000), r.Microusd)
	require.NoError(t, s.Settle(t.Context(), r.ID, 0, 1000, nil, "fixture", true))
	assertPoolSpend(t, store, 5000, 0)
	*now = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	restarted := hosting.New(&s.Config, store, s.Now, s.ValidateBinding)
	status, err := restarted.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.True(t, status.Enabled)
	require.True(t, status.PoolAvailable)
	require.Equal(t, int64(5_000_000), status.Starter.Quota.RemainingMicrousd)
	assertPoolSpend(t, store, 5000, 0)
	job = poolJob(t, restarted, owner)
	r, err = restarted.Reserve(t.Context(), job.ModelToken, "consume-next-month-pool", 1000)
	require.NoError(t, err)
	require.NoError(t, restarted.Settle(t.Context(), r.ID, 0, 1000, nil, "fixture", true))
	assertPoolSpend(t, store, 10000, 0)
	restarted.Config.GlobalMicrousd = 6000
	job = poolJob(t, restarted, owner)
	_, err = restarted.Reserve(t.Context(), job.ModelToken, "exceed-manual-increase", 201)
	require.ErrorIs(t, err, hosting.ErrPlatformBudget)
	r, err = restarted.Reserve(t.Context(), job.ModelToken, "consume-manual-increase", 200)
	require.NoError(t, err)
	require.Equal(t, int64(1000), r.Microusd)
	require.NoError(t, restarted.Settle(t.Context(), r.ID, 0, 200, nil, "fixture", true))
	assertPoolSpend(t, store, 11000, 0)
	restarted.Config.GlobalMicrousd = 4000
	status, err = restarted.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.False(t, status.PoolAvailable)
	_, err = restarted.Wake(t.Context(), owner)
	require.ErrorIs(t, err, hosting.ErrPlatformBudget)
}

func TestZeroPlatformPoolPausesPaidAndZeroPriceModels(t *testing.T) {
	s, store, owner, _, policies := poolFixture(t)
	job := poolJob(t, s, owner)
	s.Config.GlobalMicrousd = 0
	require.True(t, s.Enabled())
	status, err := s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.True(t, status.Enabled)
	require.False(t, status.PoolAvailable)
	for _, policy := range policies {
		_, err = s.ReserveForPolicy(t.Context(), job.ModelToken, "paused-"+policy.ID,
			1000, policy.ID, policy.MaxOutputTokens, &policy)
		require.ErrorIs(t, err, hosting.ErrPlatformBudget)
	}
	assertPoolSpend(t, store, 0, 0)
}

func TestOperatorPoolChangesAreRecheckedBeforeGeneration(t *testing.T) {
	for _, scenario := range []string{"paid_exact_hold", "paid_lowered", "paid_zero", "free_zero", "free_exhausted"} {
		t.Run(scenario, func(t *testing.T) {
			s, store, owner, _, policies := poolFixture(t)
			job := poolJob(t, s, owner)
			policy := policies[0]
			if scenario == "free_zero" || scenario == "free_exhausted" {
				policy = policies[1]
			}
			r := poolReserve(t, s, job, "operator-pool-change", &policy)
			decision := &hosting.ModerationDecision{
				Verdict:       "allowed",
				PolicyVersion: policy.PolicyVersion,
			}
			if policy.Moderation == "off" {
				decision.Verdict = "skipped_by_policy"
			}
			require.NoError(t, s.RecordModeration(t.Context(), r.ID, decision))
			var spent int64
			switch scenario {
			case "paid_exact_hold":
				s.Config.GlobalMicrousd = r.Microusd
			case "paid_lowered":
				s.Config.GlobalMicrousd = r.Microusd - 1
			case "paid_zero", "free_zero":
				s.Config.GlobalMicrousd = 0
			case "free_exhausted":
				spent = s.Config.GlobalMicrousd
				require.NoError(
					t,
					store.WithHostingState(t.Context(), func(state *hosting.State) error {
						state.SpentMicrousd = spent
						return nil
					}),
				)
			}
			err := s.BeginGeneration(t.Context(), r.ID)
			if scenario == "paid_exact_hold" {
				require.NoError(t, err)
				require.NoError(t, s.Settle(t.Context(), r.ID, 10, 5, nil, "fixture", true))
				assertPoolSpend(t, store, 20, 0)
				return
			}
			require.ErrorIs(t, err, hosting.ErrPlatformBudget)
			require.NoError(t, s.CancelBeforeGeneration(t.Context(), r.ID))
			assertPoolSpend(t, store, spent, 0)
			require.NoError(
				t,
				store.WithHostingState(t.Context(), func(state *hosting.State) error {
					require.False(t, state.Requests[r.ID].GenerationStarted)
					require.Zero(t, state.Requests[r.ID].ActualMicrousd)
					return nil
				}),
			)
		})
	}
}

func TestConcurrentUsersCannotOverspendLastPlatformPoolBalance(t *testing.T) {
	s, store, owner, _, policies := poolFixture(t)
	s.Config.GlobalMicrousd = 2000
	owners := []string{owner}
	for index := range 7 {
		user, err := store.EnsureUser(
			t.Context(),
			fmt.Sprintf("pool-user-%d@example.test", index),
			"Fixture",
			"user",
		)
		require.NoError(t, err)
		owners = append(owners, user.UserID)
	}
	var jobs []*hosting.Job
	for index, user := range owners {
		_, err := s.Wake(t.Context(), user)
		require.NoError(t, err)
		job, err := s.Claim(
			t.Context(),
			&hosting.Claim{HostID: fmt.Sprintf("host-%d", index), Capacity: 1},
		)
		require.NoError(t, err)
		require.Equal(t, user, job.OwnerUserID)
		jobs = append(jobs, job)
	}
	var wg sync.WaitGroup
	var lock sync.Mutex
	var accepted []*hosting.Reservation
	rejected := 0
	for index, job := range jobs {
		wg.Go(func() {
			worker := hosting.New(&s.Config, store, s.Now, s.ValidateBinding)
			r, err := worker.ReserveForPolicy(t.Context(), job.ModelToken,
				fmt.Sprintf("last-pool-%d", index), 1000,
				policies[0].ID, policies[0].MaxOutputTokens, &policies[0])
			lock.Lock()
			defer lock.Unlock()
			if err == nil {
				accepted = append(accepted, r)
			} else {
				require.ErrorIs(t, err, hosting.ErrPlatformBudget)
				rejected++
			}
		})
	}
	wg.Wait()
	require.Len(t, accepted, 1)
	require.Equal(t, len(jobs)-1, rejected)
	assertPoolSpend(t, store, 0, 2000)
	r := accepted[0]
	require.NoError(t, s.RecordModeration(t.Context(), r.ID,
		&hosting.ModerationDecision{Verdict: "allowed", PolicyVersion: policies[0].PolicyVersion}))
	require.NoError(t, s.BeginGeneration(t.Context(), r.ID))
	require.NoError(t, s.Settle(t.Context(), r.ID, 0, 1000, nil, "fixture", true))
	assertPoolSpend(t, store, 2000, 0)
	newUser, err := store.EnsureUser(t.Context(), "new-after-empty@example.test", "Fixture", "user")
	require.NoError(t, err)
	status, err := s.Status(t.Context(), newUser.UserID, true)
	require.NoError(t, err)
	require.False(t, status.PoolAvailable)
	assertPoolSpend(t, store, 2000, 0)
}
