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

func monthlyPoolState(t *testing.T, store *storage.MemoryStore) hosting.State {
	t.Helper()
	var result hosting.State
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		result = *state
		return nil
	}))
	return result
}

func TestMonthlyPlatformPoolUsesUTCAndCannotRefreshTwiceAfterRestart(t *testing.T) {
	s, store, owner, now := monthlyFixture(t)
	s.Config.GlobalMicrousd = 5000
	*now = time.Date(2026, 11, 30, 23, 30, 0, 0, time.FixedZone("fixture-west", -8*3600))
	status, err := s.Status(t.Context(), owner, true)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), status.PoolPeriodStart)
	require.Equal(t, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), status.PoolResetsAt)
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		state.SpentMicrousd = 5000
		return nil
	}))
	restarted := hosting.New(&s.Config, store, s.Now, s.ValidateBinding)
	status, err = restarted.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.False(t, status.PoolAvailable)
	require.Zero(t, monthlyPoolState(t, store).PoolSpentBaselineMicrousd)
	*now = time.Date(2027, 2, 15, 12, 0, 0, 0, time.UTC)
	status, err = restarted.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.True(t, status.PoolAvailable)
	require.Equal(t, time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC), status.PoolPeriodStart)
	require.Equal(t, int64(5000), monthlyPoolState(t, store).PoolSpentBaselineMicrousd)
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		state.SpentMicrousd += 1000
		return nil
	}))
	restarted.Config.GlobalMicrousd = 1000
	for _, shifted := range []time.Time{
		time.Date(2027, 1, 31, 23, 59, 0, 0, time.UTC),
		time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2027, 2, 28, 23, 59, 59, 0, time.UTC),
	} {
		*now = shifted
		status, err = restarted.Status(t.Context(), owner, false)
		require.NoError(t, err)
		require.False(t, status.PoolAvailable)
		require.Equal(t, time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC), status.PoolPeriodStart)
		require.Equal(t, int64(5000), monthlyPoolState(t, store).PoolSpentBaselineMicrousd)
	}
	*now = time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	status, err = restarted.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.True(t, status.PoolAvailable)
	require.Equal(t, int64(6000), monthlyPoolState(t, store).PoolSpentBaselineMicrousd)
	assertPoolSpend(t, store, 6000, 0)
	job := poolJob(t, restarted, owner)
	_, err = restarted.Reserve(t.Context(), job.ModelToken, "no-missed-month-budget", 201)
	require.ErrorIs(t, err, hosting.ErrPlatformBudget)
	r, err := restarted.Reserve(t.Context(), job.ModelToken, "single-month-budget-only", 200)
	require.NoError(t, err)
	require.Equal(t, int64(1000), r.Microusd)
}

func TestMonthlyPlatformPoolAdoptsLegacySpendAndZeroStaysPaused(t *testing.T) {
	s, store, owner, now := monthlyFixture(t)
	s.Config.GlobalMicrousd = 5000
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		state.SpentMicrousd = 5000
		return nil
	}))
	for range 2 {
		s = hosting.New(&s.Config, store, s.Now, s.ValidateBinding)
		status, err := s.Status(t.Context(), owner, true)
		require.NoError(t, err)
		require.False(t, status.PoolAvailable)
		require.Zero(t, monthlyPoolState(t, store).PoolSpentBaselineMicrousd)
	}
	s.Config.GlobalMicrousd = 0
	for _, month := range []time.Month{time.December, time.January, time.March} {
		year := 2027
		if month == time.December {
			year = 2026
		}
		*now = time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
		status, err := s.Status(t.Context(), owner, false)
		require.NoError(t, err)
		require.True(t, status.Enabled)
		require.False(t, status.PoolAvailable)
		require.Equal(t, int64(5000), monthlyPoolState(t, store).PoolSpentBaselineMicrousd)
	}
	assertPoolSpend(t, store, 5000, 0)
}

func TestMonthlyPlatformPoolDefersAdmissionUntilOldRequestsFinish(t *testing.T) {
	for _, finish := range []string{"known", "prestart_known", "unknown", "cancel", "started_orphan", "pending_orphan"} {
		t.Run(finish, func(t *testing.T) {
			s, store, owner, now := monthlyFixture(t)
			s.Config.GlobalMicrousd = 10000
			*now = time.Date(2026, 11, 30, 23, 59, 30, 0, time.UTC)
			job := poolJob(t, s, owner)
			other, err := store.EnsureUser(
				t.Context(),
				"next-month@example.test",
				"Fixture",
				"user",
			)
			require.NoError(t, err)
			otherJob := monthlyPoolJob(t, s, other.UserID, "host-b")
			r, err := s.ReserveModerated(t.Context(), job.ModelToken, "old-platform-period", 1000)
			require.NoError(t, err)
			started := finish == "known" || finish == "unknown" || finish == "started_orphan"
			if started || finish == "prestart_known" {
				require.NoError(t, s.RecordModeration(t.Context(), r.ID,
					&hosting.ModerationDecision{Verdict: "allowed"}))
			}
			if started {
				require.NoError(t, s.BeginGeneration(t.Context(), r.ID))
			}
			*now = now.Add(45 * time.Second)
			status, err := s.Status(t.Context(), other.UserID, false)
			require.NoError(t, err)
			require.False(t, status.PoolAvailable)
			require.Equal(t, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), status.PoolPeriodStart)
			require.Equal(
				t,
				time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
				status.Starter.Quota.PeriodStart,
			)
			_, err = s.Reserve(t.Context(), otherJob.ModelToken, "no-early-monthly-admission", 1000)
			require.ErrorIs(t, err, hosting.ErrPlatformBudget)
			_, err = s.Wake(t.Context(), other.UserID)
			require.ErrorIs(t, err, hosting.ErrPlatformBudget)
			spent := finishMonthlyPoolRequest(t, s, r, now, finish)
			status, err = s.Status(t.Context(), other.UserID, false)
			require.NoError(t, err)
			require.True(t, status.PoolAvailable)
			require.Equal(t, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), status.PoolPeriodStart)
			require.Equal(t, spent, monthlyPoolState(t, store).PoolSpentBaselineMicrousd)
			assertPoolSpend(t, store, spent, 0)
			late := s.Settle(t.Context(), r.ID, 100, 20, nil, "late-duplicate", true)
			if finish == "cancel" || finish == "pending_orphan" {
				require.ErrorIs(t, late, hosting.ErrConflict)
			} else {
				require.NoError(t, late)
			}
			assertPoolSpend(t, store, spent, 0)
			otherJob = monthlyPoolJob(t, s, other.UserID, "host-b")
			next, err := s.ReserveModerated(
				t.Context(),
				otherJob.ModelToken,
				"funded-current-month",
				1000,
			)
			require.NoError(t, err)
			require.NoError(t, s.RecordModeration(t.Context(), next.ID,
				&hosting.ModerationDecision{Verdict: "allowed"}))
			require.NoError(t, s.BeginGeneration(t.Context(), next.ID))
		})
	}
}

func monthlyPoolJob(t *testing.T, s *hosting.Service, owner, host string) *hosting.Job {
	t.Helper()
	_, err := s.Wake(t.Context(), owner)
	require.NoError(t, err)
	job, err := s.Claim(t.Context(), &hosting.Claim{HostID: host, Capacity: 1})
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, owner, job.OwnerUserID)
	return job
}

func finishMonthlyPoolRequest(
	t *testing.T,
	s *hosting.Service,
	r *hosting.Reservation,
	now *time.Time,
	finish string,
) int64 {
	t.Helper()
	switch finish {
	case "known", "prestart_known":
		if finish == "prestart_known" {
			require.NoError(t, s.BeginGeneration(t.Context(), r.ID))
		}
		require.NoError(t, s.Settle(t.Context(), r.ID, 100, 20, nil, "fixture", true))
		return 200
	case "unknown":
		require.NoError(t, s.Settle(t.Context(), r.ID, 0, 0, nil, "fixture", false))
		return r.Microusd
	case "cancel":
		require.NoError(t, s.CancelBeforeGeneration(t.Context(), r.ID))
	case "started_orphan", "pending_orphan":
		*now = r.CreatedAt.Add(3*time.Minute + time.Second)
		if finish == "started_orphan" {
			return r.Microusd
		}
	}
	return 0
}

func TestMonthlyPlatformPoolLetsOldZeroPriceHoldFinishBeforeRollover(t *testing.T) {
	s, store, owner, now, policies := poolFixture(t)
	*now = time.Date(2026, 11, 30, 23, 59, 30, 0, time.UTC)
	job := poolJob(t, s, owner)
	r := poolReserve(t, s, job, "old-zero-price-period", &policies[1])
	require.NoError(t, s.RecordModeration(
		t.Context(),
		r.ID,
		&hosting.ModerationDecision{
			Verdict:       "skipped_by_policy",
			PolicyVersion: policies[1].PolicyVersion,
		},
	))
	*now = now.Add(45 * time.Second)
	status, err := s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.False(t, status.PoolAvailable)
	require.NoError(t, s.BeginGeneration(t.Context(), r.ID))
	require.NoError(t, s.Settle(t.Context(), r.ID, 10, 2, nil, "fixture", true))
	status, err = s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.True(t, status.PoolAvailable)
	require.Equal(t, time.December, status.PoolPeriodStart.Month())
	assertPoolSpend(t, store, 0, 0)
}

func TestConcurrentMonthlyPoolRolloverCannotOverspendTheNewMonth(t *testing.T) {
	s, store, owner, now, policies := poolFixture(t)
	s.Config.GlobalMicrousd = 2000
	*now = time.Date(2026, 11, 30, 23, 59, 30, 0, time.UTC)
	owners := []string{owner}
	for index := range 7 {
		user, err := store.EnsureUser(
			t.Context(),
			fmt.Sprintf("monthly-pool-%d@example.test", index),
			"Fixture",
			"user",
		)
		require.NoError(t, err)
		owners = append(owners, user.UserID)
	}
	var jobs []*hosting.Job
	for index, user := range owners {
		jobs = append(jobs, monthlyPoolJob(t, s, user, fmt.Sprintf("monthly-host-%d", index)))
	}
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		state.SpentMicrousd = 2000
		return nil
	}))
	*now = now.Add(45 * time.Second)
	var wg sync.WaitGroup
	var lock sync.Mutex
	var accepted []*hosting.Reservation
	rejected := 0
	for index, job := range jobs {
		wg.Go(func() {
			worker := hosting.New(&s.Config, store, s.Now, s.ValidateBinding)
			r, err := worker.ReserveForPolicy(
				t.Context(),
				job.ModelToken,
				fmt.Sprintf("monthly-last-pool-%d", index),
				1000,
				policies[0].ID,
				policies[0].MaxOutputTokens,
				&policies[0],
			)
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
	state := monthlyPoolState(t, store)
	require.Equal(t, int64(2000), state.PoolSpentBaselineMicrousd)
	require.Equal(t, time.December, state.PoolPeriodStart.Month())
	assertPoolSpend(t, store, 2000, 2000)
	r := accepted[0]
	require.NoError(t, s.RecordModeration(t.Context(), r.ID,
		&hosting.ModerationDecision{Verdict: "allowed", PolicyVersion: policies[0].PolicyVersion}))
	require.NoError(t, s.BeginGeneration(t.Context(), r.ID))
	require.NoError(t, s.Settle(t.Context(), r.ID, 0, 1000, nil, "fixture", true))
	assertPoolSpend(t, store, 4000, 0)
	restarted := hosting.New(&s.Config, store, s.Now, s.ValidateBinding)
	status, err := restarted.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.False(t, status.PoolAvailable)
	require.Equal(t, int64(2000), monthlyPoolState(t, store).PoolSpentBaselineMicrousd)
}
