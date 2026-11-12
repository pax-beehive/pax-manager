package voice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

const testTimeout = 5 * time.Second

// serverConn is the provider side of a fake provider connection. The test
// goroutine drives it directly to script the vendor protocol.
type serverConn struct {
	*websocket.Conn
	header  http.Header
	release chan struct{}
}

func fakeProviderServer(t *testing.T) (string, <-chan *serverConn) {
	t.Helper()
	conns := make(chan *serverConn, 1)
	var upgrader websocket.Upgrader
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		sc := &serverConn{Conn: conn, header: r.Header.Clone(), release: make(chan struct{})}
		conns <- sc
		<-sc.release
		_ = conn.Close()
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http"), conns
}

func acceptConn(t *testing.T, conns <-chan *serverConn) *serverConn {
	t.Helper()
	select {
	case sc := <-conns:
		t.Cleanup(func() { close(sc.release) })
		return sc
	case <-time.After(testTimeout):
		t.Fatal("provider was not dialed")
		return nil
	}
}

func (c *serverConn) readFrame(t *testing.T) []byte {
	t.Helper()
	require.NoError(t, c.SetReadDeadline(time.Now().Add(testTimeout)))
	_, data, err := c.ReadMessage()
	require.NoError(t, err)
	return data
}

func (c *serverConn) readClientFrame(t *testing.T) clientFrame {
	t.Helper()
	return decodeClientFrame(t, c.readFrame(t))
}

func (c *serverConn) writeBinary(t *testing.T, data []byte) {
	t.Helper()
	require.NoError(t, c.WriteMessage(websocket.BinaryMessage, data))
}

func (c *serverConn) writeJSONEvent(t *testing.T, event map[string]string) {
	t.Helper()
	require.NoError(t, c.WriteJSON(event))
}

type openResult struct {
	stream Stream
	err    error
}

func openAsync(ctx context.Context, provider Provider, opts Options) <-chan openResult {
	result := make(chan openResult, 1)
	go func() {
		stream, err := provider.Open(ctx, opts)
		result <- openResult{stream: stream, err: err}
	}()
	return result
}

func awaitOpen(t *testing.T, opened <-chan openResult) Stream {
	t.Helper()
	select {
	case result := <-opened:
		require.NoError(t, result.err)
		t.Cleanup(func() { _ = result.stream.Close() })
		return result.stream
	case <-time.After(testTimeout):
		t.Fatal("provider did not open")
		return nil
	}
}

func nextSnapshot(t *testing.T, stream Stream) Snapshot {
	t.Helper()
	select {
	case snapshot, ok := <-stream.Snapshots():
		require.True(t, ok, "stream ended early: %v", stream.Err())
		return snapshot
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for a snapshot")
		return Snapshot{}
	}
}

// drain waits for the stream to end and returns its last snapshot.
func drain(t *testing.T, stream Stream) Snapshot {
	t.Helper()
	var last Snapshot
	deadline := time.After(testTimeout)
	for {
		select {
		case snapshot, ok := <-stream.Snapshots():
			if !ok {
				return last
			}
			last = snapshot
		case <-deadline:
			t.Fatal("stream did not end")
			return last
		}
	}
}
