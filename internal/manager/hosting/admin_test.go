package hosting_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

func number(value int64) *int64 { return &value }
func boolean(value bool) *bool  { return &value }
func text(value string) *string { return &value }

func adminUpdate(
	t *testing.T,
	s *hosting.Service,
	update *hosting.AdminUpdate,
) *hosting.AdminSnapshot {
	t.Helper()
	before, err := s.AdminSnapshot(t.Context(), hosting.AdminQuery{})
	require.NoError(t, err)
	update.Revision = before.Revision
	if update.Action == "model" && update.ExpectedModelBase == "" {
		update.ExpectedModelBase = before.ModelBases[update.TargetID]
	}
	after, err := s.AdminUpdate(t.Context(), "admin-user", update)
	require.NoError(t, err)
	require.Equal(t, before.Revision+1, after.Revision)
	return after
}

func TestAdminPoolUpdatesPreserveSpendAndHoldsAcrossRestart(t *testing.T) {
	s, store, owner, _ := fixture(t)
	job := poolJob(t, s, owner)
	first, err := s.Reserve(t.Context(), job.ModelToken, "pool-first-request", 1000)
	require.NoError(t, err)
	require.NoError(t, s.Settle(t.Context(), first.ID, 100, 100, nil, "upstream-one", true))
	held, err := s.Reserve(t.Context(), job.ModelToken, "pool-held-request", 1000)
	require.NoError(t, err)
	after := adminUpdate(t, s, &hosting.AdminUpdate{Action: "pool", MonthlyMicrousd: number(0)})
	require.Equal(t, int64(600), after.Pool.SpentMicrousd)
	require.Equal(t, held.Microusd, after.Pool.ReservedMicrousd)
	require.False(t, after.Pool.Available)
	restarted := hosting.New(&s.Config, store, s.Now, nil)
	snapshot, err := restarted.AdminSnapshot(t.Context(), hosting.AdminQuery{})
	require.NoError(t, err)
	require.Equal(t, after.Pool, snapshot.Pool)
	require.NoError(t, restarted.Settle(t.Context(), held.ID, 10, 10, nil, "upstream-two", true))
	_, err = restarted.Reserve(t.Context(), job.ModelToken, "pool-reject-request", 1000)
	require.ErrorIs(t, err, hosting.ErrPlatformBudget)
	_, err = restarted.AdminUpdate(t.Context(), "admin-user", &hosting.AdminUpdate{
		Revision: 0, Action: "pool", MonthlyMicrousd: number(10_000_000),
	})
	require.ErrorIs(t, err, hosting.ErrAdminRevision)
	after = adminUpdate(
		t,
		restarted,
		&hosting.AdminUpdate{Action: "pool", MonthlyMicrousd: number(10_000_000)},
	)
	require.Equal(t, int64(660), after.Pool.SpentMicrousd)
	require.Zero(t, after.Pool.ReservedMicrousd)
	require.Equal(t, int64(10_000_000-660), after.Pool.RemainingMicrousd)
}

func TestAdminDefaultsAndUserOverridesDoNotRegrantCurrentMonth(t *testing.T) {
	s, store, owner, now := fixture(t)
	job := poolJob(t, s, owner)
	r, err := s.Reserve(t.Context(), job.ModelToken, "quota-spent-request", 1000)
	require.NoError(t, err)
	require.NoError(t, s.Settle(t.Context(), r.ID, 100, 100, nil, "", true))
	adminUpdate(t, s, &hosting.AdminUpdate{
		Action: "defaults", MonthlyMicrousd: number(5_000_000), MonthlyTokens: number(900_000),
	})
	current, err := s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.Equal(t, int64(1_000_000), current.Starter.Quota.GrantedMicrousd)
	require.Equal(t, int64(600), current.Starter.Quota.SpentMicrousd)
	other, err := s.Status(t.Context(), "new-user", true)
	require.NoError(t, err)
	require.Equal(t, int64(5_000_000), other.Starter.Quota.GrantedMicrousd)
	adminUpdate(t, s, &hosting.AdminUpdate{
		Action: "user", TargetID: owner, MonthlyMicrousd: number(500), MonthlyTokens: number(300),
	})
	current, err = s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.Equal(t, int64(600), current.Starter.Quota.SpentMicrousd)
	require.Zero(t, current.Starter.Quota.RemainingMicrousd)
	*now = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	restarted := hosting.New(&s.Config, store, s.Now, nil)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			status, statusErr := restarted.Status(t.Context(), owner, true)
			require.NoError(t, statusErr)
			require.Equal(t, int64(500), status.Starter.Quota.GrantedMicrousd)
			require.Equal(t, int64(300), status.Starter.Quota.GrantedTokens)
			require.Zero(t, status.Starter.Quota.SpentMicrousd)
		})
	}
	wg.Wait()
	other, err = restarted.Status(t.Context(), "new-user", false)
	require.NoError(t, err)
	require.Equal(t, int64(5_000_000), other.Starter.Quota.GrantedMicrousd)
	require.Equal(t, int64(900_000), other.Starter.Quota.GrantedTokens)
}

func TestAdminPauseStopsFreeAdmissionButPermitsAdmittedSettlement(t *testing.T) {
	s, _, owner, _, policies := poolFixture(t)
	job := poolJob(t, s, owner)
	r := poolReserve(t, s, job, "pause-begin-request", &policies[0])
	require.NoError(t, s.RecordModeration(t.Context(), r.ID, &hosting.ModerationDecision{
		Verdict: "allowed", Model: hosting.ModerationModel, PolicyVersion: policies[0].PolicyVersion,
	}))
	adminUpdate(
		t,
		s,
		&hosting.AdminUpdate{Action: "user", TargetID: owner, AccessPaused: boolean(true)},
	)
	require.ErrorIs(t, s.Authorize(t.Context(), job.ModelToken), hosting.ErrAccessPaused)
	require.ErrorIs(t, s.BeginGeneration(t.Context(), r.ID), hosting.ErrAccessPaused)
	_, err := s.RuntimePolicies(t.Context(), job.ModelToken)
	require.ErrorIs(t, err, hosting.ErrAccessPaused)
	_, err = s.Reserve(t.Context(), job.ModelToken, "pause-reserve-request", 1000)
	require.ErrorIs(t, err, hosting.ErrAccessPaused)
	_, err = s.Wake(t.Context(), owner)
	require.ErrorIs(t, err, hosting.ErrAccessPaused)
	claim, err := s.Claim(t.Context(), &hosting.Claim{HostID: "host-a", Capacity: 1})
	require.NoError(t, err)
	require.Nil(t, claim)
	status, err := s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.True(t, status.Starter.AccessPaused)
	adminUpdate(
		t,
		s,
		&hosting.AdminUpdate{Action: "user", TargetID: owner, AccessPaused: boolean(false)},
	)
	require.NoError(t, s.BeginGeneration(t.Context(), r.ID))
	adminUpdate(
		t,
		s,
		&hosting.AdminUpdate{Action: "user", TargetID: owner, AccessPaused: boolean(true)},
	)
	require.NoError(
		t,
		s.SettleWithError(t.Context(), r.ID, 10, 10, nil, "", true, "model_stream_failed"),
	)
}

func TestAdminModelChangesFenceStaleSnapshotsAndPreserveIdentity(t *testing.T) {
	s, store, owner, _, policies := poolFixture(t)
	job := poolJob(t, s, owner)
	before, err := s.RuntimePolicies(t.Context(), job.ModelToken)
	require.NoError(t, err)
	first := poolReserve(t, s, job, "model-before-update", &before[0])
	require.NoError(t, s.RecordModeration(t.Context(), first.ID, &hosting.ModerationDecision{
		Verdict: "allowed", Model: hosting.ModerationModel, PolicyVersion: before[0].PolicyVersion,
	}))
	updated := adminUpdate(t, s, &hosting.AdminUpdate{
		Action: "model", TargetID: policies[0].ID, Moderation: text("off"),
		MaxOutputTokens: number(1024), InputPrice: number(7), OutputPrice: number(11),
	})
	policy := updated.Models[0]
	require.Equal(t, policies[0].Provider, policy.Provider)
	require.Equal(t, policies[0].UpstreamModel, policy.UpstreamModel)
	require.Equal(t, "admin-1", policy.PolicyVersion)
	require.ErrorIs(t, s.BeginGeneration(t.Context(), first.ID), hosting.ErrConflict)
	require.NoError(t, s.CancelBeforeGeneration(t.Context(), first.ID))
	_, err = s.ReserveForPolicy(t.Context(), job.ModelToken, "stale-policy-request", 1000,
		before[0].ID, before[0].MaxOutputTokens, &before[0])
	require.ErrorIs(t, err, hosting.ErrConflict)
	adminUpdate(t, s, &hosting.AdminUpdate{Action: "pool", MonthlyMicrousd: number(10_000_000)})
	restarted := hosting.New(&s.Config, store, s.Now, nil)
	current, err := restarted.RuntimePolicies(t.Context(), job.ModelToken)
	require.NoError(t, err)
	require.Equal(t, policy, current[0])
	second := poolReserve(t, restarted, job, "current-policy-request", &current[0])
	require.NoError(
		t,
		restarted.RecordModeration(t.Context(), second.ID, &hosting.ModerationDecision{
			Verdict: "skipped_by_policy", PolicyVersion: current[0].PolicyVersion,
		}),
	)
	require.NoError(t, restarted.BeginGeneration(t.Context(), second.ID))
	require.NoError(t, restarted.Settle(t.Context(), second.ID, 0, 0, nil, "", true))
	config := restarted.Config
	config.Models = append([]hosting.ModelPolicy(nil), config.Models...)
	config.Models[0].Provider = "replacement-provider"
	changed := hosting.New(&config, store, s.Now, nil)
	snapshot, err := changed.AdminSnapshot(t.Context(), hosting.AdminQuery{})
	require.NoError(t, err)
	require.False(t, snapshot.Models[0].Enabled)
	require.Equal(t, "base_policy_changed", snapshot.ModelWarnings[policies[0].ID])
	_, err = changed.ReserveForPolicy(t.Context(), job.ModelToken, "changed-base-request", 1000,
		current[0].ID, current[0].MaxOutputTokens, &current[0])
	require.ErrorIs(t, err, hosting.ErrConflict)
	approved := adminUpdate(t, changed, &hosting.AdminUpdate{
		Action: "model", TargetID: policies[0].ID, Enabled: boolean(true),
	})
	require.Equal(t, "openai", approved.Models[0].Moderation)
	require.Empty(t, approved.ModelWarnings)
}

func TestAdminSnapshotIsBoundedAndOmitsRawUsageAndCredentials(t *testing.T) {
	s, store, owner, _ := fixture(t)
	job := poolJob(t, s, owner)
	for i := range 3 {
		r, err := s.Reserve(t.Context(), job.ModelToken, fmt.Sprintf("history-request-%d", i), 1000)
		require.NoError(t, err)
		require.NoError(t, s.SettleWithError(t.Context(), r.ID, 10, 20,
			[]byte(`{"prompt":"RAW_USAGE_SECRET"}`), "upstream-id", true, "model_upstream_failed"))
	}
	for i := range 3 {
		_, err := s.Status(t.Context(), fmt.Sprintf("history-user-%d", i), true)
		require.NoError(t, err)
	}
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		state.Admin.Audit = make([]hosting.AdminAudit, 1000)
		return nil
	}))
	adminUpdate(t, s, &hosting.AdminUpdate{Action: "pool", MonthlyMicrousd: number(100_000_000)})
	snapshot, err := s.AdminSnapshot(t.Context(), hosting.AdminQuery{Limit: 2})
	require.NoError(t, err)
	require.Len(t, snapshot.Users, 2)
	require.Len(t, snapshot.Requests, 2)
	require.Len(t, snapshot.Audit, 50)
	require.NotEmpty(t, snapshot.UsersNext)
	require.NotEmpty(t, snapshot.RequestsNext)
	require.Equal(t, int64(10), snapshot.Requests[0].InputTokens)
	require.Equal(t, int64(20), snapshot.Requests[0].OutputTokens)
	require.Equal(t, "model_upstream_failed", snapshot.Requests[0].ErrorCode)
	next, err := s.AdminSnapshot(t.Context(), hosting.AdminQuery{
		Limit: 2, UsersAfter: snapshot.UsersNext, RequestsBefore: snapshot.RequestsNext,
	})
	require.NoError(t, err)
	require.Len(t, next.Users, 2)
	require.Len(t, next.Requests, 1)
	require.NotEqual(t, snapshot.Requests[0].ID, next.Requests[0].ID)
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	for _, excluded := range []string{"RAW_USAGE_SECRET", job.ModelToken, job.LeaseToken,
		s.Config.HostKey, s.Config.GatewayKey, "RawUsage", "raw_usage"} {
		require.NotContains(t, string(raw), excluded)
	}
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		require.Len(t, state.Admin.Audit, 1000)
		return nil
	}))
}

func TestAdminRevisionSerializesConcurrentUpdatesAndRejectsInvalidControls(t *testing.T) {
	s, _, _, _ := fixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := range 12 {
		wg.Go(func() {
			_, err := s.AdminUpdate(t.Context(), "admin-user", &hosting.AdminUpdate{
				Action: "pool", MonthlyMicrousd: number(int64(i)),
			})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else {
			require.ErrorIs(t, err, hosting.ErrAdminRevision)
		}
	}
	require.Equal(t, 1, success)
	for _, update := range []*hosting.AdminUpdate{
		{Action: "pool", MonthlyMicrousd: number(-1)},
		{Action: "pool", MonthlyMicrousd: number(1), AccessPaused: boolean(true)},
		{Action: "defaults", MonthlyTokens: number(1_000_000_001)},
		{Action: "model", TargetID: "new-model", Enabled: boolean(true)},
		{Action: "unknown", MonthlyMicrousd: number(1)},
	} {
		update.Revision = 1
		_, err := s.AdminUpdate(t.Context(), "admin-user", update)
		require.Error(t, err)
	}
	_, err := s.AdminUpdate(t.Context(), strings.Repeat("a", 257), &hosting.AdminUpdate{
		Revision: 1, Action: "pool", MonthlyMicrousd: number(1),
	})
	require.ErrorIs(t, err, hosting.ErrAdminUpdate)
	snapshot, err := s.AdminSnapshot(t.Context(), hosting.AdminQuery{})
	require.NoError(t, err)
	require.Equal(t, int64(1), snapshot.Revision)
	require.Len(t, snapshot.Audit, 1)
}

type beforeHostingStore struct {
	base   hosting.Store
	before func()
}

func (store beforeHostingStore) WithHostingState(
	ctx context.Context,
	run func(*hosting.State) error,
) error {
	store.before()
	return store.base.WithHostingState(ctx, run)
}

func TestAdminPolicyChangeBetweenGatewaySnapshotAndStoreAdmissionIsRejected(t *testing.T) {
	s, store, owner, _, policies := poolFixture(t)
	job := poolJob(t, s, owner)
	admission := hosting.New(&s.Config, beforeHostingStore{base: store, before: func() {
		adminUpdate(t, s, &hosting.AdminUpdate{
			Action: "model", TargetID: policies[0].ID, Enabled: boolean(false),
		})
	}}, s.Now, nil)
	_, err := admission.ReserveForPolicy(t.Context(), job.ModelToken, "store-race-request", 1000,
		policies[0].ID, policies[0].MaxOutputTokens, &policies[0])
	require.ErrorIs(t, err, hosting.ErrConflict)
	snapshot, err := s.AdminSnapshot(t.Context(), hosting.AdminQuery{})
	require.NoError(t, err)
	require.Zero(t, snapshot.Pool.ReservedMicrousd)
	require.Empty(t, snapshot.Requests)
}

func TestAdminUserCapPreservesHoldAndStopsUnstartedGeneration(t *testing.T) {
	s, _, owner, _, policies := poolFixture(t)
	job := poolJob(t, s, owner)
	r := poolReserve(t, s, job, "user-cap-held-request", &policies[0])
	require.NoError(t, s.RecordModeration(t.Context(), r.ID, &hosting.ModerationDecision{
		Verdict: "allowed", Model: hosting.ModerationModel, PolicyVersion: policies[0].PolicyVersion,
	}))
	snapshot := adminUpdate(t, s, &hosting.AdminUpdate{
		Action: "user", TargetID: owner, MonthlyMicrousd: number(0), MonthlyTokens: number(0),
	})
	require.Equal(t, r.Microusd, snapshot.Users[0].Quota.ReservedMicrousd)
	require.Equal(t, r.Tokens, snapshot.Users[0].Quota.ReservedTokens)
	require.ErrorIs(t, s.BeginGeneration(t.Context(), r.ID), hosting.ErrBudget)
	require.NoError(t, s.CancelBeforeGeneration(t.Context(), r.ID))
	adminUpdate(t, s, &hosting.AdminUpdate{
		Action: "user", TargetID: owner, MonthlyMicrousd: number(1_000_000), MonthlyTokens: number(10_000),
	})
	r = poolReserve(t, s, job, "user-cap-started-request", &policies[0])
	require.NoError(t, s.RecordModeration(t.Context(), r.ID, &hosting.ModerationDecision{
		Verdict: "allowed", Model: hosting.ModerationModel, PolicyVersion: policies[0].PolicyVersion,
	}))
	require.NoError(t, s.BeginGeneration(t.Context(), r.ID))
	adminUpdate(t, s, &hosting.AdminUpdate{
		Action: "user", TargetID: owner, MonthlyMicrousd: number(0), MonthlyTokens: number(0),
	})
	require.NoError(t, s.Settle(t.Context(), r.ID, 100, 100, nil, "", true))
	status, err := s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.Equal(t, int64(300), status.Starter.Quota.SpentMicrousd)
	require.Zero(t, status.Starter.Quota.ReservedMicrousd)
	require.Zero(t, status.Starter.Quota.RemainingMicrousd)
}

func TestAdminFailureCodesRejectProviderTextAndSettlementRemainsIdempotent(t *testing.T) {
	s, _, owner, _ := fixture(t)
	job := poolJob(t, s, owner)
	r, err := s.Reserve(t.Context(), job.ModelToken, "failure-code-request", 1000)
	require.NoError(t, err)
	require.ErrorIs(t, s.SettleWithError(t.Context(), r.ID, 1, 1, nil, "", true,
		"provider failure: secret-api-key"), hosting.ErrAdminUpdate)
	require.NoError(t, s.SettleWithError(t.Context(), r.ID, 1, 1, nil, "", true,
		"model_upstream_failed"))
	require.NoError(t, s.SettleWithError(t.Context(), r.ID, 99, 99, nil, "", true,
		"model_stream_failed"))
	snapshot, err := s.AdminSnapshot(t.Context(), hosting.AdminQuery{})
	require.NoError(t, err)
	require.Equal(t, int64(6), snapshot.Pool.SpentMicrousd)
	require.Equal(t, "model_upstream_failed", snapshot.Requests[0].ErrorCode)
}

func TestAdminModelBasePreconditionFencesDeploymentChangesWithoutRevisionChange(t *testing.T) {
	s, store, _, _, policies := poolFixture(t)
	before, err := s.AdminSnapshot(t.Context(), hosting.AdminQuery{})
	require.NoError(t, err)
	raw, err := json.Marshal(policies[0])
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	require.Equal(t, hex.EncodeToString(sum[:]), before.ModelBases[policies[0].ID])
	config := s.Config
	config.Models = append([]hosting.ModelPolicy(nil), config.Models...)
	config.Models[0].Provider = "deployed-provider"
	changed := hosting.New(&config, store, s.Now, nil)
	afterDeployment, err := changed.AdminSnapshot(t.Context(), hosting.AdminQuery{})
	require.NoError(t, err)
	require.Equal(t, before.Revision, afterDeployment.Revision)
	require.NotEqual(
		t,
		before.ModelBases[policies[0].ID],
		afterDeployment.ModelBases[policies[0].ID],
	)
	_, err = changed.AdminUpdate(t.Context(), "admin-user", &hosting.AdminUpdate{
		Revision: before.Revision, Action: "model", TargetID: policies[0].ID,
		ExpectedModelBase: before.ModelBases[policies[0].ID], Moderation: text("off"),
	})
	require.ErrorIs(t, err, hosting.ErrAdminRevision)
	unchanged, err := changed.AdminSnapshot(t.Context(), hosting.AdminQuery{})
	require.NoError(t, err)
	require.Equal(t, before.Revision, unchanged.Revision)
	require.Empty(t, unchanged.Audit)
	require.Equal(t, "openai", unchanged.Models[0].Moderation)
	approved := adminUpdate(t, changed, &hosting.AdminUpdate{
		Action: "model", TargetID: policies[0].ID,
		ExpectedModelBase: afterDeployment.ModelBases[policies[0].ID], Enabled: boolean(true),
	})
	require.Equal(t, "deployed-provider", approved.Models[0].Provider)
	require.Equal(t, "openai", approved.Models[0].Moderation)
}

func TestAdminModelBasePreconditionIsRequiredOnlyForModelEdits(t *testing.T) {
	s, _, _, _, policies := poolFixture(t)
	snapshot, err := s.AdminSnapshot(t.Context(), hosting.AdminQuery{})
	require.NoError(t, err)
	for _, update := range []*hosting.AdminUpdate{
		{Action: "model", TargetID: policies[0].ID, Enabled: boolean(true)},
		{Action: "model", TargetID: policies[0].ID, Enabled: boolean(true), ExpectedModelBase: "short"},
		{Action: "model", TargetID: policies[0].ID, Enabled: boolean(true), ExpectedModelBase: strings.Repeat("z", 64)},
		{Action: "pool", MonthlyMicrousd: number(1), ExpectedModelBase: snapshot.ModelBases[policies[0].ID]},
	} {
		_, err = s.AdminUpdate(t.Context(), "admin-user", update)
		require.ErrorIs(t, err, hosting.ErrAdminUpdate)
	}
}
