package storage

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type teamMemexDocumentRow struct {
	DocumentID string          `gorm:"column:document_id;primaryKey"`
	TeamID     string          `gorm:"column:team_id"`
	Path       string          `gorm:"column:path"`
	Title      string          `gorm:"column:title"`
	Summary    string          `gorm:"column:summary"`
	Tags       json.RawMessage `gorm:"column:tags_json"`
	BodyMD     string          `gorm:"column:body_md"`
	Status     string          `gorm:"column:status"`
	CreatedAt  time.Time       `gorm:"column:created_at"`
	UpdatedAt  time.Time       `gorm:"column:updated_at"`
	ArchivedAt *time.Time      `gorm:"column:archived_at"`
}

func (teamMemexDocumentRow) TableName() string {
	return "team_memex_documents"
}

func teamMemexDocumentFromModel(row *teamMemexDocumentRow) TeamMemexDocument {
	if row == nil {
		return TeamMemexDocument{}
	}
	return TeamMemexDocument{
		DocumentID: row.DocumentID,
		TeamID:     row.TeamID,
		Path:       row.Path,
		Title:      row.Title,
		Summary:    row.Summary,
		Tags:       jsonDefault(row.Tags, "[]"),
		BodyMD:     row.BodyMD,
		Status:     row.Status,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
		ArchivedAt: row.ArchivedAt,
	}
}

func (s *PostgresStore) ListTeamMemexDocuments(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
) ([]TeamMemexDocument, error) {
	if err := s.ensureActiveTeamMember(ctx, principal, teamID); err != nil {
		return nil, err
	}
	var rows []teamMemexDocumentRow
	err := s.gormDB.WithContext(ctx).
		Where("team_id = ? AND status = ?", teamID, domain.TeamMemexDocumentStatusActive).
		Order("path ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]TeamMemexDocument, 0, len(rows))
	for i := range rows {
		out = append(out, teamMemexDocumentFromModel(&rows[i]))
	}
	return out, nil
}

func (s *PostgresStore) GetTeamMemexDocument(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
	path string,
) (TeamMemexDocument, error) {
	if err := s.ensureActiveTeamMember(ctx, principal, teamID); err != nil {
		return TeamMemexDocument{}, err
	}
	var row teamMemexDocumentRow
	err := s.gormDB.WithContext(ctx).
		Where("team_id = ? AND path = ? AND status = ?", teamID, path, domain.TeamMemexDocumentStatusActive).
		First(&row).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return TeamMemexDocument{}, ErrNotFound
		}
		return TeamMemexDocument{}, err
	}
	return teamMemexDocumentFromModel(&row), nil
}

func (s *PostgresStore) ensureActiveTeamMember(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
) error {
	var count int64
	err := s.gormDB.WithContext(ctx).
		Table("team_members AS tm").
		Joins("JOIN teams t ON t.team_id = tm.team_id").
		Where(
			"tm.team_id = ? AND tm.user_id = ? AND tm.status = ? AND t.status = ?",
			teamID,
			principal.User.UserID,
			domain.TeamMemberStatusActive,
			domain.TeamStatusActive,
		).
		Count(&count).Error
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MemoryStore) ListTeamMemexDocuments(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
) ([]TeamMemexDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.activeTeamMemberLocked(teamID, principal.User.UserID); !ok {
		return nil, ErrNotFound
	}
	team, ok := s.teams[teamID]
	if !ok || team.Status != domain.TeamStatusActive {
		return nil, ErrNotFound
	}
	out := make([]TeamMemexDocument, 0)
	for _, doc := range s.teamMemexDocuments {
		if doc.TeamID == teamID && doc.Status == domain.TeamMemexDocumentStatusActive {
			out = append(out, cloneTeamMemexDocument(doc))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Path < out[j].Path
	})
	return out, nil
}

func (s *MemoryStore) GetTeamMemexDocument(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
	path string,
) (TeamMemexDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.activeTeamMemberLocked(teamID, principal.User.UserID); !ok {
		return TeamMemexDocument{}, ErrNotFound
	}
	team, ok := s.teams[teamID]
	if !ok || team.Status != domain.TeamStatusActive {
		return TeamMemexDocument{}, ErrNotFound
	}
	doc, ok := s.teamMemexDocuments[teamMemexDocumentKey{TeamID: teamID, Path: path}]
	if !ok || doc.Status != domain.TeamMemexDocumentStatusActive {
		return TeamMemexDocument{}, ErrNotFound
	}
	return cloneTeamMemexDocument(doc), nil
}

func cloneTeamMemexDocument(doc TeamMemexDocument) TeamMemexDocument {
	doc.Tags = append(json.RawMessage(nil), doc.Tags...)
	return doc
}
