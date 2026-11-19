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

func prepareAtomic(
	t *testing.T,
	s *hosting.Service,
	job *hosting.Job,
	policy hosting.ModelPolicy,
	protocol string,
) *hosting.PreparedRequest {
	t.Helper()
	prepared, err := s.Prepare(t.Context(), &hosting.PrepareRequest{
		Token: job.ModelToken, RequestID: "atomic-request-" + protocol, Model: policy.ID,
		InputTokens: 100, Protocol: protocol,
	})
	require.NoError(t, err)
	return prepared
}

func atomicDecision(policy hosting.ModelPolicy, verdict string) *hosting.ModerationDecision {
	decision := &hosting.ModerationDecision{
		Verdict: verdict, Model: hosting.ModerationModel, PolicyVersion: policy.PolicyVersion,
		InputCount: 1,
	}
	if policy.Moderation == "off" {
		decision.Model, decision.InputCount = "", 0
	}
	return decision
}

func readAtomicState(t *testing.T, store *storage.MemoryStore) *hosting.State {
	t.Helper()
	var result *hosting.State
	require.NoError(t, store.WithHostingState(t.Context(), func(state *hosting.State) error {
		result = state
		return nil
	}))
	return result
}

func TestAtomicAdmissionFixesPolicyCapAndBudgetsForThreeProtocols(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			s, store, owner, _, policies := poolFixture(t)
			job := poolJob(t, s, owner)
			prepared := prepareAtomic(t, s, job, policies[0], protocol)
			require.Equal(t, policies[0], prepared.Policy)
			require.EqualValues(t, 2048, prepared.MaxOutputTokens)
			require.EqualValues(t, 2148, prepared.Tokens)
			state := readAtomicState(t, store)
			r := state.Requests[prepared.ReservationID]
			require.True(t, r.AtomicAdmission)
			require.Equal(t, protocol, r.Protocol)
			require.EqualValues(t, 4196, r.Microusd)
			require.Equal(t, r.Microusd, state.ReservedMicrousd)
			require.Equal(t, r.Tokens, state.Environments[owner].Starter.Quota.ReservedTokens)
			_, err := s.Prepare(t.Context(), &hosting.PrepareRequest{
				Token: job.ModelToken, RequestID: r.RequestID, Model: policies[0].ID,
				InputTokens: 100, Protocol: protocol,
			})
			require.ErrorIs(t, err, hosting.ErrConflict)
			require.NoError(t, s.Finish(t.Context(), &hosting.FinishRequest{
				ReservationID: r.ID, Outcome: "completed", Started: true, Known: true,
				InputTokens: 10, OutputTokens: 20,
				Moderation: atomicDecision(policies[0], "allowed"),
			}))
			require.EqualValues(t, 50, readAtomicState(t, store).SpentMicrousd)
		})
	}
}

func TestAtomicAdmissionRejectsInvalidParametersWithoutHoldingBudget(t *testing.T) {
	for _, change := range []func(*hosting.PrepareRequest){
		func(r *hosting.PrepareRequest) { r.InputTokens = -1 },
		func(r *hosting.PrepareRequest) { r.InputTokens = 1_000_000 },
		func(r *hosting.PrepareRequest) { r.MaxOutputTokens = -1 },
		func(r *hosting.PrepareRequest) { r.MaxOutputTokens = 2049 },
		func(r *hosting.PrepareRequest) { r.Protocol = "unknown" },
	} {
		s, store, owner, _, policies := poolFixture(t)
		job := poolJob(t, s, owner)
		request := &hosting.PrepareRequest{
			Token: job.ModelToken, RequestID: "atomic-invalid", Model: policies[0].ID,
			InputTokens: 100, Protocol: "chat",
		}
		change(request)
		_, err := s.Prepare(t.Context(), request)
		require.ErrorIs(t, err, hosting.ErrInvalidModelRequest)
		state := readAtomicState(t, store)
		require.Empty(t, state.Requests)
		require.Zero(t, state.ReservedMicrousd)
	}
}

func TestAtomicAdmissionUsesRequestedLowerCapAndRejectsDisabledModel(t *testing.T) {
	s, store, owner, _, policies := poolFixture(t)
	job := poolJob(t, s, owner)
	s.Config.Models[0].Enabled = false
	request := &hosting.PrepareRequest{
		Token: job.ModelToken, RequestID: "atomic-lower-cap", Model: policies[0].ID,
		InputTokens: 100, MaxOutputTokens: 50, Protocol: "chat",
	}
	_, err := s.Prepare(t.Context(), request)
	require.ErrorIs(t, err, hosting.ErrConflict)
	s.Config.Models[0].Enabled = true
	prepared, err := s.Prepare(t.Context(), request)
	require.NoError(t, err)
	require.EqualValues(t, 50, prepared.MaxOutputTokens)
	require.EqualValues(t, 2048, prepared.Policy.MaxOutputTokens)
	require.EqualValues(t, 150, prepared.Tokens)
	require.EqualValues(t, 200, readAtomicState(t, store).ReservedMicrousd)
}

func TestAtomicAdmissionValidatesReasoningAgainstTrustedProviderAndUpstream(t *testing.T) {
	for _, upstream := range []string{
		"openai/gpt-6.1-sol", "openai/gpt-6.2-astra", "anthropic/claude-opus-5.5",
	} {
		for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
			t.Run(upstream+"/"+effort, func(t *testing.T) {
				s, _, owner, _, policies := poolFixture(t)
				s.Config.Models[0].Provider = "openrouter"
				s.Config.Models[0].UpstreamModel = upstream
				job := poolJob(t, s, owner)
				_, err := s.Prepare(t.Context(), &hosting.PrepareRequest{
					Token: job.ModelToken, RequestID: "atomic-reasoning", Model: policies[0].ID,
					InputTokens: 1, Protocol: "chat", ReasoningEffort: &effort,
				})
				require.NoError(t, err)
			})
		}
	}
	for _, test := range []struct{ provider, upstream, protocol, effort string }{
		{"openai", "openai/gpt-6.1-sol", "chat", "high"},
		{"openrouter", "openai/gpt-6.1-sol-unknown", "chat", "high"},
		{"openrouter", "vendor/unknown", "chat", "high"},
		{"openrouter", "openai/gpt-6.1-sol", "messages", "high"},
		{"openrouter", "openai/gpt-6.1-sol", "responses", "high"},
		{"openrouter", "openai/gpt-6.1-sol", "chat", ""},
		{"openrouter", "openai/gpt-6.1-sol", "chat", "minimal"},
		{"openrouter", "openai/gpt-6.1-sol", "chat", "HIGH"},
	} {
		s, store, owner, _, _ := poolFixture(t)
		s.Config.Models[0].ID = "openai/gpt-6.1-sol"
		s.Config.Model = s.Config.Models[0].ID
		s.Config.Models[0].Provider, s.Config.Models[0].UpstreamModel = test.provider, test.upstream
		job := poolJob(t, s, owner)
		_, err := s.Prepare(t.Context(), &hosting.PrepareRequest{
			Token: job.ModelToken, RequestID: "atomic-reasoning-invalid", Model: s.Config.Model,
			InputTokens: 1, Protocol: test.protocol, ReasoningEffort: &test.effort,
		})
		require.ErrorIs(t, err, hosting.ErrReasoningEffort, "%+v", test)
		require.Empty(t, readAtomicState(t, store).Requests)
	}
}

func TestAtomicAdmissionSerializesCompetingHolds(t *testing.T) {
	s, store, owner, _, policies := poolFixture(t)
	job := poolJob(t, s, owner)
	var wg sync.WaitGroup
	errors := make(chan error, 10)
	for i := range 10 {
		wg.Go(func() {
			_, err := s.Prepare(t.Context(), &hosting.PrepareRequest{
				Token: job.ModelToken, RequestID: fmt.Sprintf("atomic-race-%d", i),
				Model: policies[0].ID, InputTokens: 100, Protocol: "chat",
			})
			errors <- err
		})
	}
	wg.Wait()
	close(errors)
	admitted := 0
	for err := range errors {
		if err == nil {
			admitted++
		} else {
			require.ErrorIs(t, err, hosting.ErrConflict)
		}
	}
	require.Equal(t, 1, admitted)
	require.Len(t, readAtomicState(t, store).Requests, 1)
}

func TestAtomicAdmissionRequiresCurrentLease(t *testing.T) {
	s, store, owner, now, policies := poolFixture(t)
	job := poolJob(t, s, owner)
	*now = now.Add(4 * time.Minute)
	_, err := s.Prepare(t.Context(), &hosting.PrepareRequest{
		Token: job.ModelToken, RequestID: "atomic-expired", Model: policies[0].ID,
		InputTokens: 100, Protocol: "chat",
	})
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	require.Empty(t, readAtomicState(t, store).Requests)
}

func TestAtomicAdmissionEnforcesUserAndPlatformBudgetsIncludingZeroPrice(t *testing.T) {
	for _, test := range []struct {
		name   string
		pool   int64
		user   int64
		policy int
		err    error
	}{
		{"user exhausted", 100_000, 1, 0, hosting.ErrBudget},
		{"pool cannot fund hold", 1, 100_000, 0, hosting.ErrPlatformBudget},
		{"closed pool blocks zero price", 0, 100_000, 1, hosting.ErrPlatformBudget},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, store, owner, _, policies := poolFixture(t)
			job := poolJob(t, s, owner)
			require.NoError(
				t,
				store.WithHostingState(t.Context(), func(state *hosting.State) error {
					state.Admin.PoolMicrousd = number(test.pool)
					state.Environments[owner].Starter.Quota.GrantedMicrousd = test.user
					return nil
				}),
			)
			_, err := s.Prepare(t.Context(), &hosting.PrepareRequest{
				Token: job.ModelToken, RequestID: "atomic-budget-check", Model: policies[test.policy].ID,
				InputTokens: 100, Protocol: "chat",
			})
			require.ErrorIs(t, err, test.err)
			state := readAtomicState(t, store)
			require.Empty(t, state.Requests)
			require.Zero(t, state.ReservedMicrousd)
		})
	}
}
