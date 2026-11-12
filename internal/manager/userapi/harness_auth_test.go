package userapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
	userapimocks "github.com/pax-beehive/pax-manager/internal/manager/userapi/mocks"
)

type harnessAuthFixture struct {
	svc        *userapi.Service
	client     *fakeNodeControlClient
	store      *userapimocks.MockStore
	principals *userapimocks.MockPrincipalResolver
	now        time.Time
}

func newHarnessAuthFixture(t *testing.T) *harnessAuthFixture {
	t.Helper()
	f := &harnessAuthFixture{now: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)}
	f.store = userapimocks.NewMockStore(t)
	f.principals = userapimocks.NewMockPrincipalResolver(t)
	principal := userPrincipal("owner", false)
	f.principals.EXPECT().
		Principal(mock.Anything, auth.RequestMetadata{}).
		Return(principal, nil).
		Maybe()
	f.store.EXPECT().GetNode(mock.Anything, principal, "node").
		Return(domain.Node{NodeID: "node", OwnerUserID: "owner"}, nil).Maybe()
	secrets := userapimocks.NewMockSecretIssuer(t)
	secrets.EXPECT().New("ctlq").Return("query", nil).Maybe()
	f.client = &fakeNodeControlClient{remoteID: "remote", result: json.RawMessage(
		`{"type":"harness_auth.status","harness_auth":{"harness":"claude","state":"logged_out","logged_in":false}}`,
	)}
	f.svc = userapi.NewService(f.store, func() time.Time { return f.now }, f.principals, secrets)
	f.svc.SetNodeControlClient(f.client)
	return f
}

func (f *harnessAuthFixture) start(
	t *testing.T,
	harness, method, code string,
) domain.HarnessAuthView {
	t.Helper()
	f.client.result = json.RawMessage(
		`{"harness_auth":{"harness":"` + harness + `","state":"logged_out"}}`,
	)
	f.client.commandAck = json.RawMessage(
		`{"command_id":"start","ok":true,"result":{"harness_auth":{"harness":"` + harness + `","session_id":"attempt","state":"starting","expires_at":"2026-11-11T00:05:00Z"}}}`,
	)
	status, data, err := f.svc.LoginNodeHarness(
		context.Background(),
		auth.RequestMetadata{},
		domain.LoginNodeHarnessRequest{
			NodeID: "node", Harness: harness, CommandID: "start", Operation: "start", Method: method, Code: code,
		},
	)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, status)
	return data.(domain.HarnessAuthData).HarnessAuth
}

func TestHarnessAuthGivenAcceptedStartWithLostAckWhenPollingThenRecoversOwnedAttempt(t *testing.T) {
	for _, method := range []string{"subscription", "api-key", "access-token"} {
		t.Run(method, func(t *testing.T) {
			f := newHarnessAuthFixture(t)
			harness, code := "claude", ""
			if method != "subscription" {
				harness, code = "codex", "private"
			}
			f.client.result = json.RawMessage(
				`{"harness_auth":{"harness":"` + harness + `","state":"logged_out"}}`,
			)
			f.client.commandErr = context.DeadlineExceeded
			_, _, err := f.svc.LoginNodeHarness(
				t.Context(),
				auth.RequestMetadata{},
				domain.LoginNodeHarnessRequest{
					NodeID: "node", Harness: harness, CommandID: "lost", Operation: "start", Method: method, Code: code,
				},
			)
			assertHarnessAuthError(t, err, http.StatusGatewayTimeout)
			f.client.result = json.RawMessage(
				`{"harness_auth":{"harness":"` + harness + `","session_id":"accepted","state":"exchanging","method":"` + method + `"}}`,
			)
			status, data, err := f.svc.GetNodeHarnessAuthStatus(
				t.Context(),
				auth.RequestMetadata{},
				"node",
				harness,
				"",
			)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, status)
			assert.Equal(t, "accepted", data.(domain.HarnessAuthData).HarnessAuth.SessionID)
			_, _, err = f.svc.GetNodeHarnessAuthStatus(
				t.Context(),
				auth.RequestMetadata{},
				"node",
				harness,
				"accepted",
			)
			require.NoError(t, err)
		})
	}
}

func TestHarnessAuthGivenAmbiguousStartWhenOldAttemptOrDifferentMethodAppearsThenDoesNotAdoptIt(
	t *testing.T,
) {
	for _, tc := range []struct{ session, method string }{{"previous", "subscription"}, {"different", "console"}} {
		f := newHarnessAuthFixture(t)
		f.client.result = json.RawMessage(
			`{"harness_auth":{"harness":"claude","session_id":"previous","state":"failed","method":"subscription"}}`,
		)
		f.client.commandErr = context.DeadlineExceeded
		_, _, err := f.svc.LoginNodeHarness(
			t.Context(),
			auth.RequestMetadata{},
			domain.LoginNodeHarnessRequest{
				NodeID:    "node",
				Harness:   "claude",
				CommandID: "lost",
				Operation: "start",
			},
		)
		assertHarnessAuthError(t, err, http.StatusGatewayTimeout)
		f.client.result = json.RawMessage(
			`{"harness_auth":{"harness":"claude","session_id":"` + tc.session + `","state":"awaiting_code","method":"` + tc.method + `","authorization_url":"private"}}`,
		)
		_, data, err := f.svc.GetNodeHarnessAuthStatus(
			t.Context(),
			auth.RequestMetadata{},
			"node",
			"claude",
			"",
		)
		assertHarnessAuthError(t, err, http.StatusConflict)
		assert.Nil(t, data)
	}
}

func assertHarnessAuthError(t *testing.T, err error, status int) {
	t.Helper()
	if errors.Is(err, domain.ErrNotFound) {
		assert.Equal(t, http.StatusNotFound, status)
		return
	}
	var apiErr apperr.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, status, apiErr.Status)
}

func TestHarnessAuthGivenSupportedMethodsWhenStartingThenForwardsEphemeralCommand(t *testing.T) {
	for _, tc := range []struct{ harness, method, code string }{
		{"claude", "", ""}, {"claude", "subscription", ""}, {"claude", "console", ""},
		{"codex", "", ""}, {"codex", "device", ""}, {"codex", "api-key", "private-key"},
		{"codex", "access-token", "private-token"},
	} {
		t.Run(tc.harness+"/"+tc.method, func(t *testing.T) {
			f := newHarnessAuthFixture(t)
			assert.Equal(t, "attempt", f.start(t, tc.harness, tc.method, tc.code).SessionID)
			assert.Equal(t, "node", f.client.nodeID)
			assert.Equal(t, "start", f.client.commandID)
			command := f.client.command.(map[string]any)
			assert.Equal(t, "harness_auth.login", command["type"])
			payload := command["harness_auth_login"].(domain.HarnessAuthLogin)
			assert.Equal(t, tc.code, payload.Code)
			assert.Equal(t, tc.method, payload.Method)
			f.store.AssertNotCalled(
				t,
				"CreateNodeDaemonCommand",
				mock.Anything,
				mock.Anything,
				mock.Anything,
			)
		})
	}
}

func TestHarnessAuthGivenOwnedAttemptWhenPollingSubmittingAndCancellingThenReturnsNormalizedViews(
	t *testing.T,
) {
	f := newHarnessAuthFixture(t)
	f.start(t, "claude", "", "")
	f.client.result = json.RawMessage(
		`{"harness_auth":{"harness":"claude","session_id":"attempt","state":"awaiting_code","authorization_url":"https://claude.ai/oauth/authorize?state=secret","extra":"discard"}}`,
	)
	status, data, err := f.svc.GetNodeHarnessAuthStatus(
		t.Context(),
		auth.RequestMetadata{},
		"node",
		"claude",
		"attempt",
	)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, data.(domain.HarnessAuthData).HarnessAuth.AuthorizationURL, "claude.ai")
	assert.Equal(
		t,
		map[string]any{
			"type":                "harness_auth.status",
			"harness_auth_status": map[string]any{"harness": "claude", "session_id": "attempt"},
		},
		f.client.query,
	)
	for _, operation := range []string{"submit", "cancel"} {
		f.client.commandAck = json.RawMessage(
			`{"command_id":"` + operation + `","ok":true,"result":{"harness_auth":{"harness":"claude","session_id":"attempt","state":"exchanging"}}}`,
		)
		code := ""
		if operation == "submit" {
			code = "complete-code#state"
		}
		_, _, err = f.svc.LoginNodeHarness(
			t.Context(),
			auth.RequestMetadata{},
			domain.LoginNodeHarnessRequest{
				NodeID: "node", Harness: "claude", CommandID: operation, Operation: operation, SessionID: "attempt", Code: code,
			},
		)
		require.NoError(t, err)
		assert.Equal(
			t,
			code,
			f.client.command.(map[string]any)["harness_auth_login"].(domain.HarnessAuthLogin).Code,
		)
	}
	f.client.result = json.RawMessage(
		`{"harness_auth":{"harness":"claude","session_id":"attempt","state":"succeeded","authorization_url":"discard","user_code":"discard","logged_in":true}}`,
	)
	_, data, err = f.svc.GetNodeHarnessAuthStatus(
		t.Context(),
		auth.RequestMetadata{},
		"node",
		"claude",
		"",
	)
	require.NoError(t, err)
	assert.Empty(t, data.(domain.HarnessAuthData).HarnessAuth.AuthorizationURL)
	assert.Empty(t, data.(domain.HarnessAuthData).HarnessAuth.UserCode)
}

func TestHarnessAuthGivenInvalidAcknowledgementWhenStartingThenFailsClosed(t *testing.T) {
	for _, raw := range []string{
		`not-json`, `{}`, `{"command_id":"different","ok":true}`,
		`{"command_id":"cmd","ok":false}`,
		`{"command_id":"cmd","ok":true,"result":{"harness_auth":{"harness":"claude","state":"logged_in"}}}`,
		`{"command_id":"cmd","error":{"code":"conflict","message":"private"}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			f := newHarnessAuthFixture(t)
			f.client.commandAck = json.RawMessage(raw)
			_, data, err := f.svc.LoginNodeHarness(
				t.Context(),
				auth.RequestMetadata{},
				domain.LoginNodeHarnessRequest{
					NodeID:    "node",
					Harness:   "claude",
					CommandID: "cmd",
					Operation: "start",
				},
			)
			require.Error(t, err)
			assert.Nil(t, data)
			assert.NotContains(t, err.Error(), "private")
		})
	}
}

func TestHarnessAuthGivenOfflineOrChangedRemoteWhenAccessingThenDoesNotReuseClaim(t *testing.T) {
	for name, mutate := range map[string]func(*harnessAuthFixture){
		"nil client":        func(f *harnessAuthFixture) { f.svc.SetNodeControlClient(nil) },
		"unreported remote": func(f *harnessAuthFixture) { f.client.remoteID = "" },
		"disconnected":      func(f *harnessAuthFixture) { f.client.remoteIDErr = errors.New("private") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newHarnessAuthFixture(t)
			mutate(f)
			_, _, err := f.svc.GetNodeHarnessAuthStatus(
				t.Context(),
				auth.RequestMetadata{},
				"node",
				"claude",
				"",
			)
			assertHarnessAuthError(t, err, http.StatusServiceUnavailable)
			assert.Zero(t, f.client.calls)
		})
	}
	f := newHarnessAuthFixture(t)
	f.start(t, "claude", "", "")
	f.client.remoteID = "reconnected-as-different-remote"
	_, _, err := f.svc.GetNodeHarnessAuthStatus(
		t.Context(),
		auth.RequestMetadata{},
		"node",
		"claude",
		"attempt",
	)
	assertHarnessAuthError(t, err, http.StatusNotFound)
}

func TestHarnessAuthGivenInvalidOrUnauthorizedTargetWhenAccessingThenNeverContactsDaemon(
	t *testing.T,
) {
	f := newHarnessAuthFixture(t)
	for _, target := range []struct{ node, harness, session string }{
		{"", "claude", ""}, {"node", "pi", ""}, {"node", "claude", strings.Repeat("x", 129)},
	} {
		_, _, err := f.svc.GetNodeHarnessAuthStatus(
			t.Context(),
			auth.RequestMetadata{},
			target.node,
			target.harness,
			target.session,
		)
		assertHarnessAuthError(t, err, http.StatusBadRequest)
	}
	principal := userPrincipal("owner", false)
	f.store.EXPECT().
		GetNode(mock.Anything, principal, "other-node").
		Return(domain.Node{NodeID: "other-node", OwnerUserID: "other-user"}, nil)
	_, _, err := f.svc.GetNodeHarnessAuthStatus(
		t.Context(),
		auth.RequestMetadata{},
		"other-node",
		"claude",
		"",
	)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	f.store.EXPECT().
		GetNode(mock.Anything, principal, "missing-node").
		Return(domain.Node{}, domain.ErrNotFound)
	_, _, err = f.svc.LoginNodeHarness(
		t.Context(),
		auth.RequestMetadata{},
		domain.LoginNodeHarnessRequest{
			NodeID:    "missing-node",
			Harness:   "claude",
			CommandID: "cmd",
			Operation: "start",
		},
	)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	assert.Zero(t, f.client.calls)
	assert.Zero(t, f.client.commandCalls)
}

func TestHarnessAuthGivenInvalidInputWhenLoggingInThenRejectsWithoutDispatch(t *testing.T) {
	base := domain.LoginNodeHarnessRequest{
		NodeID:    "node",
		Harness:   "claude",
		CommandID: "cmd",
		Operation: "start",
	}
	for name, mutate := range map[string]func(*domain.LoginNodeHarnessRequest){
		"node":           func(r *domain.LoginNodeHarnessRequest) { r.NodeID = "" },
		"harness":        func(r *domain.LoginNodeHarnessRequest) { r.Harness = "pi" },
		"command":        func(r *domain.LoginNodeHarnessRequest) { r.CommandID = "" },
		"long command":   func(r *domain.LoginNodeHarnessRequest) { r.CommandID = strings.Repeat("x", 129) },
		"operation":      func(r *domain.LoginNodeHarnessRequest) { r.Operation = "other" },
		"start session":  func(r *domain.LoginNodeHarnessRequest) { r.SessionID = "attempt" },
		"start secret":   func(r *domain.LoginNodeHarnessRequest) { r.Code = "secret" },
		"claude method":  func(r *domain.LoginNodeHarnessRequest) { r.Method = "api-key" },
		"codex method":   func(r *domain.LoginNodeHarnessRequest) { r.Harness = "codex"; r.Method = "other" },
		"missing key":    func(r *domain.LoginNodeHarnessRequest) { r.Harness = "codex"; r.Method = "api-key" },
		"device secret":  func(r *domain.LoginNodeHarnessRequest) { r.Harness = "codex"; r.Code = "secret" },
		"submit session": func(r *domain.LoginNodeHarnessRequest) { r.Operation = "submit"; r.Code = "code" },
		"submit method": func(r *domain.LoginNodeHarnessRequest) {
			r.Operation = "submit"
			r.SessionID = "attempt"
			r.Code = "code"
			r.Method = "console"
		},
		"codex submit": func(r *domain.LoginNodeHarnessRequest) {
			r.Operation = "submit"
			r.Harness = "codex"
			r.SessionID = "attempt"
			r.Code = "code"
		},
		"multiline": func(r *domain.LoginNodeHarnessRequest) {
			r.Operation = "submit"
			r.SessionID = "attempt"
			r.Code = "code\nsecret"
		},
		"oversize": func(r *domain.LoginNodeHarnessRequest) {
			r.Operation = "submit"
			r.SessionID = "attempt"
			r.Code = strings.Repeat("x", 4097)
		},
		"whitespace": func(r *domain.LoginNodeHarnessRequest) {
			r.Operation = "submit"
			r.SessionID = "attempt"
			r.Code = " code"
		},
		"cancel session": func(r *domain.LoginNodeHarnessRequest) { r.Operation = "cancel" },
		"cancel secret": func(r *domain.LoginNodeHarnessRequest) {
			r.Operation = "cancel"
			r.SessionID = "attempt"
			r.Code = "secret"
		},
		"long session": func(r *domain.LoginNodeHarnessRequest) { r.SessionID = strings.Repeat("x", 129) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newHarnessAuthFixture(t)
			req := base
			mutate(&req)
			_, _, err := f.svc.LoginNodeHarness(t.Context(), auth.RequestMetadata{}, req)
			assertHarnessAuthError(t, err, http.StatusBadRequest)
			assert.Zero(t, f.client.commandCalls)
		})
	}
}

func TestHarnessAuthGivenUnownedOrExpiredAttemptWhenAccessingThenDoesNotExposeOrMutateChallenge(
	t *testing.T,
) {
	f := newHarnessAuthFixture(t)
	f.client.result = json.RawMessage(
		`{"harness_auth":{"harness":"claude","session_id":"someone-else","state":"awaiting_code","authorization_url":"private"}}`,
	)
	_, data, err := f.svc.GetNodeHarnessAuthStatus(
		t.Context(),
		auth.RequestMetadata{},
		"node",
		"claude",
		"",
	)
	assertHarnessAuthError(t, err, http.StatusConflict)
	assert.Nil(t, data)
	_, _, err = f.svc.GetNodeHarnessAuthStatus(
		t.Context(),
		auth.RequestMetadata{},
		"node",
		"claude",
		"someone-else",
	)
	assertHarnessAuthError(t, err, http.StatusNotFound)
	for _, operation := range []string{"start", "submit", "cancel"} {
		req := domain.LoginNodeHarnessRequest{
			NodeID:    "node",
			Harness:   "claude",
			CommandID: "replayed",
			Operation: operation,
		}
		if operation != "start" {
			req.SessionID = "someone-else"
		}
		if operation == "submit" {
			req.Code = "code"
		}
		_, _, err = f.svc.LoginNodeHarness(t.Context(), auth.RequestMetadata{}, req)
		require.Error(t, err)
		assert.Zero(t, f.client.commandCalls)
	}
	f.start(t, "claude", "", "")
	f.now = f.now.Add(6 * time.Minute)
	_, _, err = f.svc.GetNodeHarnessAuthStatus(
		t.Context(),
		auth.RequestMetadata{},
		"node",
		"claude",
		"attempt",
	)
	assertHarnessAuthError(t, err, http.StatusNotFound)
}

func TestHarnessAuthGivenDaemonFailureWhenQueryingOrDispatchingThenReturnsSafeError(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		status int
	}{
		{`{"error":{"code":"invalid_argument","message":"private"}}`, 400},
		{`{"error":{"code":"not_found","message":"private"}}`, 404},
		{`{"error":{"code":"conflict","message":"private"}}`, 409},
		{`{"error":{"code":"internal_error","message":"private"}}`, 502},
		{`not-json`, 502}, {`{}`, 502},
		{`{"harness_auth":{"harness":"codex","state":"logged_out"}}`, 502},
		{`{"harness_auth":{"harness":"claude","state":"unknown"}}`, 502},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			f := newHarnessAuthFixture(t)
			f.client.result = json.RawMessage(tc.raw)
			_, _, err := f.svc.GetNodeHarnessAuthStatus(
				t.Context(),
				auth.RequestMetadata{},
				"node",
				"claude",
				"",
			)
			assertHarnessAuthError(t, err, tc.status)
			assert.NotContains(t, err.Error(), "private")
		})
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{context.DeadlineExceeded, 504}, {errors.New("private"), 503}} {
		f := newHarnessAuthFixture(t)
		f.client.commandErr = tc.err
		_, _, err := f.svc.LoginNodeHarness(
			t.Context(),
			auth.RequestMetadata{},
			domain.LoginNodeHarnessRequest{
				NodeID:    "node",
				Harness:   "claude",
				CommandID: "cmd",
				Operation: "start",
			},
		)
		assertHarnessAuthError(t, err, tc.status)
		assert.NotContains(t, err.Error(), "private")
	}
}
