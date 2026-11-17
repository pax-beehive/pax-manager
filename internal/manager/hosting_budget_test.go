package manager

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

func TestStarterHTTPReportsMonthlyCreditWithoutFundingAnEmptyPool(t *testing.T) {
	s := hostingTestServer(t, "https://model.example.com/v1")
	s.hosting.Config.GrantMicrousd = 5_000_000
	s.hosting.Config.GlobalMicrousd = 0
	response := hostingRequest(t, s, "POST", "/api/v1/starter", "{}", "alice@example.com", "")
	require.Equal(t, http.StatusOK, response.Code)
	status := decodeData[hosting.Status](t, response.Body.Bytes())
	require.True(t, status.Enabled)
	require.False(t, status.PoolAvailable)
	require.NotNil(t, status.Starter)
	require.Equal(t, int64(5_000_000), status.Starter.Quota.RemainingMicrousd)
	now := s.hosting.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	require.Equal(t, start, status.Starter.Quota.PeriodStart)
	require.Equal(t, start.AddDate(0, 1, 0), status.Starter.Quota.ResetsAt)
	require.Equal(t, start, status.PoolPeriodStart)
	require.Equal(t, start.AddDate(0, 1, 0), status.PoolResetsAt)
	require.Contains(t, response.Body.String(), `"pool_available":false`)
	response = hostingRequest(t, s, "POST", "/api/v1/starter/wake", "{}", "alice@example.com", "")
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Contains(t, response.Body.String(), "starter_platform_budget_exhausted")
	response = hostingRequest(t, s, "GET", "/api/v1/starter", "", "bob@example.com", "")
	other := decodeData[hosting.Status](t, response.Body.Bytes())
	require.False(t, other.PoolAvailable)
	require.Nil(t, other.Starter)
}

func TestStarterHTTPMonthlyPoolRecoversWithNewPeriodMetadata(t *testing.T) {
	s := hostingTestServer(t, "https://model.example.com/v1")
	now := time.Date(2026, 11, 30, 23, 58, 0, 0, time.UTC)
	s.hosting.Now = func() time.Time { return now }
	s.hosting.Config.GrantMicrousd, s.hosting.Config.GlobalMicrousd = 5_000_000, 5000
	response := hostingRequest(t, s, "POST", "/api/v1/starter", "{}", "alice@example.com", "")
	require.Equal(t, http.StatusOK, response.Code)
	status := decodeData[hosting.Status](t, response.Body.Bytes())
	require.True(t, status.PoolAvailable)
	_, err := s.hosting.Wake(t.Context(), status.Starter.OwnerUserID)
	require.NoError(t, err)
	job, err := s.hosting.Claim(t.Context(), &hosting.Claim{HostID: "monthly-fixture", Capacity: 1})
	require.NoError(t, err)
	require.NotNil(t, job)
	reservation, err := s.hosting.Reserve(t.Context(), job.ModelToken, "consume-http-pool", 1000)
	require.NoError(t, err)
	require.NoError(t, s.hosting.Settle(t.Context(), reservation.ID, 0, 1000, nil, "fixture", true))
	response = hostingRequest(t, s, "GET", "/api/v1/starter", "", "alice@example.com", "")
	status = decodeData[hosting.Status](t, response.Body.Bytes())
	require.False(t, status.PoolAvailable)
	require.Equal(t, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), status.PoolResetsAt)
	now = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	response = hostingRequest(t, s, "GET", "/api/v1/starter", "", "alice@example.com", "")
	status = decodeData[hosting.Status](t, response.Body.Bytes())
	require.True(t, status.PoolAvailable)
	require.Equal(t, now, status.PoolPeriodStart)
	require.Equal(t, now.AddDate(0, 1, 0), status.PoolResetsAt)
	require.Equal(t, int64(5_000_000), status.Starter.Quota.RemainingMicrousd)
	require.NoError(
		t,
		s.hosting.Store.WithHostingState(t.Context(), func(state *hosting.State) error {
			require.Equal(t, int64(5000), state.SpentMicrousd)
			return nil
		}),
	)
}

func TestGatewayAccountingCannotReserveAgainstPausedPoolEvenForZeroPrice(t *testing.T) {
	s := hostingPoolServer(t)
	job := gatewayAccountingJob(t, s)
	s.hosting.Config.GlobalMicrousd = 0
	policy := s.hosting.Config.Models[1]
	response := gatewayAccountingRequest(t, s, map[string]any{
		"action": "reserve", "token": job.ModelToken, "request_id": "paused-pool-zero-price",
		"tokens": 1000, "model": policy.ID, "max_output_tokens": policy.MaxOutputTokens,
		"policy": policy,
	})
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Contains(t, response.Body.String(), "starter_platform_budget_exhausted")
	require.NoError(
		t,
		s.hosting.Store.WithHostingState(t.Context(), func(state *hosting.State) error {
			require.Empty(t, state.Requests)
			require.Zero(t, state.ReservedMicrousd)
			return nil
		}),
	)
}
