package userapi_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func harnessAuthAccountJSON(t *testing.T, data any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	var view struct {
		HarnessAuth struct {
			Account json.RawMessage `json:"account"`
		} `json:"harness_auth"`
	}
	require.NoError(t, json.Unmarshal(raw, &view))
	return view.HarnessAuth.Account
}

func TestHarnessAuthAccountGivenLoggedInHarnessWhenQueryingThenReturnsOnlyPublicAccountFields(
	t *testing.T,
) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			f := newHarnessAuthFixture(t)
			f.client.result = json.RawMessage(
				`{"harness_auth":{"harness":"` + harness + `","state":"logged_in","logged_in":true,"account":{"email":" person@example.com ","display_name":" Person ","organization":" Workspace ","plan":" Pro ","access_token":"private-token","provider":{"secret":"private"}}}}`,
			)
			_, data, err := f.svc.GetNodeHarnessAuthStatus(
				t.Context(),
				auth.RequestMetadata{},
				"node",
				harness,
				"",
			)
			require.NoError(t, err)
			assert.JSONEq(
				t,
				`{"email":"person@example.com","display_name":"Person","organization":"Workspace","plan":"Pro"}`,
				string(harnessAuthAccountJSON(t, data)),
			)
			raw, err := json.Marshal(data)
			require.NoError(t, err)
			assert.NotContains(t, string(raw), "private")
			assert.NotContains(t, string(raw), "access_token")
		})
	}
}

func TestHarnessAuthAccountGivenCompletedLoginWhenAcknowledgedThenPreservesAccount(t *testing.T) {
	f := newHarnessAuthFixture(t)
	f.client.commandAck = json.RawMessage(
		`{"command_id":"login","ok":true,"result":{"harness_auth":{"harness":"claude","session_id":"attempt","state":"succeeded","logged_in":true,"account":{"email":"person@example.com"}}}}`,
	)
	_, data, err := f.svc.LoginNodeHarness(
		t.Context(),
		auth.RequestMetadata{},
		domain.LoginNodeHarnessRequest{
			NodeID: "node", Harness: "claude", CommandID: "login", Operation: "start",
		},
	)
	require.NoError(t, err)
	assert.JSONEq(t, `{"email":"person@example.com"}`, string(harnessAuthAccountJSON(t, data)))
}

func TestHarnessAuthAccountGivenIncompleteOrLoggedOutLoginWhenPollingThenClearsStaleIdentity(
	t *testing.T,
) {
	for _, state := range []string{"starting", "awaiting_browser", "awaiting_code", "exchanging", "failed", "expired", "cancelled", "logged_out"} {
		t.Run(state, func(t *testing.T) {
			f := newHarnessAuthFixture(t)
			f.start(t, "claude", "", "")
			f.client.result = json.RawMessage(
				`{"harness_auth":{"harness":"claude","session_id":"attempt","state":"` + state + `","account":{"email":"stale@example.com"}}}`,
			)
			_, data, err := f.svc.GetNodeHarnessAuthStatus(
				t.Context(),
				auth.RequestMetadata{},
				"node",
				"claude",
				"attempt",
			)
			require.NoError(t, err)
			assert.Empty(t, harnessAuthAccountJSON(t, data))
		})
	}
}

func TestHarnessAuthAccountGivenMissingOrUnsafeMetadataWhenQueryingThenOmitsInvalidFields(
	t *testing.T,
) {
	for name, account := range map[string]string{
		"absent": "", "null": `,"account":null`, "empty": `,"account":{}`,
		"whitespace":   `,"account":{"email":"  ","plan":" "}`,
		"unknown only": `,"account":{"token":"private"}`,
		"oversized":    `,"account":{"email":"` + strings.Repeat("e", 255) + `","display_name":"` + strings.Repeat("n", 257) + `","organization":"` + strings.Repeat("o", 257) + `","plan":"` + strings.Repeat("p", 129) + `"}`,
		"control":      `,"account":{"email":"email\n@example.com","display_name":"name\u0000","organization":"org\u007f","plan":"plan\u200b"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newHarnessAuthFixture(t)
			f.client.result = json.RawMessage(
				`{"harness_auth":{"harness":"claude","state":"logged_in"` + account + `}}`,
			)
			_, data, err := f.svc.GetNodeHarnessAuthStatus(
				t.Context(),
				auth.RequestMetadata{},
				"node",
				"claude",
				"",
			)
			require.NoError(t, err)
			assert.Empty(t, harnessAuthAccountJSON(t, data))
		})
	}
	for _, tc := range []struct{ account, expected string }{
		{`{"email":"person@example.com","plan":"bad\nplan"}`, `{"email":"person@example.com"}`},
		{`{"display_name":"Example \u4e2d\u6587","organization":"Example"}`, `{"display_name":"Example \u4e2d\u6587","organization":"Example"}`},
		{`{"email":"` + strings.Repeat("e", 254) + `","display_name":"` + strings.Repeat("n", 256) + `","organization":"` + strings.Repeat("o", 256) + `","plan":"` + strings.Repeat("p", 128) + `"}`, `{"email":"` + strings.Repeat("e", 254) + `","display_name":"` + strings.Repeat("n", 256) + `","organization":"` + strings.Repeat("o", 256) + `","plan":"` + strings.Repeat("p", 128) + `"}`},
	} {
		f := newHarnessAuthFixture(t)
		f.client.result = json.RawMessage(
			`{"harness_auth":{"harness":"claude","state":"logged_in","account":` + tc.account + `}}`,
		)
		_, data, err := f.svc.GetNodeHarnessAuthStatus(
			t.Context(),
			auth.RequestMetadata{},
			"node",
			"claude",
			"",
		)
		require.NoError(t, err)
		assert.JSONEq(t, tc.expected, string(harnessAuthAccountJSON(t, data)))
	}
}
