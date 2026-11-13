package voice

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestSoniox(t *testing.T, settings SonioxSettings) (Provider, <-chan *serverConn) {
	t.Helper()
	endpoint, conns := fakeProviderServer(t)
	settings.URL = endpoint
	if settings.APIKey == "" {
		settings.APIKey = "sk-soniox"
	}
	provider, err := newSonioxProvider(settings)
	require.NoError(t, err)
	return provider, conns
}

func openTestSoniox(t *testing.T) (Stream, *serverConn) {
	t.Helper()
	provider, conns := newTestSoniox(t, SonioxSettings{})
	opened := openAsync(t.Context(), provider, Options{})
	server := acceptConn(t, conns)
	stream := awaitOpen(t, opened)
	server.readFrame(t)
	return stream, server
}

func readSonioxFrame(t *testing.T, server *serverConn) (int, []byte) {
	t.Helper()
	require.NoError(t, server.SetReadDeadline(time.Now().Add(testTimeout)))
	kind, data, err := server.ReadMessage()
	require.NoError(t, err)
	return kind, data
}

func writeSonioxResponse(t *testing.T, server *serverConn, response string) {
	t.Helper()
	require.NoError(t, server.WriteMessage(websocket.TextMessage, []byte(response)))
}

func TestSonioxGivenDefaultSettingsThenProviderUsesRealtimeDefaults(t *testing.T) {
	provider, err := NewProvider(Settings{
		Provider: " Soniox ",
		Soniox:   SonioxSettings{APIKey: " sk-soniox "},
	})
	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.Equal(t, ProviderSoniox, provider.Name())
	assert.Equal(t, 16000, provider.SampleRate())
}

func TestSonioxGivenNoHintsThenInitialTextFrameConfiguresRawPCM(t *testing.T) {
	provider, conns := newTestSoniox(t, SonioxSettings{APIKey: " sk-soniox "})
	opened := openAsync(t.Context(), provider, Options{})
	server := acceptConn(t, conns)
	awaitOpen(t, opened)
	assert.Equal(t, "Bearer sk-soniox", server.header.Get("Authorization"))
	kind, data := readSonioxFrame(t, server)
	assert.Equal(t, websocket.TextMessage, kind)
	assert.JSONEq(t, `{
		"model": "stt-rt-v6",
		"audio_format": "pcm_s16le",
		"sample_rate": 16000,
		"num_channels": 1,
		"enable_endpoint_detection": true
	}`, string(data))
}

func TestSonioxGivenSessionHintsThenConfigUsesNormalizedLanguagesAndContext(t *testing.T) {
	provider, conns := newTestSoniox(t, SonioxSettings{
		Model:     " custom-model ",
		Languages: []string{" zh-CN ", "en", "EN", "zh", ""},
	})
	opened := openAsync(t.Context(), provider, Options{
		Keywords: []string{" session-composer ", "Mic", "Mic", "<tag>"},
		Context:  []string{" older\nturn ", "newer turn"},
	})
	server := acceptConn(t, conns)
	awaitOpen(t, opened)
	assert.JSONEq(t, `{
		"model": "custom-model",
		"audio_format": "pcm_s16le",
		"sample_rate": 16000,
		"num_channels": 1,
		"enable_endpoint_detection": true,
		"language_hints": ["zh", "en"],
		"context": {
			"terms": ["session-composer", "Mic", "tag"],
			"text": "older turn\nnewer turn"
		}
	}`, string(server.readFrame(t)))
}

func TestSonioxGivenOversizedHintsThenConfigBoundsTermsAndContext(t *testing.T) {
	provider, conns := newTestSoniox(t, SonioxSettings{})
	keywords := []string{strings.Repeat("x", maxKeywordRunes+20)}
	for i := 0; i < maxKeywords+20; i++ {
		keywords = append(keywords, strings.Repeat("a", i+1))
	}
	opened := openAsync(t.Context(), provider, Options{
		Keywords: keywords,
		Context:  []string{strings.Repeat("old ", 400), "newest"},
	})
	server := acceptConn(t, conns)
	awaitOpen(t, opened)
	var config struct {
		Context struct {
			Terms []string `json:"terms"`
			Text  string   `json:"text"`
		} `json:"context"`
	}
	require.NoError(t, json.Unmarshal(server.readFrame(t), &config))
	assert.LessOrEqual(t, len(config.Context.Terms), maxKeywords)
	require.NotEmpty(t, config.Context.Terms)
	assert.Len(t, config.Context.Terms[0], maxKeywordRunes)
	assert.LessOrEqual(t, len(config.Context.Text), maxContextRunes+1)
	assert.True(t, strings.HasSuffix(config.Context.Text, "\nnewest"))
}

func TestSonioxGivenAudioThenFramesContainOriginalBinaryPCM(t *testing.T) {
	stream, server := openTestSoniox(t)
	require.NoError(t, stream.SendAudio(nil))
	require.NoError(t, stream.SendAudio([]byte{}))
	audio := []byte{0, 0, 255, 127, 0, 128, 13, 10}
	require.NoError(t, stream.SendAudio(audio))
	kind, data := readSonioxFrame(t, server)
	assert.Equal(t, websocket.BinaryMessage, kind)
	assert.Equal(t, audio, data, "empty audio must not emit an end-of-stream frame")
}

func TestSonioxGivenDictationThenFinalTokensAppendAndPartialTailIsReplaced(t *testing.T) {
	stream, server := openTestSoniox(t)
	writeSonioxResponse(t, server, `{"tokens":[{"text":"Hel","is_final":false}]}`)
	assert.Equal(t, Snapshot{Partial: "Hel"}, nextSnapshot(t, stream))
	writeSonioxResponse(
		t,
		server,
		`{"tokens":[{"text":"Hello","is_final":false},{"text":" wor","is_final":false}]}`,
	)
	assert.Equal(t, Snapshot{Partial: "Hello wor"}, nextSnapshot(t, stream))
	writeSonioxResponse(
		t,
		server,
		`{"tokens":[{"text":"Hello","is_final":true},{"text":" world","is_final":false}]}`,
	)
	assert.Equal(t, Snapshot{Final: "Hello", Partial: " world"}, nextSnapshot(t, stream))
	writeSonioxResponse(
		t,
		server,
		`{"tokens":[{"text":" world","is_final":true},{"text":".","is_final":true},{"text":"<end>","is_final":true}]}`,
	)
	assert.Equal(t, Snapshot{Final: "Hello world."}, nextSnapshot(t, stream))
	writeSonioxResponse(
		t,
		server,
		`{"tokens":[{"text":"\u4f60","is_final":true},{"text":"\u597d","is_final":false}]}`,
	)
	assert.Equal(
		t,
		Snapshot{Final: "Hello world.\u4f60", Partial: "\u597d"},
		nextSnapshot(t, stream),
	)
}

func TestSonioxGivenControlMarkersThenOnlyDocumentedMarkersAreOmitted(t *testing.T) {
	stream, server := openTestSoniox(t)
	writeSonioxResponse(
		t,
		server,
		`{"tokens":[{"text":"  a","is_final":true},{"text":"\n","is_final":true},{"text":"<tag>","is_final":true},{"text":"<end>","is_final":true},{"text":"<fin>","is_final":true},{"text":"  tail\t","is_final":false},{"text":"<end>","is_final":false},{"text":"<fin>","is_final":false}]}`,
	)
	assert.Equal(t, Snapshot{Final: "  a\n<tag>", Partial: "  tail\t"}, nextSnapshot(t, stream))
}

func TestSonioxGivenFinalizedSubwordsThenSnapshotsDoNotInsertWordBreaks(t *testing.T) {
	stream, server := openTestSoniox(t)
	writeSonioxResponse(t, server, `{"tokens":[{"text":"com","is_final":true}]}`)
	first := nextSnapshot(t, stream)
	assert.Empty(t, first.Final, "hold an unfinished Latin word outside the immutable prefix")
	assert.Equal(t, "com", first.Text())
	writeSonioxResponse(
		t,
		server,
		`{"tokens":[{"text":"po","is_final":true},{"text":"ser","is_final":false}]}`,
	)
	second := nextSnapshot(t, stream)
	assert.Empty(t, second.Final)
	assert.Equal(t, "composer", second.Text())
	writeSonioxResponse(
		t,
		server,
		`{"tokens":[{"text":"ser ","is_final":true},{"text":"test","is_final":false}]}`,
	)
	third := nextSnapshot(t, stream)
	assert.Equal(t, "composer ", third.Final)
	assert.Equal(t, "composer test", third.Text())
	writeSonioxResponse(t, server, `{"tokens":[{"text":"test","is_final":true}],"finished":true}`)
	assert.Equal(t, Snapshot{Final: "composer test"}, drain(t, stream))
}

func TestSonioxGivenEmptyTokenReplacementThenProvisionalTextDisappears(t *testing.T) {
	stream, server := openTestSoniox(t)
	writeSonioxResponse(
		t,
		server,
		`{"tokens":[{"text":"Hello ","is_final":true},{"text":"wrong","is_final":false}]}`,
	)
	assert.Equal(t, Snapshot{Final: "Hello ", Partial: "wrong"}, nextSnapshot(t, stream))
	writeSonioxResponse(t, server, `{"tokens":[]}`)
	assert.Equal(t, Snapshot{Final: "Hello "}, nextSnapshot(t, stream))
}

func TestSonioxGivenCloseReasonEchoesAPIKeyThenErrorIsRedacted(t *testing.T) {
	stream, server := openTestSoniox(t)
	require.NoError(t, server.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "invalid sk-soniox")))
	drain(t, stream)
	require.Error(t, stream.Err())
	assert.NotContains(t, stream.Err().Error(), "sk-soniox")
	assert.Contains(t, stream.Err().Error(), "invalid")
}

func TestSonioxGivenStopThenEmptyTextFrameDrainsLateFinalUntilFinished(t *testing.T) {
	stream, server := openTestSoniox(t)
	writeSonioxResponse(t, server, `{"tokens":[{"text":"draft","is_final":false}]}`)
	assert.Equal(t, Snapshot{Partial: "draft"}, nextSnapshot(t, stream))
	require.NoError(t, stream.Finish())
	require.NoError(t, stream.Finish(), "repeated finish must not emit another frame")
	assert.ErrorIs(t, stream.SendAudio([]byte{1, 2}), errStreamFinished)
	kind, data := readSonioxFrame(t, server)
	assert.Equal(t, websocket.TextMessage, kind)
	assert.Empty(t, data)
	select {
	case _, ok := <-stream.Snapshots():
		t.Fatalf("stream ended before provider finished: open=%t err=%v", ok, stream.Err())
	default:
	}
	writeSonioxResponse(
		t,
		server,
		`{"tokens":[{"text":"Final text.","is_final":true},{"text":"<end>","is_final":true}]}`,
	)
	assert.Equal(t, Snapshot{Final: "Final text."}, nextSnapshot(t, stream))
	writeSonioxResponse(t, server, `{"finished":true}`)
	drain(t, stream)
	assert.NoError(t, stream.Err())
	require.NoError(t, server.SetReadDeadline(time.Now().Add(testTimeout)))
	_, _, err := server.ReadMessage()
	assert.Error(t, err, "no second end-of-stream frame is sent")
}

func TestSonioxGivenFinalTokensWithFinishedThenLastTranscriptIsPreserved(t *testing.T) {
	stream, server := openTestSoniox(t)
	require.NoError(t, stream.Finish())
	server.readFrame(t)
	writeSonioxResponse(
		t,
		server,
		`{"tokens":[{"text":"Done.","is_final":true},{"text":"<end>","is_final":true}],"finished":true}`,
	)
	assert.Equal(t, Snapshot{Final: "Done."}, drain(t, stream))
	assert.NoError(t, stream.Err())
}

func TestSonioxGivenProviderErrorThenStreamFailsWithOriginalDetails(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		response string
		details  []string
	}{
		{
			name:     "all-error-fields",
			response: `{"error_code":401,"error_message":"Invalid API key","error_type":"authentication_error"}`,
			details:  []string{"401", "Invalid API key", "authentication_error"},
		},
		{name: "code-only", response: `{"error_code":429}`, details: []string{"429"}},
		{name: "message-only", response: `{"error_message":"Quota exhausted"}`, details: []string{"Quota exhausted"}},
		{name: "type-only", response: `{"error_type":"invalid_request"}`, details: []string{"invalid_request"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			stream, server := openTestSoniox(t)
			writeSonioxResponse(t, server, scenario.response)
			drain(t, stream)
			require.Error(t, stream.Err())
			assert.Contains(t, stream.Err().Error(), "soniox")
			for _, detail := range scenario.details {
				assert.Contains(t, stream.Err().Error(), detail)
			}
		})
	}
}

func TestSonioxGivenProviderEchoesAPIKeyThenErrorRedactsTheCredential(t *testing.T) {
	stream, server := openTestSoniox(t)
	writeSonioxResponse(
		t,
		server,
		`{"error_code":401,"error_message":"Invalid key sk-soniox","error_type":"authentication_error"}`,
	)
	drain(t, stream)
	require.Error(t, stream.Err())
	assert.NotContains(t, stream.Err().Error(), "sk-soniox")
	assert.Contains(t, stream.Err().Error(), "401")
	assert.Contains(t, stream.Err().Error(), "authentication_error")
}

func TestSonioxGivenMalformedEventThenStreamFails(t *testing.T) {
	stream, server := openTestSoniox(t)
	writeSonioxResponse(t, server, `{"tokens":`)
	drain(t, stream)
	assert.Error(t, stream.Err())
}

func TestSonioxGivenEOFBeforeFinishedThenStreamFails(t *testing.T) {
	for _, stopping := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "during-dictation", true: "after-stop"}[stopping],
			func(t *testing.T) {
				stream, server := openTestSoniox(t)
				if stopping {
					require.NoError(t, stream.Finish())
					server.readFrame(t)
				}
				require.NoError(t, server.Close())
				drain(t, stream)
				assert.Error(t, stream.Err())
			},
		)
	}
}

func TestSonioxGivenCloseThenRepeatedCloseIsSafe(t *testing.T) {
	stream, _ := openTestSoniox(t)
	assert.NoError(t, stream.Close())
	assert.NoError(t, stream.Close())
	drain(t, stream)
}

func TestSonioxGivenClosedConnectionThenWritesFail(t *testing.T) {
	stream, _ := openTestSoniox(t)
	require.NoError(t, stream.Close())
	assert.Error(t, stream.SendAudio([]byte{1, 2}))
	assert.Error(t, stream.Finish())
	drain(t, stream)
}

func TestSonioxGivenRejectedHandshakeThenOpenReportsProviderReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "account is not active", http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	provider, err := newSonioxProvider(SonioxSettings{
		APIKey: "sk-soniox", URL: "ws" + strings.TrimPrefix(server.URL, "http"),
	})
	require.NoError(t, err)
	stream, err := provider.Open(t.Context(), Options{})
	assert.Nil(t, stream)
	assert.ErrorContains(t, err, "HTTP 403")
	assert.ErrorContains(t, err, "account is not active")
}

func TestSonioxGivenRejectedHandshakeEchoesKeyThenOpenRedactsTheCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "key sk-soniox is invalid", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	provider, err := newSonioxProvider(SonioxSettings{
		APIKey: "sk-soniox", URL: "ws" + strings.TrimPrefix(server.URL, "http"),
	})
	require.NoError(t, err)
	_, err = provider.Open(t.Context(), Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 401")
	assert.NotContains(t, err.Error(), "sk-soniox")
}

func TestSonioxGivenInvalidSettingsThenConstructionFails(t *testing.T) {
	for _, key := range []string{"", " \n\t "} {
		_, err := newSonioxProvider(SonioxSettings{APIKey: key})
		assert.ErrorContains(t, err, "SONIOX_API_KEY")
	}
	for _, endpoint := range []string{"not a url", "https://example.com", "ws:///missing-host", "://bad"} {
		_, err := newSonioxProvider(SonioxSettings{APIKey: "sk-soniox", URL: endpoint})
		assert.Error(t, err, endpoint)
	}
}
