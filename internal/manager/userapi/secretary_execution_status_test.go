package userapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryExecutionAPIGivenPersistedStateThenExposesSafeDetailsWithoutChangingLegacyStates(
	t *testing.T,
) {
	for _, test := range []struct {
		state, reason, legacy string
		processing            bool
	}{
		{"running", "", "processing", true},
		{"needs_recovery", "lease_expired", "error", true},
		{"unknown", "execution_unknown", "error", true},
		{"blocked", "invalid_output", "error", false},
		{"blocked", "execution_failed", "error", false},
		{"blocked", "canceled", "error", false},
		{"blocked", "retry_required", "error", false},
		{"output_received", "", "idle", false},
		{"queued", "", "idle", false},
		{"waiting", "", "idle", false},
	} {
		t.Run(test.state+"/"+test.reason, func(t *testing.T) {
			store := &secretaryAPIFixture{
				status: domain.SecretaryStatus{
					SecretarySettings: domain.SecretarySettings{
						AgentID: "secretary",
						Enabled: true,
					},
					ExecutionState: test.state,
					BlockedReason:  test.reason,
					Processing:     test.processing,
				},
			}
			service := NewSecretaryService(
				workspacePrincipal{owner: "owner"},
				store,
				nil,
				func(context.Context, string, string) bool { return true },
			)
			response, err := service.Get(t.Context(), auth.RequestMetadata{})
			require.NoError(t, err)
			assert.Equal(t, test.state, response.ExecutionState)
			assert.Equal(t, test.reason, response.BlockedReason)
			assert.Equal(t, test.legacy, response.State)
			if test.legacy == "error" {
				assert.NotEmpty(t, response.LastError)
			}
		})
	}
}

func TestSecretaryExecutionAPIGivenUntrustedStatusStringsThenNeverLeaksRawContent(t *testing.T) {
	for _, state := range []string{"private source text", "blocked", ""} {
		store := &secretaryAPIFixture{
			status: domain.SecretaryStatus{
				SecretarySettings: domain.SecretarySettings{AgentID: "secretary", Enabled: true},
				ExecutionState:    state,
				BlockedReason:     "private source text",
				LastError:         "private source text",
			},
		}
		service := NewSecretaryService(
			workspacePrincipal{owner: "owner"},
			store,
			nil,
			func(context.Context, string, string) bool { return true },
		)
		response, err := service.Get(t.Context(), auth.RequestMetadata{})
		require.NoError(t, err)
		encoded, err := json.Marshal(response)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), "private source text")
		assert.Empty(t, response.BlockedReason)
		if state != "blocked" {
			assert.Empty(t, response.ExecutionState)
		}
	}
}

func TestSecretaryExecutionAPIGivenStoppedSettingsThenRetainsDisabledAndOfflinePriority(
	t *testing.T,
) {
	for _, enabled := range []bool{false, true} {
		store := &secretaryAPIFixture{
			status: domain.SecretaryStatus{
				SecretarySettings: domain.SecretarySettings{AgentID: "secretary", Enabled: enabled},
				ExecutionState:    "unknown",
				BlockedReason:     "execution_unknown",
			},
		}
		service := NewSecretaryService(workspacePrincipal{owner: "owner"}, store, nil, nil)
		response, err := service.Get(t.Context(), auth.RequestMetadata{})
		require.NoError(t, err)
		if enabled {
			assert.Equal(t, "waiting_for_agent", response.State)
		} else {
			assert.Equal(t, "disabled", response.State)
		}
		assert.Equal(t, "unknown", response.ExecutionState)
	}
}

func TestSecretaryExecutionAPIGivenConnectionRetryThenReportsSafeWaitingOrExhaustion(t *testing.T) {
	for _, test := range []struct {
		name, execution, reason, state, message string
		enabled, available, processing          bool
	}{
		{"backoff online", "waiting", "connection_backoff", "waiting", "", true, true, false},
		{"backoff offline", "waiting", "connection_backoff", "waiting", "", true, false, false},
		{"backoff lease", "waiting", "connection_backoff", "waiting", "", true, true, true},
		{"backoff disabled", "waiting", "connection_backoff", "disabled", "", false, true, false},
		{"exhausted online", "blocked", "connection_retries_exhausted", "error",
			"Secretary could not connect to the agent after automatic retries. Retry the run when the agent is available.",
			true, true, false},
		{"exhausted offline", "blocked", "connection_retries_exhausted", "error",
			"Secretary could not connect to the agent after automatic retries. Retry the run when the agent is available.",
			true, false, false},
		{"exhausted disabled", "blocked", "connection_retries_exhausted", "disabled",
			"Secretary could not connect to the agent after automatic retries. Retry the run when the agent is available.",
			false, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &secretaryAPIFixture{status: domain.SecretaryStatus{
				SecretarySettings: domain.SecretarySettings{
					AgentID: "secretary",
					Enabled: test.enabled,
				},
				ExecutionState: test.execution,
				BlockedReason:  test.reason,
				Processing:     test.processing,
				LastError:      "private raw tunnel credentials",
			}}
			service := NewSecretaryService(workspacePrincipal{owner: "owner"}, store, nil,
				func(context.Context, string, string) bool { return test.available })
			response, err := service.Get(t.Context(), auth.RequestMetadata{})
			require.NoError(t, err)
			assert.Equal(t, test.execution, response.ExecutionState)
			assert.Equal(t, test.reason, response.BlockedReason)
			assert.Equal(t, test.state, response.State)
			assert.Equal(t, test.message, response.LastError)
			assert.NotContains(t, response.LastError, "credentials")
		})
	}
}
