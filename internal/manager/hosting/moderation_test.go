package hosting_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

func TestModerationRejectsWithoutChargingAndPersistsDecision(t *testing.T) {
	for _, verdict := range []string{"blocked", "unavailable"} {
		t.Run(verdict, func(t *testing.T) {
			s, store, owner, now := fixture(t)
			_, err := s.Wake(t.Context(), owner)
			require.NoError(t, err)
			job, err := s.Claim(t.Context(), &hosting.Claim{HostID: "host-a", Capacity: 1})
			require.NoError(t, err)
			r, err := s.ReserveModerated(t.Context(), job.ModelToken, "moderation-request", 1000)
			require.NoError(t, err)
			decision := &hosting.ModerationDecision{
				Verdict: verdict, Model: hosting.ModerationModel, InputCount: 2,
				Categories: []string{"violence"}, ProviderRequestID: "modr-test",
			}
			require.NoError(t, s.RecordModeration(t.Context(), r.ID, decision))
			decision.Categories[0] = "mutated"
			require.ErrorIs(t, s.BeginGeneration(t.Context(), r.ID), hosting.ErrConflict)
			require.NoError(t, s.CancelBeforeGeneration(t.Context(), r.ID))
			require.NoError(
				t,
				store.WithHostingState(t.Context(), func(state *hosting.State) error {
					stored := state.Requests[r.ID]
					require.Equal(t, "moderation_"+verdict, stored.Status)
					require.False(t, stored.GenerationStarted)
					require.Zero(t, stored.ActualMicrousd)
					require.Zero(t, stored.ActualTokens)
					require.Equal(t, hosting.ModerationPolicy, stored.Moderation.PolicyVersion)
					require.Equal(t, *now, stored.Moderation.CheckedAt)
					require.Equal(t, []string{"violence"}, stored.Moderation.Categories)
					require.Zero(t, state.ReservedMicrousd)
					require.Zero(t, state.SpentMicrousd)
					return nil
				}),
			)
			status, err := s.Status(t.Context(), owner, false)
			require.NoError(t, err)
			require.Equal(t, int64(1_000_000), status.Starter.Quota.RemainingMicrousd)
			require.Equal(t, int64(200000), status.Starter.Quota.RemainingTokens)
		})
	}
}

func TestModerationGenerationRequiresAllowedAuditAndLiveLease(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "expired"}[expire], func(t *testing.T) {
			s, store, owner, now := fixture(t)
			_, err := s.Wake(t.Context(), owner)
			require.NoError(t, err)
			job, err := s.Claim(t.Context(), &hosting.Claim{HostID: "host-a", Capacity: 1})
			require.NoError(t, err)
			r, err := s.ReserveModerated(t.Context(), job.ModelToken, "generation-request", 1000)
			require.NoError(t, err)
			require.ErrorIs(t, s.BeginGeneration(t.Context(), r.ID), hosting.ErrConflict)
			require.NoError(
				t,
				s.RecordModeration(
					t.Context(),
					r.ID,
					&hosting.ModerationDecision{Verdict: "allowed"},
				),
			)
			require.ErrorIs(t, s.RecordModeration(t.Context(), r.ID,
				&hosting.ModerationDecision{Verdict: "blocked"}), hosting.ErrConflict)
			if expire {
				*now = job.LeaseExpiresAt
				require.ErrorIs(t, s.BeginGeneration(t.Context(), r.ID), hosting.ErrUnauthorized)
				require.NoError(t, s.CancelBeforeGeneration(t.Context(), r.ID))
			} else {
				require.NoError(t, s.BeginGeneration(t.Context(), r.ID))
				require.ErrorIs(t, s.BeginGeneration(t.Context(), r.ID), hosting.ErrConflict)
				require.ErrorIs(t, s.CancelBeforeGeneration(t.Context(), r.ID), hosting.ErrConflict)
			}
			require.NoError(
				t,
				store.WithHostingState(t.Context(), func(state *hosting.State) error {
					require.Equal(t, !expire, state.Requests[r.ID].GenerationStarted)
					if expire {
						require.Zero(t, state.ReservedMicrousd)
						require.Zero(t, state.SpentMicrousd)
					}
					return nil
				}),
			)
		})
	}
}

func TestModerationOrphanReconciliationChargesOnlyStartedGeneration(t *testing.T) {
	for _, stage := range []string{"pending", "allowed", "started"} {
		t.Run(stage, func(t *testing.T) {
			s, store, owner, now := fixture(t)
			_, err := s.Wake(t.Context(), owner)
			require.NoError(t, err)
			job, err := s.Claim(t.Context(), &hosting.Claim{HostID: "host-a", Capacity: 1})
			require.NoError(t, err)
			r, err := s.ReserveModerated(t.Context(), job.ModelToken, "orphaned-request", 1000)
			require.NoError(t, err)
			if stage != "pending" {
				require.NoError(
					t,
					s.RecordModeration(
						t.Context(),
						r.ID,
						&hosting.ModerationDecision{Verdict: "allowed"},
					),
				)
			}
			if stage == "started" {
				require.NoError(t, s.BeginGeneration(t.Context(), r.ID))
			}
			*now = now.Add(4 * time.Minute)
			status, err := s.Status(t.Context(), owner, false)
			require.NoError(t, err)
			require.Zero(t, status.Starter.Quota.ReservedMicrousd)
			require.Zero(t, status.Starter.Quota.ReservedTokens)
			if stage == "started" {
				require.Equal(t, r.Microusd, status.Starter.Quota.SpentMicrousd)
				require.Equal(t, r.Tokens, status.Starter.Quota.SpentTokens)
			} else {
				require.Zero(t, status.Starter.Quota.SpentMicrousd)
				require.Zero(t, status.Starter.Quota.SpentTokens)
			}
			require.NoError(
				t,
				store.WithHostingState(t.Context(), func(state *hosting.State) error {
					expected := "moderation_interrupted"
					if stage == "started" {
						expected = "conservative"
					}
					require.Equal(t, expected, state.Requests[r.ID].Status)
					return nil
				}),
			)
		})
	}
}

func TestHostingRequiresGatewayCredential(t *testing.T) {
	s, _, owner, _ := fixture(t)
	s.Config.GatewayKey = ""
	require.False(t, s.Enabled())
	_, err := s.Wake(t.Context(), owner)
	require.ErrorIs(t, err, hosting.ErrDisabled)
}

func TestHostingRejectsSharedServiceSecretsAndIncompatibleGatewayOrigins(t *testing.T) {
	for _, name := range []string{"host_key", "token_secret", "port", "query", "whitespace"} {
		t.Run(name, func(t *testing.T) {
			s, _, _, _ := fixture(t)
			switch name {
			case "host_key":
				s.Config.GatewayKey = s.Config.HostKey
			case "token_secret":
				s.Config.GatewayKey = s.Config.TokenSecret
			case "port":
				s.Config.GatewayURL = "https://model.example.com:8443/v1"
			case "query":
				s.Config.GatewayURL = "https://model.example.com/v1?"
			case "whitespace":
				s.Config.GatewayURL = "https://model.example.com/v1 "
			}
			require.False(t, s.Enabled())
		})
	}
}
