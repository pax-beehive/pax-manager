package manager

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

func gatewayAtomicPrepare(
	t *testing.T,
	s *Service,
	job *hosting.Job,
	protocol string,
) *hosting.PreparedRequest {
	t.Helper()
	response := gatewayAccountingRequest(t, s, map[string]any{
		"action": "prepare", "token": job.ModelToken, "request_id": "atomic-http-" + protocol,
		"model": "trial-model", "input_tokens": 100, "max_output_tokens": 0, "protocol": protocol,
	})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	prepared := decodeData[hosting.PreparedRequest](t, response.Body.Bytes())
	require.NotEmpty(t, prepared.ReservationID)
	return &prepared
}

func TestGatewayAtomicAccountingThreeProtocolsUseTwoAtomicCalls(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			s := hostingTestServer(t, "https://model.example.com/v1")
			job := gatewayAccountingJob(t, s)
			prepared := gatewayAtomicPrepare(t, s, job, protocol)
			require.EqualValues(t, 2048, prepared.MaxOutputTokens)
			require.EqualValues(t, 2148, prepared.Tokens)
			require.Equal(t, "trial-model", prepared.Policy.ID)
			s.hosting.Now = func() time.Time { return time.Now().Add(4 * time.Minute) }
			finish := map[string]any{
				"action": "finish", "reservation_id": prepared.ReservationID,
				"outcome": "completed", "started": true, "known": true,
				"moderation": map[string]any{
					"verdict": "allowed", "model": hosting.ModerationModel,
					"policy_version": prepared.Policy.PolicyVersion, "input_count": 1,
				},
				"usage": map[string]any{
					"input_tokens": 10, "output_tokens": 20, "upstream_request_id": "provider-atomic",
					"raw_usage": map[string]any{"total_tokens": 30, "prompt": "DO_NOT_STORE"},
				},
			}
			for range 2 {
				response := gatewayAccountingRequest(t, s, finish)
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			}
			r := gatewayStoredReservation(t, s, prepared.ReservationID)
			require.True(t, r.AtomicAdmission)
			require.True(t, r.GenerationStarted)
			require.Equal(t, protocol, r.Protocol)
			require.EqualValues(t, 110, r.ActualMicrousd)
			require.NotContains(t, string(r.RawUsage), "DO_NOT_STORE")
		})
	}
}

func TestGatewayAtomicAccountingReasoningMalformedValuesDoNotCreateHolds(t *testing.T) {
	s := hostingTestServer(t, "https://model.example.com/v1")
	job := gatewayAccountingJob(t, s)
	s.hosting.Config.Models = []hosting.ModelPolicy{{
		ID: "trial-model", Provider: "openrouter", UpstreamModel: "openai/gpt-6.1-sol",
		Moderation: "openai", PolicyVersion: "reasoning-v1", MaxOutputTokens: 2048, Enabled: true,
	}}
	for i, effort := range []any{nil, "", 1, true, []string{"high"}, "minimal", " HIGH"} {
		response := gatewayAccountingRequest(t, s, map[string]any{
			"action": "prepare", "token": job.ModelToken, "request_id": fmt.Sprintf("atomic-bad-%d", i),
			"model": "trial-model", "input_tokens": 100, "protocol": "chat", "reasoning_effort": effort,
		})
		require.Equal(t, http.StatusBadRequest, response.Code)
		require.Contains(t, response.Body.String(), "invalid_reasoning_effort")
	}
	require.NoError(
		t,
		s.hosting.Store.WithHostingState(t.Context(), func(state *hosting.State) error {
			require.Empty(t, state.Requests)
			require.Zero(t, state.ReservedMicrousd)
			return nil
		}),
	)
	response := gatewayAccountingRequest(t, s, map[string]any{
		"action": "prepare", "token": job.ModelToken, "request_id": "atomic-good-high",
		"model": "trial-model", "input_tokens": 100, "protocol": "chat", "reasoning_effort": "high",
	})
	require.Equal(t, http.StatusOK, response.Code)
}

func TestGatewayAtomicAccountingFinishReleasesBeforeModerationAndRejectsFabricatedUsage(
	t *testing.T,
) {
	s := hostingTestServer(t, "https://model.example.com/v1")
	prepared := gatewayAtomicPrepare(t, s, gatewayAccountingJob(t, s), "chat")
	for _, body := range []map[string]any{
		{"action": "finish", "reservation_id": prepared.ReservationID, "outcome": "completed", "started": true, "known": true},
		{"action": "finish", "reservation_id": prepared.ReservationID, "outcome": "blocked"},
		{"action": "finish", "reservation_id": prepared.ReservationID, "outcome": "cancelled", "usage": map[string]any{"input_tokens": 1}},
	} {
		response := gatewayAccountingRequest(t, s, body)
		require.Equal(t, http.StatusConflict, response.Code)
	}
	response := gatewayAccountingRequest(t, s, map[string]any{
		"action": "finish", "reservation_id": prepared.ReservationID, "outcome": "cancelled",
	})
	require.Equal(t, http.StatusOK, response.Code)
	r := gatewayStoredReservation(t, s, prepared.ReservationID)
	require.Equal(t, "cancelled", r.Outcome)
	require.Nil(t, r.Moderation)
	require.Zero(t, r.ActualMicrousd)
}

func TestGatewayAtomicAccountingFinishStillWorksAfterHostingDisabled(t *testing.T) {
	s := hostingTestServer(t, "https://model.example.com/v1")
	job := gatewayAccountingJob(t, s)
	prepared := gatewayAtomicPrepare(t, s, job, "chat")
	s.hosting.Config.GrantMicrousd = 0
	require.False(t, s.hosting.Enabled())
	response := gatewayAccountingRequest(t, s, map[string]any{
		"action": "prepare", "token": job.ModelToken, "request_id": "atomic-disabled-next",
		"model": "trial-model", "input_tokens": 100, "protocol": "chat",
	})
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	response = gatewayAccountingRequest(t, s, map[string]any{
		"action": "finish", "reservation_id": prepared.ReservationID,
		"outcome": "completed", "started": true, "known": true,
		"moderation": map[string]any{
			"verdict": "allowed", "model": hosting.ModerationModel,
			"policy_version": prepared.Policy.PolicyVersion,
		},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 20},
	})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.EqualValues(
		t,
		110,
		gatewayStoredReservation(t, s, prepared.ReservationID).ActualMicrousd,
	)
}
