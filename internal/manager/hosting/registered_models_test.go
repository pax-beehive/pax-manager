package hosting_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

func createModelUpdate(id string) *hosting.AdminUpdate {
	return &hosting.AdminUpdate{
		Action: "model_create", TargetID: id, Provider: "openrouter",
		UpstreamModel: "vendor/frontier-model", Moderation: text("off"),
		MaxOutputTokens: number(1024), InputPrice: number(1_000_000),
		OutputPrice: number(5_000_000),
	}
}

func registeredPolicy(
	t *testing.T,
	snapshot *hosting.AdminSnapshot,
	id string,
) hosting.ModelPolicy {
	t.Helper()
	for _, policy := range snapshot.Models {
		if policy.ID == id {
			return policy
		}
	}
	t.Fatalf("missing registered model %q", id)
	return hosting.ModelPolicy{}
}

func TestRegisteredModelPersistsAndUsesExistingSharedLedger(t *testing.T) {
	s, store, owner, _, seeds := poolFixture(t)
	job := poolJob(t, s, owner)
	before, err := s.AdminSnapshot(t.Context(), hosting.AdminQuery{})
	require.NoError(t, err)
	created := adminUpdate(t, s, createModelUpdate("operator/frontier"))
	require.Equal(t, seeds, created.Models[:len(seeds)])
	policy := registeredPolicy(t, created, "operator/frontier")
	require.False(t, policy.Enabled)
	require.Equal(t, "admin-1", policy.PolicyVersion)
	require.NotEmpty(t, created.ModelBases[policy.ID])
	require.Equal(t, before.Pool, created.Pool)
	_, err = s.ReserveForPolicy(t.Context(), job.ModelToken, "disabled-created-model", 1000,
		policy.ID, policy.MaxOutputTokens, &policy)
	require.ErrorIs(t, err, hosting.ErrConflict)

	restarted := hosting.New(&s.Config, store, s.Now, nil)
	snapshot, err := restarted.AdminSnapshot(t.Context(), hosting.AdminQuery{})
	require.NoError(t, err)
	require.Equal(t, created, snapshot)
	enabled := adminUpdate(t, restarted, &hosting.AdminUpdate{
		Action: "model", TargetID: policy.ID, Enabled: boolean(true),
	})
	policy = registeredPolicy(t, enabled, policy.ID)
	require.True(t, policy.Enabled)
	require.Equal(t, "admin-2", policy.PolicyVersion)
	catalog, err := restarted.RuntimePolicies(t.Context(), job.ModelToken)
	require.NoError(t, err)
	require.Contains(t, catalog, policy)
	reservation := poolReserve(t, restarted, job, "registered-ledger-request", &policy)
	require.NoError(t, restarted.RecordModeration(t.Context(), reservation.ID,
		&hosting.ModerationDecision{
			Verdict: "skipped_by_policy", PolicyVersion: policy.PolicyVersion,
		}))
	require.NoError(t, restarted.BeginGeneration(t.Context(), reservation.ID))
	require.NoError(
		t,
		restarted.Settle(t.Context(), reservation.ID, 10, 20, nil, "mock-request", true),
	)
	after, err := restarted.AdminSnapshot(t.Context(), hosting.AdminQuery{})
	require.NoError(t, err)
	require.EqualValues(t, 110, after.Pool.SpentMicrousd)
	require.Zero(t, after.Pool.ReservedMicrousd)
	require.EqualValues(t, 110, after.Users[0].Quota.SpentMicrousd)
	require.Equal(t, seeds, after.Models[:len(seeds)])
}

func TestRegisteredModelRejectsDuplicatesAndStaleCreatesWithoutMutation(t *testing.T) {
	s, _, _, _, seeds := poolFixture(t)
	created := adminUpdate(t, s, createModelUpdate("operator/duplicate"))
	for _, id := range []string{"operator/duplicate", seeds[0].ID} {
		update := createModelUpdate(id)
		update.Revision = created.Revision
		_, err := s.AdminUpdate(t.Context(), "admin-user", update)
		require.ErrorIs(t, err, hosting.ErrAdminModelConflict)
	}
	alias := createModelUpdate("operator/same-route-alias")
	alias.Revision = created.Revision
	_, err := s.AdminUpdate(t.Context(), "admin-user", alias)
	require.ErrorIs(t, err, hosting.ErrAdminModelConflict)
	_, err = s.AdminUpdate(t.Context(), "admin-user", createModelUpdate("operator/stale"))
	require.ErrorIs(t, err, hosting.ErrAdminRevision)
	after, err := s.AdminSnapshot(t.Context(), hosting.AdminQuery{})
	require.NoError(t, err)
	require.Equal(t, created, after)
	legacy, _, _, _ := fixture(t)
	_, err = legacy.AdminUpdate(t.Context(), "admin-user", createModelUpdate(legacy.Config.Model))
	require.ErrorIs(t, err, hosting.ErrAdminModelConflict)
	seeded, _, _, _, _ := poolFixture(t)
	seeded.Config.Models[0].Provider = "openrouter"
	seeded.Config.Models[0].UpstreamModel = "vendor/frontier-model"
	_, err = seeded.AdminUpdate(t.Context(), "admin-user", createModelUpdate("operator/seed-route"))
	require.ErrorIs(t, err, hosting.ErrAdminModelConflict)
}

func TestRegisteredModelCreationValidatesIdentityAndExplicitPolicy(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*hosting.AdminUpdate)
	}{
		{"unknown provider", func(u *hosting.AdminUpdate) { u.Provider = "custom" }},
		{"provider case", func(u *hosting.AdminUpdate) { u.Provider = "OpenRouter" }},
		{"long id", func(u *hosting.AdminUpdate) { u.TargetID = strings.Repeat("m", 129) }},
		{"long upstream", func(u *hosting.AdminUpdate) { u.UpstreamModel = strings.Repeat("m", 257) }},
		{"long concrete route", func(u *hosting.AdminUpdate) { u.UpstreamModel = strings.Repeat("m", 246) }},
		{"empty route segment", func(u *hosting.AdminUpdate) { u.UpstreamModel = "vendor//model" }},
		{"dot route segment", func(u *hosting.AdminUpdate) { u.UpstreamModel = "vendor/./model" }},
		{"parent route segment", func(u *hosting.AdminUpdate) { u.UpstreamModel = "vendor/../model" }},
		{"url upstream", func(u *hosting.AdminUpdate) { u.UpstreamModel = "https://model.invalid" }},
		{"url alias", func(u *hosting.AdminUpdate) { u.TargetID = "https://model.invalid" }},
		{"whitespace", func(u *hosting.AdminUpdate) { u.TargetID = "model with space" }},
		{"missing upstream", func(u *hosting.AdminUpdate) { u.UpstreamModel = "" }},
		{"missing moderation", func(u *hosting.AdminUpdate) { u.Moderation = nil }},
		{"unknown moderation", func(u *hosting.AdminUpdate) { u.Moderation = text("custom") }},
		{"missing cap", func(u *hosting.AdminUpdate) { u.MaxOutputTokens = nil }},
		{"high cap", func(u *hosting.AdminUpdate) { u.MaxOutputTokens = number(8193) }},
		{"missing input price", func(u *hosting.AdminUpdate) { u.InputPrice = nil }},
		{"negative price", func(u *hosting.AdminUpdate) { u.OutputPrice = number(-1) }},
		{"high price", func(u *hosting.AdminUpdate) { u.OutputPrice = number(1_000_000_000_001) }},
		{"auto enable", func(u *hosting.AdminUpdate) { u.Enabled = boolean(true) }},
		{"grant fields", func(u *hosting.AdminUpdate) { u.MonthlyTokens = number(1000) }},
		{"caller base", func(u *hosting.AdminUpdate) { u.ExpectedModelBase = strings.Repeat("a", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _, _, _ := fixture(t)
			update := createModelUpdate("operator/invalid")
			test.change(update)
			_, err := s.AdminUpdate(t.Context(), "admin-user", update)
			require.ErrorIs(t, err, hosting.ErrAdminUpdate)
			snapshot, err := s.AdminSnapshot(t.Context(), hosting.AdminQuery{})
			require.NoError(t, err)
			require.Zero(t, snapshot.Revision)
			require.Empty(t, snapshot.Models)
		})
	}
	for _, provider := range []string{"openrouter", "openai", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			s, _, _, _ := fixture(t)
			update := createModelUpdate("operator/" + provider)
			update.Provider, update.InputPrice, update.OutputPrice = provider, number(0), number(0)
			update.UpstreamModel = strings.Repeat("m", 255-len(provider))
			update.Enabled = boolean(false)
			created := adminUpdate(t, s, update)
			require.False(t, created.Models[0].Enabled)
		})
	}
}

func TestRegisteredModelFutureSeedCollisionFailsClosed(t *testing.T) {
	s, store, owner, _, seeds := poolFixture(t)
	job := poolJob(t, s, owner)
	adminUpdate(t, s, createModelUpdate("operator/collision"))
	created := adminUpdate(t, s, &hosting.AdminUpdate{
		Action: "model", TargetID: "operator/collision", Enabled: boolean(true),
	})
	policy := registeredPolicy(t, created, "operator/collision")
	config := s.Config
	config.Models = append(append([]hosting.ModelPolicy(nil), seeds...), policy)
	changed := hosting.New(&config, store, s.Now, nil)
	snapshot, err := changed.AdminSnapshot(t.Context(), hosting.AdminQuery{})
	require.NoError(t, err)
	require.False(t, registeredPolicy(t, snapshot, policy.ID).Enabled)
	require.Equal(t, "model_id_conflict", snapshot.ModelWarnings[policy.ID])
	require.Equal(t, seeds, snapshot.Models[:len(seeds)])
	_, err = changed.ReserveForPolicy(t.Context(), job.ModelToken, "collision-reserve-model", 1000,
		policy.ID, policy.MaxOutputTokens, &policy)
	require.ErrorIs(t, err, hosting.ErrConflict)
	_, err = changed.AdminUpdate(t.Context(), "admin-user", &hosting.AdminUpdate{
		Revision: snapshot.Revision, Action: "model", TargetID: policy.ID,
		ExpectedModelBase: snapshot.ModelBases[policy.ID], Enabled: boolean(true),
	})
	require.ErrorIs(t, err, hosting.ErrAdminModelConflict)
}

func TestRegisteredModelCountIncludesSeedPolicies(t *testing.T) {
	s, store, _, _, seeds := poolFixture(t)
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		for i := range 128 - len(seeds) {
			policy := seeds[0]
			policy.ID, policy.Provider = fmt.Sprintf("operator/model-%03d", i), "openrouter"
			policy.UpstreamModel = fmt.Sprintf("vendor/model-%03d", i)
			state.Admin.CreatedModels[policy.ID] = policy
		}
		return nil
	}))
	_, err := s.AdminUpdate(t.Context(), "admin-user", createModelUpdate("operator/too-many"))
	require.ErrorIs(t, err, hosting.ErrAdminUpdate)
	duplicate := createModelUpdate("operator/duplicate-at-limit")
	duplicate.UpstreamModel = "vendor/model-000"
	_, err = s.AdminUpdate(t.Context(), "admin-user", duplicate)
	require.ErrorIs(t, err, hosting.ErrAdminModelConflict)
	snapshot, err := s.AdminSnapshot(t.Context(), hosting.AdminQuery{})
	require.NoError(t, err)
	require.Len(t, snapshot.Models, 128)
	require.Zero(t, snapshot.Revision)
}

func TestRegisteredDefaultBeforeBootstrapUsesItsPolicyInsteadOfLegacy(t *testing.T) {
	s, store, owner, _ := fixture(t)
	s.Config.Model = ""
	created := adminUpdate(t, s, createModelUpdate("operator/bootstrap"))
	require.False(t, created.Enabled)
	config := s.Config
	config.Model = "operator/bootstrap"
	bootstrapped := hosting.New(&config, store, s.Now, nil)
	enabled := adminUpdate(t, bootstrapped, &hosting.AdminUpdate{
		Action: "model", TargetID: config.Model, Enabled: boolean(true),
	})
	policy := registeredPolicy(t, enabled, config.Model)
	job := poolJob(t, bootstrapped, owner)
	require.Equal(t, policy.MaxOutputTokens, job.ModelMaxOutputTokens)
	reservation, err := bootstrapped.Reserve(
		t.Context(),
		job.ModelToken,
		"bootstrap-default-policy",
		1000,
	)
	require.NoError(t, err)
	require.Equal(t, &policy, reservation.Policy)
	require.NoError(t, bootstrapped.CancelBeforeGeneration(t.Context(), reservation.ID))
	legacy := policy
	legacy.Provider, legacy.PolicyVersion = "configured", hosting.ModerationPolicy
	legacy.InputPrice, legacy.OutputPrice, legacy.Moderation = 0, 0, "openai"
	_, err = bootstrapped.ReserveForPolicy(
		t.Context(),
		job.ModelToken,
		"bootstrap-reject-legacy",
		1000,
		policy.ID,
		policy.MaxOutputTokens,
		&legacy,
	)
	require.ErrorIs(t, err, hosting.ErrConflict)
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		invalid := state.Admin.CreatedModels[config.Model]
		invalid.Provider = "unsupported"
		state.Admin.CreatedModels[config.Model] = invalid
		return nil
	}))
	_, err = bootstrapped.Reserve(
		t.Context(), job.ModelToken, "bootstrap-invalid-no-fallback", 1000,
	)
	require.ErrorIs(t, err, hosting.ErrConflict)
}
