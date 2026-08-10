package manager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestACPSessionMuxInterruptsOnlyTheMissingRuntimeSession(t *testing.T) {
	mux := newACPSessionMux()
	waiterA, cancelA := mux.addResponseWaiter("11", "sess-a", "session/prompt")
	defer cancelA()
	waiterB, cancelB := mux.addResponseWaiter("22", "sess-b", "session/prompt")
	defer cancelB()
	subA := mux.subscribe("sess-a")
	defer mux.unsubscribe(subA)
	subB := mux.subscribe("sess-b")
	defer mux.unsubscribe(subB)

	interrupted := errors.New("runtime turn disappeared")
	require.Equal(t, 2, mux.interruptSession("sess-a", interrupted))

	waiterResult := <-waiterA
	require.ErrorIs(t, waiterResult.err, interrupted)
	terminalErr := <-subA.terminal
	require.ErrorIs(t, terminalErr, interrupted)

	require.True(t, mux.notifyResponseWaiter("22", []byte(`{"id":22,"result":{}}`)))
	require.JSONEq(t, `{"id":22,"result":{}}`, string((<-waiterB).payload))
	require.True(t, mux.publish("sess-b", []byte(`{"method":"session/update"}`)))
	require.JSONEq(t, `{"method":"session/update"}`, string(<-subB.ch))
}

func TestACPSessionMuxKeepsBufferedPromptResponseWhenIdleSnapshotArrives(t *testing.T) {
	mux := newACPSessionMux()
	waiter, cancel := mux.addResponseWaiter("11", "sess-a", "session/prompt")
	defer cancel()
	sub := mux.subscribe("sess-a")
	defer mux.unsubscribe(sub)

	require.True(t, mux.notifyResponseWaiter("11", []byte(`{"id":11,"result":{}}`)))
	require.Zero(t, mux.interruptSession("sess-a", errors.New("runtime turn disappeared")))
	require.JSONEq(t, `{"id":11,"result":{}}`, string((<-waiter).payload))
	require.True(t, mux.publish("sess-a", []byte(`{"method":"session/update"}`)))
	require.JSONEq(t, `{"method":"session/update"}`, string(<-sub.ch))
}

func TestACPSessionMuxInterruptsAfterIdleQuietPeriod(t *testing.T) {
	mux := newACPSessionMux()
	waiter, cancel := mux.addResponseWaiter("11", "sess-a", "session/prompt")
	defer cancel()
	sub := mux.subscribe("sess-a")
	defer mux.unsubscribe(sub)

	interrupted := errors.New("runtime turn disappeared")
	require.True(t, mux.deferIdleInterrupt("sess-a", interrupted, 20*time.Millisecond))

	select {
	case result := <-waiter:
		require.ErrorIs(t, result.err, interrupted)
	case <-time.After(time.Second):
		t.Fatal("idle prompt was not interrupted after the quiet period")
	}
	select {
	case terminalErr := <-sub.terminal:
		require.ErrorIs(t, terminalErr, interrupted)
	case <-time.After(time.Second):
		t.Fatal("idle subscriber was not interrupted after the quiet period")
	}
}

func TestACPSessionMuxFrameRestartsIdleQuietPeriod(t *testing.T) {
	mux := newACPSessionMux()
	waiter, cancel := mux.addResponseWaiter("11", "sess-a", "session/prompt")
	defer cancel()
	sub := mux.subscribe("sess-a")
	defer mux.unsubscribe(sub)

	interrupted := errors.New("runtime turn disappeared")
	require.True(t, mux.deferIdleInterrupt("sess-a", interrupted, 80*time.Millisecond))
	time.Sleep(50 * time.Millisecond)
	require.True(t, mux.publish("sess-a", []byte(`{"method":"session/update"}`)))
	require.JSONEq(t, `{"method":"session/update"}`, string(<-sub.ch))

	select {
	case result := <-waiter:
		t.Fatalf("frame did not restart idle quiet period: %v", result.err)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case result := <-waiter:
		require.ErrorIs(t, result.err, interrupted)
	case <-time.After(time.Second):
		t.Fatal("idle prompt was not interrupted after frames became quiet")
	}
}

func TestACPSessionMuxPromptResponseCancelsIdleInterrupt(t *testing.T) {
	mux := newACPSessionMux()
	waiter, cancel := mux.addResponseWaiter("11", "sess-a", "session/prompt")
	defer cancel()
	sub := mux.subscribe("sess-a")
	defer mux.unsubscribe(sub)

	require.True(t, mux.deferIdleInterrupt(
		"sess-a",
		errors.New("runtime turn disappeared"),
		20*time.Millisecond,
	))
	require.True(t, mux.notifyResponseWaiter("11", []byte(`{"id":11,"result":{}}`)))
	require.JSONEq(t, `{"id":11,"result":{}}`, string((<-waiter).payload))
	time.Sleep(40 * time.Millisecond)

	require.True(t, mux.publish("sess-a", []byte(`{"method":"session/update"}`)))
	require.JSONEq(t, `{"method":"session/update"}`, string(<-sub.ch))
}

func TestACPSessionMuxIdleTimerDoesNotInterruptNewPrompt(t *testing.T) {
	mux := newACPSessionMux()
	oldWaiter, cancelOld := mux.addResponseWaiter("11", "sess-a", "session/prompt")
	defer cancelOld()
	require.True(t, mux.deferIdleInterrupt(
		"sess-a",
		errors.New("runtime turn disappeared"),
		20*time.Millisecond,
	))

	newWaiter, cancelNew := mux.addResponseWaiter("12", "sess-a", "session/prompt")
	defer cancelNew()
	time.Sleep(40 * time.Millisecond)

	select {
	case result := <-oldWaiter:
		t.Fatalf("old idle timer interrupted a prompt generation: %v", result.err)
	default:
	}
	select {
	case result := <-newWaiter:
		t.Fatalf("old idle timer interrupted the new prompt: %v", result.err)
	default:
	}
}

func TestACPSessionMuxRoutesInterleavedSessionlessResponsesByRequestID(t *testing.T) {
	agent := &ACPTunnelAgent{}
	waiterA, cancelA := agent.addResponseWaiter("11", "sess-a", "session/prompt")
	defer cancelA()
	waiterB, cancelB := agent.addResponseWaiter("22", "sess-b", "session/set_mode")
	defer cancelB()

	routed := make([]string, 0, 2)
	pipeline := newACPFramePipeline(acpSessionMuxMiddleware{})
	dispatch := func(payload string) {
		t.Helper()
		frame := newACPFrameContext(
			agent,
			acpAgentToUser,
			websocket.TextMessage,
			[]byte(payload),
		)
		require.NoError(t, pipeline.Handle(
			context.Background(),
			frame,
			func(_ context.Context, frame *acpFrameContext) error {
				routed = append(routed, frame.managerSessionID+":"+frame.requestKind)
				require.True(t, agent.notifyResponseWaiter(
					acpJSONRPCID(frame.frame),
					frame.payload,
				))
				return nil
			},
		))
	}

	dispatch(`{"jsonrpc":"2.0","id":22,"result":{"ok":"b"}}`)
	select {
	case result := <-waiterA:
		t.Fatalf("session A received session B response: %s", result.payload)
	default:
	}
	require.JSONEq(t, `{"jsonrpc":"2.0","id":22,"result":{"ok":"b"}}`, string((<-waiterB).payload))

	dispatch(`{"jsonrpc":"2.0","id":11,"result":{"ok":"a"}}`)
	require.JSONEq(t, `{"jsonrpc":"2.0","id":11,"result":{"ok":"a"}}`, string((<-waiterA).payload))
	require.Equal(t, []string{
		"sess-b:session/set_mode",
		"sess-a:session/prompt",
	}, routed)
}

func TestACPSessionMuxPublishesNotificationsOnlyToExactSession(t *testing.T) {
	mux := newACPSessionMux()
	subA := mux.subscribe("sess-a")
	defer mux.unsubscribe(subA)
	subB := mux.subscribe("sess-b")
	defer mux.unsubscribe(subB)

	require.True(t, mux.publish("sess-b", []byte(`{"method":"session/update"}`)))
	require.JSONEq(t, `{"method":"session/update"}`, string(<-subB.ch))
	select {
	case payload := <-subA.ch:
		t.Fatalf("session A received session B notification: %s", payload)
	default:
	}
	require.False(t, mux.publish("", []byte(`{"method":"unknown"}`)))
}

func TestACPSessionMuxFailsClosedForUnknownSessionlessFrame(t *testing.T) {
	agent := &ACPTunnelAgent{}
	subA := agent.subscribeSSE("sess-a")
	defer agent.unsubscribeSSE(subA)
	subB := agent.subscribeSSE("sess-b")
	defer agent.unsubscribeSSE(subB)

	frame := newACPFrameContext(
		agent,
		acpAgentToUser,
		websocket.TextMessage,
		[]byte(`{"jsonrpc":"2.0","method":"unknown/control"}`),
	)
	called := false
	require.NoError(t, newACPFramePipeline(acpSessionMuxMiddleware{}).Handle(
		context.Background(),
		frame,
		func(context.Context, *acpFrameContext) error {
			called = true
			return nil
		},
	))
	require.False(t, called)
	require.True(t, frame.handled)
	require.Equal(t, "unclassified_sessionless_frame", frame.dropReason)
	for _, sub := range []*acpSSESubscriber{subA, subB} {
		select {
		case payload := <-sub.ch:
			t.Fatalf("unknown sessionless frame was broadcast: %s", payload)
		default:
		}
	}
}

func TestACPSessionMuxClosingOneSessionDoesNotAffectAnother(t *testing.T) {
	mux := newACPSessionMux()
	_, cancelA := mux.addResponseWaiter("11", "sess-a", "session/prompt")
	waiterB, cancelB := mux.addResponseWaiter("22", "sess-b", "session/prompt")
	defer cancelB()
	subA := mux.subscribe("sess-a")
	subB := mux.subscribe("sess-b")
	defer mux.unsubscribe(subB)

	cancelA()
	mux.unsubscribe(subA)

	sessionID, requestKind, ok := mux.responseContext("22")
	require.True(t, ok)
	require.Equal(t, "sess-b", sessionID)
	require.Equal(t, "session/prompt", requestKind)
	require.True(t, mux.notifyResponseWaiter("22", []byte(`{"id":22,"result":{}}`)))
	require.JSONEq(t, `{"id":22,"result":{}}`, string((<-waiterB).payload))
	require.True(t, mux.publish("sess-b", []byte(`{"method":"session/update"}`)))
	require.JSONEq(t, `{"method":"session/update"}`, string(<-subB.ch))
}

func TestACPSessionMuxCorrelatesWorkerResponseWithNativeSession(t *testing.T) {
	agent := &ACPTunnelAgent{}
	pipeline := newACPFramePipeline(acpSessionMuxMiddleware{})
	request := newACPFrameContext(
		agent,
		acpAgentToUser,
		websocket.TextMessage,
		[]byte(`{"jsonrpc":"2.0","id":"permission-1","method":"session/request_permission"}`),
	)
	request.managerSessionID = "sess-manager"
	request.nativeSessionID = "sess-native"
	require.NoError(t, pipeline.Handle(
		context.Background(),
		request,
		func(context.Context, *acpFrameContext) error { return nil },
	))

	response := newACPFrameContext(
		agent,
		acpUserToAgent,
		websocket.TextMessage,
		[]byte(`{"jsonrpc":"2.0","id":"permission-1","result":{"outcome":"selected"}}`),
	)
	response.managerSessionID = "sess-manager"
	response.nativeSessionID = "sess-native"
	called := false
	require.NoError(t, pipeline.Handle(
		context.Background(),
		response,
		func(_ context.Context, frame *acpFrameContext) error {
			called = true
			require.Equal(t, "sess-manager", frame.managerSessionID)
			require.Equal(t, "sess-native", frame.nativeSessionID)
			require.Equal(t, "session/request_permission", frame.requestKind)
			return nil
		},
	))
	require.True(t, called)

	duplicate := newACPFrameContext(
		agent,
		acpUserToAgent,
		websocket.TextMessage,
		[]byte(`{"jsonrpc":"2.0","id":"permission-1","result":{}}`),
	)
	duplicate.managerSessionID = "sess-manager"
	duplicate.nativeSessionID = "sess-native"
	require.NoError(t, pipeline.Handle(
		context.Background(),
		duplicate,
		func(context.Context, *acpFrameContext) error {
			t.Fatal("duplicate worker response was dispatched")
			return nil
		},
	))
	require.Equal(t, "unknown_worker_response", duplicate.dropReason)
}

func TestACPSessionMuxDoesNotGuessAmbiguousWorkerRequestID(t *testing.T) {
	mux := newACPSessionMux()
	mux.trackWorkerRequest("7", "sess-a", "native-a", "session/request_permission")
	mux.trackWorkerRequest("7", "sess-b", "native-b", "session/request_permission")

	_, ok := mux.workerRequestContext("7", "", true)
	require.False(t, ok)
	request, ok := mux.workerRequestContext("7", "sess-b", false)
	require.True(t, ok)
	require.Equal(t, "native-b", request.nativeSessionID)
}
