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

func (s *PostgresStore) CreateEnvelope(
	ctx context.Context,
	envelope Envelope,
) (Envelope, error) {
	row := envelopeModel(envelope)
	if err := s.gormDB.WithContext(ctx).Create(row).Error; err != nil {
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
		Where(
			"recipient_user_id = ? OR (recipient_user_id IS NULL AND recipient_email = ?)",
			filter.Principal.User.UserID,
			recipientEmail,
		).
		Order("created_at DESC").
		Limit(limit)
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
	return s.updateRecipientEnvelope(ctx, principal, envelopeID, map[string]any{
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
			"recipient_user_id = ? OR (recipient_user_id IS NULL AND recipient_email = ?)",
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

func envelopeRecipientUserID(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
