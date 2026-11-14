//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// integrationAdminEmail is listed in ADMIN_EMAILS by docker-compose.integration.yml.
const integrationAdminEmail = "local@example.local"

type runtimeConfigEntry struct {
	Key     string          `json:"key"`
	Value   json.RawMessage `json:"value"`
	Source  string          `json:"source"`
	Version int64           `json:"version"`
}

type runtimeConfigView struct {
	Revision int64                `json:"revision"`
	Entries  []runtimeConfigEntry `json:"entries"`
}

func (f *integrationFixture) rawJSON(
	t *testing.T,
	method, path, email string,
	body any,
) (int, []byte) {
	t.Helper()
	req := f.newRequest(t, method, path, body)
	req.Header.Set("X-User-Email", email)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, readAll(t, resp)
}

// openFeatures moves feature gates to ga through the admin API. Features
// default to admin-only, and these integration tests act as ordinary users.
func openFeatures(t *testing.T, f *integrationFixture, keys ...string) {
	t.Helper()
	for _, key := range keys {
		for attempt := 0; ; attempt++ {
			view := getJSON[runtimeConfigView](t, f, "/api/v1/admin/config",
				map[string]string{"X-User-Email": integrationAdminEmail}, http.StatusOK)
			var current runtimeConfigEntry
			for _, entry := range view.Entries {
				if entry.Key == key {
					current = entry
				}
			}
			if bytes.Contains(current.Value, []byte(`"stage":"ga"`)) {
				break
			}
			status, raw := f.rawJSON(t, http.MethodPut, "/api/v1/admin/config/"+key,
				integrationAdminEmail, map[string]any{
					"value":            map[string]any{"stage": "ga", "beta_emails": []string{}},
					"expected_version": current.Version,
					"reason":           "integration tests act as ordinary users",
				})
			if status == http.StatusOK {
				break
			}
			if status != http.StatusConflict || attempt >= 5 {
				t.Fatalf("open %s: status %d: %s", key, status, raw)
			}
		}
	}
}

func TestRuntimeConfigAdminAPIAndFeatureGatesIntegration(t *testing.T) {
	fixture := newIntegrationFixture(t)
	fixture.waitForHealth(t)

	status, raw := fixture.rawJSON(t, http.MethodGet, "/api/v1/admin/config", fixture.email, nil)
	if status != http.StatusForbidden || !bytes.Contains(raw, []byte(`"not_admin"`)) {
		t.Fatalf("non-admin list: status %d: %s", status, raw)
	}
	health := getJSON[map[string]any](t, fixture, "/api/v1/health", nil, http.StatusOK)
	if health["config"] != "ok" {
		t.Fatalf("health config = %v", health["config"])
	}

	// A fresh deployment keeps Secretary admin-only unless an override exists.
	view := getJSON[runtimeConfigView](t, fixture, "/api/v1/admin/config",
		map[string]string{"X-User-Email": integrationAdminEmail}, http.StatusOK)
	var secretary runtimeConfigEntry
	for _, entry := range view.Entries {
		if entry.Key == "feature.secretary" {
			secretary = entry
		}
	}
	if secretary.Source == "default" {
		status, raw = fixture.rawJSON(
			t,
			http.MethodGet,
			"/api/v1/user/self/secretary",
			fixture.email,
			nil,
		)
		if status != http.StatusNotFound || !bytes.Contains(raw, []byte(`"feature_unavailable"`)) {
			t.Fatalf("gated secretary: status %d: %s", status, raw)
		}
	}

	// A plain setting round-trips through set and reset against real Postgres.
	status, raw = fixture.rawJSON(
		t,
		http.MethodPut,
		"/api/v1/admin/config/transport.journal_gc_disabled",
		integrationAdminEmail,
		map[string]any{"value": false, "expected_version": 0, "reason": "itest"},
	)
	if status != http.StatusOK && status != http.StatusConflict {
		t.Fatalf("set: status %d: %s", status, raw)
	}
	if status == http.StatusOK {
		entry := decodeEnvelope[runtimeConfigEntry](t, raw).Data
		if entry.Source != "db" || entry.Version < 1 {
			t.Fatalf("unexpected entry after set: %+v", entry)
		}
		status, raw = fixture.rawJSON(t, http.MethodDelete,
			"/api/v1/admin/config/transport.journal_gc_disabled?reason=itest&expected_version="+
				jsonInt(entry.Version), integrationAdminEmail, nil)
		if status != http.StatusOK {
			t.Fatalf("reset: status %d: %s", status, raw)
		}
	}
	features := getJSON[struct {
		Features map[string]bool `json:"features"`
	}](t, fixture, "/api/v1/user/self/features", fixture.userHeaders(), http.StatusOK)
	if len(features.Features) != 7 {
		t.Fatalf("features = %v", features.Features)
	}
}

func jsonInt(value int64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
