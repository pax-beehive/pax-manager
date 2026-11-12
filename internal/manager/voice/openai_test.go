package voice

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestOpenAI(t *testing.T) (Provider, <-chan *serverConn) {
	t.Helper()
	url, conns := fakeProviderServer(t)
	provider, err := newOpenAIProvider(
		OpenAISettings{APIKey: "sk-test", URL: url},
		[]string{" zh-CN ", "en", ""},
	)
	require.NoError(t, err)
	return provider, conns
}

func openTestOpenAI(t *testing.T, opts Options) (Stream, *serverConn) {
	t.Helper()
	provider, conns := newTestOpenAI(t)
	opened := openAsync(t.Context(), provider, opts)
	server := acceptConn(t, conns)
	stream := awaitOpen(t, opened)
	server.readFrame(t)
	return stream, server
}

func TestOpenAIGivenSessionHintsThenSessionUpdateConfiguresTranscription(t *testing.T) {
	provider, conns := newTestOpenAI(t)
	opened := openAsync(t.Context(), provider, Options{
		Keywords: []string{"paxd"},
		Context:  []string{"run make lint"},
		UserRef:  "user-hash",
	})
	server := acceptConn(t, conns)
	awaitOpen(t, opened)
	assert.Equal(t, "Bearer sk-test", server.header.Get("Authorization"))
	assert.Equal(t, "user-hash", server.header.Get("OpenAI-Safety-Identifier"))
	assert.JSONEq(t, `{
		"type": "session.update",
		"session": {
			"type": "transcription",
			"audio": {"input": {
				"format": {"type": "audio/pcm", "rate": 24000},
				"transcription": {
					"model": "gpt-live-transcribe",
					"delay": "low",
					"languages": ["zh-cn", "en"],
					"prompt": "Dictation for a coding agent. Recent conversation:\nrun make lint",
					"keywords": ["paxd"]
				},
				"turn_detection": null
			}}
		}
	}`, string(server.readFrame(t)))
}

func TestOpenAIGivenDictationThenDeltasAndCompletionBecomeSnapshots(t *testing.T) {
	stream, server := openTestOpenAI(t, Options{})
	audio := make([]byte, openAIMinCommitBytes)
	audio[0] = 7
	require.NoError(t, stream.SendAudio(audio))
	var appended struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
	}
	require.NoError(t, json.Unmarshal(server.readFrame(t), &appended))
	assert.Equal(t, "input_audio_buffer.append", appended.Type)
	decoded, err := base64.StdEncoding.DecodeString(appended.Audio)
	require.NoError(t, err)
	assert.Equal(t, audio, decoded)

	server.writeJSONEvent(t, map[string]string{
		"type": "conversation.item.input_audio_transcription.delta", "item_id": "item_1",
		"delta": "Hel",
	})
	assert.Equal(t, Snapshot{Partial: "Hel"}, nextSnapshot(t, stream))
	server.writeJSONEvent(t, map[string]string{
		"type": "conversation.item.input_audio_transcription.delta", "item_id": "item_1",
		"delta": "lo",
	})
	assert.Equal(t, Snapshot{Partial: "Hello"}, nextSnapshot(t, stream))

	require.NoError(t, stream.Finish())
	assert.JSONEq(t, `{"type":"input_audio_buffer.commit"}`, string(server.readFrame(t)))
	assert.ErrorIs(t, stream.SendAudio(audio), errStreamFinished)
	server.writeJSONEvent(t, map[string]string{
		"type": "input_audio_buffer.committed", "item_id": "item_1",
	})
	server.writeJSONEvent(t, map[string]string{
		"type": "conversation.item.input_audio_transcription.completed", "item_id": "item_1",
		"transcript": "Hello.",
	})
	assert.Equal(t, Snapshot{Final: "Hello."}, drain(t, stream))
	assert.NoError(t, stream.Err())
}

func TestOpenAIGivenCompletionForUnseenItemThenPendingTextIsReplaced(t *testing.T) {
	stream, server := openTestOpenAI(t, Options{})
	require.NoError(t, stream.SendAudio(make([]byte, openAIMinCommitBytes)))
	server.readFrame(t)
	server.writeJSONEvent(t, map[string]string{
		"type":  "conversation.item.input_audio_transcription.delta",
		"delta": "draft",
	})
	assert.Equal(t, Snapshot{Partial: "draft"}, nextSnapshot(t, stream))

	require.NoError(t, stream.Finish())
	server.readFrame(t)
	server.writeJSONEvent(t, map[string]string{
		"type": "conversation.item.input_audio_transcription.completed", "item_id": "item_9",
		"transcript": "Final text.",
	})
	assert.Equal(t, Snapshot{Final: "Final text."}, drain(t, stream))
	assert.NoError(t, stream.Err())
}

func TestOpenAIGivenTooLittleAudioThenFinishEndsWithoutCommit(t *testing.T) {
	stream, server := openTestOpenAI(t, Options{})
	require.NoError(t, stream.SendAudio([]byte{1, 2}))
	server.readFrame(t)

	require.NoError(t, stream.Finish())
	drain(t, stream)
	assert.NoError(t, stream.Err())
	require.NoError(t, server.SetReadDeadline(time.Now().Add(testTimeout)))
	_, _, err := server.ReadMessage()
	assert.Error(t, err, "no commit is sent after the stream closes")
}

func TestOpenAIGivenErrorEventThenStreamFails(t *testing.T) {
	stream, server := openTestOpenAI(t, Options{})
	require.NoError(t, server.WriteJSON(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": "invalid_request_error", "message": "bad keyword"},
	}))
	drain(t, stream)
	require.Error(t, stream.Err())
	assert.Contains(t, stream.Err().Error(), "bad keyword")
}
