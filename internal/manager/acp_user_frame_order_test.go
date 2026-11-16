package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/paxkit/reliablemq"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestACPUserFrameGivenImmediateResponseThenPrepareHistoryBoundaryBeforeSending(t *testing.T) {
	agent, store, session := newAliasTestAgent(t)
	agent.store = store
	delta := func(seq int64, text string) error {
		payload := json.RawMessage(
			fmt.Sprintf(
				`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":%q}}}}`,
				session.SessionID,
				text,
			),
		)
		return projectACPTransportMessageWithTextSinkForTurn(
			t.Context(),
			store,
			acpAgentHistoryTextSink{agent: agent},
			agent.agentID,
			agent.ownerUserID,
			agent.nodeID,
			domain.TransportStreamPaxdToManager,
			seq,
			agent.historyGroupIDForSession(seq, session.SessionID, payload),
			session.SessionID,
			"",
			payload,
		)
	}
	require.NoError(t, delta(1, "before"))
	prompt := []byte(
		`{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"sess_manager","prompt":[{"type":"text","text":"question"}]}}`,
	)
	err := writeAndProjectACPUserFrame(
		t.Context(),
		agent,
		session.SessionID,
		"turn",
		prompt,
		func() error {
			return delta(2, "h")
		},
	)
	require.NoError(t, err)
	require.NoError(t, delta(3, "i"))
	require.NoError(t, agent.flushHistoryText(t.Context()))
	messages, err := store.ListMessages(t.Context(), agent.agentID, session.SessionID, 100)
	require.NoError(t, err)
	var answers, prompts []string
	for _, msg := range messages {
		parts, err := store.ListMessageParts(t.Context(), msg.MessageID)
		require.NoError(t, err)
		for _, part := range parts {
			switch msg.Role {
			case "assistant":
				answers = append(answers, part.Text)
			case "user":
				prompts = append(prompts, part.Text)
				assert.Equal(t, "turn", msg.TurnID)
			}
		}
	}
	assert.ElementsMatch(t, []string{"before", "hi"}, answers)
	assert.Equal(t, []string{"question"}, prompts)
}

func TestACPUserFrameGivenSendFailureThenDoNotPersistOutgoingPrompt(t *testing.T) {
	agent, store, session := newAliasTestAgent(t)
	agent.store = store
	failure := errors.New("send failed")
	prompt := []byte(
		`{"jsonrpc":"2.0","method":"session/prompt","params":{"sessionId":"sess_manager","prompt":[{"type":"text","text":"question"}]}}`,
	)
	err := writeAndProjectACPUserFrame(
		t.Context(),
		agent,
		session.SessionID,
		"turn",
		prompt,
		func() error { return failure },
	)
	assert.ErrorIs(t, err, failure)
	messages, err := store.ListMessages(t.Context(), agent.agentID, session.SessionID, 100)
	require.NoError(t, err)
	assert.Empty(t, messages)
}

func TestACPUserFrameGivenNoHistoryProjectionThenStillInvokeSend(t *testing.T) {
	failure := errors.New("send failed")
	for _, tc := range []struct {
		agent *ACPTunnelAgent
		raw   string
	}{
		{nil, `{}`}, {&ACPTunnelAgent{}, `{}`},
		{&ACPTunnelAgent{store: &countingListSessionsStore{}}, `{invalid`},
		{&ACPTunnelAgent{store: &countingListSessionsStore{}}, `{}`},
	} {
		err := writeAndProjectACPUserFrame(
			t.Context(),
			tc.agent,
			"session",
			"",
			[]byte(tc.raw),
			func() error { return failure },
		)
		assert.ErrorIs(t, err, failure)
	}
}

func TestACPUserFrameGivenPriorHistoryFlushFailureThenDoNotSendNewWork(t *testing.T) {
	failure := errors.New("history flush failed")
	agent := &ACPTunnelAgent{
		store: &orderedHistoryTestStore{appendText: func(string) error { return failure }},
	}
	batcher := agent.historyTextBatcher()
	t.Cleanup(func() {
		batcher.mu.Lock()
		defer batcher.mu.Unlock()
		if batcher.timer != nil {
			batcher.timer.Stop()
		}
	})
	require.NoError(t, agent.appendHistoryText(t.Context(), "before", 0, "text"))
	called := false
	err := writeAndProjectACPUserFrame(
		context.Background(),
		agent,
		"session",
		"",
		[]byte(`{"method":"initialize"}`),
		func() error {
			called = true
			return nil
		},
	)
	assert.ErrorIs(t, err, failure)
	assert.False(t, called)
}

func TestACPUserFrameGivenUnacceptedRequestThenCancelResponseWaiter(t *testing.T) {
	for _, failFlush := range []bool{false, true} {
		t.Run(fmt.Sprintf("flush_failure_%t", failFlush), func(t *testing.T) {
			failure := errors.New("history flush failed")
			agent := &ACPTunnelAgent{agentID: "agent"}
			want := reliablemq.ErrProducerNotReady
			if failFlush {
				want = failure
				agent.store = &orderedHistoryTestStore{
					appendText: func(string) error { return failure },
				}
				require.NoError(t, agent.appendHistoryText(t.Context(), "before", 0, "text"))
				batcher := agent.historyTextBatcher()
				t.Cleanup(func() {
					batcher.mu.Lock()
					defer batcher.mu.Unlock()
					if batcher.timer != nil {
						batcher.timer.Stop()
					}
				})
			}
			done := make(chan error, 1)
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if err != nil {
						done <- err
						return
					}
					defer func() { _ = ws.Close() }()
					done <- relayUserFramesToAgent(r.Context(), ws, agent, newACPFramePipeline())
				}),
			)
			defer server.Close()
			client, _, err := websocket.DefaultDialer.Dial(
				"ws"+strings.TrimPrefix(server.URL, "http"),
				nil,
			)
			require.NoError(t, err)
			defer func() { _ = client.Close() }()
			require.NoError(
				t,
				client.WriteMessage(
					websocket.TextMessage,
					[]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`),
				),
			)
			select {
			case err := <-done:
				assert.ErrorIs(t, err, want)
			case <-time.After(2 * time.Second):
				t.Fatal("relay did not stop after rejecting the request")
			}
			assert.Zero(t, agent.asyncReceiverCounts().responseWaiters)
		})
	}
}
