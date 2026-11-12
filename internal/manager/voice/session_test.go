package voice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeProvider struct {
	stream  *fakeStream
	openErr error
	opened  chan Options
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{stream: newFakeStream(), opened: make(chan Options, 1)}
}

func (p *fakeProvider) Name() string { return "fake" }

func (p *fakeProvider) SampleRate() int { return 16000 }

func (p *fakeProvider) Open(_ context.Context, opts Options) (Stream, error) {
	p.opened <- opts
	if p.openErr != nil {
		return nil, p.openErr
	}
	return p.stream, nil
}

type fakeEvent struct {
	snapshot Snapshot
	done     bool
	err      error
}

// fakeStream publishes scripted events from one producer goroutine, like the
// read loop of a real provider.
type fakeStream struct {
	pipe       *snapshotPipe
	events     chan fakeEvent
	audio      chan []byte
	finished   chan struct{}
	finishOnce sync.Once
	closed     chan struct{}
	closeOnce  sync.Once
}

func newFakeStream() *fakeStream {
	s := &fakeStream{
		pipe:     newSnapshotPipe(),
		events:   make(chan fakeEvent),
		audio:    make(chan []byte, 16),
		finished: make(chan struct{}),
		closed:   make(chan struct{}),
	}
	go func() {
		for {
			select {
			case event := <-s.events:
				if event.done {
					s.pipe.finish(event.err)
					return
				}
				s.pipe.publish(event.snapshot)
			case <-s.closed:
				s.pipe.finish(errors.New("stream closed"))
				return
			}
		}
	}()
	return s
}

func (s *fakeStream) SendAudio(pcm []byte) error {
	s.audio <- append([]byte(nil), pcm...)
	return nil
}

func (s *fakeStream) Finish() error {
	s.finishOnce.Do(func() { close(s.finished) })
	return nil
}

func (s *fakeStream) Snapshots() <-chan Snapshot { return s.pipe.ch }

func (s *fakeStream) Err() error { return s.pipe.err }

func (s *fakeStream) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

func (s *fakeStream) emit(t *testing.T, event fakeEvent) {
	t.Helper()
	select {
	case s.events <- event:
	case <-time.After(testTimeout):
		t.Fatal("fake stream did not accept the event")
	}
}

func waitFor(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("timed out waiting for %s", what)
	}
}

type sessionMessage struct {
	Type       string  `json:"type"`
	Provider   string  `json:"provider"`
	SampleRate int     `json:"sample_rate"`
	Encoding   string  `json:"encoding"`
	Final      *string `json:"final"`
	Partial    *string `json:"partial"`
	Code       string  `json:"code"`
}

func serveTestSession(
	t *testing.T,
	provider Provider,
	limits Limits,
) (*websocket.Conn, <-chan error) {
	t.Helper()
	result := make(chan error, 1)
	var upgrader websocket.Upgrader
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			result <- err
			return
		}
		defer func() { _ = conn.Close() }()
		result <- Serve(r.Context(), conn, provider, limits, "user-ref")
	}))
	t.Cleanup(server.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client, result
}

func send(t *testing.T, client *websocket.Conn, message string) {
	t.Helper()
	require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(message)))
}

func receive(t *testing.T, client *websocket.Conn) sessionMessage {
	t.Helper()
	require.NoError(t, client.SetReadDeadline(time.Now().Add(testTimeout)))
	messageType, data, err := client.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, websocket.TextMessage, messageType)
	var message sessionMessage
	require.NoError(t, json.Unmarshal(data, &message))
	return message
}

func awaitServe(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(testTimeout):
		t.Fatal("session did not end")
		return nil
	}
}

func expectClosed(t *testing.T, client *websocket.Conn) {
	t.Helper()
	require.NoError(t, client.SetReadDeadline(time.Now().Add(testTimeout)))
	_, _, err := client.ReadMessage()
	assert.True(t, websocket.IsCloseError(err, websocket.CloseNormalClosure), "got %v", err)
}

func TestServeGivenDictationThenClientReceivesSnapshotsAndDone(t *testing.T) {
	provider := newFakeProvider()
	client, result := serveTestSession(t, provider, Limits{})
	send(t, client, `{"type":"start","keywords":["paxd"],"context":["earlier turn"]}`)
	ready := receive(t, client)
	assert.Equal(t, "ready", ready.Type)
	assert.Equal(t, "fake", ready.Provider)
	assert.Equal(t, 16000, ready.SampleRate)
	assert.Equal(t, Encoding, ready.Encoding)
	assert.Equal(t, Options{
		Keywords: []string{"paxd"},
		Context:  []string{"earlier turn"},
		UserRef:  "user-ref",
	}, <-provider.opened)

	require.NoError(t, client.WriteMessage(websocket.BinaryMessage, []byte{1, 2, 3, 4}))
	assert.Equal(t, []byte{1, 2, 3, 4}, <-provider.stream.audio)
	provider.stream.emit(t, fakeEvent{snapshot: Snapshot{Partial: "hel"}})
	transcript := receive(t, client)
	assert.Equal(t, "transcript", transcript.Type)
	assert.Equal(t, "", *transcript.Final)
	assert.Equal(t, "hel", *transcript.Partial)

	send(t, client, `{"type":"stop"}`)
	waitFor(t, provider.stream.finished, "finish")
	provider.stream.emit(t, fakeEvent{snapshot: Snapshot{Final: "hello", Partial: "there"}})
	receive(t, client)
	provider.stream.emit(t, fakeEvent{done: true})
	done := receive(t, client)
	assert.Equal(t, "done", done.Type)
	assert.Equal(t, "hello there", *done.Final)
	assert.Equal(t, "", *done.Partial)
	expectClosed(t, client)
	assert.NoError(t, awaitServe(t, result))
}

func TestServeGivenAudioBeforeStartThenClientGetsBadRequest(t *testing.T) {
	client, result := serveTestSession(t, newFakeProvider(), Limits{})
	require.NoError(t, client.WriteMessage(websocket.BinaryMessage, []byte{1}))
	message := receive(t, client)
	assert.Equal(t, "error", message.Type)
	assert.Equal(t, ErrorCodeBadRequest, message.Code)
	assert.NoError(t, awaitServe(t, result))
}

func TestServeGivenProviderUnavailableThenClientGetsError(t *testing.T) {
	provider := newFakeProvider()
	provider.openErr = errors.New("dial rejected with HTTP 401")
	client, result := serveTestSession(t, provider, Limits{})
	send(t, client, `{"type":"start"}`)
	message := receive(t, client)
	assert.Equal(t, ErrorCodeProviderUnavailable, message.Code)
	assert.ErrorContains(t, awaitServe(t, result), "401")
}

func TestServeGivenProviderFailureThenClientGetsErrorAndClose(t *testing.T) {
	provider := newFakeProvider()
	client, result := serveTestSession(t, provider, Limits{})
	send(t, client, `{"type":"start"}`)
	receive(t, client)
	provider.stream.emit(t, fakeEvent{done: true, err: errors.New("provider dropped")})
	message := receive(t, client)
	assert.Equal(t, "error", message.Type)
	assert.Equal(t, ErrorCodeProviderFailed, message.Code)
	expectClosed(t, client)
	assert.ErrorContains(t, awaitServe(t, result), "provider dropped")
}

func TestServeGivenAudioPastMaxDurationThenSessionFinishes(t *testing.T) {
	provider := newFakeProvider()
	// 1 ms of 16 kHz PCM16 is 32 bytes.
	client, result := serveTestSession(t, provider, Limits{MaxDuration: time.Millisecond})
	send(t, client, `{"type":"start"}`)
	receive(t, client)
	require.NoError(t, client.WriteMessage(websocket.BinaryMessage, make([]byte, 32)))
	assert.Len(t, <-provider.stream.audio, 32)
	require.NoError(t, client.WriteMessage(websocket.BinaryMessage, make([]byte, 2)))
	waitFor(t, provider.stream.finished, "automatic finish")
	assert.Empty(t, provider.stream.audio)
	provider.stream.emit(t, fakeEvent{done: true})
	assert.Equal(t, "done", receive(t, client).Type)
	assert.NoError(t, awaitServe(t, result))
}

func TestServeGivenClientDisconnectThenProviderStreamIsClosed(t *testing.T) {
	provider := newFakeProvider()
	client, result := serveTestSession(t, provider, Limits{})
	send(t, client, `{"type":"start"}`)
	receive(t, client)
	require.NoError(t, client.Close())
	waitFor(t, provider.stream.closed, "provider close")
	assert.NoError(t, awaitServe(t, result))
}

func TestServeGivenCancelMessageThenSessionEndsWithoutError(t *testing.T) {
	provider := newFakeProvider()
	client, result := serveTestSession(t, provider, Limits{})
	send(t, client, `{"type":"start"}`)
	receive(t, client)
	send(t, client, `{"type":"cancel"}`)
	waitFor(t, provider.stream.closed, "provider close")
	assert.NoError(t, awaitServe(t, result))
}
