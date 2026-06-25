package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/paxkit/reliablemq"
)

func (s *PostgresStore) AppendOutboundData(
	ctx context.Context,
	queueID string,
	stream reliablemq.Stream,
	payload json.RawMessage,
	metadata reliablemq.Metadata,
) (reliablemq.Frame, error) {
	if !json.Valid(payload) {
		return reliablemq.Frame{}, fmt.Errorf("%w: payload must be valid JSON", reliablemq.ErrInvalidFrame)
	}
	return s.appendOutboundReliableFrame(ctx, queueID, stream, reliablemq.FrameKindData, payload, "", metadata)
}

func (s *PostgresStore) AppendOutboundTombstone(
	ctx context.Context,
	queueID string,
	stream reliablemq.Stream,
	errorMessage string,
	metadata reliablemq.Metadata,
) (reliablemq.Frame, error) {
	return s.appendOutboundReliableFrame(ctx, queueID, stream, reliablemq.FrameKindTombstone, nil, errorMessage, metadata)
}

func (s *PostgresStore) SaveInboundIfAbsent(
	ctx context.Context,
	frame reliablemq.Frame,
) (bool, reliablemq.Frame, error) {
	if err := reliablemq.ValidateFrame(frame); err != nil {
		return false, reliablemq.Frame{}, err
	}
	if frame.Key.Direction != reliablemq.DirectionInbound {
		return false, reliablemq.Frame{}, fmt.Errorf("%w: inbound frame direction is %q", reliablemq.ErrInvalidFrame, frame.Key.Direction)
	}
	now := s.now().UTC()
	frame = frame.Clone()
	frame.Status = reliablemq.StatusReceived
	frame.CreatedAt = now
	frame.UpdatedAt = now

	row := s.db.QueryRowContext(ctx, `
		INSERT INTO transport_journal (
			queue_id, agent_id, stream, seq, direction, local_direction, kind,
			payload_json, metadata_json, status, error_message, error,
			retry_count, created_at, updated_at, received_at
		)
		VALUES ($1,$2,$3,$4,$5,$5,$6,$7,$8,$9,$10,NULLIF($10,''),0,$11,$12,$12)
		ON CONFLICT (queue_id, stream, seq, direction) DO NOTHING
		RETURNING `+reliableFrameReturningSQL+`
	`, frame.Key.QueueID, reliableFrameAgentID(frame), string(frame.Key.Stream), frame.Key.Seq,
		string(frame.Key.Direction), string(frame.Kind), nullablePayload(frame.Payload),
		mustMarshalReliableMetadata(frame.Metadata), string(frame.Status), frame.ErrorMessage,
		frame.CreatedAt, frame.UpdatedAt)
	stored, err := scanReliableFrame(row)
	if err == sql.ErrNoRows {
		stored, err = s.getReliableFrame(ctx, frame.Key)
		if err != nil {
			return false, reliablemq.Frame{}, err
		}
		return false, stored, nil
	}
	if err != nil {
		return false, reliablemq.Frame{}, err
	}
	return true, stored, nil
}

func (s *PostgresStore) ListOutboundReplay(
	ctx context.Context,
	queueID string,
	stream reliablemq.Stream,
	limit int,
) ([]reliablemq.Frame, error) {
	return s.listReliableFrames(ctx, queueID, stream, reliablemq.DirectionOutbound, []reliablemq.Status{
		reliablemq.StatusPending,
		reliablemq.StatusSent,
	}, limit)
}

func (s *PostgresStore) ListInboundReplay(
	ctx context.Context,
	queueID string,
	stream reliablemq.Stream,
	limit int,
) ([]reliablemq.Frame, error) {
	return s.listReliableFrames(ctx, queueID, stream, reliablemq.DirectionInbound, []reliablemq.Status{
		reliablemq.StatusReceived,
	}, limit)
}

func (s *PostgresStore) MarkSent(ctx context.Context, key reliablemq.FrameKey) error {
	return s.updateReliableFrameStatus(ctx, key, reliablemq.StatusSent, "")
}

func (s *PostgresStore) AckOutboundThrough(
	ctx context.Context,
	queueID string,
	stream reliablemq.Stream,
	throughSeq int64,
) error {
	now := s.now().UTC()
	_, err := s.db.ExecContext(ctx, `
		UPDATE transport_journal
		SET status = $1, error_message = '', error = NULL, acked_at = $2, updated_at = $2
		WHERE queue_id = $3 AND stream = $4 AND direction = $5
			AND seq <= $6 AND status != $1
	`, string(reliablemq.StatusAcked), now, queueID, string(stream), string(reliablemq.DirectionOutbound), throughSeq)
	return err
}

func (s *PostgresStore) MarkApplied(ctx context.Context, key reliablemq.FrameKey) error {
	return s.updateReliableFrameStatus(ctx, key, reliablemq.StatusApplied, "")
}

func (s *PostgresStore) MarkRejected(ctx context.Context, key reliablemq.FrameKey, errorMessage string) error {
	return s.updateReliableFrameStatus(ctx, key, reliablemq.StatusRejected, errorMessage)
}

func (s *PostgresStore) RecordSendFailure(ctx context.Context, key reliablemq.FrameKey, errorMessage string) error {
	return s.updateReliableFrameError(ctx, key, errorMessage)
}

func (s *PostgresStore) RecordDispatchFailure(ctx context.Context, key reliablemq.FrameKey, errorMessage string) error {
	return s.updateReliableFrameError(ctx, key, errorMessage)
}

func (s *PostgresStore) UpdateMetadata(ctx context.Context, key reliablemq.FrameKey, metadata reliablemq.Metadata) error {
	now := s.now().UTC()
	_, err := s.db.ExecContext(ctx, `
		UPDATE transport_journal
		SET metadata_json = $1, agent_id = $2, updated_at = $3
		WHERE queue_id = $4 AND stream = $5 AND seq = $6 AND direction = $7
	`, mustMarshalReliableMetadata(metadata), reliableMetadataAgentID(metadata, key.QueueID), now,
		key.QueueID, string(key.Stream), key.Seq, string(key.Direction))
	return err
}

func (s *PostgresStore) SaveTransportFrame(ctx context.Context, frame *TransportFrame) error {
	reliableFrame, err := transportFrameToReliable(frame)
	if err != nil {
		return err
	}
	saved, err := s.insertReliableFrame(ctx, reliableFrame)
	if err != nil {
		return err
	}
	*frame = transportFrameFromReliable(saved, frame.AgentID)
	return nil
}

func (s *PostgresStore) SaveTransportFrameIfAbsent(
	ctx context.Context,
	frame *TransportFrame,
) (bool, error) {
	reliableFrame, err := transportFrameToReliable(frame)
	if err != nil {
		return false, err
	}
	inserted, stored, err := s.SaveInboundIfAbsent(ctx, reliableFrame)
	if err != nil {
		return false, err
	}
	*frame = transportFrameFromReliable(stored, frame.AgentID)
	return inserted, nil
}

func (s *PostgresStore) NextTransportSeq(
	ctx context.Context,
	agentID string,
	stream string,
	direction string,
) (int64, error) {
	queueID := agentID
	normalizedStream := normalizeReliableStream(stream)
	normalizedDirection := reliablemq.Direction(direction)
	row := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0) + 1
		FROM transport_journal
		WHERE queue_id = $1 AND stream = $2 AND direction = $3
	`, queueID, string(normalizedStream), string(normalizedDirection))
	var next int64
	if err := row.Scan(&next); err != nil {
		return 0, err
	}
	return next, nil
}

func (s *PostgresStore) GetTransportFrame(
	ctx context.Context,
	agentID string,
	stream string,
	seq int64,
	direction string,
) (*TransportFrame, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+reliableFrameReturningSQL+`
		FROM transport_journal
		WHERE queue_id = $1 AND stream = $2 AND seq = $3 AND direction = $4
	`, agentID, string(normalizeReliableStream(stream)), seq, direction)
	frame, err := scanReliableFrame(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	compat := transportFrameFromReliable(frame, agentID)
	return &compat, nil
}

func (s *PostgresStore) ListTransportFrames(
	ctx context.Context,
	agentID string,
	stream string,
	direction string,
	statuses []string,
	limit int,
) ([]TransportFrame, error) {
	reliableStatuses := make([]reliablemq.Status, 0, len(statuses))
	for _, status := range statuses {
		reliableStatuses = append(reliableStatuses, reliablemq.Status(status))
	}
	frames, err := s.listReliableFrames(
		ctx,
		agentID,
		normalizeReliableStream(stream),
		reliablemq.Direction(direction),
		reliableStatuses,
		limit,
	)
	if err != nil {
		return nil, err
	}
	compat := make([]TransportFrame, 0, len(frames))
	for _, frame := range frames {
		compat = append(compat, transportFrameFromReliable(frame, agentID))
	}
	return compat, nil
}

func (s *PostgresStore) UpdateTransportFrameStatus(
	ctx context.Context,
	agentID string,
	stream string,
	seq int64,
	direction string,
	status string,
	errMsg string,
) error {
	return s.updateReliableFrameStatus(ctx, reliablemq.FrameKey{
		QueueID:   agentID,
		Stream:    normalizeReliableStream(stream),
		Seq:       seq,
		Direction: reliablemq.Direction(direction),
	}, reliablemq.Status(status), errMsg)
}

func (s *PostgresStore) AckOutboundTransportFrames(
	ctx context.Context,
	agentID string,
	stream string,
	throughSeq int64,
) error {
	return s.AckOutboundThrough(ctx, agentID, normalizeReliableStream(stream), throughSeq)
}

func (s *PostgresStore) DeleteCompletedTransportFrames(
	ctx context.Context,
	cutoff time.Time,
	limit int,
) (int64, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	result, err := s.db.ExecContext(ctx, `
		WITH doomed AS (
			SELECT id
			FROM transport_journal
			WHERE status IN ($1, $2) AND updated_at < $3
			ORDER BY id
			LIMIT $4
		)
		DELETE FROM transport_journal f
		USING doomed
		WHERE f.id = doomed.id
	`, string(reliablemq.StatusAcked), string(reliablemq.StatusApplied), cutoff.UTC(), limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *PostgresStore) appendOutboundReliableFrame(
	ctx context.Context,
	queueID string,
	stream reliablemq.Stream,
	kind reliablemq.FrameKind,
	payload json.RawMessage,
	errorMessage string,
	metadata reliablemq.Metadata,
) (reliablemq.Frame, error) {
	if queueID == "" {
		return reliablemq.Frame{}, fmt.Errorf("%w: queue_id is required", reliablemq.ErrInvalidFrame)
	}
	if stream == "" {
		return reliablemq.Frame{}, fmt.Errorf("%w: stream is required", reliablemq.ErrInvalidFrame)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return reliablemq.Frame{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var seq int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0) + 1
		FROM transport_journal
		WHERE queue_id = $1 AND stream = $2 AND direction = $3
	`, queueID, string(stream), string(reliablemq.DirectionOutbound)).Scan(&seq); err != nil {
		return reliablemq.Frame{}, err
	}
	now := s.now().UTC()
	frame := reliablemq.Frame{
		Key: reliablemq.FrameKey{
			QueueID:   queueID,
			Stream:    stream,
			Seq:       seq,
			Direction: reliablemq.DirectionOutbound,
		},
		Kind:         kind,
		Payload:      append(json.RawMessage(nil), payload...),
		Metadata:     metadata.Clone(),
		Status:       reliablemq.StatusPending,
		ErrorMessage: errorMessage,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := reliablemq.ValidateFrame(frame); err != nil {
		return reliablemq.Frame{}, err
	}
	if _, err := insertReliableFrameTx(ctx, tx, frame); err != nil {
		return reliablemq.Frame{}, err
	}
	if err := tx.Commit(); err != nil {
		return reliablemq.Frame{}, err
	}
	return frame, nil
}

func (s *PostgresStore) insertReliableFrame(ctx context.Context, frame reliablemq.Frame) (reliablemq.Frame, error) {
	if err := reliablemq.ValidateFrame(frame); err != nil {
		return reliablemq.Frame{}, err
	}
	if frame.CreatedAt.IsZero() {
		frame.CreatedAt = s.now().UTC()
	}
	if frame.UpdatedAt.IsZero() {
		frame.UpdatedAt = frame.CreatedAt
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO transport_journal (
			queue_id, agent_id, stream, seq, direction, local_direction, kind,
			payload_json, metadata_json, status, error_message, error,
			retry_count, created_at, updated_at, sent_at, received_at, acked_at, applied_at
		)
		VALUES ($1,$2,$3,$4,$5,$5,$6,$7,$8,$9,$10,NULLIF($10,''),0,$11,$12,$13,$14,$15,$16)
		RETURNING `+reliableFrameReturningSQL+`
	`, frame.Key.QueueID, reliableFrameAgentID(frame), string(frame.Key.Stream), frame.Key.Seq,
		string(frame.Key.Direction), string(frame.Kind), nullablePayload(frame.Payload),
		mustMarshalReliableMetadata(frame.Metadata), string(frame.Status), frame.ErrorMessage,
		frame.CreatedAt, frame.UpdatedAt,
		transportTimestamp(frame, reliablemq.StatusSent),
		transportTimestamp(frame, reliablemq.StatusReceived),
		transportTimestamp(frame, reliablemq.StatusAcked),
		transportTimestamp(frame, reliablemq.StatusApplied))
	return scanReliableFrame(row)
}

func insertReliableFrameTx(ctx context.Context, tx *sql.Tx, frame reliablemq.Frame) (bool, error) {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO transport_journal (
			queue_id, agent_id, stream, seq, direction, local_direction, kind,
			payload_json, metadata_json, status, error_message, error,
			retry_count, created_at, updated_at, sent_at, received_at, acked_at, applied_at
		)
		VALUES ($1,$2,$3,$4,$5,$5,$6,$7,$8,$9,$10,NULLIF($10,''),0,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (queue_id, stream, seq, direction) DO NOTHING
	`, frame.Key.QueueID, reliableFrameAgentID(frame), string(frame.Key.Stream), frame.Key.Seq,
		string(frame.Key.Direction), string(frame.Kind), nullablePayload(frame.Payload),
		mustMarshalReliableMetadata(frame.Metadata), string(frame.Status), frame.ErrorMessage,
		frame.CreatedAt, frame.UpdatedAt,
		transportTimestamp(frame, reliablemq.StatusSent),
		transportTimestamp(frame, reliablemq.StatusReceived),
		transportTimestamp(frame, reliablemq.StatusAcked),
		transportTimestamp(frame, reliablemq.StatusApplied))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (s *PostgresStore) getReliableFrame(ctx context.Context, key reliablemq.FrameKey) (reliablemq.Frame, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+reliableFrameReturningSQL+`
		FROM transport_journal
		WHERE queue_id = $1 AND stream = $2 AND seq = $3 AND direction = $4
	`, key.QueueID, string(key.Stream), key.Seq, string(key.Direction))
	return scanReliableFrame(row)
}

func (s *PostgresStore) listReliableFrames(
	ctx context.Context,
	queueID string,
	stream reliablemq.Stream,
	direction reliablemq.Direction,
	statuses []reliablemq.Status,
	limit int,
) ([]reliablemq.Frame, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	args := []any{queueID, string(stream), string(direction)}
	statusSQL := ""
	if len(statuses) > 0 {
		placeholders := make([]string, 0, len(statuses))
		for _, status := range statuses {
			args = append(args, string(status))
			placeholders = append(placeholders, "$"+strconvArg(len(args)))
		}
		statusSQL = " AND status IN (" + strings.Join(placeholders, ",") + ")"
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+reliableFrameReturningSQL+`
		FROM transport_journal
		WHERE queue_id = $1 AND stream = $2 AND direction = $3`+statusSQL+`
		ORDER BY seq
		LIMIT $`+strconvArg(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var frames []reliablemq.Frame
	for rows.Next() {
		frame, err := scanReliableFrame(rows)
		if err != nil {
			return nil, err
		}
		frames = append(frames, frame)
	}
	return frames, rows.Err()
}

func (s *PostgresStore) updateReliableFrameStatus(
	ctx context.Context,
	key reliablemq.FrameKey,
	status reliablemq.Status,
	errMsg string,
) error {
	now := s.now().UTC()
	column := timestampColumnForReliableStatus(status)
	query := `
		UPDATE transport_journal
		SET status = $1, error_message = $2, error = NULLIF($2, ''), updated_at = $3`
	args := []any{string(status), errMsg, now}
	if column != "" {
		args = append(args, now)
		query += `, ` + column + ` = $` + strconvArg(len(args))
	}
	args = append(args, key.QueueID, string(key.Stream), key.Seq, string(key.Direction))
	query += `
		WHERE queue_id = $` + strconvArg(len(args)-3) + `
			AND stream = $` + strconvArg(len(args)-2) + `
			AND seq = $` + strconvArg(len(args)-1) + `
			AND direction = $` + strconvArg(len(args))
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

func (s *PostgresStore) updateReliableFrameError(
	ctx context.Context,
	key reliablemq.FrameKey,
	errorMessage string,
) error {
	now := s.now().UTC()
	_, err := s.db.ExecContext(ctx, `
		UPDATE transport_journal
		SET error_message = $1, error = NULLIF($1, ''), updated_at = $2
		WHERE queue_id = $3 AND stream = $4 AND seq = $5 AND direction = $6
	`, errorMessage, now, key.QueueID, string(key.Stream), key.Seq, string(key.Direction))
	return err
}

const reliableFrameReturningSQL = `
	id, queue_id, agent_id, stream, seq, direction, kind, COALESCE(payload_json, '{}'::jsonb),
	COALESCE(metadata_json, '{}'::jsonb), status, COALESCE(error_message, COALESCE(error, '')),
	created_at, updated_at`

func scanReliableFrame(row interface{ Scan(dest ...any) error }) (reliablemq.Frame, error) {
	var id int64
	var agentID string
	var stream string
	var direction string
	var kind string
	var payload json.RawMessage
	var metadataJSON json.RawMessage
	var status string
	var frame reliablemq.Frame
	if err := row.Scan(
		&id,
		&frame.Key.QueueID,
		&agentID,
		&stream,
		&frame.Key.Seq,
		&direction,
		&kind,
		&payload,
		&metadataJSON,
		&status,
		&frame.ErrorMessage,
		&frame.CreatedAt,
		&frame.UpdatedAt,
	); err != nil {
		return reliablemq.Frame{}, err
	}
	frame.Key.Stream = reliablemq.Stream(stream)
	frame.Key.Direction = reliablemq.Direction(direction)
	frame.Kind = reliablemq.FrameKind(kind)
	frame.Status = reliablemq.Status(status)
	if len(payload) > 0 && string(payload) != "{}" {
		frame.Payload = append(json.RawMessage(nil), payload...)
	}
	if err := json.Unmarshal(metadataJSON, &frame.Metadata); err != nil {
		return reliablemq.Frame{}, err
	}
	if frame.Metadata == nil {
		frame.Metadata = reliablemq.Metadata{}
	}
	if agentID != "" && frame.Metadata["agent_id"] == "" {
		frame.Metadata["agent_id"] = agentID
	}
	return frame, nil
}

func transportFrameToReliable(frame *TransportFrame) (reliablemq.Frame, error) {
	if frame == nil {
		return reliablemq.Frame{}, fmt.Errorf("transport frame is required")
	}
	queueID := firstNonEmpty(frame.QueueID, frame.AgentID)
	direction := firstNonEmpty(frame.Direction, frame.LocalDirection)
	kind := firstNonEmpty(frame.Kind, string(reliablemq.FrameKindData))
	status := firstNonEmpty(frame.Status, string(reliablemq.DefaultStatus(reliablemq.Direction(direction))))
	metadata := reliablemq.Metadata(frame.Metadata).Clone()
	if metadata == nil {
		metadata = reliablemq.Metadata{}
	}
	if frame.AgentID != "" {
		metadata["agent_id"] = frame.AgentID
	}
	errMsg := firstNonEmpty(frame.ErrorMessage, frame.Error)
	reliableFrame := reliablemq.Frame{
		Key: reliablemq.FrameKey{
			QueueID:   queueID,
			Stream:    normalizeReliableStream(frame.Stream),
			Seq:       frame.Seq,
			Direction: reliablemq.Direction(direction),
		},
		Kind:         reliablemq.FrameKind(kind),
		Payload:      append(json.RawMessage(nil), frame.PayloadJSON...),
		Metadata:     metadata,
		Status:       reliablemq.Status(status),
		ErrorMessage: errMsg,
		CreatedAt:    frame.CreatedAt,
		UpdatedAt:    frame.UpdatedAt,
	}
	return reliableFrame, reliablemq.ValidateFrame(reliableFrame)
}

func transportFrameFromReliable(frame reliablemq.Frame, agentID string) TransportFrame {
	if agentID == "" {
		agentID = frame.Metadata["agent_id"]
	}
	compat := TransportFrame{
		QueueID:        frame.Key.QueueID,
		AgentID:        agentID,
		Stream:         string(frame.Key.Stream),
		Seq:            frame.Key.Seq,
		Direction:      string(frame.Key.Direction),
		LocalDirection: string(frame.Key.Direction),
		Kind:           string(frame.Kind),
		PayloadJSON:    append(json.RawMessage(nil), frame.Payload...),
		Metadata:       map[string]string(frame.Metadata.Clone()),
		Status:         string(frame.Status),
		ErrorMessage:   frame.ErrorMessage,
		Error:          frame.ErrorMessage,
		CreatedAt:      frame.CreatedAt,
		UpdatedAt:      frame.UpdatedAt,
	}
	setTransportStatusTimestamp(&compat, compat.Status, compat.UpdatedAt)
	return compat
}

func normalizeReliableStream(stream string) reliablemq.Stream {
	switch stream {
	case "", domain.TransportStreamACP, domain.TransportStreamManagerToPaxd, domain.TransportStreamPaxdToManager:
		return reliablemq.StreamACP
	default:
		return reliablemq.Stream(stream)
	}
}

func reliableFrameAgentID(frame reliablemq.Frame) string {
	return reliableMetadataAgentID(frame.Metadata, frame.Key.QueueID)
}

func reliableMetadataAgentID(metadata reliablemq.Metadata, fallback string) string {
	if metadata != nil && metadata["agent_id"] != "" {
		return metadata["agent_id"]
	}
	return fallback
}

func mustMarshalReliableMetadata(metadata reliablemq.Metadata) json.RawMessage {
	if metadata == nil {
		metadata = reliablemq.Metadata{}
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}

func nullablePayload(payload json.RawMessage) any {
	if len(payload) == 0 {
		return nil
	}
	return payload
}

func transportTimestamp(frame reliablemq.Frame, status reliablemq.Status) *time.Time {
	if frame.Status != status {
		return nil
	}
	ts := frame.UpdatedAt
	if ts.IsZero() {
		ts = frame.CreatedAt
	}
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return &ts
}

func timestampColumnForReliableStatus(status reliablemq.Status) string {
	switch status {
	case reliablemq.StatusSent:
		return "sent_at"
	case reliablemq.StatusAcked:
		return "acked_at"
	case reliablemq.StatusReceived:
		return "received_at"
	case reliablemq.StatusApplied:
		return "applied_at"
	default:
		return ""
	}
}
