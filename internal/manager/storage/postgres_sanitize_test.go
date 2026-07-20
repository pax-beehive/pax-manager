package storage

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/pax-beehive/paxkit/reliablemq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresSafeJSONReplacesNULWithoutChangingValidUnicode(t *testing.T) {
	raw := json.RawMessage(
		`{"text":"before\u0000after","nested":{"\u0000key":["\u0001","\ud83d\ude00"]}}`,
	)

	normalized, report, err := postgresSafeJSON(raw)

	require.NoError(t, err)
	assert.Equal(t, 2, report.NULReplacements)
	assert.JSONEq(
		t,
		`{"text":"before\ufffdafter","nested":{"\ufffdkey":["\u0001","\ud83d\ude00"]}}`,
		string(normalized),
	)
}

func TestPostgresSafeJSONRejectsNormalizedKeyCollision(t *testing.T) {
	_, _, err := postgresSafeJSON(
		json.RawMessage(`{"x\u0000":1,"x\ufffd":2}`),
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "normalized key collision")
}

func TestPostgresSafeJSONPreservesCompatibleInputBytes(t *testing.T) {
	raw := json.RawMessage(`{"routing_tags":["review"],"skills":["review","tests"]}`)

	normalized, report, err := postgresSafeJSON(raw)

	require.NoError(t, err)
	assert.Zero(t, report.NULReplacements)
	assert.Equal(t, string(raw), string(normalized))
}

func TestPostgresSafeJSONRepairsLoneSurrogate(t *testing.T) {
	normalized, report, err := postgresSafeJSON(json.RawMessage(`{"text":"\ud800"}`))

	require.NoError(t, err)
	assert.Zero(t, report.NULReplacements)
	assert.JSONEq(t, `{"text":"\ufffd"}`, string(normalized))
}

func TestPostgresJSONArgumentHelpersReplaceNUL(t *testing.T) {
	want := `{"text":"before\ufffdafter"}`
	raw := json.RawMessage(`{"text":"before\u0000after"}`)

	assert.JSONEq(t, want, string(nullRaw(raw).(json.RawMessage)))
	assert.JSONEq(t, want, string(jsonDefault(raw, "{}")))
	assert.JSONEq(t, want, string(jsonOrNil(map[string]string{
		"text": "before\x00after",
	}).([]byte)))
	assert.JSONEq(t, want, string(jsonOrDefault(map[string]string{
		"text": "before\x00after",
	}, "{}")))
}

func TestNormalizeReliableFrameForPostgresReturnsSanitizedCopy(t *testing.T) {
	original := reliablemq.Frame{
		Key: reliablemq.FrameKey{
			QueueID:   "queue_1",
			Stream:    reliablemq.StreamACP,
			Seq:       1,
			Direction: reliablemq.DirectionInbound,
		},
		Kind:         reliablemq.FrameKindData,
		Payload:      json.RawMessage(`{"text":"a\u0000b","\u0000key":"value"}`),
		Metadata:     reliablemq.Metadata{"agent_id": "agent_1", "trace": "a\x00b"},
		Status:       reliablemq.StatusReceived,
		ErrorMessage: "before\x00after",
	}

	normalized, err := normalizeReliableFrameForPostgres(original)

	require.NoError(t, err)
	assert.JSONEq(t, `{"text":"a\ufffdb","\ufffdkey":"value"}`, string(normalized.Payload))
	assert.Equal(t, "a\uFFFDb", normalized.Metadata["trace"])
	assert.Equal(t, "true", normalized.Metadata[reliableMetadataPayloadSanitized])
	assert.Equal(t, "2", normalized.Metadata[reliableMetadataNULReplacementCount])
	assert.Equal(t, "before\uFFFDafter", normalized.ErrorMessage)
	assert.Contains(t, string(original.Payload), `\u0000`)
	assert.Equal(t, "a\x00b", original.Metadata["trace"])
	assert.Equal(t, "before\x00after", original.ErrorMessage)
}

func TestPostgresInsertReliableFrameUsesSanitizedValuesAndReturnsStoredFrame(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	sanitizedPayload := []byte("{\"text\":\"before\uFFFDafter\"}")
	sanitizedMetadata := []byte(
		`{"agent_id":"agent_1","nul_replacements":"1","payload_sanitized":"true"}`,
	)
	script := &scriptedPostgresScript{
		queries: []scriptedRows{{
			columns: []string{
				"id", "queue_id", "agent_id", "stream", "seq", "direction", "kind",
				"payload_json", "metadata_json", "status", "error_message", "created_at", "updated_at",
			},
			values: [][]driver.Value{{
				int64(1), "queue_1", "agent_1", string(reliablemq.StreamACP), int64(1),
				string(reliablemq.DirectionInbound), string(reliablemq.FrameKindData),
				sanitizedPayload, sanitizedMetadata, string(reliablemq.StatusReceived), "", now, now,
			}},
		}},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()

	stored, err := store.insertReliableFrame(context.Background(), reliablemq.Frame{
		Key: reliablemq.FrameKey{
			QueueID:   "queue_1",
			Stream:    reliablemq.StreamACP,
			Seq:       1,
			Direction: reliablemq.DirectionInbound,
		},
		Kind:      reliablemq.FrameKindData,
		Payload:   json.RawMessage(`{"text":"before\u0000after"}`),
		Metadata:  reliablemq.Metadata{"agent_id": "agent_1"},
		Status:    reliablemq.StatusReceived,
		CreatedAt: now,
		UpdatedAt: now,
	})

	require.NoError(t, err)
	assert.JSONEq(t, string(sanitizedPayload), string(stored.Payload))
	require.Len(t, script.queryArgs, 1)
	require.Len(t, script.queryArgs[0], 16)
	assert.JSONEq(t, string(sanitizedPayload), string(script.queryArgs[0][6].Value.([]byte)))
	assert.JSONEq(t, string(sanitizedMetadata), string(script.queryArgs[0][7].Value.([]byte)))
}

func TestPostgresSaveInboundReturnsSanitizedStoredFrame(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	sanitizedPayload := []byte("{\"text\":\"before\uFFFDafter\"}")
	sanitizedMetadata := []byte(
		`{"agent_id":"agent_1","nul_replacements":"1","payload_sanitized":"true"}`,
	)
	script := &scriptedPostgresScript{
		queries: []scriptedRows{
			{columns: []string{"inbound_applied_through"}, values: [][]driver.Value{{int64(0)}}},
			{
				columns: reliableFrameTestColumns(),
				values: [][]driver.Value{reliableFrameTestRow(
					now,
					sanitizedPayload,
					sanitizedMetadata,
					reliablemq.DirectionInbound,
					reliablemq.StatusReceived,
				)},
			},
		},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()

	inserted, stored, err := store.SaveInboundIfAbsent(context.Background(), reliablemq.Frame{
		Key: reliablemq.FrameKey{
			QueueID:   "queue_1",
			Stream:    reliablemq.StreamACP,
			Seq:       1,
			Direction: reliablemq.DirectionInbound,
		},
		Kind:     reliablemq.FrameKindData,
		Payload:  json.RawMessage(`{"text":"before\u0000after"}`),
		Metadata: reliablemq.Metadata{"agent_id": "agent_1"},
	})

	require.NoError(t, err)
	assert.True(t, inserted)
	assert.JSONEq(t, string(sanitizedPayload), string(stored.Payload))
	assert.Equal(t, "true", stored.Metadata[reliableMetadataPayloadSanitized])
	require.Len(t, script.execArgs, 2)
	assert.JSONEq(t, string(sanitizedPayload), string(script.execArgs[1][6].Value.([]byte)))
}

func TestPostgresAppendOutboundReturnsSanitizedFrame(t *testing.T) {
	sanitizedPayload := []byte("{\"text\":\"before\uFFFDafter\"}")
	script := &scriptedPostgresScript{
		queries: []scriptedRows{{
			columns: []string{"next_outbound_seq"},
			values:  [][]driver.Value{{int64(1)}},
		}},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()

	frame, err := store.AppendOutboundData(
		context.Background(),
		"queue_1",
		reliablemq.StreamACP,
		json.RawMessage(`{"text":"before\u0000after"}`),
		reliablemq.Metadata{"agent_id": "agent_1"},
	)

	require.NoError(t, err)
	assert.JSONEq(t, string(sanitizedPayload), string(frame.Payload))
	assert.Equal(t, "true", frame.Metadata[reliableMetadataPayloadSanitized])
	require.Len(t, script.execArgs, 3)
	assert.JSONEq(t, string(sanitizedPayload), string(script.execArgs[2][6].Value.([]byte)))
}

func TestNormalizeReliableFrameForPostgresRejectsMetadataKeyCollision(t *testing.T) {
	_, err := normalizeReliableFrameForPostgres(reliablemq.Frame{
		Payload: json.RawMessage(`{}`),
		Metadata: reliablemq.Metadata{
			"x\x00":   "first",
			"x\uFFFD": "second",
		},
	})

	require.Error(t, err)
	assert.True(t, errors.Is(err, reliablemq.ErrInvalidFrame))
}

func reliableFrameTestColumns() []string {
	return []string{
		"id", "queue_id", "agent_id", "stream", "seq", "direction", "kind",
		"payload_json", "metadata_json", "status", "error_message", "created_at", "updated_at",
	}
}

func reliableFrameTestRow(
	now time.Time,
	payload []byte,
	metadata []byte,
	direction reliablemq.Direction,
	status reliablemq.Status,
) []driver.Value {
	return []driver.Value{
		int64(1), "queue_1", "agent_1", string(reliablemq.StreamACP), int64(1),
		string(direction), string(reliablemq.FrameKindData), payload, metadata,
		string(status), "", now, now,
	}
}
