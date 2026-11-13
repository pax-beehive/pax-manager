package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/voice"
)

type testVoiceProvider struct {
	opened chan voice.Options
}

func (p *testVoiceProvider) Name() string { return "test" }

func (p *testVoiceProvider) SampleRate() int { return 24000 }

func (p *testVoiceProvider) Open(_ context.Context, opts voice.Options) (voice.Stream, error) {
	p.opened <- opts
	return &testVoiceStream{snapshots: make(chan voice.Snapshot, 1)}, nil
}

// testVoiceStream reports how much audio it heard once the client stops.
type testVoiceStream struct {
	audio     []byte
	mu        sync.Mutex
	closed    bool
	snapshots chan voice.Snapshot
}

func (s *testVoiceStream) SendAudio(pcm []byte) error {
	s.audio = append(s.audio, pcm...)
	return nil
}

func (s *testVoiceStream) Finish() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.snapshots <- voice.Snapshot{Final: fmt.Sprintf("heard %d bytes", len(s.audio))}
		s.closed = true
		close(s.snapshots)
	}
	return nil
}

func (s *testVoiceStream) Snapshots() <-chan voice.Snapshot { return s.snapshots }

func (s *testVoiceStream) Err() error { return nil }

func (s *testVoiceStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.snapshots)
	}
	return nil
}

func newVoiceTestServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	cfg.AllowLocalUserHeader = true
	cfg.AdminEmails = map[string]bool{"admin@example.com": true}
	srv := newServer(cfg, NewMemoryStore(time.Now))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, srv.CloseTransportStore(ctx))
	})
	return srv
}

func getVoiceConfig(t *testing.T, srv *Server, email string) (int, voiceConfigResponse) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/user/self/voice/config", nil)
	if email != "" {
		r.Header.Set("X-User-Email", email)
	}
	w := httptest.NewRecorder()
	srv.routes().ServeHTTP(w, r)
	var body struct {
		Data voiceConfigResponse `json:"data"`
	}
	if w.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	}
	return w.Code, body.Data
}

func dialVoiceStream(
	t *testing.T,
	srv *Server,
	email string,
) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/user/", srv.handleVoiceStream)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/user/self/voice/stream"
	conn, resp, err := websocket.DefaultDialer.Dial(url, http.Header{"X-User-Email": {email}})
	if conn != nil {
		t.Cleanup(func() { _ = conn.Close() })
	}
	return conn, resp, err
}

func TestVoiceConfigGivenNoProviderThenVoiceIsDisabled(t *testing.T) {
	srv := newVoiceTestServer(t, Config{})
	status, config := getVoiceConfig(t, srv, "admin@example.com")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, voiceConfigResponse{}, config)

	status, _ = getVoiceConfig(t, srv, "")
	assert.Equal(t, http.StatusUnauthorized, status)
}

func TestVoiceConfigGivenAdminOnlyProviderThenOnlyAdminsSeeIt(t *testing.T) {
	srv := newVoiceTestServer(t, Config{VoiceAdminOnly: true, VoiceMaxSession: 2 * time.Minute})
	srv.voice = &testVoiceProvider{opened: make(chan voice.Options, 1)}

	_, config := getVoiceConfig(t, srv, "admin@example.com")
	assert.Equal(t, voiceConfigResponse{
		Enabled:            true,
		Provider:           "test",
		SampleRate:         24000,
		Encoding:           voice.Encoding,
		MaxDurationSeconds: 120,
	}, config)

	_, config = getVoiceConfig(t, srv, "member@example.com")
	assert.False(t, config.Enabled)
}

func TestVoiceStreamGivenConfiguredProviderThenDictationRoundTrips(t *testing.T) {
	srv := newVoiceTestServer(t, Config{})
	provider := &testVoiceProvider{opened: make(chan voice.Options, 1)}
	srv.voice = provider
	conn, _, err := dialVoiceStream(t, srv, "member@example.com")
	require.NoError(t, err)

	require.NoError(t, conn.WriteMessage(
		websocket.TextMessage,
		[]byte(`{"type":"start","keywords":["paxd"]}`),
	))
	var ready map[string]any
	require.NoError(t, conn.ReadJSON(&ready))
	assert.Equal(t, "ready", ready["type"])
	assert.Equal(t, float64(24000), ready["sample_rate"])
	opts := <-provider.opened
	assert.Equal(t, []string{"paxd"}, opts.Keywords)
	assert.Equal(t, voiceUserRef(principalUserID(t, srv, "member@example.com")), opts.UserRef)

	require.NoError(t, conn.WriteMessage(websocket.BinaryMessage, make([]byte, 480)))
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"stop"}`)))
	var transcript, done map[string]any
	require.NoError(t, conn.ReadJSON(&transcript))
	assert.Equal(t, "heard 480 bytes", transcript["final"])
	require.NoError(t, conn.ReadJSON(&done))
	assert.Equal(t, "done", done["type"])
	assert.Equal(t, "heard 480 bytes", done["final"])
}

func TestVoiceStreamGivenUnavailableVoiceThenUpgradeIsRefused(t *testing.T) {
	srv := newVoiceTestServer(t, Config{})
	_, resp, err := dialVoiceStream(t, srv, "admin@example.com")
	require.ErrorIs(t, err, websocket.ErrBadHandshake)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	srv = newVoiceTestServer(t, Config{VoiceAdminOnly: true})
	srv.voice = &testVoiceProvider{opened: make(chan voice.Options, 1)}
	_, resp, err = dialVoiceStream(t, srv, "member@example.com")
	require.ErrorIs(t, err, websocket.ErrBadHandshake)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestConfigureVoiceGivenProviderSettingsThenOnlyValidConfigEnablesVoice(t *testing.T) {
	srv := newVoiceTestServer(t, Config{VoiceProvider: voice.ProviderOpenAI})
	assert.Nil(t, srv.voice, "a provider without credentials stays disabled")

	srv = newVoiceTestServer(t, Config{
		VoiceProvider: voice.ProviderOpenAI,
		OpenAIAPIKey:  "sk-test",
	})
	require.NotNil(t, srv.voice)
	assert.Equal(t, voice.ProviderOpenAI, srv.voice.Name())

	srv = newVoiceTestServer(t, Config{
		VoiceProvider:      voice.ProviderVolcengine,
		VoiceSeedASRAPIKey: "seed-key",
	})
	require.NotNil(t, srv.voice)
	assert.Equal(t, 16000, srv.voice.SampleRate())

	srv = newVoiceTestServer(t, Config{
		VoiceProvider:   voice.ProviderQwen,
		DashScopeAPIKey: "sk-dashscope",
	})
	require.NotNil(t, srv.voice)
	assert.Equal(t, voice.ProviderQwen, srv.voice.Name())
	assert.Equal(t, 16000, srv.voice.SampleRate())
}

func TestVoiceSettingsGivenSonioxConfigurationThenMapsAllProviderSettings(t *testing.T) {
	settings := voiceSettings(Config{
		VoiceProvider:    "soniox",
		VoiceLanguages:   []string{"zh-cn", "en"},
		SonioxAPIKey:     "soniox-test-key",
		VoiceSonioxURL:   "wss://soniox.example/transcribe-websocket",
		VoiceSonioxModel: "stt-rt-test",
	})

	assert.Equal(t, "soniox", settings.Provider)
	assert.Equal(t, voice.SonioxSettings{
		APIKey:    "soniox-test-key",
		URL:       "wss://soniox.example/transcribe-websocket",
		Model:     "stt-rt-test",
		Languages: []string{"zh-cn", "en"},
	}, settings.Soniox)
}

func TestVoiceConfigGivenAdminOnlySonioxThenOnlyAdminsSeeConfiguredProvider(t *testing.T) {
	srv := newVoiceTestServer(t, Config{
		VoiceProvider:  "soniox",
		SonioxAPIKey:   "soniox-test-key",
		VoiceAdminOnly: true,
	})
	require.NotNil(t, srv.voice)

	status, config := getVoiceConfig(t, srv, "admin@example.com")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, voiceConfigResponse{
		Enabled:            true,
		Provider:           "soniox",
		SampleRate:         16000,
		Encoding:           voice.Encoding,
		MaxDurationSeconds: 300,
	}, config)

	status, config = getVoiceConfig(t, srv, "member@example.com")
	assert.Equal(t, http.StatusOK, status)
	assert.False(t, config.Enabled)
}

func TestVoiceConfigGivenSonioxWithoutCredentialsThenVoiceStaysDisabled(t *testing.T) {
	srv := newVoiceTestServer(t, Config{
		VoiceProvider:  "soniox",
		VoiceAdminOnly: true,
	})
	assert.Nil(t, srv.voice)

	status, config := getVoiceConfig(t, srv, "admin@example.com")
	assert.Equal(t, http.StatusOK, status)
	assert.False(t, config.Enabled)
}

func TestVoiceOpenAPIDocumentsVoiceEndpoints(t *testing.T) {
	raw, err := openAPIDocument("https://manager.example")
	require.NoError(t, err)
	var document struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(raw, &document))
	assert.Contains(t, document.Paths, openAPIUserVoiceConfigPath)
	assert.Contains(t, document.Paths, openAPIUserVoiceStreamPath)
}

func principalUserID(t *testing.T, srv *Server, email string) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-User-Email", email)
	principal, err := srv.auth.Principal(t.Context(), httpRequestMetadata(r))
	require.NoError(t, err)
	return principal.User.UserID
}
