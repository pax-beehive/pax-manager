package hosting_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func fixture(t *testing.T) (*hosting.Service, *storage.MemoryStore, string, *time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	store := storage.NewMemoryStore(func() time.Time { return now })
	user, err := store.EnsureUser(t.Context(), "starter@example.com", "Starter", "user")
	require.NoError(t, err)
	config := hosting.Config{
		HostKey:         strings.Repeat("h", 32),
		TokenSecret:     strings.Repeat("s", 32),
		PublicURL:       "https://api.example.com",
		CloudURL:        "https://wsapi.example.com",
		GatewayURL:      "https://model.example.com/v1",
		GatewayKey:      strings.Repeat("g", 32),
		Model:           "trial-model",
		GrantMicrousd:   1_000_000,
		GrantTokens:     200000,
		GlobalMicrousd:  100000000,
		InputPrice:      1000000,
		OutputPrice:     5000000,
		MaxOutputTokens: 2048,
	}
	service := hosting.New(
		&config,
		store,
		func() time.Time { return now },
		func(_ context.Context, owner, node, agent string) error {
			if owner != user.UserID || node != "node-owned" || agent != "agent-owned" {
				return hosting.ErrUnauthorized
			}
			return nil
		},
	)
	return service, store, user.UserID, &now
}

func TestConcurrentEnsureGrantsOnceAndDoesNotAllocateCompute(t *testing.T) {
	s, _, owner, _ := fixture(t)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { _, err := s.Status(t.Context(), owner, true); require.NoError(t, err) })
	}
	wg.Wait()
	status, err := s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.Equal(t, int64(1000000), status.Starter.Quota.GrantedMicrousd)
	require.Equal(t, "sleeping", status.Starter.State)
	job, err := s.Claim(t.Context(), &hosting.Claim{HostID: "host-a", Capacity: 1})
	require.NoError(t, err)
	require.Nil(t, job)
}

func TestHostLeaseFencesOldModelCredentialsAndPreservesHostAffinity(t *testing.T) {
	s, store, owner, now := fixture(t)
	_, err := s.Wake(t.Context(), owner)
	require.NoError(t, err)
	first, err := s.Claim(t.Context(), &hosting.Claim{HostID: "host-a", Capacity: 1})
	require.NoError(t, err)
	require.NotNil(t, first)
	registration, err := store.ResolveRegistrationToken(
		t.Context(),
		hashTest(first.RegistrationToken),
	)
	require.NoError(t, err)
	require.Equal(t, owner, registration.UserID)
	_, err = s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	_, err = store.ResolveRegistrationToken(t.Context(), hashTest(first.RegistrationToken))
	require.Error(t, err)
	other, err := s.Claim(t.Context(), &hosting.Claim{HostID: "host-b", Capacity: 1})
	require.NoError(t, err)
	require.Nil(t, other)
	*now = now.Add(4 * time.Minute)
	_, err = s.Reserve(t.Context(), first.ModelToken, "expired-request", 1000)
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	_, err = s.Wake(t.Context(), owner)
	require.NoError(t, err)
	other, err = s.Claim(t.Context(), &hosting.Claim{HostID: "host-b", Capacity: 1})
	require.NoError(t, err)
	require.Nil(t, other)
	second, err := s.Claim(t.Context(), &hosting.Claim{HostID: "host-a", Capacity: 1})
	require.NoError(t, err)
	require.Greater(t, second.Generation, first.Generation)
	_, err = s.Update(
		t.Context(),
		&hosting.Update{
			HostID:     "host-a",
			JobID:      first.JobID,
			Generation: first.Generation,
			LeaseToken: first.LeaseToken,
		},
		false,
	)
	require.Error(t, err)
}

func TestReadyRejectsUnrelatedRuntimeAndRenewsSameModelToken(t *testing.T) {
	s, _, owner, now := fixture(t)
	_, err := s.Wake(t.Context(), owner)
	require.NoError(t, err)
	job, err := s.Claim(t.Context(), &hosting.Claim{HostID: "host-a", Capacity: 1})
	require.NoError(t, err)
	update := hosting.Update{
		HostID:     "host-a",
		JobID:      job.JobID,
		Generation: job.Generation,
		LeaseToken: job.LeaseToken,
		State:      "ready",
		NodeID:     "another-node",
		AgentID:    "another-agent",
	}
	_, err = s.Update(t.Context(), &update, true)
	require.ErrorIs(t, err, hosting.ErrUnauthorized)
	update.NodeID = "node-owned"
	update.AgentID = "agent-owned"
	*now = now.Add(time.Minute)
	_, err = s.Update(t.Context(), &update, true)
	require.NoError(t, err)
	r, err := s.Reserve(t.Context(), job.ModelToken, "request-renewed", 1000)
	require.NoError(t, err)
	require.NotNil(t, r)
	status, err := s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.Equal(t, "agent-owned", status.Starter.AgentID)
}

func TestConcurrentReservationsCannotOverspendAndSettlementIsIdempotent(t *testing.T) {
	s, _, owner, _ := fixture(t)
	_, err := s.Wake(t.Context(), owner)
	require.NoError(t, err)
	job, err := s.Claim(t.Context(), &hosting.Claim{HostID: "host-a", Capacity: 1})
	require.NoError(t, err)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var accepted []*hosting.Reservation
	for i := range 10 {
		wg.Go(func() {
			reservation, err := s.Reserve(
				t.Context(),
				job.ModelToken,
				fmt.Sprintf("request-%03d", i),
				1000,
			)
			if err == nil {
				mu.Lock()
				accepted = append(accepted, reservation)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	require.Len(t, accepted, 1)
	r := accepted[0]
	require.NoError(
		t,
		s.Settle(
			t.Context(),
			r.ID,
			100,
			20,
			[]byte(`{"prompt_tokens":100,"completion_tokens":20}`),
			"upstream-1",
			true,
		),
	)
	require.NoError(t, s.Settle(t.Context(), r.ID, 999, 999, nil, "duplicate", true))
	status, err := s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.Equal(t, int64(200), status.Starter.Quota.SpentMicrousd)
	require.Zero(t, status.Starter.Quota.ReservedMicrousd)
	_, err = s.Reserve(t.Context(), job.ModelToken, "overspend-request", 300000)
	require.ErrorIs(t, err, hosting.ErrBudget)
}

func TestMissingUsageConservativelyChargesAndScopesOwner(t *testing.T) {
	s, _, owner, _ := fixture(t)
	_, err := s.Wake(t.Context(), owner)
	require.NoError(t, err)
	job, err := s.Claim(t.Context(), &hosting.Claim{HostID: "host-a", Capacity: 1})
	require.NoError(t, err)
	r, err := s.Reserve(t.Context(), job.ModelToken, "unknown-usage", 1000)
	require.NoError(t, err)
	require.NoError(t, s.Settle(t.Context(), r.ID, 0, 0, nil, "", false))
	status, err := s.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.Equal(t, r.Microusd, status.Starter.Quota.SpentMicrousd)
	other, err := s.Status(t.Context(), "another-owner", false)
	require.NoError(t, err)
	require.Nil(t, other.Starter)
}

func TestRestartRecoversOrphanReservationWithoutRefundingOrBlockingForever(t *testing.T) {
	s, store, owner, now := fixture(t)
	_, err := s.Wake(t.Context(), owner)
	require.NoError(t, err)
	job, err := s.Claim(t.Context(), &hosting.Claim{HostID: "host-a", Capacity: 1})
	require.NoError(t, err)
	r, err := s.Reserve(t.Context(), job.ModelToken, "orphan-request", 1000)
	require.NoError(t, err)
	*now = now.Add(4 * time.Minute)
	restarted := hosting.New(&s.Config, store, s.Now, s.ValidateBinding)
	status, err := restarted.Status(t.Context(), owner, false)
	require.NoError(t, err)
	require.Zero(t, status.Starter.Quota.ReservedMicrousd)
	require.Equal(t, r.Microusd, status.Starter.Quota.SpentMicrousd)
	_, err = restarted.Wake(t.Context(), owner)
	require.NoError(t, err)
	next, err := restarted.Claim(t.Context(), &hosting.Claim{HostID: "host-a", Capacity: 1})
	require.NoError(t, err)
	_, err = restarted.Reserve(t.Context(), next.ModelToken, "next-generation-request", 1000)
	require.NoError(t, err)
}

func hashTest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
