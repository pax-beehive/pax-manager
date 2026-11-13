package userapi_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestHarnessAuthGivenClaudeCodeAgentTypeWhenSigningInThenUsesNativeNameAndAliasesInteroperate(
	t *testing.T,
) {
	f := newHarnessAuthFixture(t)
	f.client.result = json.RawMessage(`{"harness_auth":{"harness":"claude","state":"logged_out"}}`)
	f.client.commandAck = json.RawMessage(
		`{"command_id":"start","ok":true,"result":{"harness_auth":{"harness":"claude",` +
			`"session_id":"attempt","state":"starting","expires_at":"2026-11-11T00:05:00Z"}}}`,
	)
	status, data, err := f.svc.LoginNodeHarness(t.Context(), auth.RequestMetadata{},
		domain.LoginNodeHarnessRequest{
			NodeID: "node", Harness: "claude-code", CommandID: "start", Operation: "start",
		})
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, status)
	view := data.(domain.HarnessAuthData).HarnessAuth
	assert.Equal(t, "claude-code", view.Harness)
	assert.Equal(t, "attempt", view.SessionID)
	command, err := json.Marshal(f.client.command)
	require.NoError(t, err)
	assert.Contains(t, string(command), `"harness":"claude"`)
	assert.NotContains(t, string(command), "claude-code")

	f.client.result = json.RawMessage(
		`{"harness_auth":{"harness":"claude","session_id":"attempt","state":"exchanging",` +
			`"expires_at":"2026-11-11T00:05:00Z"}}`,
	)
	// The legacy alias and the agent type share one owned attempt.
	for _, harness := range []string{"claude", "claude-code"} {
		status, data, err = f.svc.GetNodeHarnessAuthStatus(
			t.Context(), auth.RequestMetadata{}, "node", harness, "attempt",
		)
		require.NoError(t, err, harness)
		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, harness, data.(domain.HarnessAuthData).HarnessAuth.Harness)
		query, err := json.Marshal(f.client.query)
		require.NoError(t, err)
		assert.Contains(t, string(query), `"harness":"claude"`)
	}
}

func TestHarnessAuthGivenAgentTypeWithoutNativeLoginWhenCheckingThenReportsUnsupported(
	t *testing.T,
) {
	for name, result := range map[string]string{
		"older paxd":   `{"type":"harness_auth.status","error":{"code":"invalid_argument"}}`,
		"current paxd": `{"harness_auth":{"harness":"kimi","state":"unsupported","logged_in":true,"account":{"email":"private@example.test"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newHarnessAuthFixture(t)
			f.client.result = json.RawMessage(result)
			status, data, err := f.svc.GetNodeHarnessAuthStatus(
				t.Context(), auth.RequestMetadata{}, "node", "kimi", "",
			)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, status)
			view := data.(domain.HarnessAuthData).HarnessAuth
			assert.Equal(t, domain.HarnessAuthView{Harness: "kimi", State: "unsupported"}, view)
		})
	}

	// Claude Code and Codex always have native login, so invalid_argument stays an error.
	f := newHarnessAuthFixture(t)
	f.client.result = json.RawMessage(
		`{"type":"harness_auth.status","error":{"code":"invalid_argument"}}`,
	)
	_, _, err := f.svc.GetNodeHarnessAuthStatus(
		t.Context(),
		auth.RequestMetadata{},
		"node",
		"codex",
		"",
	)
	assertHarnessAuthError(t, err, http.StatusBadRequest)

	// Starting a login for such an agent type is forwarded and the rejection surfaces as 400.
	f = newHarnessAuthFixture(t)
	f.client.result = json.RawMessage(
		`{"type":"harness_auth.status","error":{"code":"invalid_argument"}}`,
	)
	f.client.commandAck = json.RawMessage(
		`{"command_id":"start","ok":false,"error":{"code":"invalid_argument"}}`,
	)
	_, _, err = f.svc.LoginNodeHarness(
		t.Context(),
		auth.RequestMetadata{},
		domain.LoginNodeHarnessRequest{
			NodeID:    "node",
			Harness:   "kimi",
			CommandID: "start",
			Operation: "start",
		},
	)
	assertHarnessAuthError(t, err, http.StatusBadRequest)
	assert.Equal(t, 1, f.client.commandCalls)
}

func harnessUsageItemsFor(t *testing.T, items string) []domain.HarnessUsageItem {
	t.Helper()
	f := newHarnessAuthFixture(t)
	f.client.result = harnessUsageResult(
		`{"harness":"kimi","state":"available","items":` + items + `}`,
	)
	view, err := readHarnessUsage(t, f, "kimi")
	require.NoError(t, err)
	return view.Items
}

func TestHarnessUsageGivenItemsWhenQueryingThenSanitizesEachKind(t *testing.T) {
	limit, zero := 50.0, 0.0
	for name, tc := range map[string]struct {
		items    string
		expected []domain.HarnessUsageItem
	}{
		"percent keeps valid optional fields": {
			`[{"id":"weekly","label":" Weekly ","kind":"percent","used_percent":7,` +
				`"window_minutes":10080,"resets_at":"2026-11-16T01:00:00-08:00","severity":"warning",` +
				`"token":"private"}]`,
			[]domain.HarnessUsageItem{{
				ID: "weekly", Label: "Weekly", Kind: "percent", Severity: "warning",
				UsedPercent: usagePercent(7), WindowMinutes: 10080, ResetsAt: "2026-11-16T09:00:00Z",
			}},
		},
		"percent drops invalid optional fields only": {
			`[{"id":"w","label":"W","kind":"percent","used_percent":0,"window_minutes":1.5,` +
				`"resets_at":"soon","severity":"panic"}]`,
			[]domain.HarnessUsageItem{{ID: "w", Label: "W", Kind: "percent", UsedPercent: &zero}},
		},
		"percent without a valid percentage is dropped": {
			`[{"id":"a","label":"A","kind":"percent"},` +
				`{"id":"b","label":"B","kind":"percent","used_percent":null},` +
				`{"id":"c","label":"C","kind":"percent","used_percent":101},` +
				`{"id":"d","label":"D","kind":"percent","used_percent":"7"}]`,
			nil,
		},
		"amount keeps limit and allowlisted unit": {
			`[{"id":"credits","label":"API credits","kind":"amount","used":12.4,"limit":50,` +
				`"unit":"usd","resets_at":"2026-12-01T00:00:00Z"}]`,
			[]domain.HarnessUsageItem{{
				ID: "credits", Label: "API credits", Kind: "amount", Used: usagePercent(12.4),
				Limit: &limit, Unit: "usd", ResetsAt: "2026-12-01T00:00:00Z",
			}},
		},
		"amount drops unknown unit and non-positive limit": {
			`[{"id":"spend","label":"Spend","kind":"amount","used":0,"limit":0,"unit":"btc"}]`,
			[]domain.HarnessUsageItem{{ID: "spend", Label: "Spend", Kind: "amount", Used: &zero}},
		},
		"amount without valid usage is dropped": {
			`[{"id":"a","label":"A","kind":"amount","used":-1},{"id":"b","label":"B","kind":"amount"}]`,
			nil,
		},
		"status needs a safe value": {
			`[{"id":"extra","label":"Extra usage","kind":"status","value":"off","resets_at":"2026-12-01T00:00:00Z"},` +
				`{"id":"bad","label":"Bad","kind":"status","value":"a\nb"},` +
				`{"id":"none","label":"None","kind":"status"}]`,
			[]domain.HarnessUsageItem{{ID: "extra", Label: "Extra usage", Kind: "status", Value: "off"}},
		},
		"unknown kind, bad id, missing label and duplicates are dropped": {
			`[{"id":"x","label":"X","kind":"gauge","used_percent":1},` +
				`{"id":"Bad-ID","label":"B","kind":"status","value":"on"},` +
				`{"id":"nolabel","kind":"status","value":"on"},` +
				`{"id":"dup","label":"First","kind":"status","value":"1"},` +
				`{"id":"dup","label":"Second","kind":"status","value":"2"},"not an object"]`,
			[]domain.HarnessUsageItem{{ID: "dup", Label: "First", Kind: "status", Value: "1"}},
		},
		"items that are not a list are dropped": {`{"id":"x"}`, nil},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.expected, harnessUsageItemsFor(t, tc.items))
		})
	}
}

func TestHarnessUsageGivenManyItemsWhenQueryingThenKeepsFirstTwenty(t *testing.T) {
	entries := make([]string, 0, 25)
	for i := range 25 {
		entries = append(entries,
			`{"id":"i`+strconv.Itoa(i)+`","label":"Item","kind":"status","value":"on"}`)
	}
	items := harnessUsageItemsFor(t, "["+strings.Join(entries, ",")+"]")
	require.Len(t, items, 20)
	assert.Equal(t, "i19", items[19].ID)
}

func TestHarnessUsageGivenOlderPaxdWindowsWhenQueryingThenDerivesItemLabels(t *testing.T) {
	f := newHarnessAuthFixture(t)
	f.client.result = harnessUsageResult(`{"harness":"codex","state":"available","windows":[` +
		`{"id":"a","window_minutes":60,"used_percent":1},` +
		`{"id":"b","window_minutes":2880,"used_percent":2},` +
		`{"id":"c","window_minutes":90,"used_percent":3},` +
		`{"id":"d","window_minutes":10080,"scope":"Opus","used_percent":4}]}`)
	view, err := readHarnessUsage(t, f, "codex")
	require.NoError(t, err)
	labels := make([]string, 0, len(view.Items))
	for _, item := range view.Items {
		labels = append(labels, item.Label)
	}
	assert.Equal(t, []string{"1-hour", "2-day", "90-minute", "Weekly \u00b7 Opus"}, labels)
	assert.Equal(t, "Codex", view.HarnessLabel)
}

func TestHarnessUsageGivenAgentTypesWhenQueryingThenMapsNamesAndUnsupportedStates(t *testing.T) {
	f := newHarnessAuthFixture(t)
	f.client.result = harnessUsageResult(`{"harness":"claude","state":"unsupported"}`)
	view, err := readHarnessUsage(t, f, "claude-code")
	require.NoError(t, err)
	assert.Equal(t, domain.HarnessUsageView{
		Harness: "claude-code", HarnessLabel: "Claude Code", State: "unsupported",
	}, view)
	query, err := json.Marshal(f.client.query)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"harness_usage.status","harness_usage_status":{"harness":"claude"}}`,
		string(query))

	f = newHarnessAuthFixture(t)
	f.client.result = harnessUsageResult(
		`{"harness":"hermes","harness_label":"Hermes","state":"unsupported","items":[]}`,
	)
	view, err = readHarnessUsage(t, f, "hermes")
	require.NoError(t, err)
	assert.Equal(t, domain.HarnessUsageView{
		Harness: "hermes", HarnessLabel: "Hermes", State: "unsupported",
	}, view)

	f = newHarnessAuthFixture(t)
	f.client.result = harnessUsageResult(
		`{"harness":"kimi","harness_label":"Kimi\u202eCode","state":"unsupported"}`,
	)
	view, err = readHarnessUsage(t, f, "kimi")
	require.NoError(t, err)
	assert.Empty(t, view.HarnessLabel)

	// The response must name the native harness that was queried.
	f = newHarnessAuthFixture(t)
	f.client.result = harnessUsageResult(`{"harness":"claude-code","state":"unsupported"}`)
	_, err = readHarnessUsage(t, f, "claude-code")
	assertHarnessAuthError(t, err, http.StatusBadGateway)
}
