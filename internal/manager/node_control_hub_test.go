package manager

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestNodeControlConnectionQueryAllowsInterleavedReport(t *testing.T) {
	ws := newFakeNodeControlWebSocket()
	conn := newNodeControlConnection("node_1", ws)
	resultCh := make(chan json.RawMessage, 1)
	errCh := make(chan error, 1)

	go func() {
		result, err := conn.Query(
			context.Background(),
			"req_1",
			map[string]any{"type": "status.get"},
		)
		resultCh <- result
		errCh <- err
	}()

	written := ws.nextWrite(t)
	require.JSONEq(t, `{
		"kind":"query",
		"request_id":"req_1",
		"query":{"type":"status.get"}
	}`, string(written))

	handled, err := conn.HandleIncoming([]byte(`{
		"kind":"report",
		"version":1,
		"report_id":"rpt_1",
		"report":{"type":"heartbeat","remote_id":"remote_prod"}
	}`))
	require.NoError(t, err)
	require.False(t, handled)
	require.NoError(t, conn.ObserveReport([]byte(`{
		"kind":"report",
		"report":{"remote_id":"remote_prod"}
	}`)))
	require.Equal(t, "remote_prod", conn.RemoteID())

	handled, err = conn.HandleIncoming([]byte(`{
		"kind":"response",
		"request_id":"req_1",
		"query_result":{"type":"status.get","status":{"phase":"running"}}
	}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.NoError(t, <-errCh)
	require.JSONEq(
		t,
		`{"type":"status.get","status":{"phase":"running"}}`,
		string(<-resultCh),
	)
}

func TestNodeControlConnectionCommandReturnsMatchingAck(t *testing.T) {
	ws := newFakeNodeControlWebSocket()
	conn := newNodeControlConnection("node_1", ws)
	resultCh := make(chan json.RawMessage, 1)
	errCh := make(chan error, 1)

	go func() {
		result, err := conn.Command(
			context.Background(),
			"cmd_1",
			map[string]any{
				"command_id": "cmd_1",
				"type":       "agent_connection.restart",
			},
		)
		resultCh <- result
		errCh <- err
	}()

	written := ws.nextWrite(t)
	require.JSONEq(t, `{
		"kind":"command",
		"command_id":"cmd_1",
		"command":{
			"command_id":"cmd_1",
			"type":"agent_connection.restart"
		}
	}`, string(written))

	handled, err := conn.HandleIncoming([]byte(`{
		"kind":"ack",
		"command_id":"cmd_1",
		"command_ack":{"command_id":"cmd_1","ok":true,"status":"received"}
	}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.NoError(t, <-errCh)
	require.JSONEq(
		t,
		`{"command_id":"cmd_1","ok":true,"status":"received"}`,
		string(<-resultCh),
	)
}

func TestNodeControlConnectionSerializesRequests(t *testing.T) {
	ws := newFakeNodeControlWebSocket()
	conn := newNodeControlConnection("node_1", ws)
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)

	go func() {
		_, err := conn.Query(context.Background(), "req_1", map[string]any{"type": "status.get"})
		firstDone <- err
	}()
	require.JSONEq(t, `{
		"kind":"query",
		"request_id":"req_1",
		"query":{"type":"status.get"}
	}`, string(ws.nextWrite(t)))

	go func() {
		_, err := conn.Query(context.Background(), "req_2", map[string]any{"type": "remotes.list"})
		secondDone <- err
	}()

	select {
	case payload := <-ws.writes:
		t.Fatalf("second request was written before first completed: %s", payload)
	case <-time.After(50 * time.Millisecond):
	}

	handled, err := conn.HandleIncoming([]byte(`{
		"kind":"response",
		"request_id":"req_1",
		"query_result":{"type":"status.get"}
	}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.NoError(t, <-firstDone)

	require.JSONEq(t, `{
		"kind":"query",
		"request_id":"req_2",
		"query":{"type":"remotes.list"}
	}`, string(ws.nextWrite(t)))
	handled, err = conn.HandleIncoming([]byte(`{
		"kind":"response",
		"request_id":"req_2",
		"query_result":{"type":"remotes.list"}
	}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.NoError(t, <-secondDone)
}

func TestNodeControlConnectionDisconnectFailsPendingRequest(t *testing.T) {
	ws := newFakeNodeControlWebSocket()
	conn := newNodeControlConnection("node_1", ws)
	errCh := make(chan error, 1)

	go func() {
		_, err := conn.Query(context.Background(), "req_1", map[string]any{"type": "status.get"})
		errCh <- err
	}()
	_ = ws.nextWrite(t)

	conn.Fail(ErrNodeControlDisconnected)
	require.ErrorIs(t, <-errCh, ErrNodeControlDisconnected)
}

func TestNodeControlHubReplacementDoesNotRemoveNewConnection(t *testing.T) {
	hub := NewNodeControlHub()
	first := newNodeControlConnection("node_1", newFakeNodeControlWebSocket())
	second := newNodeControlConnection("node_1", newFakeNodeControlWebSocket())

	hub.Add("node_1", first)
	hub.Add("node_1", second)
	hub.Remove("node_1", first)

	got, err := hub.Connection("node_1")
	require.NoError(t, err)
	require.Same(t, second, got)
	require.ErrorIs(t, first.Err(), ErrNodeControlReplaced)
}

type fakeNodeControlWebSocket struct {
	writes    chan []byte
	closeOnce sync.Once
	closed    chan struct{}
}

func newFakeNodeControlWebSocket() *fakeNodeControlWebSocket {
	return &fakeNodeControlWebSocket{
		writes: make(chan []byte, 8),
		closed: make(chan struct{}),
	}
}

func (c *fakeNodeControlWebSocket) WriteMessage(messageType int, payload []byte) error {
	if messageType != websocket.TextMessage {
		return errors.New("unexpected message type")
	}
	copyPayload := append([]byte(nil), payload...)
	select {
	case <-c.closed:
		return ErrNodeControlDisconnected
	case c.writes <- copyPayload:
		return nil
	}
}

func (c *fakeNodeControlWebSocket) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *fakeNodeControlWebSocket) nextWrite(t *testing.T) []byte {
	t.Helper()
	select {
	case payload := <-c.writes:
		return payload
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for node control write")
		return nil
	}
}
