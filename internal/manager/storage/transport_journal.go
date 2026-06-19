package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *PostgresStore) SaveTransportFrame(ctx context.Context, frame *TransportFrame) error {
	if err := validateTransportFrame(frame); err != nil {
		return err
	}
	if frame.Status == "" {
		frame.Status = defaultTransportStatus(frame.LocalDirection)
	}
	now := s.now().UTC()
	if frame.CreatedAt.IsZero() {
		frame.CreatedAt = now
	}
	if frame.UpdatedAt.IsZero() {
		frame.UpdatedAt = now
	}
	if frame.Status == domain.TransportStatusReceived && frame.ReceivedAt == nil {
		frame.ReceivedAt = &now
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO transport_journal (
			agent_id, stream, seq, local_direction, payload_json, status, error,
			retry_count, created_at, updated_at, sent_at, received_at, acked_at, applied_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,$9,$10,$11,$12,$13,$14)
		RETURNING `+transportFrameReturningSQL+`
	`, frame.AgentID, frame.Stream, frame.Seq, frame.LocalDirection, frame.PayloadJSON,
		frame.Status, frame.Error, frame.RetryCount, frame.CreatedAt, frame.UpdatedAt,
		frame.SentAt, frame.ReceivedAt, frame.AckedAt, frame.AppliedAt)
	saved, err := scanTransportFrame(row)
	if err != nil {
		return err
	}
	*frame = saved
	return nil
}

func (s *PostgresStore) SaveTransportFrameIfAbsent(
	ctx context.Context,
	frame *TransportFrame,
) (bool, error) {
	if err := validateTransportFrame(frame); err != nil {
		return false, err
	}
	if frame.Status == "" {
		frame.Status = defaultTransportStatus(frame.LocalDirection)
	}
	now := s.now().UTC()
	if frame.CreatedAt.IsZero() {
		frame.CreatedAt = now
	}
	if frame.UpdatedAt.IsZero() {
		frame.UpdatedAt = now
	}
	if frame.Status == domain.TransportStatusReceived && frame.ReceivedAt == nil {
		frame.ReceivedAt = &now
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO transport_journal (
			agent_id, stream, seq, local_direction, payload_json, status, error,
			retry_count, created_at, updated_at, sent_at, received_at, acked_at, applied_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (agent_id, stream, seq, local_direction) DO NOTHING
		RETURNING `+transportFrameReturningSQL+`
	`, frame.AgentID, frame.Stream, frame.Seq, frame.LocalDirection, frame.PayloadJSON,
		frame.Status, frame.Error, frame.RetryCount, frame.CreatedAt, frame.UpdatedAt,
		frame.SentAt, frame.ReceivedAt, frame.AckedAt, frame.AppliedAt)
	saved, err := scanTransportFrame(row)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	*frame = saved
	return true, nil
}

func (s *PostgresStore) NextTransportSeq(
	ctx context.Context,
	agentID string,
	stream string,
	direction string,
) (int64, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0) + 1
		FROM transport_journal
		WHERE agent_id = $1 AND stream = $2 AND local_direction = $3
	`, agentID, stream, direction)
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
		SELECT `+transportFrameReturningSQL+`
		FROM transport_journal
		WHERE agent_id = $1 AND stream = $2 AND seq = $3 AND local_direction = $4
	`, agentID, stream, seq, direction)
	frame, err := scanTransportFrame(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &frame, nil
}

func (s *PostgresStore) ListTransportFrames(
	ctx context.Context,
	agentID string,
	stream string,
	direction string,
	statuses []string,
	limit int,
) ([]TransportFrame, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	args := []any{agentID, stream, direction}
	statusSQL := ""
	if len(statuses) > 0 {
		placeholders := make([]string, 0, len(statuses))
		for _, status := range statuses {
			args = append(args, status)
			placeholders = append(placeholders, "$"+strconvArg(len(args)))
		}
		statusSQL = " AND status IN (" + strings.Join(placeholders, ",") + ")"
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+transportFrameReturningSQL+`
		FROM transport_journal
		WHERE agent_id = $1 AND stream = $2 AND local_direction = $3`+statusSQL+`
		ORDER BY seq
		LIMIT $`+strconvArg(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var frames []TransportFrame
	for rows.Next() {
		frame, err := scanTransportFrame(rows)
		if err != nil {
			return nil, err
		}
		frames = append(frames, frame)
	}
	return frames, rows.Err()
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
	now := s.now().UTC()
	column := timestampColumnForTransportStatus(status)
	query := `
		UPDATE transport_journal
		SET status = $1, error = NULLIF($2, ''), updated_at = $3`
	args := []any{status, errMsg, now}
	if column != "" {
		args = append(args, now)
		query += `, ` + column + ` = $` + strconvArg(len(args))
	}
	args = append(args, agentID, stream, seq, direction)
	query += `
		WHERE agent_id = $` + strconvArg(len(args)-3) + `
			AND stream = $` + strconvArg(len(args)-2) + `
			AND seq = $` + strconvArg(len(args)-1) + `
			AND local_direction = $` + strconvArg(len(args))
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

func (s *PostgresStore) AckOutboundTransportFrames(
	ctx context.Context,
	agentID string,
	stream string,
	throughSeq int64,
) error {
	now := s.now().UTC()
	_, err := s.db.ExecContext(ctx, `
		UPDATE transport_journal
		SET status = $1, acked_at = $2, updated_at = $2
		WHERE agent_id = $3 AND stream = $4 AND local_direction = $5
			AND seq <= $6 AND status != $1
	`, domain.TransportStatusAcked, now, agentID, stream, domain.TransportDirectionOutbound, throughSeq)
	return err
}

func (s *PostgresStore) DeleteCompletedTransportFrames(
	ctx context.Context,
	cutoff time.Time,
	limit int,
) (int64, error) {
	// TODO: Wire this into a low-priority maintenance worker. Only acked
	// outbound frames and applied inbound frames are eligible; pending/sent/
	// received/failed rows must remain replayable/debuggable.
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
	`, domain.TransportStatusAcked, domain.TransportStatusApplied, cutoff.UTC(), limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

const transportFrameReturningSQL = `
	id, agent_id, stream, seq, local_direction, payload_json, status,
	COALESCE(error, ''), retry_count, created_at, updated_at,
	sent_at, received_at, acked_at, applied_at`

func scanTransportFrame(row interface{ Scan(dest ...any) error }) (TransportFrame, error) {
	var frame TransportFrame
	if err := row.Scan(
		&frame.ID,
		&frame.AgentID,
		&frame.Stream,
		&frame.Seq,
		&frame.LocalDirection,
		&frame.PayloadJSON,
		&frame.Status,
		&frame.Error,
		&frame.RetryCount,
		&frame.CreatedAt,
		&frame.UpdatedAt,
		&frame.SentAt,
		&frame.ReceivedAt,
		&frame.AckedAt,
		&frame.AppliedAt,
	); err != nil {
		return TransportFrame{}, err
	}
	return frame, nil
}

func validateTransportFrame(frame *TransportFrame) error {
	if frame.AgentID == "" {
		return fmt.Errorf("agent_id is required")
	}
	if frame.Stream == "" {
		return fmt.Errorf("stream is required")
	}
	if frame.Seq <= 0 {
		return fmt.Errorf("seq must be positive")
	}
	if frame.LocalDirection == "" {
		return fmt.Errorf("local_direction is required")
	}
	if len(frame.PayloadJSON) == 0 || !json.Valid(frame.PayloadJSON) {
		return fmt.Errorf("payload_json must be valid JSON")
	}
	return nil
}

func defaultTransportStatus(direction string) string {
	if direction == domain.TransportDirectionInbound {
		return domain.TransportStatusReceived
	}
	return domain.TransportStatusPending
}

func timestampColumnForTransportStatus(status string) string {
	switch status {
	case domain.TransportStatusSent:
		return "sent_at"
	case domain.TransportStatusAcked:
		return "acked_at"
	case domain.TransportStatusReceived:
		return "received_at"
	case domain.TransportStatusApplied:
		return "applied_at"
	default:
		return ""
	}
}
