package storage

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type envelopeRow struct {
	EnvelopeID      string          `gorm:"column:envelope_id;primaryKey"`
	SenderUserID    string          `gorm:"column:sender_user_id"`
	SenderEmail     string          `gorm:"column:sender_email"`
	RecipientUserID *string         `gorm:"column:recipient_user_id"`
	RecipientEmail  string          `gorm:"column:recipient_email"`
	FromAgentID     string          `gorm:"column:from_agent_id"`
	ToAgentID       string          `gorm:"column:to_agent_id"`
	PayloadType     string          `gorm:"column:payload_type"`
	PayloadJSON     json.RawMessage `gorm:"column:payload_json;type:jsonb"`
	Message         string          `gorm:"column:message"`
	Status          string          `gorm:"column:status"`
	CreatedAt       time.Time       `gorm:"column:created_at"`
	AcceptedAt      *time.Time      `gorm:"column:accepted_at"`
	ArchivedAt      *time.Time      `gorm:"column:archived_at"`
}

func (envelopeRow) TableName() string {
	return "envelopes"
}

func envelopeModel(envelope Envelope) *envelopeRow {
	var recipientUserID *string
	if envelope.RecipientUserID != "" {
		recipientUserID = &envelope.RecipientUserID
	}
	return &envelopeRow{
		EnvelopeID:      envelope.EnvelopeID,
		SenderUserID:    envelope.SenderUserID,
		SenderEmail:     envelope.SenderEmail,
		RecipientUserID: recipientUserID,
		RecipientEmail:  envelope.RecipientEmail,
		FromAgentID:     envelope.FromAgentID,
		ToAgentID:       envelope.ToAgentID,
		PayloadType:     envelope.PayloadType,
		PayloadJSON:     envelope.PayloadJSON,
		Message:         envelope.Message,
		Status:          envelope.Status,
		CreatedAt:       envelope.CreatedAt,
		AcceptedAt:      envelope.AcceptedAt,
		ArchivedAt:      envelope.ArchivedAt,
	}
}

func envelopeFromModel(row *envelopeRow) Envelope {
	if row == nil {
		return Envelope{}
	}
	return Envelope{
		EnvelopeID:      row.EnvelopeID,
		SenderUserID:    row.SenderUserID,
		SenderEmail:     row.SenderEmail,
		RecipientUserID: envelopeRecipientUserID(row.RecipientUserID),
		RecipientEmail:  row.RecipientEmail,
		FromAgentID:     row.FromAgentID,
		ToAgentID:       row.ToAgentID,
		PayloadType:     row.PayloadType,
		PayloadJSON:     row.PayloadJSON,
		Message:         row.Message,
		Status:          row.Status,
		CreatedAt:       row.CreatedAt,
		AcceptedAt:      row.AcceptedAt,
		ArchivedAt:      row.ArchivedAt,
	}
}

func envelopesFromModels(rows []envelopeRow) []Envelope {
	out := make([]Envelope, 0, len(rows))
	for i := range rows {
		out = append(out, envelopeFromModel(&rows[i]))
	}
	return out
}

func (s *PostgresStore) GetEnvelopeAgentRecipient(
	ctx context.Context,
	principal UserPrincipal,
	fromAgentID string,
	toAgentID string,
) (User, error) {
	var user User
	err := s.gormDB.WithContext(ctx).
		Raw(`
			SELECT u.user_id, u.email, u.display_name, u.role, u.created_at
			FROM users u
			JOIN agents target_agent ON target_agent.owner_user_id = u.user_id
			JOIN agents source_agent ON source_agent.agent_id = ?
			WHERE target_agent.agent_id = ?
				AND source_agent.owner_user_id = ?
				AND EXISTS (
					SELECT 1
					FROM team_members sender_tm
					JOIN team_members receiver_tm ON receiver_tm.team_id = sender_tm.team_id
					JOIN teams t ON t.team_id = sender_tm.team_id
					WHERE sender_tm.user_id = ?
						AND receiver_tm.user_id = target_agent.owner_user_id
						AND sender_tm.status = ?
						AND receiver_tm.status = ?
						AND t.status = ?
				)
			LIMIT 1
		`,
			fromAgentID,
			toAgentID,
			principal.User.UserID,
			principal.User.UserID,
			domain.TeamMemberStatusActive,
			domain.TeamMemberStatusActive,
			domain.TeamStatusActive,
		).
		Scan(&user).Error
	if err != nil {
		return User{}, mapGormError(err)
	}
	if user.UserID == "" {
		return User{}, ErrNotFound
	}
	return user, nil
}

func (s *PostgresStore) CreateEnvelope(
	ctx context.Context,
	envelope Envelope,
) (Envelope, error) {
	payload, _, err := postgresSafeJSON(envelope.PayloadJSON)
	if err != nil {
		return Envelope{}, err
	}
	envelope.PayloadJSON = payload
	envelope.Message, _ = postgresSafeText(envelope.Message)
	row := envelopeModel(envelope)
	if err = s.gormDB.WithContext(ctx).Create(row).Error; err != nil {
		return Envelope{}, err
	}
	return envelopeFromModel(row), nil
}

func (s *PostgresStore) ListEnvelopes(
	ctx context.Context,
	filter ListEnvelopesFilter,
) ([]Envelope, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	recipientEmail := normalizeEmail(filter.Principal.User.Email)
	query := s.gormDB.WithContext(ctx).
		Model(&envelopeRow{}).
		Order("created_at DESC").
		Limit(limit)
	switch filter.Direction {
	case domain.EnvelopeDirectionSent:
		query = query.Where("sender_user_id = ?", filter.Principal.User.UserID)
	case "", domain.EnvelopeDirectionReceived:
		query = query.Where(
			"recipient_user_id = ? OR (recipient_user_id IS NULL AND recipient_email = ?)",
			filter.Principal.User.UserID,
			recipientEmail,
		)
	default:
		return []Envelope{}, nil
	}
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if filter.Cursor != "" {
		query = query.Where("envelope_id < ?", filter.Cursor)
	}
	var rows []envelopeRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, mapGormError(err)
	}
	return envelopesFromModels(rows), nil
}

func (s *PostgresStore) GetEnvelope(
	ctx context.Context,
	principal UserPrincipal,
	envelopeID string,
) (Envelope, error) {
	var row envelopeRow
	err := s.readableEnvelopeQuery(ctx, principal).
		Where("envelope_id = ?", envelopeID).
		First(&row).
		Error
	if err != nil {
		return Envelope{}, mapGormError(err)
	}
	return envelopeFromModel(&row), nil
}

func (s *PostgresStore) AcceptEnvelope(
	ctx context.Context,
	principal UserPrincipal,
	envelopeID string,
	acceptedAt time.Time,
) (Envelope, error) {
	return s.updatePendingRecipientEnvelope(ctx, principal, envelopeID, map[string]any{
		"recipient_user_id": principal.User.UserID,
		"status":            domain.EnvelopeStatusAccepted,
		"accepted_at":       acceptedAt,
	})
}

func (s *PostgresStore) ArchiveEnvelope(
	ctx context.Context,
	principal UserPrincipal,
	envelopeID string,
	archivedAt time.Time,
) (Envelope, error) {
	return s.updateRecipientEnvelope(ctx, principal, envelopeID, map[string]any{
		"recipient_user_id": principal.User.UserID,
		"status":            domain.EnvelopeStatusArchived,
		"archived_at":       archivedAt,
	})
}

func (s *PostgresStore) readableEnvelopeQuery(
	ctx context.Context,
	principal UserPrincipal,
) *gorm.DB {
	recipientEmail := normalizeEmail(principal.User.Email)
	return s.gormDB.WithContext(ctx).
		Model(&envelopeRow{}).
		Where(
			"sender_user_id = ? OR recipient_user_id = ? OR (recipient_user_id IS NULL AND recipient_email = ?)",
			principal.User.UserID,
			principal.User.UserID,
			recipientEmail,
		)
}

func (s *PostgresStore) updateRecipientEnvelope(
	ctx context.Context,
	principal UserPrincipal,
	envelopeID string,
	values map[string]any,
) (Envelope, error) {
	recipientEmail := normalizeEmail(principal.User.Email)
	result := s.gormDB.WithContext(ctx).
		Model(&envelopeRow{}).
		Where(
			"envelope_id = ? AND (recipient_user_id = ? OR (recipient_user_id IS NULL AND recipient_email = ?))",
			envelopeID,
			principal.User.UserID,
			recipientEmail,
		).
		Updates(values)
	if result.Error != nil {
		return Envelope{}, mapGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return Envelope{}, ErrNotFound
	}
	return s.GetEnvelope(ctx, principal, envelopeID)
}

func (s *PostgresStore) updatePendingRecipientEnvelope(
	ctx context.Context,
	principal UserPrincipal,
	envelopeID string,
	values map[string]any,
) (Envelope, error) {
	recipientEmail := normalizeEmail(principal.User.Email)
	result := s.gormDB.WithContext(ctx).
		Model(&envelopeRow{}).
		Where(
			"envelope_id = ? AND status = ? AND (recipient_user_id = ? OR (recipient_user_id IS NULL AND recipient_email = ?))",
			envelopeID,
			domain.EnvelopeStatusPending,
			principal.User.UserID,
			recipientEmail,
		).
		Updates(values)
	if result.Error != nil {
		return Envelope{}, mapGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return Envelope{}, ErrNotFound
	}
	return s.GetEnvelope(ctx, principal, envelopeID)
}

func envelopeRecipientUserID(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
