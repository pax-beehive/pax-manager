package voice

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestSeedASR(t *testing.T) (Provider, <-chan *serverConn) {
	t.Helper()
	url, conns := fakeProviderServer(t)
	provider, err := newSeedASRProvider(
		ProviderBytePlus,
		SeedASRSettings{URL: url, APIKey: "key-1"},
		defaultBytePlusURL,
	)
	require.NoError(t, err)
	return provider, conns
}

func TestSeedASRGivenDictationThenTwoPassResultsBecomeSnapshots(t *testing.T) {
	provider, conns := newTestSeedASR(t)
	opened := openAsync(t.Context(), provider, Options{
		Keywords: []string{"paxd"},
		Context:  []string{"older turn", "newer turn"},
	})
	server := acceptConn(t, conns)
	assert.Equal(t, "key-1", server.header.Get("X-Api-Key"))
	assert.Equal(t, defaultSeedASRResourceID, server.header.Get("X-Api-Resource-Id"))
	assert.NotEmpty(t, server.header.Get("X-Api-Request-Id"))
	assert.Equal(t, server.header.Get("X-Api-Request-Id"), server.header.Get("X-Api-Connect-Id"))

	request := server.readClientFrame(t)
	assert.Equal(t, [4]byte{0x11, 0x11, 0x11, 0x00}, request.header)
	assert.Equal(t, int32(1), request.sequence)
	var body struct {
		Audio   seedAudio `json:"audio"`
		Request struct {
			ModelName       string `json:"model_name"`
			EnableNonstream bool   `json:"enable_nonstream"`
			ResultType      string `json:"result_type"`
			Corpus          struct {
				Context string `json:"context"`
			} `json:"corpus"`
		} `json:"request"`
	}
	require.NoError(t, json.Unmarshal(request.payload, &body))
	assert.Equal(
		t,
		seedAudio{Format: "pcm", Codec: "raw", Rate: 16000, Bits: 16, Channel: 1},
		body.Audio,
	)
	assert.Equal(t, "bigmodel", body.Request.ModelName)
	assert.True(t, body.Request.EnableNonstream)
	assert.Equal(t, "full", body.Request.ResultType)
	assert.JSONEq(
		t,
		`{"hotwords":[{"word":"paxd"}],"context_type":"dialog_ctx",`+
			`"context_data":[{"text":"newer turn"},{"text":"older turn"}]}`,
		body.Request.Corpus.Context,
	)
	server.writeBinary(t, serverFrame(t, seedFlagSequence, 1, `{"result":{"text":""}}`))
	stream := awaitOpen(t, opened)

	require.NoError(t, stream.SendAudio([]byte{1, 2, 3, 4}))
	audio := server.readClientFrame(t)
	assert.Equal(t, [4]byte{0x11, 0x21, 0x11, 0x00}, audio.header)
	assert.Equal(t, int32(2), audio.sequence)
	assert.Equal(t, []byte{1, 2, 3, 4}, audio.payload)

	server.writeBinary(t, serverFrame(t, seedFlagSequence, 2,
		`{"result":{"text":"hello","utterances":[{"text":"hello","definite":false}]}}`))
	assert.Equal(t, Snapshot{Partial: "hello"}, nextSnapshot(t, stream))
	server.writeBinary(t, serverFrame(t, seedFlagSequence, 3,
		`{"result":{"utterances":[{"text":"Hello world.","definite":true},`+
			`{"text":"and","definite":false}]}}`))
	assert.Equal(t, Snapshot{Final: "Hello world.", Partial: "and"}, nextSnapshot(t, stream))

	require.NoError(t, stream.Finish())
	last := server.readClientFrame(t)
	assert.Equal(t, [4]byte{0x11, 0x23, 0x11, 0x00}, last.header)
	assert.Equal(t, int32(-3), last.sequence)
	assert.Len(t, last.payload, seedASRTrailingSilenceBytes)
	assert.ErrorIs(t, stream.SendAudio([]byte{1}), errStreamFinished)

	server.writeBinary(t, serverFrame(t, seedFlagLastSequence, -3,
		`{"result":{"utterances":[{"text":"Hello world.","definite":true},`+
			`{"text":"And more.","definite":true}]}}`))
	assert.Equal(t, Snapshot{Final: "Hello world. And more."}, drain(t, stream))
	assert.NoError(t, stream.Err())
}

func TestSeedASRGivenRejectedSessionRequestThenOpenFails(t *testing.T) {
	provider, conns := newTestSeedASR(t)
	opened := openAsync(t.Context(), provider, Options{})
	server := acceptConn(t, conns)
	server.readClientFrame(t)
	server.writeBinary(t, serverErrorFrame(t, 45000001, "invalid resource id"))
	result := <-opened
	require.Error(t, result.err)
	assert.Contains(t, result.err.Error(), "45000001")
	assert.Contains(t, result.err.Error(), "invalid resource id")
}

func TestSeedASRGivenServiceErrorMidStreamThenStreamFails(t *testing.T) {
	provider, conns := newTestSeedASR(t)
	opened := openAsync(t.Context(), provider, Options{})
	server := acceptConn(t, conns)
	server.readClientFrame(t)
	server.writeBinary(t, serverFrame(t, seedFlagSequence, 1, `{}`))
	stream := awaitOpen(t, opened)

	server.writeBinary(t, serverErrorFrame(t, 55000000, "server busy"))
	drain(t, stream)
	require.Error(t, stream.Err())
	assert.Contains(t, stream.Err().Error(), "server busy")
}

func TestParseSeedResponseGivenResultShapesThenSnapshotsKeepOrder(t *testing.T) {
	snapshot, ok, err := parseSeedResponse([]byte(
		`{"result":[{"utterances":[{"text":"a","definite":false},` +
			`{"text":"b","definite":true}]}]}`,
	))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, Snapshot{Partial: "a b"}, snapshot)

	_, ok, err = parseSeedResponse([]byte(`{"audio_info":{"duration":10}}`))
	require.NoError(t, err)
	assert.False(t, ok)

	_, _, err = parseSeedResponse([]byte(`not json`))
	assert.Error(t, err)
}
