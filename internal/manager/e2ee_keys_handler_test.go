package manager

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2EEKeyDistributionGivenNewBrowserWhenNodeCompletesPairingThenBrowserLoadsOpaquePackage(
	t *testing.T,
) {
	srv, _ := testServer(t, "e2ee-keys@example.com")
	fixture := testNodeAgent(t, srv, "e2ee-keys@example.com")
	pairingPath := "/api/v1/user/self/agents/" + fixture.agentID + "/e2ee/pairings"
	createBody := map[string]any{
		"pairing_id": "pair_browser_1", "device_id": "device_browser_1",
		"device_name": "Chrome on Mac", "key_epoch": 1,
		"recipient_public_key": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 65)),
		"secret_commitment":    base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{5}, 32)),
	}
	create := e2eeKeyRequest(
		t,
		srv,
		http.MethodPost,
		pairingPath,
		fixture.userEmail,
		"",
		createBody,
	)
	require.Equal(t, http.StatusCreated, create.Code, create.Body.String())

	nodePairingPath := "/api/v1/node/agents/" + fixture.agentID + "/e2ee/pairings/pair_browser_1"
	loadedRequest := e2eeKeyRequest(
		t,
		srv,
		http.MethodGet,
		nodePairingPath,
		"",
		fixture.nodeAPIKey,
		nil,
	)
	require.Equal(t, http.StatusOK, loadedRequest.Code, loadedRequest.Body.String())
	requestData := decodeData[e2eePairingResponse](t, loadedRequest.Body.Bytes())
	assert.Equal(t, createBody["secret_commitment"], requestData.SecretCommitment)

	packageBody := map[string]any{
		"sender_ephemeral_public_key": base64.StdEncoding.EncodeToString(
			bytes.Repeat([]byte{6}, 65),
		),
		"nonce": base64.StdEncoding.EncodeToString([]byte("123456789012")),
		"ciphertext": base64.StdEncoding.EncodeToString(
			bytes.Repeat([]byte{7}, 48),
		),
	}
	complete := e2eeKeyRequest(
		t, srv, http.MethodPost, nodePairingPath+"/package", "", fixture.nodeAPIKey, packageBody,
	)
	require.Equal(t, http.StatusCreated, complete.Code, complete.Body.String())

	packagePath := "/api/v1/user/self/agents/" + fixture.agentID +
		"/e2ee/key-packages/device_browser_1?key_epoch=1"
	loadedPackage := e2eeKeyRequest(t, srv, http.MethodGet, packagePath, fixture.userEmail, "", nil)
	require.Equal(t, http.StatusOK, loadedPackage.Code, loadedPackage.Body.String())
	packageData := decodeData[e2eeKeyPackageResponse](t, loadedPackage.Body.Bytes())
	assert.Equal(t, packageBody["ciphertext"], packageData.Ciphertext)
	assert.Equal(t, "pair_browser_1", packageData.PairingID)
}

func TestE2EEKeyDistributionGivenDifferentNodeWhenReadingPairingThenItCannotAccessIt(t *testing.T) {
	srv, _ := testServer(t, "e2ee-owner@example.com")
	owner := testNodeAgent(t, srv, "e2ee-owner@example.com")
	other := testNodeAgent(t, srv, "e2ee-other@example.com")
	pairingPath := "/api/v1/user/self/agents/" + owner.agentID + "/e2ee/pairings"
	create := e2eeKeyRequest(
		t,
		srv,
		http.MethodPost,
		pairingPath,
		owner.userEmail,
		"",
		map[string]any{
			"pairing_id": "pair_owner_1", "device_id": "device_owner_1", "key_epoch": 1,
			"recipient_public_key": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 65)),
			"secret_commitment":    base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{5}, 32)),
		},
	)
	require.Equal(t, http.StatusCreated, create.Code, create.Body.String())

	path := "/api/v1/node/agents/" + owner.agentID + "/e2ee/pairings/pair_owner_1"
	response := e2eeKeyRequest(t, srv, http.MethodGet, path, "", other.nodeAPIKey, nil)
	assert.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
}

func TestE2EEKeyDistributionGivenExistingBrowserWhenItApprovesRequestThenNewBrowserLoadsPackage(
	t *testing.T,
) {
	srv, _ := testServer(t, "e2ee-browser-approval@example.com")
	fixture := testNodeAgent(t, srv, "e2ee-browser-approval@example.com")
	pairingPath := "/api/v1/user/self/agents/" + fixture.agentID + "/e2ee/pairings"
	create := e2eeKeyRequest(
		t,
		srv,
		http.MethodPost,
		pairingPath,
		fixture.userEmail,
		"",
		validCreatePairingBody("pair_browser_approval"),
	)
	require.Equal(t, http.StatusCreated, create.Code, create.Body.String())

	listed := e2eeKeyRequest(t, srv, http.MethodGet, pairingPath, fixture.userEmail, "", nil)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	requests := decodeData[[]e2eePairingResponse](t, listed.Body.Bytes())
	require.Len(t, requests, 1)
	assert.Equal(t, "pair_browser_approval", requests[0].PairingID)

	completePath := pairingPath + "/pair_browser_approval/package"
	complete := e2eeKeyRequest(
		t,
		srv,
		http.MethodPost,
		completePath,
		fixture.userEmail,
		"",
		validKeyPackageBody(),
	)
	require.Equal(t, http.StatusCreated, complete.Code, complete.Body.String())

	loaded := e2eeKeyRequest(
		t,
		srv,
		http.MethodGet,
		"/api/v1/user/self/agents/"+fixture.agentID+"/e2ee/key-packages/device_browser_1?key_epoch=1",
		fixture.userEmail,
		"",
		nil,
	)
	require.Equal(t, http.StatusOK, loaded.Code, loaded.Body.String())
	assert.Equal(
		t,
		"pair_browser_approval",
		decodeData[e2eeKeyPackageResponse](t, loaded.Body.Bytes()).PairingID,
	)
}

func TestE2EEKeyDistributionGivenMalformedBrowserRequestWhenCreatedThenItFailsClosed(t *testing.T) {
	srv, _ := testServer(t, "e2ee-validation@example.com")
	fixture := testNodeAgent(t, srv, "e2ee-validation@example.com")
	path := "/api/v1/user/self/agents/" + fixture.agentID + "/e2ee/pairings"
	tests := []struct {
		name string
		body any
	}{
		{name: "malformed JSON", body: json.RawMessage(`{"pairing_id"`)},
		{name: "bad public key", body: withCreatePairingField("recipient_public_key", "bad")},
		{name: "bad commitment", body: withCreatePairingField("secret_commitment", "bad")},
		{name: "unsafe identifier", body: withCreatePairingField("pairing_id", "pair/unsafe")},
		{name: "invalid epoch", body: withCreatePairingField("key_epoch", -1)},
		{
			name: "oversized device name",
			body: withCreatePairingField("device_name", string(bytes.Repeat([]byte{'x'}, 257))),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := e2eeKeyRequest(
				t,
				srv,
				http.MethodPost,
				path,
				fixture.userEmail,
				"",
				test.body,
			)
			assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		})
	}
}

func TestE2EEKeyDistributionGivenMalformedWrappedPackageWhenCompletedThenItFailsClosed(
	t *testing.T,
) {
	srv, _ := testServer(t, "e2ee-package-validation@example.com")
	fixture := testNodeAgent(t, srv, "e2ee-package-validation@example.com")
	pairingPath := "/api/v1/user/self/agents/" + fixture.agentID + "/e2ee/pairings"
	create := e2eeKeyRequest(
		t,
		srv,
		http.MethodPost,
		pairingPath,
		fixture.userEmail,
		"",
		validCreatePairingBody("pair_package_validation"),
	)
	require.Equal(t, http.StatusCreated, create.Code, create.Body.String())
	completePath := pairingPath + "/pair_package_validation/package"
	tests := []struct {
		name string
		body any
	}{
		{name: "malformed JSON", body: json.RawMessage(`{"nonce"`)},
		{name: "bad sender key", body: withKeyPackageField("sender_ephemeral_public_key", "bad")},
		{name: "bad nonce", body: withKeyPackageField("nonce", "bad")},
		{
			name: "short ciphertext",
			body: withKeyPackageField(
				"ciphertext",
				base64.StdEncoding.EncodeToString([]byte("short")),
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := e2eeKeyRequest(
				t,
				srv,
				http.MethodPost,
				completePath,
				fixture.userEmail,
				"",
				test.body,
			)
			assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		})
	}

	badEpoch := e2eeKeyRequest(
		t,
		srv,
		http.MethodGet,
		"/api/v1/user/self/agents/"+fixture.agentID+"/e2ee/key-packages/device_browser_1?key_epoch=bad",
		fixture.userEmail,
		"",
		nil,
	)
	assert.Equal(t, http.StatusBadRequest, badEpoch.Code, badEpoch.Body.String())
}

func TestE2EEKeyDistributionGivenUnownedOrMissingRouteWhenAccessedThenItDoesNotLeakPairingState(
	t *testing.T,
) {
	srv, _ := testServer(t, "e2ee-route-owner@example.com")
	fixture := testNodeAgent(t, srv, "e2ee-route-owner@example.com")
	base := "/api/v1/user/self/agents/" + fixture.agentID + "/e2ee/pairings"

	unauthenticated := e2eeKeyRequest(t, srv, http.MethodGet, base, "", "", nil)
	assert.Equal(t, http.StatusNotFound, unauthenticated.Code, unauthenticated.Body.String())
	wrongUserRoute := e2eeKeyRequest(t, srv, http.MethodGet,
		"/api/v1/user/not-the-owner/agents/"+fixture.agentID+"/e2ee/pairings",
		fixture.userEmail, "", nil,
	)
	assert.Equal(t, http.StatusNotFound, wrongUserRoute.Code, wrongUserRoute.Body.String())
	missingPairing := e2eeKeyRequest(t, srv, http.MethodPost, base+"/missing/package",
		fixture.userEmail, "", validKeyPackageBody(),
	)
	assert.Equal(t, http.StatusNotFound, missingPairing.Code, missingPairing.Body.String())
}

func TestE2EEKeyDistributionIdentifiersGivenUnsafeValuesWhenValidatedThenTheyAreRejected(
	t *testing.T,
) {
	t.Parallel()
	assert.True(t, validE2EEKeyIdentifier("pair_safe-1"))
	assert.False(t, validE2EEKeyIdentifier(""))
	assert.False(t, validE2EEKeyIdentifier("pair with space"))
	assert.False(t, validE2EEKeyIdentifier(string(bytes.Repeat([]byte{'x'}, 129))))
}

func validCreatePairingBody(pairingID string) map[string]any {
	return map[string]any{
		"pairing_id": pairingID, "device_id": "device_browser_1",
		"device_name": "Chrome on Mac", "key_epoch": 1,
		"recipient_public_key": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 65)),
		"secret_commitment":    base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{5}, 32)),
	}
}

func withCreatePairingField(key string, value any) map[string]any {
	body := validCreatePairingBody("pair_validation")
	body[key] = value
	return body
}

func validKeyPackageBody() map[string]any {
	return map[string]any{
		"sender_ephemeral_public_key": base64.StdEncoding.EncodeToString(
			bytes.Repeat([]byte{6}, 65),
		),
		"nonce": base64.StdEncoding.EncodeToString([]byte("123456789012")),
		"ciphertext": base64.StdEncoding.EncodeToString(
			bytes.Repeat([]byte{7}, 48),
		),
	}
}

func withKeyPackageField(key string, value any) map[string]any {
	body := validKeyPackageBody()
	body[key] = value
	return body
}

func e2eeKeyRequest(
	t *testing.T,
	srv *Server,
	method string,
	path string,
	userEmail string,
	nodeAPIKey string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		if provided, ok := body.(json.RawMessage); ok {
			raw = append([]byte(nil), provided...)
		} else {
			var err error
			raw, err = json.Marshal(body)
			require.NoError(t, err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if userEmail != "" {
		req.Header.Set("X-User-Email", userEmail)
	}
	if nodeAPIKey != "" {
		req.Header.Set("X-Pax-Key", nodeAPIKey)
	}
	recorder := httptest.NewRecorder()
	srv.routes().ServeHTTP(recorder, req)
	return recorder
}
