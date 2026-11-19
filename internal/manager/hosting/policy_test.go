package hosting_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func TestModelRegistryIdentifierLimitsMatchGateway(t *testing.T) {
	for _, test := range []struct {
		name           string
		idLength       int
		providerLength int
		upstreamLength int
		valid          bool
	}{
		{name: "maximum identifiers", idLength: 128, providerLength: 64, upstreamLength: 256, valid: true},
		{name: "model ID too long", idLength: 129, providerLength: 64, upstreamLength: 256},
		{name: "provider too long", idLength: 128, providerLength: 65, upstreamLength: 256},
		{name: "upstream too long", idLength: 128, providerLength: 64, upstreamLength: 257},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := strings.Repeat("m", test.idLength)
			registry := hosting.ModelRegistry{
				PolicyVersion: "boundary-v1", DefaultModel: id,
				Models: []hosting.ModelPolicy{{
					ID: id, Provider: strings.Repeat("p", test.providerLength),
					UpstreamModel: strings.Repeat("u", test.upstreamLength), Moderation: "openai",
					PolicyVersion: "boundary-v1", MaxOutputTokens: 1, Enabled: true,
				}},
			}
			raw, err := json.Marshal(registry)
			require.NoError(t, err)
			_, err = hosting.ParseModelRegistry(raw)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func poolFixture(
	t *testing.T,
) (*hosting.Service, *storage.MemoryStore, string, *time.Time, []hosting.ModelPolicy) {
	t.Helper()
	s, store, owner, now := fixture(t)
	policies := []hosting.ModelPolicy{
		{
			ID:              "example/provider-a",
			Provider:        "provider-a",
			UpstreamModel:   "vendor/model-a",
			Moderation:      "openai",
			PolicyVersion:   "pool-v1",
			MaxOutputTokens: 2048,
			InputPrice:      1_000_000,
			OutputPrice:     2_000_000,
			Enabled:         true,
		},
		{
			ID:              "example/provider-b",
			Provider:        "provider-b",
			UpstreamModel:   "vendor/model-b",
			Moderation:      "off",
			PolicyVersion:   "pool-v1",
			MaxOutputTokens: 512,
			InputPrice:      0,
			OutputPrice:     0,
			Enabled:         true,
		},
	}
	s.Config.Models, s.Config.Model = append(
		[]hosting.ModelPolicy(nil),
		policies...), policies[0].ID
	return s, store, owner, now, policies
}

func poolJob(t *testing.T, s *hosting.Service, owner string) *hosting.Job {
	t.Helper()
	_, err := s.Wake(t.Context(), owner)
	require.NoError(t, err)
	job, err := s.Claim(t.Context(), &hosting.Claim{HostID: "host-a", Capacity: 10})
	require.NoError(t, err)
	require.NotNil(t, job)
	return job
}

func poolReserve(
	t *testing.T,
	s *hosting.Service,
	job *hosting.Job,
	id string,
	policy *hosting.ModelPolicy,
) *hosting.Reservation {
	t.Helper()
	r, err := s.ReserveForPolicy(
		t.Context(),
		job.ModelToken,
		id,
		1000,
		policy.ID,
		policy.MaxOutputTokens,
		policy,
	)
	require.NoError(t, err)
	return r
}

func TestModelRegistryDefaultsRequiredModerationAndRejectsInvalidOperatorPolicies(t *testing.T) {
	raw := []byte(
		`{"policy_version":"pool-v1","default_model":"example/a","models":[{"id":"example/a","provider":"provider-a","upstream_model":"vendor/a","route":"private-route","max_output_tokens":2048,"input_microusd_per_million":0,"output_microusd_per_million":0,"enabled":true}]}`,
	)
	registry, err := hosting.ParseModelRegistry(raw)
	require.NoError(t, err)
	require.Equal(t, "openai", registry.Models[0].Moderation)
	require.Equal(t, "pool-v1", registry.Models[0].PolicyVersion)
	for _, bad := range []string{
		`{"policy_version":"pool-v1","default_model":"example/a","models":[]}`,
		`{"policy_version":"pool-v1","default_model":"example/a","models":[{"id":"example/a","provider":"p","upstream_model":"u","max_output_tokens":1,"moderation":"auto","enabled":true}]}`,
		`{"policy_version":"pool-v1","default_model":"example/a","models":[{"id":"example/a","provider":"p","upstream_model":"u","max_output_tokens":0}]}`,
		`{"policy_version":"pool-v1","default_model":"example/a","models":[{"id":"example/a","provider":"p","upstream_model":"u","max_output_tokens":1,"enabled":true,"input_microusd_per_million":-1}]}`,
		`{"default_model":"example/a","models":[{"id":"example/a","provider":"p","upstream_model":"u","max_output_tokens":1,"enabled":true}]}`,
		string(raw) + `{}`,
	} {
		_, err = hosting.ParseModelRegistry([]byte(bad))
		require.Error(t, err)
	}
}

func TestPoolAdmissionRejectsUnapprovedOrModifiedPolicy(t *testing.T) {
	s, store, owner, _, policies := poolFixture(t)
	job := poolJob(t, s, owner)
	for _, name := range []string{"missing", "price", "moderation", "version", "provider", "upstream", "cap", "disabled"} {
		t.Run(name, func(t *testing.T) {
			claim := policies[0]
			pointer := &claim
			switch name {
			case "missing":
				pointer = nil
			case "price":
				claim.InputPrice = 0
			case "moderation":
				claim.Moderation = "off"
			case "version":
				claim.PolicyVersion = "pool-v0"
			case "provider":
				claim.Provider = "other"
			case "upstream":
				claim.UpstreamModel = "other"
			case "cap":
				claim.MaxOutputTokens = 8192
			case "disabled":
				claim.Enabled = false
			}
			_, err := s.ReserveForPolicy(
				t.Context(),
				job.ModelToken,
				"invalid-policy",
				1000,
				claim.ID,
				claim.MaxOutputTokens,
				pointer,
			)
			require.ErrorIs(t, err, hosting.ErrConflict)
		})
	}
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		require.Empty(t, state.Requests)
		return nil
	}))
}

func TestPoolSnapshotsPricesAndRequiresVersionedAudit(t *testing.T) {
	s, store, owner, _, policies := poolFixture(t)
	job := poolJob(t, s, owner)
	claim := policies[0]
	r := poolReserve(t, s, job, "snapshot-policy", &claim)
	claim.InputPrice = 999999
	require.Equal(t, int64(2000), r.Microusd)
	require.Equal(t, policies[0], *r.Policy)
	decision := &hosting.ModerationDecision{
		Verdict:       "allowed",
		Model:         hosting.ModerationModel,
		PolicyVersion: "old-v0",
		InputCount:    1,
	}
	require.ErrorIs(t, s.RecordModeration(t.Context(), r.ID, decision), hosting.ErrConflict)
	decision.PolicyVersion = policies[0].PolicyVersion
	require.NoError(t, s.RecordModeration(t.Context(), r.ID, decision))
	require.NoError(t, s.BeginGeneration(t.Context(), r.ID))
	s.Config.Models[0].InputPrice = 9_000_000
	require.NoError(t, s.Settle(t.Context(), r.ID, 100, 20, nil, "request-1", true))
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		stored := state.Requests[r.ID]
		require.Equal(t, int64(140), stored.ActualMicrousd)
		require.Equal(t, policies[0], *stored.Policy)
		require.Equal(t, "pool-v1", stored.Moderation.PolicyVersion)
		return nil
	}))
}

func TestPoolSkippedAuditOnlyPermitsExplicitApprovedOffPolicy(t *testing.T) {
	for _, index := range []int{0, 1} {
		t.Run(fmt.Sprintf("model_%d", index), func(t *testing.T) {
			s, _, owner, _, policies := poolFixture(t)
			job := poolJob(t, s, owner)
			r := poolReserve(t, s, job, "skip-policy-test", &policies[index])
			skip := &hosting.ModerationDecision{
				Verdict:       "skipped_by_policy",
				PolicyVersion: "pool-v1",
			}
			if index == 0 {
				require.ErrorIs(t, s.RecordModeration(t.Context(), r.ID, skip), hosting.ErrConflict)
				return
			}
			require.ErrorIs(
				t,
				s.RecordModeration(
					t.Context(),
					r.ID,
					&hosting.ModerationDecision{Verdict: "allowed", PolicyVersion: "pool-v1"},
				),
				hosting.ErrConflict,
			)
			skip.InputCount = 1
			require.ErrorIs(t, s.RecordModeration(t.Context(), r.ID, skip), hosting.ErrConflict)
			skip.InputCount = 0
			require.NoError(t, s.RecordModeration(t.Context(), r.ID, skip))
			require.NoError(t, s.BeginGeneration(t.Context(), r.ID))
			require.NoError(t, s.Settle(t.Context(), r.ID, 100, 20, nil, "request-1", true))
		})
	}
}

func TestPoolPolicyChangesDuringCheckPreventGenerationAndReleaseHold(t *testing.T) {
	for _, name := range []string{"disable", "version", "price", "mode"} {
		t.Run(name, func(t *testing.T) {
			s, store, owner, _, policies := poolFixture(t)
			job := poolJob(t, s, owner)
			r := poolReserve(t, s, job, "change-policy-test", &policies[0])
			require.NoError(
				t,
				s.RecordModeration(
					t.Context(),
					r.ID,
					&hosting.ModerationDecision{Verdict: "allowed", PolicyVersion: "pool-v1"},
				),
			)
			switch name {
			case "disable":
				s.Config.Models[0].Enabled = false
			case "version":
				s.Config.Models[0].PolicyVersion = "pool-v2"
			case "price":
				s.Config.Models[0].InputPrice++
			case "mode":
				s.Config.Models[0].Moderation = "off"
			}
			require.ErrorIs(t, s.BeginGeneration(t.Context(), r.ID), hosting.ErrConflict)
			require.NoError(t, s.CancelBeforeGeneration(t.Context(), r.ID))
			require.NoError(
				t,
				store.WithHostingState(t.Context(), func(state *hosting.State) error {
					require.Zero(t, state.SpentMicrousd)
					require.Zero(t, state.ReservedMicrousd)
					return nil
				}),
			)
		})
	}
}

func TestPoolRateLimitCountsBlockedAndSkippedAcrossModelsAndRestarts(t *testing.T) {
	s, store, owner, now, policies := poolFixture(t)
	job := poolJob(t, s, owner)
	for index := range 20 {
		policy := &policies[index%2]
		r := poolReserve(t, s, job, fmt.Sprintf("rate-request-%03d", index), policy)
		decision := &hosting.ModerationDecision{
			Verdict:       "blocked",
			Model:         hosting.ModerationModel,
			PolicyVersion: "pool-v1",
		}
		if policy.Moderation == "off" {
			decision.Verdict, decision.Model = "skipped_by_policy", ""
		}
		require.NoError(t, s.RecordModeration(t.Context(), r.ID, decision))
		if policy.Moderation == "off" {
			require.NoError(t, s.CancelBeforeGeneration(t.Context(), r.ID))
		}
	}
	_, err := s.ReserveForPolicy(
		t.Context(),
		job.ModelToken,
		"rate-request-over",
		1000,
		policies[0].ID,
		policies[0].MaxOutputTokens,
		&policies[0],
	)
	require.ErrorIs(t, err, hosting.ErrRateLimited)
	restarted := hosting.New(&s.Config, store, s.Now, s.ValidateBinding)
	_, err = restarted.ReserveForPolicy(
		t.Context(),
		job.ModelToken,
		"rate-after-restart",
		1000,
		policies[1].ID,
		policies[1].MaxOutputTokens,
		&policies[1],
	)
	require.ErrorIs(t, err, hosting.ErrRateLimited)
	*now = now.Add(60 * time.Second)
	r := poolReserve(t, restarted, job, "rate-after-window", &policies[0])
	require.NotNil(t, r.Policy)
}

func TestPoolGlobalRateLimitIsAtomicAcrossOwners(t *testing.T) {
	s, store, owner, _, policies := poolFixture(t)
	s.Config.GlobalRequestsPerMinute = 1
	first := poolJob(t, s, owner)
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		state.Environments[owner].Starter.State = "ready"
		return nil
	}))
	other, err := store.EnsureUser(t.Context(), "other@example.com", "Other", "user")
	require.NoError(t, err)
	second := poolJob(t, s, other.UserID)
	require.NotEqual(t, first.EnvironmentID, second.EnvironmentID)
	services := []*hosting.Service{s, hosting.New(&s.Config, store, s.Now, s.ValidateBinding)}
	var wg sync.WaitGroup
	var lock sync.Mutex
	accepted, limited := 0, 0
	for index, job := range []*hosting.Job{first, second} {
		wg.Go(func() {
			_, err := services[index].ReserveForPolicy(
				t.Context(),
				job.ModelToken,
				fmt.Sprintf("global-rate-%d", index),
				1000,
				policies[0].ID,
				policies[0].MaxOutputTokens,
				&policies[0],
			)
			lock.Lock()
			defer lock.Unlock()
			switch err {
			case nil:
				accepted++
			case hosting.ErrRateLimited:
				limited++
			default:
				t.Errorf("unexpected admission error: %v", err)
			}
		})
	}
	wg.Wait()
	require.Equal(t, 1, accepted)
	require.Equal(t, 1, limited)
}

func TestPoolDisabledDefaultKeepsSettlementAndOtherModelsAvailable(t *testing.T) {
	s, store, owner, _, policies := poolFixture(t)
	job := poolJob(t, s, owner)
	r := poolReserve(t, s, job, "settle-disabled-default", &policies[0])
	require.NoError(
		t,
		s.RecordModeration(
			t.Context(),
			r.ID,
			&hosting.ModerationDecision{Verdict: "allowed", PolicyVersion: "pool-v1"},
		),
	)
	require.NoError(t, s.BeginGeneration(t.Context(), r.ID))
	s.Config.Models[0].Enabled = false
	require.True(t, s.Enabled())
	require.NoError(t, s.Settle(t.Context(), r.ID, 100, 20, nil, "finished-before-disable", true))
	_, err := s.ReserveForPolicy(
		t.Context(),
		job.ModelToken,
		"deny-disabled-default",
		1000,
		policies[0].ID,
		policies[0].MaxOutputTokens,
		&policies[0],
	)
	require.ErrorIs(t, err, hosting.ErrConflict)
	r = poolReserve(t, s, job, "use-other-enabled-model", &policies[1])
	require.Equal(t, policies[1], *r.Policy)
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		require.Equal(t, int64(140), state.SpentMicrousd)
		return nil
	}))
}

func TestPoolWakeCanUseExplicitZeroPriceModelAfterUserMoneyExhaustion(t *testing.T) {
	for _, scenario := range []string{"zero_available", "zero_disabled", "tokens_exhausted", "global_exhausted", "global_over_cap"} {
		t.Run(scenario, func(t *testing.T) {
			s, store, owner, _, policies := poolFixture(t)
			_, err := s.Status(t.Context(), owner, true)
			require.NoError(t, err)
			require.NoError(
				t,
				store.WithHostingState(t.Context(), func(state *hosting.State) error {
					quota := &state.Environments[owner].Starter.Quota
					quota.SpentMicrousd = quota.GrantedMicrousd
					state.SpentMicrousd = quota.SpentMicrousd
					if scenario == "tokens_exhausted" {
						quota.SpentTokens = quota.GrantedTokens
					}
					if scenario == "global_exhausted" || scenario == "global_over_cap" {
						state.SpentMicrousd = s.Config.GlobalMicrousd
						if scenario == "global_over_cap" {
							state.SpentMicrousd++
						}
					}
					return nil
				}),
			)
			if scenario == "zero_disabled" {
				s.Config.Models[1].Enabled = false
			}
			status, err := s.Status(t.Context(), owner, false)
			require.NoError(t, err)
			require.Equal(
				t,
				scenario != "zero_disabled" && scenario != "tokens_exhausted",
				status.UserBudgetAvailable,
			)
			_, err = s.Wake(t.Context(), owner)
			if scenario == "global_exhausted" || scenario == "global_over_cap" {
				require.ErrorIs(t, err, hosting.ErrPlatformBudget)
				return
			}
			if scenario != "zero_available" {
				require.ErrorIs(t, err, hosting.ErrBudget)
				return
			}
			require.NoError(t, err)
			job, err := s.Claim(t.Context(), &hosting.Claim{HostID: "host-a", Capacity: 1})
			require.NoError(t, err)
			require.Equal(
				t,
				policies[0].ID,
				job.ModelID,
				"wake does not silently change the default model",
			)
			r := poolReserve(t, s, job, "zero-price-after-money", &policies[1])
			require.Zero(t, r.Microusd)
			_, err = s.ReserveForPolicy(
				t.Context(),
				job.ModelToken,
				"paid-after-money",
				1000,
				policies[0].ID,
				policies[0].MaxOutputTokens,
				&policies[0],
			)
			require.ErrorIs(t, err, hosting.ErrBudget)
		})
	}
}

func TestPoolModelsConsumeOneSharedGrant(t *testing.T) {
	s, _, owner, _, policies := poolFixture(t)
	job := poolJob(t, s, owner)
	for index, policy := range policies {
		r := poolReserve(t, s, job, fmt.Sprintf("shared-model-grant-%d", index), &policy)
		decision := &hosting.ModerationDecision{
			Verdict:       "allowed",
			Model:         hosting.ModerationModel,
			PolicyVersion: "pool-v1",
		}
		if policy.Moderation == "off" {
			decision.Verdict, decision.Model = "skipped_by_policy", ""
		}
		require.NoError(t, s.RecordModeration(t.Context(), r.ID, decision))
		require.NoError(t, s.BeginGeneration(t.Context(), r.ID))
		require.NoError(t, s.Settle(t.Context(), r.ID, 100, 20, nil, "shared-usage", true))
	}
	status, err := s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.Equal(t, int64(1_000_000), status.Starter.Quota.GrantedMicrousd)
	require.Equal(t, int64(200000), status.Starter.Quota.GrantedTokens)
	require.Equal(t, int64(140), status.Starter.Quota.SpentMicrousd)
	require.Equal(t, int64(240), status.Starter.Quota.SpentTokens)
	require.Equal(t, int64(199760), status.Starter.Quota.RemainingTokens)
}
