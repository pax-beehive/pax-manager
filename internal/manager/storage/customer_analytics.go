package storage

import (
	"context"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// Aggregate each relation before joining to avoid multiplying message counts.
const customerAnalyticsSQL = `
WITH device_counts AS (
 SELECT owner_user_id, COUNT(*) AS devices FROM nodes
 WHERE kind = 'paxd' AND deleted_at IS NULL GROUP BY owner_user_id
), agent_counts AS (
 SELECT a.owner_user_id,
 COUNT(*) FILTER (WHERE a.deleted_at IS NULL AND n.deleted_at IS NULL) AS agents,
 MIN(a.registered_at) AS first_bound_at
 FROM agents a JOIN nodes n ON n.node_id = a.node_id
 WHERE n.kind = 'paxd' AND a.last_heartbeat IS NOT NULL
 GROUP BY a.owner_user_id
), message_counts AS (
 SELECT COALESCE(m.owner_user_id,a.owner_user_id) AS owner_user_id,
 COUNT(*) AS user_messages, MIN(m.created_at) AS first_message_at,
 MAX(m.created_at) AS last_message_at
 FROM messages m LEFT JOIN agents a ON a.agent_id = m.agent_id
 WHERE m.role = 'user' AND m.direction = 'user_to_agent'
 GROUP BY COALESCE(m.owner_user_id,a.owner_user_id)
), encrypted_counts AS (
 SELECT owner_user_id, COUNT(*) AS encrypted_records FROM e2ee_messages
 GROUP BY owner_user_id
), session_counts AS (
 SELECT a.owner_user_id, COUNT(*) AS sessions FROM agent_sessions s
 JOIN agents a ON a.agent_id = s.agent_id GROUP BY a.owner_user_id
)
SELECT u.user_id, u.email, u.created_at, v.first_visit_at, v.last_visit_at,
 COALESCE(d.devices,0) AS devices, COALESCE(a.agents,0) AS agents, a.first_bound_at,
 COALESCE(m.user_messages,0) AS user_messages, m.first_message_at, m.last_message_at,
 COALESCE(e.encrypted_records,0) AS encrypted_records, COALESCE(s.sessions,0) AS sessions
FROM users u LEFT JOIN customer_visits v ON v.user_id = u.user_id
LEFT JOIN device_counts d ON d.owner_user_id = u.user_id
LEFT JOIN agent_counts a ON a.owner_user_id = u.user_id
LEFT JOIN message_counts m ON m.owner_user_id = u.user_id
LEFT JOIN encrypted_counts e ON e.owner_user_id = u.user_id
LEFT JOIN session_counts s ON s.owner_user_id = u.user_id
ORDER BY u.created_at,u.email`

func (s *PostgresStore) CustomerAnalytics(ctx context.Context) ([]domain.CustomerAnalytics, error) {
	rows := make([]domain.CustomerAnalytics, 0)
	err := s.gormDB.WithContext(ctx).Raw(customerAnalyticsSQL).Scan(&rows).Error
	return rows, err
}

// Server time and the authenticated principal determine the visit. Replays
// cannot move activity backwards or fabricate a different user's visit.
func (s *PostgresStore) RecordCustomerVisit(
	ctx context.Context,
	userID string,
	at time.Time,
) error {
	return s.gormDB.WithContext(ctx).Exec(`
 INSERT INTO customer_visits(user_id,first_visit_at,last_visit_at)
 SELECT user_id,?,? FROM users WHERE user_id = ?
 ON CONFLICT(user_id) DO UPDATE SET last_visit_at = EXCLUDED.last_visit_at
 WHERE customer_visits.last_visit_at < EXCLUDED.last_visit_at`, at, at, userID).Error
}
