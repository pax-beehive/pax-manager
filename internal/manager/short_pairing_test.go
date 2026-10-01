package manager

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestShortPairingHTTPRequiresOwnerAndRoleCapability(t *testing.T) {
	srv, _ := testServer(t, "short-owner@example.com")
	owner := testNodeAgent(t, srv, "short-owner@example.com")
	other := testNodeAgent(t, srv, "short-other@example.com")
	base := "/api/v1/user/self/agents/" + owner.agentID + "/e2ee/pairings"
	recipient := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	approver := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	body := validCreatePairingBody("pair_short")
	body["protocol_version"] = domain.ShortPairingProtocol
	body["recipient_capability"] = recipient
	created := e2eeKeyRequest(t, srv, http.MethodPost, base, owner.userEmail, "", body)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	require.NotContains(t, created.Body.String(), "capability")
	request := decodeData[e2eePairingResponse](t, created.Body.Bytes())
	require.Equal(t, domain.ShortPairingProtocol, request.ProtocolVersion)
	require.False(t, request.ServerTime.IsZero())
	path := base + "/pair_short/attempts"
	login := map[string]any{
		"attempt_id":   "attempt_short",
		"generation":   0,
		"client_hello": "opaque_login",
	}
	for _, email := range []string{"", other.userEmail} {
		denied := shortHTTPRequest(t, srv, http.MethodPost, path, email, approver, login)
		require.Equal(t, http.StatusNotFound, denied.Code, denied.Body.String())
	}
	missing := shortHTTPRequest(t, srv, http.MethodPost, path, owner.userEmail, "", login)
	require.Equal(t, http.StatusNotFound, missing.Code)
	accepted := shortHTTPRequest(t, srv, http.MethodPost, path, owner.userEmail, approver, login)
	require.Equal(t, http.StatusCreated, accepted.Code, accepted.Body.String())
	require.NotContains(t, accepted.Body.String(), "capability")
	list := shortHTTPRequest(t, srv, http.MethodGet, path, owner.userEmail, approver, nil)
	require.Equal(t, http.StatusNotFound, list.Code)
	list = shortHTTPRequest(t, srv, http.MethodGet, path, owner.userEmail, recipient, nil)
	require.Equal(t, http.StatusOK, list.Code)
	require.Equal(t, "private, no-store", list.Header().Get("Cache-Control"))
	attemptPath := path + "/attempt_short"
	wrongRole := shortHTTPRequest(
		t,
		srv,
		http.MethodPost,
		attemptPath,
		owner.userEmail,
		approver,
		map[string]any{"stage": 1, "payload": "answer"},
	)
	require.Equal(t, http.StatusNotFound, wrongRole.Code)
	for stage := 1; stage <= 3; stage++ {
		cap := recipient
		if stage == 2 {
			cap = approver
		}
		advanced := shortHTTPRequest(
			t,
			srv,
			http.MethodPost,
			attemptPath,
			owner.userEmail,
			cap,
			map[string]any{"stage": stage, "payload": "encrypted_protocol_message"},
		)
		require.Equal(t, http.StatusOK, advanced.Code, advanced.Body.String())
	}
	packageBody := validKeyPackageBody()
	packageBody["attempt_id"] = "attempt_short"
	denied := shortHTTPRequest(
		t,
		srv,
		http.MethodPost,
		base+"/pair_short/package",
		owner.userEmail,
		recipient,
		packageBody,
	)
	require.Equal(t, http.StatusNotFound, denied.Code)
	approved := shortHTTPRequest(
		t,
		srv,
		http.MethodPost,
		base+"/pair_short/package",
		owner.userEmail,
		approver,
		packageBody,
	)
	require.Equal(t, http.StatusCreated, approved.Code, approved.Body.String())
	ended := shortHTTPRequest(
		t,
		srv,
		http.MethodPost,
		base+"/pair_short/end",
		owner.userEmail,
		approver,
		map[string]any{"reason": "rejected", "attempt_id": "attempt_short"},
	)
	require.Equal(t, http.StatusConflict, ended.Code, ended.Body.String())
}

func TestShortPairingHTTPRejectsInvalidProtocolAndPayload(t *testing.T) {
	srv, _ := testServer(t, "short-validation@example.com")
	owner := testNodeAgent(t, srv, "short-validation@example.com")
	base := "/api/v1/user/self/agents/" + owner.agentID + "/e2ee/pairings"
	body := validCreatePairingBody("pair_short")
	body["protocol_version"] = domain.ShortPairingProtocol
	missing := e2eeKeyRequest(t, srv, http.MethodPost, base, owner.userEmail, "", body)
	require.Equal(t, http.StatusBadRequest, missing.Code)
	cap := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	body["recipient_capability"] = cap
	accepted := e2eeKeyRequest(t, srv, http.MethodPost, base, owner.userEmail, "", body)
	require.Equal(t, http.StatusCreated, accepted.Code)
	for _, raw := range []string{
		`{"attempt_id":"a","generation":0,"client_hello":"opaque","password":"12345678"}`,
		`{"attempt_id":"a","generation":0,"client_hello":"opaque"}{}`,
	} {
		req := httptest.NewRequest(
			http.MethodPost,
			base+"/pair_short/attempts",
			bytes.NewBufferString(raw),
		)
		req.Header.Set("X-User-Email", owner.userEmail)
		req.Header.Set(shortPairingCapabilityHeader, cap)
		result := httptest.NewRecorder()
		srv.routes().ServeHTTP(result, req)
		require.Equal(t, http.StatusBadRequest, result.Code, result.Body.String())
	}
}

func shortHTTPRequest(
	t *testing.T,
	srv *Server,
	method, path, email, cap string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		require.NoError(t, err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Email", email)
	req.Header.Set(shortPairingCapabilityHeader, cap)
	result := httptest.NewRecorder()
	srv.routes().ServeHTTP(result, req)
	return result
}
