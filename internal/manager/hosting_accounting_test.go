package manager

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

const accountingPath = "/api/v1/hosting/gateway/accounting"

func gatewayAccountingRequest(t *testing.T, s *Service, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	return hostingRequest(t, s, "POST", accountingPath, string(raw), "", s.cfg.HostingGatewayKey)
}

func gatewayAccountingJob(t *testing.T, s *Service) *hosting.Job {
	t.Helper()
	response := hostingRequest(
		t,
		s,
		"POST",
		"/api/v1/starter/wake",
		"{}",
		"gateway@example.com",
		"",
	)
	require.Equal(t, http.StatusOK, response.Code)
	response = hostingRequest(t, s, "POST", "/api/v1/hosting/jobs/claim",
		`{"host_id":"gateway-host","capacity":1}`, "", s.cfg.HostingHostKey)
	require.Equal(t, http.StatusOK, response.Code)
	data := decodeData[struct {
		Job *hosting.Job `json:"job"`
	}](t, response.Body.Bytes())
	require.NotNil(t, data.Job)
	require.Equal(t, s.cfg.HostingGatewayURL, data.Job.ModelBaseURL)
	require.NotContains(t, response.Body.String(), s.cfg.HostingGatewayKey)
	return data.Job
}

func gatewayReserve(t *testing.T, s *Service, job *hosting.Job) string {
	t.Helper()
	response := gatewayAccountingRequest(t, s, map[string]any{
		"action": "reserve", "token": job.ModelToken, "request_id": "gateway-request",
		"tokens": 1000, "model": "trial-model", "max_output_tokens": 2048,
	})
	require.Equal(t, http.StatusOK, response.Code)
	data := decodeData[struct {
		ReservationID string `json:"reservation_id"`
	}](t, response.Body.Bytes())
	require.NotEmpty(t, data.ReservationID)
	return data.ReservationID
}

func gatewayStoredReservation(t *testing.T, s *Service, id string) *hosting.Reservation {
	t.Helper()
	var result *hosting.Reservation
	require.NoError(
		t,
		s.hosting.Store.WithHostingState(t.Context(), func(state *hosting.State) error {
			result = state.Requests[id]
			return nil
		}),
	)
	require.NotNil(t, result)
	return result
}

func TestGatewayAccountingAuthenticatesBeforeParsingAndBoundsServiceRequests(t *testing.T) {
	s := hostingTestServer(t, "https://model.example.com/v1")
	job := gatewayAccountingJob(t, s)
	for _, token := range []string{"", s.cfg.HostingHostKey, job.ModelToken, "wrong-service-key"} {
		response := hostingRequest(t, s, "POST", accountingPath, "{", "gateway@example.com", token)
		require.Equal(t, http.StatusUnauthorized, response.Code)
	}
	for _, body := range []string{"{", `{"action":"authorize","token":"x","prompt":"secret"}`, `{"action":"authorize"} {}`} {
		response := hostingRequest(t, s, "POST", accountingPath, body, "", s.cfg.HostingGatewayKey)
		require.Equal(t, http.StatusBadRequest, response.Code)
	}
	response := hostingRequest(
		t,
		s,
		"POST",
		accountingPath,
		strings.Repeat(" ", hostingAccountingMaxBody+1),
		"",
		s.cfg.HostingGatewayKey,
	)
	require.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
}

func TestGatewayAccountingAdmissionRequiresCurrentLeaseAndMatchingPolicy(t *testing.T) {
	s := hostingTestServer(t, "https://model.example.com/v1")
	job := gatewayAccountingJob(t, s)
	response := gatewayAccountingRequest(
		t,
		s,
		map[string]any{"action": "authorize", "token": job.ModelToken},
	)
	require.Equal(t, http.StatusOK, response.Code)
	for index, policy := range []map[string]any{
		{"model": "other", "max_output_tokens": 2048},
		{"model": "trial-model", "max_output_tokens": 8192},
	} {
		policy["action"], policy["token"], policy["tokens"] = "reserve", job.ModelToken, 1000
		policy["request_id"] = fmt.Sprintf("policy-request-%d", index)
		response = gatewayAccountingRequest(t, s, policy)
		require.Equal(t, http.StatusConflict, response.Code)
	}
	require.NoError(
		t,
		s.hosting.Store.WithHostingState(t.Context(), func(state *hosting.State) error {
			require.Empty(t, state.Requests)
			require.Zero(t, state.ReservedMicrousd)
			return nil
		}),
	)
	s.hosting.Now = func() time.Time { return time.Now().Add(4 * time.Minute) }
	response = gatewayAccountingRequest(
		t,
		s,
		map[string]any{"action": "authorize", "token": job.ModelToken},
	)
	require.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestGatewayAccountingAuditPrecedesGenerationAndSettlement(t *testing.T) {
	s := hostingTestServer(t, "https://model.example.com/v1")
	job := gatewayAccountingJob(t, s)
	id := gatewayReserve(t, s, job)
	settlement := map[string]any{
		"action": "settle", "reservation_id": id, "known": true,
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 20,
			"raw_usage": map[string]any{
				"prompt_tokens":     100,
				"completion_tokens": 20,
				"prompt":            "DO_NOT_STORE",
			},
			"upstream_request_id": "provider-1"},
	}
	response := gatewayAccountingRequest(t, s, settlement)
	require.Equal(t, http.StatusConflict, response.Code)
	response = gatewayAccountingRequest(
		t,
		s,
		map[string]any{"action": "begin", "reservation_id": id},
	)
	require.Equal(t, http.StatusConflict, response.Code)
	response = gatewayAccountingRequest(t, s, map[string]any{
		"action": "moderation", "reservation_id": id,
		"moderation": map[string]any{"verdict": "allowed", "model": hosting.ModerationModel,
			"provider_request_id": "modr-test", "input_count": 1},
	})
	require.Equal(t, http.StatusOK, response.Code)
	stored := gatewayStoredReservation(t, s, id)
	require.Equal(t, "allowed", stored.Moderation.Verdict)
	require.False(t, stored.GenerationStarted)
	require.NotZero(t, stored.Moderation.CheckedAt)
	response = gatewayAccountingRequest(
		t,
		s,
		map[string]any{"action": "begin", "reservation_id": id},
	)
	require.Equal(t, http.StatusOK, response.Code)
	response = gatewayAccountingRequest(t, s, settlement)
	require.Equal(t, http.StatusOK, response.Code)
	response = gatewayAccountingRequest(t, s, settlement)
	require.Equal(t, http.StatusOK, response.Code)
	stored = gatewayStoredReservation(t, s, id)
	require.Equal(t, "settled", stored.Status)
	require.Equal(t, int64(200), stored.ActualMicrousd)
	require.Equal(t, int64(120), stored.ActualTokens)
	require.NotContains(t, string(stored.RawUsage), "DO_NOT_STORE")
	require.NotContains(t, string(stored.RawUsage), "prompt\"")
}

func TestGatewayAccountingRejectedChecksReleaseBudgetWithoutGeneration(t *testing.T) {
	for _, verdict := range []string{"blocked", "unavailable"} {
		t.Run(verdict, func(t *testing.T) {
			s := hostingTestServer(t, "https://model.example.com/v1")
			job := gatewayAccountingJob(t, s)
			id := gatewayReserve(t, s, job)
			response := gatewayAccountingRequest(t, s, map[string]any{
				"action": "moderation", "reservation_id": id,
				"moderation": map[string]any{"verdict": verdict, "model": hosting.ModerationModel},
			})
			require.Equal(t, http.StatusOK, response.Code)
			stored := gatewayStoredReservation(t, s, id)
			require.Equal(t, "moderation_"+verdict, stored.Status)
			require.Zero(t, stored.ActualMicrousd)
			require.False(t, stored.GenerationStarted)
			require.NoError(
				t,
				s.hosting.Store.WithHostingState(t.Context(), func(state *hosting.State) error {
					require.Zero(t, state.SpentMicrousd)
					require.Zero(t, state.ReservedMicrousd)
					return nil
				}),
			)
		})
	}
}

func TestGatewayAccountingExpiredLeaseCannotBeginAndCanCancel(t *testing.T) {
	s := hostingTestServer(t, "https://model.example.com/v1")
	job := gatewayAccountingJob(t, s)
	id := gatewayReserve(t, s, job)
	response := gatewayAccountingRequest(t, s, map[string]any{
		"action": "moderation", "reservation_id": id,
		"moderation": map[string]any{"verdict": "allowed", "model": hosting.ModerationModel},
	})
	require.Equal(t, http.StatusOK, response.Code)
	s.hosting.Now = func() time.Time { return time.Now().Add(4 * time.Minute) }
	response = gatewayAccountingRequest(
		t,
		s,
		map[string]any{"action": "begin", "reservation_id": id},
	)
	require.Equal(t, http.StatusUnauthorized, response.Code)
	response = gatewayAccountingRequest(
		t,
		s,
		map[string]any{"action": "cancel", "reservation_id": id},
	)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "generation_canceled", gatewayStoredReservation(t, s, id).Status)
}
