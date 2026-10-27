package storage

import (
	"context"
	"fmt"
	"time"
)

// PruneCompletedTransportFrames keeps the persisted replay boundaries and a
// diagnostic tail. Missing queue state is deliberately ineligible for cleanup.
func (s *PostgresStore) PruneCompletedTransportFrames(
	ctx context.Context, cutoff time.Time, limit int, keepTail int64,
) (int64, error) {
	if cutoff.IsZero() || limit <= 0 || limit > 10000 || keepTail < 0 {
		return 0, fmt.Errorf("invalid transport journal cleanup parameters")
	}
	result, err := s.db.ExecContext(ctx, `
		WITH doomed AS (
			SELECT f.id
			FROM transport_journal AS f
			JOIN transport_queue_state AS q
				ON q.queue_id = f.queue_id AND q.stream = f.stream
			WHERE f.updated_at < $1
				AND f.status IN ('acked', 'applied')
				AND (
					(f.direction = 'outbound' AND f.status = 'acked'
						AND f.seq <= q.next_outbound_seq - 1 - $3)
					OR (f.direction = 'inbound' AND f.status = 'applied'
						AND f.seq <= LEAST(q.inbound_acked_through, q.inbound_applied_through) - $3)
				)
			ORDER BY f.status, f.updated_at
			LIMIT $2
			FOR UPDATE OF f SKIP LOCKED
		)
		DELETE FROM transport_journal AS f
		USING doomed
		WHERE f.id = doomed.id
	`, cutoff.UTC(), limit, keepTail)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
