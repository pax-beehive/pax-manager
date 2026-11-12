package userapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const harnessUsageWeekly = `{"id":"seven_day","window_minutes":10080,"used_percent":3}`

func harnessUsageResult(view string) json.RawMessage {
	return json.RawMessage(`{"type":"harness_usage.status","harness_usage":` + view + `}`)
}

func readHarnessUsage(
	t *testing.T,
	f *harnessAuthFixture,
	harness string,
) (domain.HarnessUsageView, error) {
	t.Helper()
	status, data, err := f.svc.GetNodeHarnessUsage(
		t.Context(),
		auth.RequestMetadata{},
		"node",
		harness,
	)
	if err != nil {
		assert.Nil(t, data)
		return domain.HarnessUsageView{}, err
	}
	assert.Equal(t, http.StatusOK, status)
	return data.(domain.HarnessUsageData).HarnessUsage, nil
}

func TestHarnessUsageGivenAvailableUsageWhenQueryingThenReturnsSanitizedUncachedView(
	t *testing.T,
) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			f := newHarnessAuthFixture(t)
			f.client.result = harnessUsageResult(
				`{"harness":"` + harness + `","state":"available",` +
					`"observed_at":"2026-11-12T09:31:51.358658-08:00","plan":" max ",` +
					`"windows":[{"id":"five_hour","window_minutes":300,"used_percent":12.5,` +
					`"resets_at":"2026-11-12T20:00:00.5+00:00","access_token":"private-token"},` +
					`{"id":"seven_day_fable","window_minutes":10080,"scope":" Fable ","used_percent":0}],` +
					`"extra_usage":{"enabled":false,"monthly_limit":500},"error_code":"read_failed",` +
					`"raw_output":"private-output"}`,
			)
			view, err := readHarnessUsage(t, f, harness)
			require.NoError(t, err)
			assert.Equal(t, domain.HarnessUsageView{
				Harness:    harness,
				State:      "available",
				ObservedAt: "2026-11-12T17:31:51Z",
				Plan:       "max",
				Windows: []domain.HarnessUsageWindow{
					{
						ID:            "five_hour",
						WindowMinutes: 300,
						UsedPercent:   12.5,
						ResetsAt:      "2026-11-12T20:00:00Z",
					},
					{ID: "seven_day_fable", WindowMinutes: 10080, Scope: "Fable"},
				},
				ExtraUsage: &domain.HarnessUsageExtra{Enabled: false},
			}, view)
			raw, err := json.Marshal(view)
			require.NoError(t, err)
			assert.NotContains(t, string(raw), "private")
			assert.NotContains(t, string(raw), "monthly_limit")
			query, err := json.Marshal(f.client.query)
			require.NoError(t, err)
			assert.JSONEq(
				t,
				`{"type":"harness_usage.status","harness_usage_status":{"harness":"`+harness+`"}}`,
				string(query),
			)
			assert.Equal(t, "node", f.client.nodeID)
			require.True(t, f.client.hasDeadline)
			assert.WithinDuration(
				t,
				time.Now().Add(15*time.Second),
				f.client.deadline,
				5*time.Second,
			)
			_, err = readHarnessUsage(t, f, harness)
			require.NoError(t, err)
			assert.Equal(t, 2, f.client.calls, "manager must not cache usage")
		})
	}
}

// Views paxd produced from real Claude Code and Codex CLIs must pass through
// unchanged, so the console sees exactly what the device reported.
func TestHarnessUsageGivenRealPaxdOutputWhenQueryingThenPassesThroughUnchanged(t *testing.T) {
	for harness, view := range map[string]string{
		"claude": `{"harness":"claude","state":"available",` +
			`"observed_at":"2026-11-12T19:14:20Z","plan":"max","windows":[` +
			`{"id":"seven_day","window_minutes":10080,"used_percent":4,` +
			`"resets_at":"2026-11-16T09:00:00Z"},` +
			`{"id":"seven_day_fable","window_minutes":10080,"scope":"Fable",` +
			`"used_percent":0,"resets_at":"2026-11-16T09:00:00Z"}],` +
			`"extra_usage":{"enabled":false}}`,
		"codex": `{"harness":"codex","state":"available",` +
			`"observed_at":"2026-11-12T19:14:22Z","plan":"pro","windows":[` +
			`{"id":"primary","window_minutes":10080,"used_percent":31,` +
			`"resets_at":"2026-11-18T08:58:18Z"}]}`,
	} {
		t.Run(harness, func(t *testing.T) {
			f := newHarnessAuthFixture(t)
			f.client.result = harnessUsageResult(view)
			got, err := readHarnessUsage(t, f, harness)
			require.NoError(t, err)
			raw, err := json.Marshal(domain.HarnessUsageData{HarnessUsage: got})
			require.NoError(t, err)
			assert.JSONEq(t, `{"harness_usage":`+view+`}`, string(raw))
		})
	}
}

func harnessUsageWindowsFor(t *testing.T, windows string) []domain.HarnessUsageWindow {
	t.Helper()
	f := newHarnessAuthFixture(t)
	f.client.result = harnessUsageResult(
		`{"harness":"claude","state":"available","windows":` + windows + `}`,
	)
	view, err := readHarnessUsage(t, f, "claude")
	require.NoError(t, err)
	return view.Windows
}

func TestHarnessUsageGivenInvalidWindowsWhenQueryingThenDropsOnlyInvalidWindows(t *testing.T) {
	window := func(fields string) string {
		return `[{"id":"seven_day","window_minutes":10080,"used_percent":3` + fields + `}]`
	}
	for name, windows := range map[string]string{
		"uppercase id":       `[{"id":"Seven_Day","window_minutes":10080,"used_percent":3}]`,
		"dash id":            `[{"id":"seven-day","window_minutes":10080,"used_percent":3}]`,
		"empty id":           `[{"id":"","window_minutes":10080,"used_percent":3}]`,
		"long id":            `[{"id":"` + strings.Repeat("a", 65) + `","window_minutes":1,"used_percent":3}]`,
		"numeric id":         `[{"id":7,"window_minutes":10080,"used_percent":3}]`,
		"zero minutes":       `[{"id":"seven_day","window_minutes":0,"used_percent":3}]`,
		"too many minutes":   `[{"id":"seven_day","window_minutes":44641,"used_percent":3}]`,
		"fractional minutes": `[{"id":"seven_day","window_minutes":1.5,"used_percent":3}]`,
		"string minutes":     `[{"id":"seven_day","window_minutes":"300","used_percent":3}]`,
		"null minutes":       `[{"id":"seven_day","window_minutes":null,"used_percent":3}]`,
		"negative percent":   `[{"id":"seven_day","window_minutes":10080,"used_percent":-0.1}]`,
		"over 100 percent":   `[{"id":"seven_day","window_minutes":10080,"used_percent":100.1}]`,
		"null percent":       `[{"id":"seven_day","window_minutes":10080,"used_percent":null}]`,
		"string percent":     `[{"id":"seven_day","window_minutes":10080,"used_percent":"3"}]`,
		"missing percent":    `[{"id":"seven_day","window_minutes":10080}]`,
		"not an object":      `["seven_day",null,7]`,
		"not an array":       harnessUsageWeekly,
		"null windows":       `null`,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, harnessUsageWindowsFor(t, windows))
		})
	}
	weekly := domain.HarnessUsageWindow{ID: "seven_day", WindowMinutes: 10080, UsedPercent: 3}
	for name, tc := range map[string]struct {
		windows  string
		expected []domain.HarnessUsageWindow
	}{
		"unsafe scope":      {window(`,"scope":"bad\u0000scope"`), []domain.HarnessUsageWindow{weekly}},
		"long scope":        {window(`,"scope":"` + strings.Repeat("s", 65) + `"`), []domain.HarnessUsageWindow{weekly}},
		"non-string scope":  {window(`,"scope":{"name":"Opus"}`), []domain.HarnessUsageWindow{weekly}},
		"invalid resets_at": {window(`,"resets_at":"tomorrow"`), []domain.HarnessUsageWindow{weekly}},
		"duplicate id keeps first": {
			`[{"id":"seven_day","window_minutes":10080,"used_percent":3},` +
				`{"id":"seven_day","window_minutes":10080,"used_percent":99}]`,
			[]domain.HarnessUsageWindow{weekly},
		},
		"invalid duplicate does not shadow valid one": {
			`[{"id":"seven_day","window_minutes":10080,"used_percent":101},` + harnessUsageWeekly + `]`,
			[]domain.HarnessUsageWindow{weekly},
		},
		"boundaries": {
			`[{"id":"` + strings.Repeat("a", 64) + `","window_minutes":1,"used_percent":0,` +
				`"scope":"` + strings.Repeat("s", 64) + `"},` +
				`{"id":"b_9","window_minutes":44640,"used_percent":100}]`,
			[]domain.HarnessUsageWindow{
				{ID: strings.Repeat("a", 64), WindowMinutes: 1, Scope: strings.Repeat("s", 64)},
				{ID: "b_9", WindowMinutes: 44640, UsedPercent: 100},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.expected, harnessUsageWindowsFor(t, tc.windows))
		})
	}
}

func TestHarnessUsageGivenManyWindowsWhenQueryingThenKeepsFirstTwelve(t *testing.T) {
	items := make([]string, 0, 13)
	for i := range 13 {
		items = append(
			items,
			`{"id":"w`+strconv.Itoa(i)+`","window_minutes":60,"used_percent":`+strconv.Itoa(i)+`}`,
		)
	}
	windows := harnessUsageWindowsFor(t, "["+strings.Join(items, ",")+"]")
	require.Len(t, windows, 12)
	assert.Equal(t, "w0", windows[0].ID)
	assert.Equal(t, "w11", windows[11].ID)
}

func TestHarnessUsageGivenEachStateWhenQueryingThenKeepsOnlyStateSpecificFields(t *testing.T) {
	usage := `"windows":[` + harnessUsageWeekly + `],"extra_usage":{"enabled":true}`
	for name, tc := range map[string]struct {
		payload  string
		expected domain.HarnessUsageView
	}{
		"unsupported drops usage and error fields": {
			`"state":"unsupported","plan":"pro",` + usage + `,"error_code":"cli_unsupported"`,
			domain.HarnessUsageView{State: "unsupported", Plan: "pro"},
		},
		"unavailable keeps allowlisted cli code": {
			`"state":"unavailable","error_code":"cli_unsupported",` + usage,
			domain.HarnessUsageView{State: "unavailable", ErrorCode: "cli_unsupported"},
		},
		"unavailable keeps timeout": {
			`"state":"unavailable","error_code":"timeout"`,
			domain.HarnessUsageView{State: "unavailable", ErrorCode: "timeout"},
		},
		"unavailable keeps daemon code": {
			`"state":"unavailable","error_code":"daemon_unsupported"`,
			domain.HarnessUsageView{State: "unavailable", ErrorCode: "daemon_unsupported"},
		},
		"unavailable hides unknown code": {
			`"state":"unavailable","error_code":"Unsupported control request subtype: private"`,
			domain.HarnessUsageView{State: "unavailable", ErrorCode: "read_failed"},
		},
		"unavailable without code": {
			`"state":"unavailable"`,
			domain.HarnessUsageView{State: "unavailable", ErrorCode: "read_failed"},
		},
		"unavailable non-string code": {
			`"state":"unavailable","error_code":42`,
			domain.HarnessUsageView{State: "unavailable", ErrorCode: "read_failed"},
		},
		"available drops error code": {
			`"state":"available","error_code":"timeout"`,
			domain.HarnessUsageView{State: "available"},
		},
		"available without enabled flag drops extra usage": {
			`"state":"available","extra_usage":{"is_enabled":true}`,
			domain.HarnessUsageView{State: "available"},
		},
		"unsafe plan and observed_at are dropped": {
			`"state":"unsupported","plan":"max\nplan","observed_at":"yesterday"`,
			domain.HarnessUsageView{State: "unsupported"},
		},
		"oversized plan is dropped": {
			`"state":"unsupported","plan":"` + strings.Repeat("p", 129) + `"`,
			domain.HarnessUsageView{State: "unsupported"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newHarnessAuthFixture(t)
			f.client.result = harnessUsageResult(`{"harness":"claude",` + tc.payload + `}`)
			view, err := readHarnessUsage(t, f, "claude")
			require.NoError(t, err)
			tc.expected.Harness = "claude"
			assert.Equal(t, tc.expected, view)
		})
	}
}

func TestHarnessUsageGivenInvalidDaemonResponseWhenQueryingThenReturnsBadGateway(t *testing.T) {
	for name, result := range map[string]string{
		"unknown state":    string(harnessUsageResult(`{"harness":"claude","state":"logged_in"}`)),
		"missing state":    string(harnessUsageResult(`{"harness":"claude"}`)),
		"non-string state": string(harnessUsageResult(`{"harness":"claude","state":7}`)),
		"harness mismatch": string(harnessUsageResult(`{"harness":"codex","state":"available"}`)),
		"null usage":       string(harnessUsageResult(`null`)),
		"missing usage":    `{"type":"harness_usage.status"}`,
		"malformed":        `not json`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newHarnessAuthFixture(t)
			f.client.result = json.RawMessage(result)
			_, err := readHarnessUsage(t, f, "claude")
			assertHarnessAuthError(t, err, http.StatusBadGateway)
		})
	}
}

func TestHarnessUsageGivenDaemonOrTransportErrorWhenQueryingThenMapsWithoutLeakingDetails(
	t *testing.T,
) {
	f := newHarnessAuthFixture(t)
	f.client.result = json.RawMessage(
		`{"type":"harness_usage.status","error":{"code":"invalid_argument",` +
			`"message":"exactly one query payload is required"}}`,
	)
	view, err := readHarnessUsage(t, f, "codex")
	require.NoError(t, err)
	assert.Equal(t, domain.HarnessUsageView{
		Harness:   "codex",
		State:     "unavailable",
		ErrorCode: "daemon_unsupported",
	}, view)

	for code, status := range map[string]int{
		"not_found":      http.StatusNotFound,
		"conflict":       http.StatusConflict,
		"internal_error": http.StatusBadGateway,
		"":               http.StatusBadGateway,
	} {
		t.Run("daemon "+code, func(t *testing.T) {
			f := newHarnessAuthFixture(t)
			f.client.result = json.RawMessage(
				`{"type":"harness_usage.status","error":{"code":"` + code + `","message":"private"}}`,
			)
			_, err := readHarnessUsage(t, f, "claude")
			assertHarnessAuthError(t, err, status)
			var apiErr apperr.Error
			require.ErrorAs(t, err, &apiErr)
			assert.NotContains(t, apiErr.Message, "private")
		})
	}
	for name, tc := range map[string]struct {
		err    error
		status int
	}{
		"timeout":      {context.DeadlineExceeded, http.StatusGatewayTimeout},
		"disconnected": {errors.New("private"), http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			f := newHarnessAuthFixture(t)
			f.client.err = tc.err
			_, err := readHarnessUsage(t, f, "claude")
			assertHarnessAuthError(t, err, tc.status)
		})
	}
}

func TestHarnessUsageGivenInvalidOrUnauthorizedTargetWhenQueryingThenNeverContactsDaemon(
	t *testing.T,
) {
	f := newHarnessAuthFixture(t)
	for _, target := range []struct{ node, harness string }{
		{"", "claude"}, {" node", "claude"}, {"node", "pi"}, {"node", ""}, {"node", "Claude"},
	} {
		_, _, err := f.svc.GetNodeHarnessUsage(
			t.Context(),
			auth.RequestMetadata{},
			target.node,
			target.harness,
		)
		assertHarnessAuthError(t, err, http.StatusBadRequest)
	}
	principal := userPrincipal("owner", false)
	f.store.EXPECT().
		GetNode(mock.Anything, principal, "other-node").
		Return(domain.Node{NodeID: "other-node", OwnerUserID: "other-user"}, nil)
	_, _, err := f.svc.GetNodeHarnessUsage(
		t.Context(),
		auth.RequestMetadata{},
		"other-node",
		"claude",
	)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	f.store.EXPECT().
		GetNode(mock.Anything, principal, "missing-node").
		Return(domain.Node{}, domain.ErrNotFound)
	_, _, err = f.svc.GetNodeHarnessUsage(
		t.Context(),
		auth.RequestMetadata{},
		"missing-node",
		"claude",
	)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	assert.Zero(t, f.client.calls)

	for name, mutate := range map[string]func(*harnessAuthFixture){
		"nil client":        func(f *harnessAuthFixture) { f.svc.SetNodeControlClient(nil) },
		"unreported remote": func(f *harnessAuthFixture) { f.client.remoteID = "" },
		"disconnected":      func(f *harnessAuthFixture) { f.client.remoteIDErr = errors.New("private") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newHarnessAuthFixture(t)
			mutate(f)
			_, err := readHarnessUsage(t, f, "claude")
			assertHarnessAuthError(t, err, http.StatusServiceUnavailable)
			assert.Zero(t, f.client.calls)
		})
	}
}
