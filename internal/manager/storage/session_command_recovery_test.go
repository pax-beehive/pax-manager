package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestMemoryCommandJournalGivenDeferredUpdatesThenSelectLatestWithinScope(t *testing.T) {
	store := NewMemoryStore(time.Now)
	store.agents["agent"] = Agent{AgentID: "agent", OwnerUserID: "owner"}
	for _, frame := range commandRecoveryFrames() {
		require.NoError(t, store.SaveTransportFrame(t.Context(), &frame))
	}
	got, err := store.LatestDeferredSessionCommands(
		t.Context(),
		"owner",
		"agent",
		"queue",
		"native",
		20,
	)
	require.NoError(t, err)
	assert.JSONEq(
		t,
		`{"session_id":"native","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"latest"}]}}`,
		string(got.Params),
	)
	assert.Equal(t, time.Unix(12, 0).UTC(), got.ObservedAt)
	for _, args := range [][4]string{
		{"other", "agent", "queue", "native"}, {"owner", "missing", "queue", "native"},
		{"owner", "agent", "wrong", "native"}, {"owner", "agent", "queue", "wrong"},
		{"", "agent", "queue", "native"}, {"owner", "agent", "", "native"},
	} {
		_, err = store.LatestDeferredSessionCommands(
			t.Context(),
			args[0],
			args[1],
			args[2],
			args[3],
			20,
		)
		assert.ErrorIs(t, err, ErrNotFound)
	}
	_, err = store.LatestDeferredSessionCommands(
		t.Context(),
		"owner",
		"agent",
		"queue",
		"native",
		10,
	)
	assert.ErrorIs(t, err, ErrNotFound)
	got.Params[0] = 'x'
	again, err := store.LatestDeferredSessionCommands(
		t.Context(),
		"owner",
		"agent",
		"queue",
		"native",
		20,
	)
	require.NoError(t, err)
	assert.True(t, json.Valid(again.Params))
}

func TestMemoryCommandJournalGivenCreationRequestsThenMatchTypedIDAndQueue(t *testing.T) {
	store := NewMemoryStore(time.Now)
	store.agents["agent"] = Agent{AgentID: "agent", OwnerUserID: "owner"}
	for _, frame := range commandRecoveryFrames() {
		require.NoError(t, store.SaveTransportFrame(t.Context(), &frame))
	}
	for _, tc := range []struct{ id, want string }{{"7", "manager-latest"}, {`"7"`, "manager-string"}, {`"\u0037"`, "manager-string"}} {
		got, err := store.RecoverSessionCreation(t.Context(), "owner", "agent", "queue", tc.id)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got)
	}
	for _, args := range [][4]string{
		{"other", "agent", "queue", "7"}, {"owner", "missing", "queue", "7"},
		{"owner", "agent", "wrong", "7"}, {"owner", "agent", "queue", "8"},
		{"owner", "agent", "queue", "9"}, {"owner", "agent", "queue", "10"}, {"owner", "agent", "queue", "invalid"},
		{"", "agent", "queue", "7"}, {"owner", "agent", "", "7"},
	} {
		_, err := store.RecoverSessionCreation(t.Context(), args[0], args[1], args[2], args[3])
		assert.ErrorIs(t, err, ErrNotFound)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := store.RecoverSessionCreation(ctx, "owner", "agent", "queue", "7")
	assert.ErrorIs(t, err, context.Canceled)
	_, err = store.LatestDeferredSessionCommands(ctx, "owner", "agent", "queue", "native", 20)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestPostgresCommandJournalGivenDeferredUpdatesAndCreationThenRecoverWithinScope(t *testing.T) {
	store := secretaryPostgresFixture(t)
	frames := commandRecoveryFrames()
	for _, frame := range frames {
		frame.AgentID = "source"
		require.NoError(t, store.SaveTransportFrame(t.Context(), &frame))
	}
	got, err := store.LatestDeferredSessionCommands(
		t.Context(),
		"owner",
		"source",
		"queue",
		"native",
		20,
	)
	require.NoError(t, err)
	assert.Contains(t, string(got.Params), "latest")
	assert.Equal(t, time.Unix(12, 0).UTC(), got.ObservedAt)
	for _, tc := range []struct{ id, want string }{{"7", "manager-latest"}, {`"7"`, "manager-string"}, {`"\u0037"`, "manager-string"}} {
		id, recoverErr := store.RecoverSessionCreation(
			t.Context(),
			"owner",
			"source",
			"queue",
			tc.id,
		)
		require.NoError(t, recoverErr)
		assert.Equal(t, tc.want, id)
	}
	for _, args := range [][4]string{
		{"other", "source", "queue", "native"}, {"owner", "secretary", "queue", "native"},
		{"owner", "source", "wrong", "native"}, {"owner", "source", "queue", "wrong"},
	} {
		_, err = store.LatestDeferredSessionCommands(
			t.Context(),
			args[0],
			args[1],
			args[2],
			args[3],
			20,
		)
		assert.ErrorIs(t, err, ErrNotFound)
	}
	_, err = store.LatestDeferredSessionCommands(
		t.Context(),
		"owner",
		"source",
		"queue",
		"native",
		10,
	)
	assert.ErrorIs(t, err, ErrNotFound)
	for _, args := range [][4]string{
		{"other", "source", "queue", "7"}, {"owner", "secretary", "queue", "7"},
		{"owner", "source", "wrong", "7"}, {"owner", "source", "queue", "8"},
		{"owner", "source", "queue", "9"}, {"owner", "source", "queue", "10"}, {"owner", "source", "queue", "invalid"},
	} {
		_, err = store.RecoverSessionCreation(t.Context(), args[0], args[1], args[2], args[3])
		assert.ErrorIs(t, err, ErrNotFound)
	}
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE agents SET deleted_at=now() WHERE agent_id='source'`,
	)
	require.NoError(t, err)
	_, err = store.RecoverSessionCreation(t.Context(), "owner", "source", "queue", "7")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.LatestDeferredSessionCommands(
		t.Context(),
		"owner",
		"source",
		"queue",
		"native",
		20,
	)
	assert.ErrorIs(t, err, ErrNotFound)
}

func commandRecoveryFrames() []TransportFrame {
	var frames []TransportFrame
	add := func(seq int64, direction, method, id, native, manager, command, tag string) {
		payload := `{"method":"` + method + `","params":{"session_id":"` + native + `","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"` + command + `"}]}}}`
		if id != "" {
			payload = `{"method":"` + method + `","id":` + id + `}`
		}
		frames = append(
			frames,
			TransportFrame{AgentID: "agent", QueueID: "queue", Stream: "acp", Seq: seq,
				Direction: direction, Kind: "data", PayloadJSON: json.RawMessage(payload),
				Metadata: map[string]string{
					domain.DeferredSessionCommandsMetadataKey: tag,
					"manager_session_id":                      manager,
				},
				CreatedAt: time.Unix(seq, 0).UTC(),
			},
		)
	}
	add(10, "inbound", "session/update", "", "native", "", "old", "true")
	add(12, "inbound", "session/update", "", "native", "", "latest", "true")
	add(13, "inbound", "session/update", "", "native", "", "untagged", "")
	add(14, "inbound", "wrong", "", "native", "", "wrong-method", "true")
	add(15, "outbound", "session/update", "", "native", "", "wrong-direction", "true")
	add(16, "inbound", "session/update", "", "other-native", "", "wrong-native", "true")
	add(17, "inbound", "session/update", "", "native", "", "wrong-stream", "true")
	frames[len(frames)-1].Stream = "control"
	add(18, "inbound", "session/update", "", "native", "", "wrong-kind", "true")
	frames[len(frames)-1].PayloadJSON = json.RawMessage(
		`{"method":"session/update","params":{"sessionId":"native","update":{"sessionUpdate":"other"}}}`,
	)
	add(19, "inbound", "session/update", "", "native", "", "rpc-not-notification", "true")
	frames[len(frames)-1].PayloadJSON = append(
		json.RawMessage(`{"id":42,`),
		frames[len(frames)-1].PayloadJSON[1:]...)
	add(21, "inbound", "session/update", "", "native", "", "future", "true")
	add(1, "outbound", "session/new", "7", "", "manager-old", "", "")
	add(2, "outbound", "session/new", "7", "", "manager-latest", "", "")
	add(3, "outbound", "session/new", `"7"`, "", "manager-string", "", "")
	add(4, "outbound", "session/new", "8", "", "", "", "")
	add(5, "outbound", "other", "9", "", "wrong-method", "", "")
	add(6, "outbound", "session/new", "10", "", "stale-manager", "", "")
	add(7, "outbound", "session/prompt", "10", "", "", "", "")
	return frames
}

func TestPostgresCommandJournalGivenInvalidScopeThenRejectWithoutQuery(t *testing.T) {
	store := &PostgresStore{}
	for _, args := range [][4]string{{"", "agent", "queue", "native"}, {"owner", "", "queue", "native"}, {"owner", "agent", "", "native"}, {"owner", "agent", "queue", ""}} {
		_, err := store.LatestDeferredSessionCommands(
			t.Context(),
			args[0],
			args[1],
			args[2],
			args[3],
			20,
		)
		assert.ErrorIs(t, err, ErrNotFound)
	}
	_, err := store.LatestDeferredSessionCommands(
		t.Context(),
		"owner",
		"agent",
		"queue",
		"native",
		0,
	)
	assert.ErrorIs(t, err, ErrNotFound)
	for _, args := range [][4]string{{"", "agent", "queue", "7"}, {"owner", "", "queue", "7"}, {"owner", "agent", "", "7"}, {"owner", "agent", "queue", ""}} {
		_, err = store.RecoverSessionCreation(t.Context(), args[0], args[1], args[2], args[3])
		assert.ErrorIs(t, err, ErrNotFound)
	}
}

func TestPostgresCommandJournalGivenQueryFailureThenPreservesError(t *testing.T) {
	store, cleanup := scriptedPostgresStore(t, &scriptedPostgresScript{})
	defer cleanup()
	_, err := store.LatestDeferredSessionCommands(
		t.Context(),
		"owner",
		"agent",
		"queue",
		"native",
		20,
	)
	assert.ErrorContains(t, err, "unexpected query")
	_, err = store.RecoverSessionCreation(t.Context(), "owner", "agent", "queue", "7")
	assert.ErrorContains(t, err, "unexpected query")
}
