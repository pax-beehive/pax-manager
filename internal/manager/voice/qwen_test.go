package voice

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestQwen(t *testing.T, language string) (Provider, <-chan *serverConn) {
	t.Helper()
	endpoint, conns := fakeProviderServer(t)
	provider, err := newQwenProvider(QwenSettings{
		APIKey:   "sk-qwen",
		URL:      endpoint,
		Language: language,
	})
	require.NoError(t, err)
	return provider, conns
}

func openTestQwen(t *testing.T) (Stream, *serverConn) {
	t.Helper()
	provider, conns := newTestQwen(t, "")
	opened := openAsync(t.Context(), provider, Options{})
	server := acceptConn(t, conns)
	stream := awaitOpen(t, opened)
	server.readFrame(t)
	return stream, server
}

type qwenClientEvent struct {
	EventID string `json:"event_id"`
	Type    string `json:"type"`
	Audio   string `json:"audio"`
	Session struct {
		Modalities              []string           `json:"modalities"`
		InputAudioFormat        string             `json:"input_audio_format"`
		SampleRate              int                `json:"sample_rate"`
		InputAudioTranscription *qwenTranscription `json:"input_audio_transcription"`
		TurnDetection           *qwenTurnDetection `json:"turn_detection"`
	} `json:"session"`
}

func readQwenEvent(t *testing.T, server *serverConn) qwenClientEvent {
	t.Helper()
	var event qwenClientEvent
	require.NoError(t, json.Unmarshal(server.readFrame(t), &event))
	assert.NotEmpty(t, event.EventID, "every client event needs an event_id")
	return event
}

func TestQwenGivenSessionHintsThenSessionUpdateUsesServerVADAndCorpus(t *testing.T) {
	provider, conns := newTestQwen(t, "zh")
	opened := openAsync(t.Context(), provider, Options{
		Keywords: []string{"paxd", "session-composer"},
		Context:  []string{"older turn", "newer turn"},
	})
	server := acceptConn(t, conns)
	awaitOpen(t, opened)
	assert.Equal(t, "Bearer sk-qwen", server.header.Get("Authorization"))

	update := readQwenEvent(t, server)
	assert.Equal(t, "session.update", update.Type)
	assert.Equal(t, []string{"text"}, update.Session.Modalities)
	assert.Equal(t, "pcm", update.Session.InputAudioFormat)
	assert.Equal(t, 16000, update.Session.SampleRate)
	assert.Equal(t, &qwenTurnDetection{
		Type:              "server_vad",
		Threshold:         0.2,
		SilenceDurationMS: 800,
	}, update.Session.TurnDetection)
	require.NotNil(t, update.Session.InputAudioTranscription)
	assert.Equal(t, "zh", update.Session.InputAudioTranscription.Language)
	assert.Equal(
		t,
		"Keywords: paxd, session-composer\n\nRecent conversation:\nolder turn\nnewer turn",
		update.Session.InputAudioTranscription.Corpus.Text,
	)
}

func TestQwenGivenNoHintsThenTranscriptionSettingsAreOmitted(t *testing.T) {
	provider, conns := newTestQwen(t, "")
	opened := openAsync(t.Context(), provider, Options{})
	server := acceptConn(t, conns)
	awaitOpen(t, opened)
	assert.Nil(t, readQwenEvent(t, server).Session.InputAudioTranscription)
}

func TestQwenGivenDictationThenUtterancesBecomeSnapshots(t *testing.T) {
	stream, server := openTestQwen(t)
	audio := []byte{1, 2, 3, 4}
	require.NoError(t, stream.SendAudio(audio))
	appended := readQwenEvent(t, server)
	assert.Equal(t, "input_audio_buffer.append", appended.Type)
	decoded, err := base64.StdEncoding.DecodeString(appended.Audio)
	require.NoError(t, err)
	assert.Equal(t, audio, decoded)

	server.writeJSONEvent(t, map[string]string{
		"type": "input_audio_buffer.speech_started", "item_id": "item_1",
	})
	server.writeJSONEvent(t, map[string]string{
		"type": "conversation.item.input_audio_transcription.text", "item_id": "item_1",
		"text": "Hello ", "stash": "wor",
	})
	assert.Equal(t, Snapshot{Partial: "Hello wor"}, nextSnapshot(t, stream))
	server.writeJSONEvent(t, map[string]string{
		"type": "conversation.item.input_audio_transcription.completed", "item_id": "item_1",
		"transcript": "Hello world.",
	})
	assert.Equal(t, Snapshot{Final: "Hello world."}, nextSnapshot(t, stream))
	server.writeJSONEvent(t, map[string]string{
		"type": "conversation.item.input_audio_transcription.text", "item_id": "item_2",
		"text": "", "stash": "and",
	})
	assert.Equal(t, Snapshot{Final: "Hello world.", Partial: "and"}, nextSnapshot(t, stream))

	require.NoError(t, stream.Finish())
	assert.Equal(t, "session.finish", readQwenEvent(t, server).Type)
	assert.ErrorIs(t, stream.SendAudio(audio), errStreamFinished)
	server.writeJSONEvent(t, map[string]string{
		"type": "conversation.item.input_audio_transcription.completed", "item_id": "item_2",
		"transcript": "And more.",
	})
	server.writeJSONEvent(t, map[string]string{"type": "session.finished"})
	assert.Equal(t, Snapshot{Final: "Hello world. And more."}, drain(t, stream))
	assert.NoError(t, stream.Err())
}

func TestQwenGivenErrorEventThenStreamFails(t *testing.T) {
	stream, server := openTestQwen(t)
	require.NoError(t, server.WriteJSON(map[string]any{
		"type":  "error",
		"error": map[string]string{"code": "InvalidApiKey", "message": "invalid api key"},
	}))
	drain(t, stream)
	require.Error(t, stream.Err())
	assert.Contains(t, stream.Err().Error(), "invalid api key")
}

func TestQwenEndpointGivenURLThenModelQueryIsAddedOnce(t *testing.T) {
	endpoint, err := qwenEndpoint(defaultQwenURL, defaultQwenModel)
	require.NoError(t, err)
	parsed, err := url.Parse(endpoint)
	require.NoError(t, err)
	assert.Equal(t, "dashscope-intl.aliyuncs.com", parsed.Host)
	assert.Equal(t, defaultQwenModel, parsed.Query().Get("model"))

	endpoint, err = qwenEndpoint(defaultQwenURL+"?model=custom", defaultQwenModel)
	require.NoError(t, err)
	assert.Equal(t, defaultQwenURL+"?model=custom", endpoint)

	_, err = qwenEndpoint("not a url", defaultQwenModel)
	assert.Error(t, err)
}
