package hosting_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

func TestUnlimitedMonthlyTokensPreserveAccountingAndMoneyLimits(t *testing.T) {
	for _, source := range []string{"config", "defaults", "user"} {
		for _, exhausted := range []string{"user", "pool"} {
			t.Run(source+"/"+exhausted, func(t *testing.T) {
				s, store, owner, now := fixture(t)
				s.Config.GrantMicrousd = 5_000_000
				s.Config.InputPrice, s.Config.OutputPrice = 5_000_000, 5_000_000
				switch source {
				case "config":
					s.Config.GrantTokens = 0
				case "defaults":
					adminUpdate(t, s, &hosting.AdminUpdate{
						Action: "defaults", MonthlyTokens: number(0),
					})
				case "user":
					_, err := s.Status(t.Context(), owner, true)
					require.NoError(t, err)
					adminUpdate(t, s, &hosting.AdminUpdate{
						Action: "user", TargetID: owner, MonthlyTokens: number(0),
					})
				}
				require.True(t, s.Enabled())
				var previousPoolSpend int64
				if exhausted == "pool" {
					previousPoolSpend = 95_000_000
					require.NoError(
						t,
						store.WithHostingState(t.Context(), func(state *hosting.State) error {
							state.SpentMicrousd = previousPoolSpend
							return nil
						}),
					)
				}
				job := poolJob(t, s, owner)
				r, err := s.ReserveModerated(
					t.Context(),
					job.ModelToken,
					"unlimited-token-request",
					1_000_000,
				)
				require.NoError(t, err)
				quota := monthlyQuota(t, s, owner)
				require.Zero(t, quota.GrantedTokens)
				require.Zero(t, quota.RemainingTokens)
				require.Equal(t, int64(1_000_000), quota.ReservedTokens)
				require.Equal(t, int64(5_000_000), quota.ReservedMicrousd)
				require.NoError(t, s.RecordModeration(t.Context(), r.ID,
					&hosting.ModerationDecision{Verdict: "allowed"}))
				require.NoError(t, s.BeginGeneration(t.Context(), r.ID))
				require.NoError(t, s.Settle(t.Context(), r.ID, 1_000_000, 0, nil, "fixture", true))
				restarted := hosting.New(&s.Config, store, s.Now, s.ValidateBinding)
				quota = monthlyQuota(t, restarted, owner)
				require.Zero(t, quota.GrantedTokens)
				require.Zero(t, quota.RemainingTokens)
				require.Zero(t, quota.ReservedTokens)
				require.Equal(t, int64(1_000_000), quota.SpentTokens)
				require.Equal(t, int64(5_000_000), quota.SpentMicrousd)
				require.Zero(t, quota.RemainingMicrousd)
				want := hosting.ErrBudget
				if exhausted == "pool" {
					want = hosting.ErrPlatformBudget
				}
				_, err = restarted.Reserve(t.Context(), job.ModelToken, "unlimited-but-no-money", 1)
				require.ErrorIs(t, err, want)
				_, err = restarted.Wake(t.Context(), owner)
				require.ErrorIs(t, err, want)
				require.NoError(
					t,
					store.WithHostingState(t.Context(), func(state *hosting.State) error {
						require.Len(t, state.Requests, 1)
						return nil
					}),
				)
				assertPoolSpend(t, store, previousPoolSpend+5_000_000, 0)
				*now = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
				quota = monthlyQuota(t, restarted, owner)
				require.Zero(t, quota.GrantedTokens)
				require.Zero(t, quota.SpentTokens)
				require.Equal(t, int64(5_000_000), quota.RemainingMicrousd)
				assertPoolSpend(t, store, previousPoolSpend+5_000_000, 0)
			})
		}
	}
}

func TestMonthlyTokenLimitChangesAreRecheckedBeforeGeneration(t *testing.T) {
	for _, unlimited := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "add-limit", true: "remove-limit"}[unlimited],
			func(t *testing.T) {
				s, _, owner, _ := fixture(t)
				if !unlimited {
					s.Config.GrantTokens = 0
				}
				job := poolJob(t, s, owner)
				r, err := s.ReserveModerated(
					t.Context(),
					job.ModelToken,
					"token-limit-change",
					1000,
				)
				require.NoError(t, err)
				require.NoError(t, s.RecordModeration(t.Context(), r.ID,
					&hosting.ModerationDecision{Verdict: "allowed"}))
				limit := int64(999)
				if unlimited {
					limit = 0
				}
				adminUpdate(t, s, &hosting.AdminUpdate{
					Action: "user", TargetID: owner, MonthlyTokens: number(limit),
				})
				err = s.BeginGeneration(t.Context(), r.ID)
				if !unlimited {
					require.ErrorIs(t, err, hosting.ErrBudget)
					require.NoError(t, s.CancelBeforeGeneration(t.Context(), r.ID))
					return
				}
				require.NoError(t, err)
				adminUpdate(t, s, &hosting.AdminUpdate{
					Action: "user", TargetID: owner, MonthlyTokens: number(1),
				})
				require.NoError(t, s.Settle(t.Context(), r.ID, 100, 20, nil, "fixture", true))
				quota := monthlyQuota(t, s, owner)
				require.Equal(t, int64(120), quota.SpentTokens)
				require.Equal(t, int64(200), quota.SpentMicrousd)
				require.Zero(t, quota.ReservedTokens)
				_, err = s.Reserve(t.Context(), job.ModelToken, "positive-limit-exhausted", 1)
				require.ErrorIs(t, err, hosting.ErrBudget)
			},
		)
	}
}
