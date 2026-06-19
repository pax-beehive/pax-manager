package storage

import (
	"context"
	"time"

	dbmodel "github.com/pax-beehive/pax-manager/internal/manager/storage/dal/model"
)

func knowledgeCapsuleModel(capsule KnowledgeCapsule) *dbmodel.KnowledgeCapsule {
	return &dbmodel.KnowledgeCapsule{
		CapsuleID:              capsule.CapsuleID,
		OwnerUserID:            capsule.OwnerUserID,
		SourceSessionID:        capsule.SourceSessionID,
		SourceAgentID:          capsule.SourceAgentID,
		SourceNodeID:           capsule.SourceNodeID,
		CreatedByUserID:        capsule.CreatedByUserID,
		Keyword:                capsule.Keyword,
		Title:                  capsule.Title,
		Summary:                capsule.Summary,
		Content:                capsule.Content,
		SuggestedSkillsJSON:    rawJSONPtr(capsule.SuggestedSkills),
		ReferencesJSON:         rawJSONPtr(capsule.References),
		OpenQuestionsJSON:      rawJSONPtr(capsule.OpenQuestions),
		RisksJSON:              rawJSONPtr(capsule.Risks),
		RedactionsJSON:         rawJSONPtr(capsule.Redactions),
		Status:                 stringPtr(capsule.Status),
		Truncated:              capsule.Truncated,
		OriginalEstimatedChars: capsule.OriginalEstimatedChars,
		CreatedAt:              &capsule.CreatedAt,
		ArchivedAt:             capsule.ArchivedAt,
	}
}

func knowledgeCapsuleFromModel(row *dbmodel.KnowledgeCapsule) KnowledgeCapsule {
	if row == nil {
		return KnowledgeCapsule{}
	}
	return KnowledgeCapsule{
		CapsuleID:              row.CapsuleID,
		OwnerUserID:            row.OwnerUserID,
		SourceSessionID:        row.SourceSessionID,
		SourceAgentID:          row.SourceAgentID,
		SourceNodeID:           row.SourceNodeID,
		CreatedByUserID:        row.CreatedByUserID,
		Keyword:                row.Keyword,
		Title:                  row.Title,
		Summary:                row.Summary,
		Content:                row.Content,
		SuggestedSkills:        rawJSONArrayValue(row.SuggestedSkillsJSON),
		References:             rawJSONArrayValue(row.ReferencesJSON),
		OpenQuestions:          rawJSONArrayValue(row.OpenQuestionsJSON),
		Risks:                  rawJSONArrayValue(row.RisksJSON),
		Redactions:             rawJSONArrayValue(row.RedactionsJSON),
		Status:                 stringValue(row.Status),
		Truncated:              row.Truncated,
		OriginalEstimatedChars: row.OriginalEstimatedChars,
		CreatedAt:              timeValue(row.CreatedAt),
		ArchivedAt:             row.ArchivedAt,
	}
}

func knowledgeCapsulesFromModels(rows []*dbmodel.KnowledgeCapsule) []KnowledgeCapsule {
	out := make([]KnowledgeCapsule, 0, len(rows))
	for _, row := range rows {
		out = append(out, knowledgeCapsuleFromModel(row))
	}
	return out
}

func knowledgeInjectionModel(
	injection SessionKnowledgeInjection,
) *dbmodel.SessionKnowledgeInjection {
	return &dbmodel.SessionKnowledgeInjection{
		InjectionID:         injection.InjectionID,
		OwnerUserID:         injection.OwnerUserID,
		CapsuleID:           injection.CapsuleID,
		TargetSessionID:     injection.TargetSessionID,
		TargetAgentID:       injection.TargetAgentID,
		TargetNodeID:        injection.TargetNodeID,
		CreatedByUserID:     injection.CreatedByUserID,
		DeliveredAsUserID:   injection.DeliveredAsUserID,
		DeliveryMethod:      stringPtr(injection.DeliveryMethod),
		DeliveryMessageID:   injection.DeliveryMessageID,
		DeliveryMessageType: stringPtr(injection.DeliveryMessageType),
		Status:              stringPtr(injection.Status),
		CreatedAt:           &injection.CreatedAt,
		DeliveredAt:         injection.DeliveredAt,
		FailedAt:            injection.FailedAt,
		RevokedAt:           injection.RevokedAt,
		Error:               injection.Error,
	}
}

func knowledgeInjectionFromModel(
	row *dbmodel.SessionKnowledgeInjection,
) SessionKnowledgeInjection {
	if row == nil {
		return SessionKnowledgeInjection{}
	}
	return SessionKnowledgeInjection{
		InjectionID:         row.InjectionID,
		OwnerUserID:         row.OwnerUserID,
		CapsuleID:           row.CapsuleID,
		TargetSessionID:     row.TargetSessionID,
		TargetAgentID:       row.TargetAgentID,
		TargetNodeID:        row.TargetNodeID,
		CreatedByUserID:     row.CreatedByUserID,
		DeliveredAsUserID:   row.DeliveredAsUserID,
		DeliveryMethod:      stringValue(row.DeliveryMethod),
		DeliveryMessageID:   row.DeliveryMessageID,
		DeliveryMessageType: stringValue(row.DeliveryMessageType),
		Status:              stringValue(row.Status),
		CreatedAt:           timeValue(row.CreatedAt),
		DeliveredAt:         row.DeliveredAt,
		FailedAt:            row.FailedAt,
		RevokedAt:           row.RevokedAt,
		Error:               row.Error,
	}
}

func knowledgeInjectionsFromModels(
	rows []*dbmodel.SessionKnowledgeInjection,
) []SessionKnowledgeInjection {
	out := make([]SessionKnowledgeInjection, 0, len(rows))
	for _, row := range rows {
		out = append(out, knowledgeInjectionFromModel(row))
	}
	return out
}

func (s *PostgresStore) CreateKnowledgeCapsule(
	ctx context.Context,
	capsule KnowledgeCapsule,
) (KnowledgeCapsule, error) {
	row := knowledgeCapsuleModel(capsule)
	if err := s.q.KnowledgeCapsule.WithContext(ctx).Create(row); err != nil {
		return KnowledgeCapsule{}, err
	}
	return knowledgeCapsuleFromModel(row), nil
}

func (s *PostgresStore) ListKnowledgeCapsules(
	ctx context.Context,
	filter ListKnowledgeCapsulesFilter,
) ([]KnowledgeCapsule, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	k := s.q.KnowledgeCapsule
	query := k.WithContext(ctx).
		Where(k.OwnerUserID.Eq(filter.Principal.User.UserID)).
		Order(k.CreatedAt.Desc()).
		Limit(limit)
	if filter.Status != "" {
		query = query.Where(k.Status.Eq(filter.Status))
	}
	if filter.Keyword != "" {
		query = query.Where(k.Keyword.Eq(filter.Keyword))
	}
	if filter.SourceSessionID != "" {
		query = query.Where(k.SourceSessionID.Eq(filter.SourceSessionID))
	}
	if filter.Cursor != "" {
		query = query.Where(k.CapsuleID.Lt(filter.Cursor))
	}
	rows, err := query.Find()
	if err != nil {
		return nil, mapGormError(err)
	}
	return knowledgeCapsulesFromModels(rows), nil
}

func (s *PostgresStore) GetKnowledgeCapsule(
	ctx context.Context,
	principal UserPrincipal,
	capsuleID string,
) (KnowledgeCapsule, error) {
	k := s.q.KnowledgeCapsule
	row, err := k.WithContext(ctx).
		Where(k.CapsuleID.Eq(capsuleID), k.OwnerUserID.Eq(principal.User.UserID)).
		First()
	if err != nil {
		return KnowledgeCapsule{}, mapGormError(err)
	}
	return knowledgeCapsuleFromModel(row), nil
}

func (s *PostgresStore) ArchiveKnowledgeCapsule(
	ctx context.Context,
	principal UserPrincipal,
	capsuleID string,
	archivedAt time.Time,
) (KnowledgeCapsule, error) {
	k := s.q.KnowledgeCapsule
	_, err := k.WithContext(ctx).
		Where(k.CapsuleID.Eq(capsuleID), k.OwnerUserID.Eq(principal.User.UserID)).
		UpdateSimple(
			k.Status.Value("archived"),
			k.ArchivedAt.Value(archivedAt),
		)
	if err != nil {
		return KnowledgeCapsule{}, mapGormError(err)
	}
	return s.GetKnowledgeCapsule(ctx, principal, capsuleID)
}

func (s *PostgresStore) CreateKnowledgeInjection(
	ctx context.Context,
	injection SessionKnowledgeInjection,
) (SessionKnowledgeInjection, error) {
	row := knowledgeInjectionModel(injection)
	if err := s.q.SessionKnowledgeInjection.WithContext(ctx).Create(row); err != nil {
		return SessionKnowledgeInjection{}, err
	}
	return knowledgeInjectionFromModel(row), nil
}

func (s *PostgresStore) ListKnowledgeInjections(
	ctx context.Context,
	filter ListKnowledgeInjectionsFilter,
) ([]SessionKnowledgeInjection, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	i := s.q.SessionKnowledgeInjection
	query := i.WithContext(ctx).
		Where(i.OwnerUserID.Eq(filter.Principal.User.UserID)).
		Order(i.CreatedAt.Desc()).
		Limit(limit)
	if filter.TargetSessionID != "" {
		query = query.Where(i.TargetSessionID.Eq(filter.TargetSessionID))
	}
	if filter.Cursor != "" {
		query = query.Where(i.InjectionID.Lt(filter.Cursor))
	}
	rows, err := query.Find()
	if err != nil {
		return nil, mapGormError(err)
	}
	return knowledgeInjectionsFromModels(rows), nil
}
