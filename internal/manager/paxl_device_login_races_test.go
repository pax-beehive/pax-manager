package manager

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestPaxlLoginGivenConfirmedIdentityWhenCommittedAndResponseLostThenRetryReturnsSameCredential(
	t *testing.T,
) {
	srv, _ := testServer(t, "owner@example.com")
	start := startClientCommitLogin(t, srv)
	approvePaxlDeviceLoginTest(t, srv, start.UserCode)
	confirmed := pollPaxlDeviceLoginTest(t, srv, start.LoginID, start.PollToken)
	require.NotNil(t, confirmed.User)
	request := PollPaxlDeviceLoginRequest{
		LoginID:        start.LoginID,
		PollToken:      start.PollToken,
		Action:         "commit",
		ExpectedUserID: confirmed.User.UserID,
	}
	first := mutateClientLogin(t, srv, request, http.StatusOK)
	assert.Equal(t, "approved", first.Status)
	require.NotEmpty(t, first.APIKey)
	second := mutateClientLogin(t, srv, request, http.StatusOK)
	assert.Equal(t, first.APIKey, second.APIKey)
	assert.Equal(t, first.NodeID, second.NodeID)
	keys, err := srv.store.ListUserAPIKeys(t.Context(), domain.UserPrincipal{User: *confirmed.User})
	require.NoError(t, err)
	assert.Len(t, keys, 1)
	request.ExpectedUserID = "usr_different"
	mutateClientLogin(t, srv, request, http.StatusConflict)
	request.ExpectedUserID = confirmed.User.UserID
	request.Action = "ack"
	acked := mutateClientLogin(t, srv, request, http.StatusOK)
	assert.Equal(t, "consumed", acked.Status)
	assert.Empty(t, acked.APIKey)
	mutateClientLogin(t, srv, request, http.StatusOK)
	request.Action = "commit"
	assert.Empty(t, mutateClientLogin(t, srv, request, http.StatusOK).APIKey)
}

func TestPaxlLoginGivenRegionalManagerThenOnlyWorkerBoundIdentityCanConfirm(t *testing.T) {
	for _, tc := range []struct {
		name, email, purpose, proofUser string
		unsigned                        bool
		want                            int
	}{
		{"home user", "cli@example.com", "user", "", false, 200},
		{"wrong home identity", "cli@example.com", "user", "usr_other", false, 401},
		{"unsigned", "cli@example.com", "user", "", true, 401},
		{"ordinary user cannot cross region", "cli@example.com", "admin", "usr_home", false, 403},
		{"authorized regional administrator", "admin@example.com", "admin", "usr_home", false, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := testServer(t, "owner@example.com")
			srv.cfg.Region = "hk"
			srv.cfg.RegionProvisioningSecret = strings.Repeat("s", 40)
			user, err := srv.store.EnsureUser(t.Context(), tc.email, "", "user")
			require.NoError(t, err)
			start := startClientCommitLogin(t, srv)
			userID := tc.proofUser
			if userID == "" {
				userID = user.UserID
			}
			body, err := json.Marshal(
				map[string]string{
					"identity_key": tc.email,
					"user_id":      userID,
					"region":       "hk",
					"user_code":    start.UserCode,
					"purpose":      tc.purpose,
				},
			)
			require.NoError(t, err)
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/user/self/paxl/device-logins/"+start.UserCode+"/approve",
				bytes.NewReader(body),
			)
			request.Header.Set("X-User-Email", tc.email)
			if !tc.unsigned {
				timestamp := strconv.FormatInt(srv.clock().Unix(), 10)
				mac := hmac.New(sha256.New, []byte(srv.cfg.RegionProvisioningSecret))
				_, _ = mac.Write([]byte("pax-login-approval-v1\n" + timestamp + "\n"))
				_, _ = mac.Write(body)
				request.Header.Set("X-Pax-Login-Timestamp", timestamp)
				request.Header.Set("X-Pax-Login-Signature", hex.EncodeToString(mac.Sum(nil)))
			}
			response := httptest.NewRecorder()
			srv.routes().ServeHTTP(response, request)
			assert.Equal(t, tc.want, response.Code)
			keys, err := srv.store.ListUserAPIKeys(t.Context(), domain.UserPrincipal{User: user})
			require.NoError(t, err)
			assert.Empty(t, keys)
		})
	}
}

func TestPaxlLoginGivenInvalidProtocolOrActionThenRequestFailsClosed(t *testing.T) {
	srv, _ := testServer(t, "owner@example.com")
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/paxl/device-login/start",
		bytes.NewBufferString(`{"protocol":"unknown"}`),
	)
	setJSON(request)
	response := httptest.NewRecorder()
	srv.routes().ServeHTTP(response, request)
	assert.Equal(t, 400, response.Code)
	start := startClientCommitLogin(t, srv)
	mutateClientLogin(
		t,
		srv,
		PollPaxlDeviceLoginRequest{
			LoginID:   start.LoginID,
			PollToken: start.PollToken,
			Action:    "unknown",
		},
		400,
	)
	mutateClientLogin(t, srv, PollPaxlDeviceLoginRequest{}, 400)
}

func startClientCommitLogin(t *testing.T, srv *Server) StartPaxlDeviceLoginResponse {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/paxl/device-login/start",
		bytes.NewBufferString(`{"client_name":"paxl","protocol":"client_commit_v1"}`),
	)
	setJSON(request)
	response := httptest.NewRecorder()
	srv.routes().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	return decodeData[StartPaxlDeviceLoginResponse](t, response.Body.Bytes())
}

func mutateClientLogin(
	t *testing.T,
	srv *Server,
	input PollPaxlDeviceLoginRequest,
	expected int,
) PollPaxlDeviceLoginResponse {
	t.Helper()
	body, err := json.Marshal(input)
	require.NoError(t, err)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/paxl/device-login/poll",
		bytes.NewReader(body),
	)
	setJSON(request)
	response := httptest.NewRecorder()
	srv.routes().ServeHTTP(response, request)
	require.Equal(t, expected, response.Code)
	if expected != http.StatusOK {
		return PollPaxlDeviceLoginResponse{}
	}
	return decodeData[PollPaxlDeviceLoginResponse](t, response.Body.Bytes())
}

func TestPaxlLoginGivenClientCommitProtocolWhenBrowserApprovesThenOnlyIdentityIsConfirmed(
	t *testing.T,
) {
	srv, _ := testServer(t, "owner@example.com")
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/paxl/device-login/start",
		bytes.NewBufferString(`{"client_name":"paxl","protocol":"client_commit_v1"}`),
	)
	setJSON(request)
	response := httptest.NewRecorder()
	srv.routes().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	start := decodeData[StartPaxlDeviceLoginResponse](t, response.Body.Bytes())
	approval := approvePaxlDeviceLoginTest(t, srv, start.UserCode)
	assert.Equal(t, "confirmed", approval.Status)
	poll := pollPaxlDeviceLoginTest(t, srv, start.LoginID, start.PollToken)
	assert.Equal(t, "confirmed", poll.Status)
	assert.Empty(t, poll.APIKey)
	assert.Empty(t, poll.NodeID)
	require.NotNil(t, poll.User)
	assert.Equal(t, "cli@example.com", poll.User.Email)
	keys, err := srv.store.ListUserAPIKeys(t.Context(), domain.UserPrincipal{User: *poll.User})
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestPaxlLoginGivenMissingRequestWhenApprovedThenNoCredentialIsCreated(t *testing.T) {
	srv, _ := testServer(t, "owner@example.com")
	user, err := srv.store.EnsureUser(t.Context(), "cli@example.com", "", "user")
	require.NoError(t, err)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/paxl/device-logins/ABSENT/approve",
		nil,
	)
	request.Header.Set("X-User-Email", user.Email)
	response := httptest.NewRecorder()
	srv.routes().ServeHTTP(response, request)
	assert.Equal(t, http.StatusNotFound, response.Code)
	keys, err := srv.store.ListUserAPIKeys(t.Context(), domain.UserPrincipal{User: user})
	require.NoError(t, err)
	assert.Empty(t, keys, "a failed approval must not leave an orphan API key")
}

func TestPaxlLoginGivenBoundIdentityWhenAnotherEmailApprovesThenOriginalBindingSurvives(
	t *testing.T,
) {
	srv, _ := testServer(t, "owner@example.com")
	start := startClientCommitLogin(t, srv)
	first := approvePaxlDeviceLoginTest(t, srv, start.UserCode)
	for _, email := range []string{"cli@example.com", "other@example.com"} {
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/user/self/paxl/device-logins/"+start.UserCode+"/approve",
			nil,
		)
		request.Header.Set("X-User-Email", email)
		response := httptest.NewRecorder()
		srv.routes().ServeHTTP(response, request)
		if email == "cli@example.com" {
			require.Equal(
				t,
				http.StatusOK,
				response.Code,
				"same-identity retries must be idempotent",
			)
			assert.Equal(
				t,
				first,
				decodeData[ApprovePaxlDeviceLoginResponse](t, response.Body.Bytes()),
			)
		} else {
			assert.Equal(t, http.StatusConflict, response.Code)
		}
	}
	poll := pollPaxlDeviceLoginTest(t, srv, start.LoginID, start.PollToken)
	require.NotNil(t, poll.User)
	assert.Equal(t, "cli@example.com", poll.User.Email)
	assert.Empty(t, poll.APIKey)
}
