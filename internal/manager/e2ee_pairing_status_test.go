package manager

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestE2EEPairingStatusLifecycle(t *testing.T) {
	srv, _ := testServer(t, "pairing-status@example.com")
	fixture := testNodeAgent(t, srv, "pairing-status@example.com")
	base := "/api/v1/user/self/agents/" + fixture.agentID + "/e2ee/pairings"
	now := time.Now().UTC()
	srv.clock = func() time.Time { return now }
	create := func(id string) {
		result := e2eeKeyRequest(
			t,
			srv,
			http.MethodPost,
			base,
			fixture.userEmail,
			"",
			validCreatePairingBody(id),
		)
		require.Equal(t, http.StatusCreated, result.Code)
	}
	status := func(id, expected string) {
		result := e2eeKeyRequest(t, srv, http.MethodGet, base+"/"+id, fixture.userEmail, "", nil)
		require.Equal(t, http.StatusOK, result.Code)
		require.Equal(t, "private, no-store", result.Header().Get("Cache-Control"))
		data := decodeData[map[string]any](t, result.Body.Bytes())
		require.Equal(t, expected, data["status"])
		require.Equal(t, id, data["pairing_id"])
		require.NotContains(t, data, "pairing_secret")
	}
	// Given an accepted request, when inspected before approval, then it is pending.
	create("pair_pending")
	status("pair_pending", "pending")
	// Given an unapproved request, when its deadline passes, then it is expired.
	now = now.Add(11 * time.Minute)
	status("pair_pending", "expired")
	// Given an earlier request, when a replacement is created, then it is superseded.
	create("pair_new")
	status("pair_pending", "superseded")
	approved := e2eeKeyRequest(
		t,
		srv,
		http.MethodPost,
		base+"/pair_new/package",
		fixture.userEmail,
		"",
		validKeyPackageBody(),
	)
	require.Equal(t, http.StatusCreated, approved.Code)
	status("pair_new", "approved")
	// Approval is terminal: the approval deadline cannot expire an approved package.
	now = now.Add(48 * time.Hour)
	status("pair_new", "approved")
}

func TestE2EEPairingStatusRequiresOwnership(t *testing.T) {
	srv, _ := testServer(t, "pairing-owner@example.com")
	owner := testNodeAgent(t, srv, "pairing-owner@example.com")
	other := testNodeAgent(t, srv, "pairing-other@example.com")
	base := "/api/v1/user/self/agents/" + owner.agentID + "/e2ee/pairings"
	created := e2eeKeyRequest(
		t,
		srv,
		http.MethodPost,
		base,
		owner.userEmail,
		"",
		validCreatePairingBody("pair_private"),
	)
	require.Equal(t, http.StatusCreated, created.Code)
	denied := e2eeKeyRequest(t, srv, http.MethodGet, base+"/pair_private", other.userEmail, "", nil)
	require.NotEqual(t, http.StatusOK, denied.Code)
	require.NotContains(t, denied.Body.String(), "secret_commitment")
	missing := e2eeKeyRequest(
		t,
		srv,
		http.MethodGet,
		base+"/pair_missing",
		owner.userEmail,
		"",
		nil,
	)
	require.Equal(t, http.StatusNotFound, missing.Code)
}
