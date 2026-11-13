package manager

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestNodeHarnessUsageGivenConnectedNodeWhenReadingUsageThenForwardsQueryAndReturnsUncachedView(
	t *testing.T,
) {
	weekly := 3.0
	for name, tc := range map[string]struct {
		harness, native string
		queryResult     string
		expected        domain.HarnessUsageView
	}{
		"paxd with windows only, via the claude-code agent type": {
			"claude-code", "claude",
			`{"type":"harness_usage.status","harness_usage":{"harness":"claude","state":"available",` +
				`"observed_at":"2026-11-12T17:31:51Z","plan":"max","windows":[{"id":"seven_day",` +
				`"window_minutes":10080,"used_percent":3,"resets_at":"2026-11-16T09:00:00Z",` +
				`"access_token":"private-token"}],"extra_usage":{"enabled":false}}}`,
			domain.HarnessUsageView{
				Harness:      "claude-code",
				HarnessLabel: "Claude Code",
				State:        "available",
				ObservedAt:   "2026-11-12T17:31:51Z",
				Plan:         "max",
				Items: []domain.HarnessUsageItem{
					{
						ID: "seven_day", Label: "Weekly", Kind: "percent", UsedPercent: &weekly,
						WindowMinutes: 10080, ResetsAt: "2026-11-16T09:00:00Z",
					},
					{ID: "extra_usage", Label: "Extra usage", Kind: "status", Value: "off"},
				},
				Windows: []domain.HarnessUsageWindow{{
					ID:            "seven_day",
					WindowMinutes: 10080,
					UsedPercent:   3,
					ResetsAt:      "2026-11-16T09:00:00Z",
				}},
				ExtraUsage: &domain.HarnessUsageExtra{},
			},
		},
		"paxd without usage support": {
			"claude", "claude",
			`{"type":"harness_usage.status","error":{"code":"invalid_argument",` +
				`"message":"exactly one query payload is required"}}`,
			domain.HarnessUsageView{
				Harness:      "claude",
				HarnessLabel: "Claude Code",
				State:        "unavailable",
				ErrorCode:    "daemon_unsupported",
			},
		},
		"paxd with items for an agent type": {
			"kimi", "kimi",
			`{"type":"harness_usage.status","harness_usage":{"harness":"kimi",` +
				`"harness_label":"Kimi Code","state":"available","items":[{"id":"credits",` +
				`"label":"Credits","kind":"amount","used":12.5,"limit":50,"unit":"credits"}],` +
				`"windows":[{"id":"ignored","window_minutes":60,"used_percent":1}]}}`,
			domain.HarnessUsageView{
				Harness:      "kimi",
				HarnessLabel: "Kimi Code",
				State:        "available",
				Items: []domain.HarnessUsageItem{{
					ID: "credits", Label: "Credits", Kind: "amount",
					Used: usagePtr(12.5), Limit: usagePtr(50), Unit: "credits",
				}},
				Windows: []domain.HarnessUsageWindow{
					{ID: "ignored", WindowMinutes: 60, UsedPercent: 1},
				},
			},
		},
		"older paxd for an agent type without native support": {
			"opencode", "opencode",
			`{"type":"harness_usage.status","error":{"code":"invalid_argument",` +
				`"message":"expected claude or codex"}}`,
			domain.HarnessUsageView{Harness: "opencode", State: "unsupported"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv, registered := testNodeControlServer(t, "todd@example.com")
			ws, closeServer := dialNodeControlTunnel(t, srv, registered)
			defer closeServer()
			defer func() { _ = ws.Close() }()
			waitNodeControlConnection(t, srv.nodeControls, registered.NodeID)
			writeNodeControlHeartbeat(t, ws, registered.NodeID, "usage")
			waitNodeControlRemoteID(t, srv.nodeControls, registered.NodeID, "remote_prod")
			path := "/api/v1/user/self/nodes/" + registered.NodeID + "/harnesses/" +
				tc.harness + "/usage"
			rec := serveAsync(t, srv, http.MethodGet, path, "", func() {
				query := readNodeControlFrame(t, ws)
				assert.Equal(t, "query", query.Kind)
				assert.JSONEq(
					t,
					`{"type":"harness_usage.status","harness_usage_status":{"harness":"`+
						tc.native+`"}}`,
					string(query.Query),
				)
				require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(
					`{"kind":"response","request_id":`+strconv.Quote(query.RequestID)+
						`,"query_result":`+tc.queryResult+`}`,
				)))
			})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			assert.Equal(
				t,
				tc.expected,
				decodeData[domain.HarnessUsageData](t, rec.Body.Bytes()).HarnessUsage,
			)
			assert.NotContains(t, rec.Body.String(), "private")
		})
	}
}

func TestNodeHarnessUsageGivenInvalidOrForeignRequestWhenReadingThenReturnsUncachedErrors(
	t *testing.T,
) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	base := "/api/v1/user/self/nodes/" + registered.NodeID + "/harnesses/"

	rec := serve(srv, http.MethodGet, base+"Pi/usage", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	rec = serve(srv, http.MethodGet, base+"claude/usage", "")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	req := httptest.NewRequest(http.MethodGet, base+"claude/usage", nil)
	req.Header.Set("X-User-Email", "different@example.com")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

func usagePtr(v float64) *float64 { return &v }
