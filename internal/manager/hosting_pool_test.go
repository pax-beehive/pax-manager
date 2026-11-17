package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

const hostingPoolRegistry = `{
 "policy_version":"pool-v1","default_model":"example/provider-a",
 "models":[
  {"id":"example/provider-a","provider":"provider-a","upstream_model":"vendor/model-a","route":"example/provider-a","max_output_tokens":2048,"input_microusd_per_million":1000000,"output_microusd_per_million":2000000,"enabled":true},
  {"id":"example/provider-b","provider":"provider-b","upstream_model":"vendor/model-b","route":"example/provider-b","moderation":"off","max_output_tokens":512,"input_microusd_per_million":0,"output_microusd_per_million":0,"enabled":true}
 ]
}`

func hostingPoolServer(t *testing.T) *Service {
	t.Helper()
	s := hostingTestServer(t, "https://model.example.com/v1")
	path := filepath.Join(t.TempDir(), "models.json")
	require.NoError(t, os.WriteFile(path, []byte(hostingPoolRegistry), 0o600))
	s.cfg.HostingModelsFile = path
	s.configureHosting(s.hosting.Store.(Store))
	require.True(t, s.hosting.Enabled())
	return s
}

func hostingPoolReserve(
	t *testing.T,
	s *Service,
	token string,
	policy hosting.ModelPolicy,
	requestID string,
) string {
	t.Helper()
	response := gatewayAccountingRequest(t, s, map[string]any{
		"action": "reserve", "token": token, "request_id": requestID, "tokens": 1000,
		"model": policy.ID, "max_output_tokens": policy.MaxOutputTokens, "policy": policy,
	})
	require.Equal(t, 200, response.Code)
	data := decodeData[struct {
		ReservationID string `json:"reservation_id"`
	}](t, response.Body.Bytes())
	return data.ReservationID
}

func TestHostingPoolLoadsOperatorFileAndRequiresExactPolicy(t *testing.T) {
	s := hostingPoolServer(t)
	job := gatewayAccountingJob(t, s)
	require.Equal(t, "example/provider-a", job.ModelID)
	require.Equal(t, int64(2048), job.ModelMaxOutputTokens)
	policy := s.hosting.Config.Models[1]
	response := gatewayAccountingRequest(t, s, map[string]any{
		"action": "reserve", "token": job.ModelToken, "request_id": "missing-policy", "tokens": 1000,
		"model": policy.ID, "max_output_tokens": policy.MaxOutputTokens,
	})
	require.Equal(t, 409, response.Code)
	modified := policy
	modified.InputPrice = 1
	response = gatewayAccountingRequest(t, s, map[string]any{
		"action": "reserve", "token": job.ModelToken, "request_id": "modified-policy", "tokens": 1000,
		"model": policy.ID, "max_output_tokens": policy.MaxOutputTokens, "policy": modified,
	})
	require.Equal(t, 409, response.Code)
	id := hostingPoolReserve(t, s, job.ModelToken, policy, "approved-off-model")
	response = gatewayAccountingRequest(t, s, map[string]any{
		"action": "moderation", "reservation_id": id,
		"moderation": map[string]any{"verdict": "skipped_by_policy", "policy_version": "pool-v1"},
	})
	require.Equal(t, 200, response.Code)
	response = gatewayAccountingRequest(
		t,
		s,
		map[string]any{"action": "begin", "reservation_id": id},
	)
	require.Equal(t, 200, response.Code)
	response = gatewayAccountingRequest(t, s, map[string]any{
		"action": "settle", "reservation_id": id, "known": true,
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 20},
	})
	require.Equal(t, 200, response.Code)
	stored := gatewayStoredReservation(t, s, id)
	require.Equal(t, policy, *stored.Policy)
	require.Equal(t, "skipped_by_policy", stored.Moderation.Verdict)
	require.Zero(t, stored.ActualMicrousd)
	require.Equal(t, int64(120), stored.ActualTokens)
}

func TestHostingPoolRequiresAuditMetadataMatchingSkippedPolicy(t *testing.T) {
	s := hostingPoolServer(t)
	job := gatewayAccountingJob(t, s)
	policy := s.hosting.Config.Models[1]
	id := hostingPoolReserve(t, s, job.ModelToken, policy, "check-skipped-audit")
	for _, metadata := range []map[string]any{
		{"verdict": "skipped_by_policy", "policy_version": "old-version"},
		{"verdict": "skipped_by_policy", "policy_version": "pool-v1", "model": hosting.ModerationModel},
		{"verdict": "skipped_by_policy", "policy_version": "pool-v1", "input_count": 1},
		{"verdict": "allowed", "policy_version": "pool-v1", "model": hosting.ModerationModel},
	} {
		response := gatewayAccountingRequest(
			t,
			s,
			map[string]any{"action": "moderation", "reservation_id": id, "moderation": metadata},
		)
		require.Equal(t, 409, response.Code)
	}
	require.Equal(t, "pending", gatewayStoredReservation(t, s, id).Moderation.Verdict)
}

func TestHostingPoolRateLimitsBlockedRequestsWithoutSpendingCredits(t *testing.T) {
	s := hostingPoolServer(t)
	s.hosting.Config.RequestsPerMinute = 2
	job := gatewayAccountingJob(t, s)
	policy := s.hosting.Config.Models[0]
	for _, id := range []string{"first-blocked-request", "second-blocked-request"} {
		reservationID := hostingPoolReserve(t, s, job.ModelToken, policy, id)
		response := gatewayAccountingRequest(t, s, map[string]any{
			"action": "moderation", "reservation_id": reservationID,
			"moderation": map[string]any{
				"verdict":        "blocked",
				"model":          hosting.ModerationModel,
				"policy_version": "pool-v1",
			},
		})
		require.Equal(t, 200, response.Code)
	}
	response := gatewayAccountingRequest(t, s, map[string]any{
		"action": "reserve", "token": job.ModelToken, "request_id": "third-blocked-request", "tokens": 1000,
		"model": policy.ID, "max_output_tokens": policy.MaxOutputTokens, "policy": policy,
	})
	require.Equal(t, 429, response.Code)
	require.Contains(t, response.Body.String(), "starter_rate_limited")
	require.Equal(t, "60", response.Header().Get("Retry-After"))
	require.NoError(
		t,
		s.hosting.Store.WithHostingState(t.Context(), func(state *hosting.State) error {
			require.Len(t, state.Requests, 2)
			require.Zero(t, state.SpentMicrousd)
			require.Zero(t, state.ReservedMicrousd)
			return nil
		}),
	)
}

func TestHostingInvalidPolicyFileCannotFallBackToLegacyMode(t *testing.T) {
	s := hostingTestServer(t, "https://model.example.com/v1")
	s.cfg.HostingModelsFile = filepath.Join(t.TempDir(), "missing.json")
	s.configureHosting(s.hosting.Store.(Store))
	require.False(t, s.hosting.Enabled())
}

func TestLegacyGatewayCannotSkipModerationThroughClaimedPoolPolicy(t *testing.T) {
	s := hostingTestServer(t, "https://model.example.com/v1")
	job := gatewayAccountingJob(t, s)
	policy := hosting.ModelPolicy{
		ID:              "trial-model",
		Provider:        "configured",
		UpstreamModel:   "vendor/model",
		Moderation:      "openai",
		PolicyVersion:   hosting.ModerationPolicy,
		MaxOutputTokens: 2048,
		Enabled:         true,
	}
	id := hostingPoolReserve(t, s, job.ModelToken, policy, "legacy-fallback-policy")
	response := gatewayAccountingRequest(t, s, map[string]any{
		"action": "moderation", "reservation_id": id,
		"moderation": map[string]any{
			"verdict":        "skipped_by_policy",
			"policy_version": hosting.ModerationPolicy,
		},
	})
	require.Equal(t, 409, response.Code)
	stored := gatewayStoredReservation(t, s, id)
	require.Nil(t, stored.Policy)
	raw, err := json.Marshal(stored)
	require.NoError(t, err)
	require.NotContains(t, string(raw), job.ModelToken)
}
