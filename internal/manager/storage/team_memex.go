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

type teamMemexRunRow struct {
	RunID                string          `gorm:"column:run_id;primaryKey"`
	TeamID               string          `gorm:"column:team_id"`
	RequestedByUserID    string          `gorm:"column:requested_by_user_id"`
	ExecutorType         string          `gorm:"column:executor_type"`
	Status               string          `gorm:"column:status"`
	Partial              bool            `gorm:"column:partial"`
	Constraints          json.RawMessage `gorm:"column:constraints_json"`
	IndexMD              string          `gorm:"column:index_md"`
	ValidationReportJSON json.RawMessage `gorm:"column:validation_report_json"`
	Error                string          `gorm:"column:error"`
	StartedAt            time.Time       `gorm:"column:started_at"`
	CompletedAt          *time.Time      `gorm:"column:completed_at"`
}

func (teamMemexDocumentRow) TableName() string {
	return "team_memex_documents"
}

func (teamMemexRunRow) TableName() string {
	return "team_memex_runs"
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

func teamMemexRunModel(run TeamMemexRun) (*teamMemexRunRow, error) {
	constraints, err := json.Marshal(run.Constraints)
	if err != nil {
		return nil, err
	}
	report, err := teamMemexValidationReportJSON(run.ValidationReport)
	if err != nil {
		return nil, err
	}
	return &teamMemexRunRow{
		RunID:                run.RunID,
		TeamID:               run.TeamID,
		RequestedByUserID:    run.RequestedByUserID,
		ExecutorType:         run.ExecutorType,
		Status:               run.Status,
		Partial:              run.Partial,
		Constraints:          constraints,
		IndexMD:              run.IndexMD,
		ValidationReportJSON: report,
		Error:                run.Error,
		StartedAt:            run.StartedAt,
		CompletedAt:          run.CompletedAt,
	}, nil
}

func teamMemexRunFromModel(row *teamMemexRunRow) (TeamMemexRun, error) {
	if row == nil {
		return TeamMemexRun{}, nil
	}
	constraints := domain.DefaultTeamMemexRunConstraints()
	if len(row.Constraints) > 0 {
		if err := json.Unmarshal(row.Constraints, &constraints); err != nil {
			return TeamMemexRun{}, err
		}
	}
	report, err := teamMemexValidationReportFromJSON(row.ValidationReportJSON)
	if err != nil {
		return TeamMemexRun{}, err
	}
	return TeamMemexRun{
		RunID:             row.RunID,
		TeamID:            row.TeamID,
		RequestedByUserID: row.RequestedByUserID,
		ExecutorType:      row.ExecutorType,
		Status:            row.Status,
		Partial:           row.Partial,
		Constraints:       constraints,
		IndexMD:           row.IndexMD,
		ValidationReport:  report,
		Error:             row.Error,
		StartedAt:         row.StartedAt,
		CompletedAt:       row.CompletedAt,
	}, nil
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

func (s *PostgresStore) CreateTeamMemexRun(
	ctx context.Context,
	principal UserPrincipal,
	run TeamMemexRun,
) (TeamMemexRun, error) {
	member, err := s.activeTeamMember(ctx, s.gormDB, run.TeamID, principal.User.UserID)
	if err != nil {
		return TeamMemexRun{}, err
	}
	if !teamRoleCanManageOwnAgents(member.Role) {
		return TeamMemexRun{}, ErrUnauthorized
	}
	row, err := teamMemexRunModel(run)
	if err != nil {
		return TeamMemexRun{}, err
	}
	if err := s.gormDB.WithContext(ctx).Create(row).Error; err != nil {
		if isUniqueViolation(err) {
			return TeamMemexRun{}, ErrConflict
		}
		return TeamMemexRun{}, err
	}
	return run, nil
}

func (s *PostgresStore) GetTeamMemexRun(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
	runID string,
) (TeamMemexRun, error) {
	if _, err := s.activeTeamMember(ctx, s.gormDB, teamID, principal.User.UserID); err != nil {
		return TeamMemexRun{}, err
	}
	var row teamMemexRunRow
	err := s.gormDB.WithContext(ctx).
		Where("team_id = ? AND run_id = ?", teamID, runID).
		First(&row).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return TeamMemexRun{}, ErrNotFound
		}
		return TeamMemexRun{}, err
	}
	return teamMemexRunFromModel(&row)
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

func (s *MemoryStore) CreateTeamMemexRun(
	ctx context.Context,
	principal UserPrincipal,
	run TeamMemexRun,
) (TeamMemexRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	member, ok := s.activeTeamMemberLocked(run.TeamID, principal.User.UserID)
	if !ok {
		return TeamMemexRun{}, ErrNotFound
	}
	team, ok := s.teams[run.TeamID]
	if !ok || team.Status != domain.TeamStatusActive {
		return TeamMemexRun{}, ErrNotFound
	}
	if !teamRoleCanManageOwnAgents(member.Role) {
		return TeamMemexRun{}, ErrUnauthorized
	}
	if _, ok := s.teamMemexRuns[run.RunID]; ok {
		return TeamMemexRun{}, ErrConflict
	}
	s.teamMemexRuns[run.RunID] = cloneTeamMemexRun(run)
	return cloneTeamMemexRun(run), nil
}

func (s *MemoryStore) GetTeamMemexRun(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
	runID string,
) (TeamMemexRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.activeTeamMemberLocked(teamID, principal.User.UserID); !ok {
		return TeamMemexRun{}, ErrNotFound
	}
	team, ok := s.teams[teamID]
	if !ok || team.Status != domain.TeamStatusActive {
		return TeamMemexRun{}, ErrNotFound
	}
	run, ok := s.teamMemexRuns[runID]
	if !ok || run.TeamID != teamID {
		return TeamMemexRun{}, ErrNotFound
	}
	return cloneTeamMemexRun(run), nil
}

func cloneTeamMemexDocument(doc TeamMemexDocument) TeamMemexDocument {
	doc.Tags = append(json.RawMessage(nil), doc.Tags...)
	return doc
}

func cloneTeamMemexRun(run TeamMemexRun) TeamMemexRun {
	run.Constraints.AllowedOperations = append([]string(nil), run.Constraints.AllowedOperations...)
	if run.ValidationReport != nil {
		report := *run.ValidationReport
		report.Errors = append(
			[]domain.TeamMemexValidationError(nil),
			run.ValidationReport.Errors...)
		report.Constraints.AllowedOperations = append(
			[]string(nil),
			run.ValidationReport.Constraints.AllowedOperations...,
		)
		run.ValidationReport = &report
	}
	return run
}

func teamMemexValidationReportJSON(
	report *domain.TeamMemexValidationReport,
) (json.RawMessage, error) {
	if report == nil {
		return nil, nil
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func teamMemexValidationReportFromJSON(
	raw json.RawMessage,
) (*domain.TeamMemexValidationReport, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var report domain.TeamMemexValidationReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, err
	}
	return &report, nil
}
