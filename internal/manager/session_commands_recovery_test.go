package manager

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/paxkit/reliablemq"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

type commandRecoveryProbe struct {
	domain.Store
	aliasErr      error
	metadataErr   error
	commandErr    error
	failSessionID string
	failCommands  int
	metadataCalls int
	recoveryCalls int
	recoveryErr   error
	latestErr     error
	latest        domain.SessionCommandUpdate
}

func (s *commandRecoveryProbe) ResolveSessionAliases(
	ctx context.Context,
	user, agent, id string,
) (domain.SessionAliases, error) {
	if s.aliasErr != nil {
		return domain.SessionAliases{}, s.aliasErr
	}
	return s.Store.(domain.SessionAliasStore).ResolveSessionAliases(ctx, user, agent, id)
}

func (s *commandRecoveryProbe) UpdateMetadata(
	ctx context.Context,
	key reliablemq.FrameKey,
	metadata reliablemq.Metadata,
) error {
	s.metadataCalls++
	if s.metadataErr != nil {
		return s.metadataErr
	}
	return s.Store.UpdateMetadata(ctx, key, metadata)
}

func (s *commandRecoveryProbe) UpdateSessionACPCommands(
	ctx context.Context,
	agent, session string,
	commands domain.SessionACPCommands,
) error {
	if s.commandErr != nil && (s.failSessionID == "" || session == s.failSessionID) &&
		s.failCommands > 0 {
		s.failCommands--
		return s.commandErr
	}
	return s.Store.UpdateSessionACPCommands(ctx, agent, session, commands)
}

func (s *commandRecoveryProbe) LatestDeferredSessionCommands(
	ctx context.Context,
	owner, agent, queue, native string,
	before int64,
) (domain.SessionCommandUpdate, error) {
	if s.latestErr != nil {
		return domain.SessionCommandUpdate{}, s.latestErr
	}
	if s.latest.Params != nil {
		return s.latest, nil
	}
	return s.Store.(domain.SessionCommandJournalStore).LatestDeferredSessionCommands(
		ctx,
		owner,
		agent,
		queue,
		native,
		before,
	)
}

func (s *commandRecoveryProbe) RecoverSessionCreation(
	ctx context.Context,
	owner, agent, queue, request string,
) (string, error) {
	s.recoveryCalls++
	if s.recoveryErr != nil {
		return "", s.recoveryErr
	}
	return s.Store.(domain.SessionCommandJournalStore).RecoverSessionCreation(
		ctx,
		owner,
		agent,
		queue,
		request,
	)
}

func TestSessionCommandsGivenNotificationBeforeCreationThenRecoverAfterRestartWithoutOrphans(
	t *testing.T,
) {
	agent, counted, _ := newAliasTestAgent(t)
	base := counted.Store.(*storage.MemoryStore)
	probe := &commandRecoveryProbe{Store: base}
	wrapped := newCanonicalSessionStore(probe)
	agent.store = wrapped
	agent.connectionID = agent.agentID + ":commands-recovery"
	queueID := agent.queueID()
	const managerID, nativeID = "sess_secretary_early_commands", "early-native"
	_, err := base.AppendOutboundData(t.Context(), queueID, reliablemq.StreamACP,
		json.RawMessage(`{"jsonrpc":"2.0","id":17,"method":"session/new","params":{}}`),
		reliablemq.Metadata{"agent_id": agent.agentID, "manager_session_id": managerID})
	require.NoError(t, err)
	commandsPayload := json.RawMessage(
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"early-native","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"status","description":"Status"}]}}}`,
	)
	save := func(seq int64, payload json.RawMessage, manager string) {
		t.Helper()
		_, _, err := base.SaveInboundIfAbsent(t.Context(), reliablemq.Frame{
			Key: reliablemq.FrameKey{
				QueueID:   queueID,
				Stream:    reliablemq.StreamACP,
				Direction: reliablemq.DirectionInbound,
				Seq:       seq,
			},
			Kind: reliablemq.FrameKindData, Payload: payload,
			Metadata: reliablemq.Metadata{
				"agent_id":           agent.agentID,
				"manager_session_id": manager,
				"native_session_id":  nativeID,
			},
		})
		require.NoError(t, err)
	}
	replay := func(a *ACPTunnelAgent) error {
		service := &Service{store: wrapped, clock: time.Now}
		pipeline := newACPFramePipeline(
			acpSessionLifecycleMiddleware{store: wrapped},
			acpSessionIDMiddleware{store: wrapped},
			sessionCommandsObservationMiddleware{service: service},
		)
		engine := reliablemq.NewEngine(
			reliablemq.Config{},
			base,
			nil,
			reliablemq.DispatcherFunc(func(ctx context.Context, frame reliablemq.Frame) error {
				return a.dispatchReliableACPFrame(ctx, pipeline, frame)
			}),
		)
		return engine.ReplayInbound(t.Context(), queueID, reliablemq.StreamACP, 1000)
	}
	save(1, commandsPayload, "")
	require.NoError(
		t,
		replay(agent),
		"an advisory catalog must not block its later creation response",
	)
	stored, err := base.GetTransportFrame(
		t.Context(),
		queueID,
		string(reliablemq.StreamACP),
		1,
		string(reliablemq.DirectionInbound),
	)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.JSONEq(t, string(commandsPayload), string(stored.PayloadJSON))
	assert.Equal(t, "true", stored.Metadata["deferred_session_commands"])
	orphans, err := base.ListMessages(t.Context(), agent.agentID, nativeID, 100)
	require.NoError(t, err)
	assert.Empty(t, orphans)
	// A new process has neither pending RPC state nor cached aliases.
	restarted := &ACPTunnelAgent{
		agentID:      agent.agentID,
		connectionID: queueID,
		nodeID:       agent.nodeID,
		ownerUserID:  agent.ownerUserID,
		store:        wrapped,
	}
	save(
		2,
		json.RawMessage(
			`{"jsonrpc":"2.0","id":17,"result":{"sessionId":"sess_secretary_early_commands"}}`,
		),
		managerID,
	)
	failure := errors.New("catalog write failed")
	probe.commandErr, probe.failSessionID, probe.failCommands = failure, managerID, 1
	assert.ErrorIs(
		t,
		replay(restarted),
		failure,
		"binding success must not consume a catalog that failed to persist",
	)
	state, err := base.LoadQueueState(t.Context(), queueID, reliablemq.StreamACP)
	require.NoError(t, err)
	assert.EqualValues(t, 1, state.InboundAppliedThrough)
	restarted = &ACPTunnelAgent{
		agentID:      agent.agentID,
		connectionID: queueID,
		nodeID:       agent.nodeID,
		ownerUserID:  agent.ownerUserID,
		store:        wrapped,
	}
	require.NoError(
		t,
		replay(restarted),
		"durable request correlation must also survive a failed hydration and another restart",
	)
	session, err := base.GetSession(
		t.Context(),
		domain.UserPrincipal{User: domain.User{UserID: agent.ownerUserID}},
		managerID,
	)
	require.NoError(t, err)
	assert.Equal(t, nativeID, session.NativeID)
	commands := domain.SessionACPCommandsFromMetadata(session.Metadata)
	require.NotNil(t, commands)
	require.Len(t, commands.AvailableCommands, 1)
	assert.JSONEq(
		t,
		`{"name":"status","description":"Status"}`,
		string(commands.AvailableCommands[0]),
	)
	state, err = base.LoadQueueState(t.Context(), queueID, reliablemq.StreamACP)
	require.NoError(t, err)
	assert.EqualValues(t, 2, state.InboundAppliedThrough)
}

func TestSessionCommandsGivenDeferralFailureThenKeepFrameRetryable(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		aliasErr, metadataErr error
		id                    string
		seq                   int64
		source                string
		want                  error
		wantMetadataCalls     int
	}{
		{name: "alias database error", aliasErr: context.Canceled, seq: 1, want: context.Canceled},
		{name: "tag write error", metadataErr: domain.ErrConflict, seq: 1, want: domain.ErrConflict, wantMetadataCalls: 1},
		{name: "not journaled", want: domain.ErrNotFound},
		{name: "RPC request", id: `,"id":9`, seq: 1, want: domain.ErrNotFound},
		{name: "spoofed actor", seq: 1, source: "other-agent", want: domain.ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent, counted, _ := newAliasTestAgent(t)
			probe := &commandRecoveryProbe{
				Store:       counted.Store,
				aliasErr:    tc.aliasErr,
				metadataErr: tc.metadataErr,
			}
			wrapped := newCanonicalSessionStore(probe)
			agent.store = wrapped
			service := &Service{store: wrapped, clock: time.Now}
			frame := newACPFrameContext(
				agent,
				acpAgentToUser,
				1,
				[]byte(
					`{"jsonrpc":"2.0"`+tc.id+`,"method":"session/update","params":{"sessionId":"missing-native","update":{"sessionUpdate":"available_commands_update","availableCommands":[]}}}`,
				),
			)
			frame.managerSessionID, frame.nativeSessionID, frame.transportSeq = "missing-native", "missing-native", tc.seq
			frame.transportMetadata = map[string]string{"agent_id": tc.source}
			called := false
			err := (sessionCommandsObservationMiddleware{service: service}).HandleACPFrame(
				t.Context(),
				frame,
				func(context.Context, *acpFrameContext) error { called = true; return nil },
			)
			assert.ErrorIs(t, err, tc.want)
			assert.False(t, called)
			assert.Equal(t, tc.wantMetadataCalls, probe.metadataCalls)
		})
	}
}

func TestSessionCommandsGivenOtherFrameKindsThenNeverDefer(t *testing.T) {
	agent, counted, _ := newAliasTestAgent(t)
	probe := &commandRecoveryProbe{Store: counted.Store}
	wrapped := newCanonicalSessionStore(probe)
	agent.store = wrapped
	service := &Service{store: wrapped, clock: time.Now}
	for _, raw := range []string{
		`{"method":"session/request_permission","id":9,"params":{"sessionId":"missing-native"}}`,
		`{"method":"session/update","params":{"sessionId":"missing-native","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"content"}}}}`,
		`{"method":"session/update","params":{"sessionId":"missing-native","update":{"sessionUpdate":"available_commands_update","availableCommands":null}}}`,
	} {
		frame := newACPFrameContext(agent, acpAgentToUser, 1, []byte(raw))
		frame.managerSessionID, frame.transportSeq = "missing-native", 1
		failure := errors.New("downstream remains retryable")
		err := (sessionCommandsObservationMiddleware{service: service}).HandleACPFrame(
			t.Context(),
			frame,
			func(context.Context, *acpFrameContext) error { return failure },
		)
		assert.ErrorIs(t, err, failure)
		assert.Zero(t, probe.metadataCalls)
	}
}

func TestSessionCreationGivenPermissionRequestIDCollisionThenNeverBindOrConsumeCorrelation(
	t *testing.T,
) {
	agent, counted, _ := newAliasTestAgent(t)
	probe := &commandRecoveryProbe{Store: counted.Store}
	wrapped := newCanonicalSessionStore(probe)
	agent.store = wrapped
	agent.trackSessionNew("9", "sess_secretary_pending")
	frame := newACPFrameContext(
		agent,
		acpAgentToUser,
		1,
		[]byte(
			`{"jsonrpc":"2.0","id":9,"method":"session/request_permission","params":{"sessionId":"native-permission"}}`,
		),
	)
	frame.transportSeq = 5
	called := false
	err := (acpSessionLifecycleMiddleware{store: wrapped}).HandleACPFrame(
		t.Context(),
		frame,
		func(context.Context, *acpFrameContext) error { called = true; return nil },
	)
	require.NoError(t, err)
	assert.True(t, called)
	assert.Zero(t, probe.recoveryCalls)
	_, pending := agent.peekSessionNew("9")
	assert.True(t, pending)
}

func TestSessionCreationGivenHydrationFailureThenRetainPendingCorrelationForRetry(t *testing.T) {
	agent, counted, _ := newAliasTestAgent(t)
	failure := errors.New("catalog write failed")
	probe := &commandRecoveryProbe{
		Store: counted.Store, commandErr: failure, failCommands: 1,
		latest: domain.SessionCommandUpdate{
			Params: json.RawMessage(
				`{"update":{"sessionUpdate":"available_commands_update","availableCommands":[]}}`,
			),
			ObservedAt: time.Now(),
		},
	}
	wrapped := newCanonicalSessionStore(probe)
	agent.store = wrapped
	agent.trackSessionNew("9", "sess_secretary_pending")
	middleware := acpSessionLifecycleMiddleware{store: wrapped}
	response := func() *acpFrameContext {
		frame := newACPFrameContext(
			agent,
			acpAgentToUser,
			1,
			[]byte(`{"jsonrpc":"2.0","id":9,"result":{"sessionId":"new-native"}}`),
		)
		frame.transportSeq = 2
		return frame
	}
	next := func(context.Context, *acpFrameContext) error { return nil }
	assert.ErrorIs(t, middleware.HandleACPFrame(t.Context(), response(), next), failure)
	_, pending := agent.peekSessionNew("9")
	assert.True(t, pending)
	require.NoError(t, middleware.HandleACPFrame(t.Context(), response(), next))
	_, pending = agent.peekSessionNew("9")
	assert.False(t, pending)
}

func TestSessionCreationGivenJournalLookupFailureThenPreserveRetryAndUnrelatedResponses(
	t *testing.T,
) {
	agent, counted, _ := newAliasTestAgent(t)
	probe := &commandRecoveryProbe{Store: counted.Store, recoveryErr: context.Canceled}
	wrapped := newCanonicalSessionStore(probe)
	agent.store = wrapped
	frame := newACPFrameContext(
		agent,
		acpAgentToUser,
		1,
		[]byte(`{"jsonrpc":"2.0","id":17,"result":{}}`),
	)
	frame.transportSeq = 2
	called := false
	next := func(context.Context, *acpFrameContext) error { called = true; return nil }
	middleware := acpSessionLifecycleMiddleware{store: wrapped}
	assert.ErrorIs(t, middleware.HandleACPFrame(t.Context(), frame, next), context.Canceled)
	assert.False(t, called)
	probe.recoveryErr = domain.ErrNotFound
	require.NoError(t, middleware.HandleACPFrame(t.Context(), frame, next))
	assert.True(t, called)
	unsupported := newCanonicalSessionStore(struct{ domain.Store }{})
	_, err := unsupported.(domain.SessionCommandJournalStore).RecoverSessionCreation(
		t.Context(),
		"owner",
		"agent",
		"queue",
		"17",
	)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = unsupported.(domain.SessionCommandJournalStore).LatestDeferredSessionCommands(
		t.Context(),
		"owner",
		"agent",
		"queue",
		"native",
		2,
	)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestSessionCommandsGivenMalformedDeferredCatalogThenKeepCreationRetryable(t *testing.T) {
	agent, counted, _ := newAliasTestAgent(t)
	probe := &commandRecoveryProbe{
		Store:  counted.Store,
		latest: domain.SessionCommandUpdate{Params: json.RawMessage(`{}`)},
	}
	frame := &acpFrameContext{agent: agent, transportSeq: 2}
	assert.ErrorIs(
		t,
		hydrateDeferredSessionCommands(t.Context(), probe, frame, "manager", "native"),
		domain.ErrConflict,
	)
	probe.latestErr = context.Canceled
	assert.ErrorIs(
		t,
		hydrateDeferredSessionCommands(t.Context(), probe, frame, "manager", "native"),
		context.Canceled,
	)
	_, pending := agent.peekSessionNew("")
	assert.False(t, pending)
}
