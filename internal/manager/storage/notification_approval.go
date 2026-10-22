package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

func notificationForApproval(approval AgentApproval) (notifications.Notification, error) {
	return notifications.BuildApprovalNotification(notifications.ApprovalRequest{
		UserID: approval.OwnerUserID, NodeID: approval.RequestNodeID,
		AgentID: approval.RequestAgentID, SessionID: approval.RequestSessionID,
		ApprovalID: approval.ApprovalID, CreatedAt: approval.CreatedAt, ExpiresAt: approval.ExpiresAt,
	})
}

func (s *MemoryStore) approvalNotificationAllowed(approval AgentApproval) bool {
	row := notificationCompletionRow{
		OwnerUserID: approval.OwnerUserID, NodeID: approval.RequestNodeID,
		AgentID: approval.RequestAgentID, SessionID: approval.RequestSessionID,
	}
	session := s.sessions[sessionKey(approval.RequestAgentID, approval.RequestSessionID)]
	return approval.Status == "pending" &&
		(approval.ExpiresAt == nil || s.now().Before(*approval.ExpiresAt)) &&
		s.notificationSessionAllowed(row) &&
		domain.NormalizeSessionApprovalMode(
			session.PaxConfig.ApprovalMode,
		) != domain.SessionApprovalModeAutoApproveAll
}

func (s *MemoryStore) enqueueApprovalNotificationLocked(approval AgentApproval) {
	if !s.approvalNotificationAllowed(approval) {
		return
	}
	notification, err := notificationForApproval(approval)
	if err != nil {
		return
	}
	var records []notifications.RegistrationRecord
	for _, record := range s.notificationReceivers {
		if !record.CreatedAt.After(approval.CreatedAt) {
			records = append(records, record)
		}
	}
	if s.notificationDeliveries == nil {
		s.notificationDeliveries = map[string]notificationDeliveryRow{}
	}
	for _, row := range completionDeliveryRows(notification, records, s.now()) {
		if _, exists := s.notificationDeliveries[row.DeliveryID]; !exists {
			s.notificationDeliveries[row.DeliveryID] = row
		}
	}
}

func (s *MemoryStore) CanDeliverApprovalNotification(
	_ context.Context,
	job notifications.LeasedDelivery,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	approval, ok := s.approvals[job.Payload.ApprovalID]
	return ok && approval.OwnerUserID == job.UserID && s.approvalNotificationAllowed(approval), nil
}

func approvalNotificationAllowed(
	ctx context.Context,
	db sqlQueryer,
	owner, approvalID string,
	now time.Time,
) (bool, error) {
	var allowed bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM agent_approvals p JOIN agents a ON a.agent_id=p.request_agent_id
		JOIN nodes n ON n.node_id=p.request_node_id
		JOIN agent_sessions s ON s.agent_id=p.request_agent_id AND s.session_id=p.request_session_id
		WHERE p.approval_id=$1 AND p.owner_user_id=$2 AND p.status='pending'
		AND (p.expires_at IS NULL OR p.expires_at > $3)
		AND a.owner_user_id=p.owner_user_id AND n.owner_user_id=p.owner_user_id
		AND a.node_id=p.request_node_id AND a.deleted_at IS NULL AND n.deleted_at IS NULL
		AND s.transport='manager'
		AND COALESCE(s.metadata->'pax_config'->>'approval_mode','manual') <> 'auto_approve_all'
	)`, approvalID, owner, now).Scan(&allowed)
	return allowed, err
}

func (s *PostgresStore) CanDeliverApprovalNotification(
	ctx context.Context,
	job notifications.LeasedDelivery,
) (bool, error) {
	return approvalNotificationAllowed(ctx, s.db, job.UserID, job.Payload.ApprovalID, s.now())
}

func (s *PostgresStore) enqueueApprovalNotification(
	ctx context.Context,
	tx *sql.Tx,
	approval AgentApproval,
) error {
	if approval.RequestSessionID == "" {
		return nil
	}
	allowed, err := approvalNotificationAllowed(
		ctx,
		tx,
		approval.OwnerUserID,
		approval.ApprovalID,
		s.now(),
	)
	if err != nil || !allowed {
		return err
	}
	notification, err := notificationForApproval(approval)
	if err != nil {
		return err
	}
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM users WHERE user_id=$1 FOR NO KEY UPDATE`, approval.OwnerUserID).Scan(&owner); err != nil {
		return err
	}
	records, err := completionReceivers(
		ctx,
		tx,
		Message{OwnerUserID: approval.OwnerUserID, CreatedAt: approval.CreatedAt},
	)
	if err != nil {
		return err
	}
	for _, row := range completionDeliveryRows(notification, records, s.now()) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_deliveries(delivery_id,notification_id,owner_user_id,receiver_id,receiver_revision,state,next_attempt_at,expires_at,created_at,updated_at,payload_json)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`, row.DeliveryID, row.NotificationID, row.OwnerUserID, row.ReceiverID, row.ReceiverRevision, row.State, row.NextAttemptAt, row.ExpiresAt, row.CreatedAt, row.UpdatedAt, row.PayloadJSON); err != nil {
			return err
		}
	}
	return nil
}
