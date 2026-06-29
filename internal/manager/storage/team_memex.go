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

type teamMemexRunAttemptRow struct {
	AttemptID            string          `gorm:"column:attempt_id;primaryKey"`
	RunID                string          `gorm:"column:run_id"`
	TeamID               string          `gorm:"column:team_id"`
	AttemptNumber        int             `gorm:"column:attempt_number"`
	ExecutorType         string          `gorm:"column:executor_type"`
	Status               string          `gorm:"column:status"`
	Manifest             json.RawMessage `gorm:"column:manifest_json"`
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

func (teamMemexRunAttemptRow) TableName() string {
	return "team_memex_run_attempts"
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

func teamMemexRunAttemptModel(attempt TeamMemexRunAttempt) (*teamMemexRunAttemptRow, error) {
	manifest, err := json.Marshal(attempt.Manifest)
	if err != nil {
		return nil, err
	}
	report, err := teamMemexValidationReportJSON(attempt.ValidationReport)
	if err != nil {
		return nil, err
	}
	return &teamMemexRunAttemptRow{
		AttemptID:            attempt.AttemptID,
		RunID:                attempt.RunID,
		TeamID:               attempt.TeamID,
		AttemptNumber:        attempt.AttemptNumber,
		ExecutorType:         attempt.ExecutorType,
		Status:               attempt.Status,
		Manifest:             manifest,
		ValidationReportJSON: report,
		Error:                attempt.Error,
		StartedAt:            attempt.StartedAt,
		CompletedAt:          attempt.CompletedAt,
	}, nil
}

func teamMemexRunAttemptFromModel(
	row *teamMemexRunAttemptRow,
) (TeamMemexRunAttempt, error) {
	if row == nil {
		return TeamMemexRunAttempt{}, nil
	}
	var manifest domain.TeamMemexManifest
	if len(row.Manifest) > 0 {
		if err := json.Unmarshal(row.Manifest, &manifest); err != nil {
			return TeamMemexRunAttempt{}, err
		}
	}
	report, err := teamMemexValidationReportFromJSON(row.ValidationReportJSON)
	if err != nil {
		return TeamMemexRunAttempt{}, err
	}
	return TeamMemexRunAttempt{
		AttemptID:        row.AttemptID,
		RunID:            row.RunID,
		TeamID:           row.TeamID,
		AttemptNumber:    row.AttemptNumber,
		ExecutorType:     row.ExecutorType,
		Status:           row.Status,
		Manifest:         manifest,
		ValidationReport: report,
		Error:            row.Error,
		StartedAt:        row.StartedAt,
		CompletedAt:      row.CompletedAt,
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

func (s *PostgresStore) ListTeamMemexDocumentPaths(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
) ([]string, error) {
	if err := s.ensureActiveTeamMember(ctx, principal, teamID); err != nil {
		return nil, err
	}
	var paths []string
	err := s.gormDB.WithContext(ctx).
		Model(&teamMemexDocumentRow{}).
		Where("team_id = ?", teamID).
		Order("path ASC").
		Pluck("path", &paths).Error
	if err != nil {
		return nil, err
	}
	return paths, nil
}

func (s *PostgresStore) AuthorizeTeamMemexRun(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
) error {
	member, err := s.activeTeamMember(ctx, s.gormDB, teamID, principal.User.UserID)
	if err != nil {
		return err
	}
	if !teamRoleCanManageOwnAgents(member.Role) {
		return ErrUnauthorized
	}
	return nil
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
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		member, err := s.activeTeamMember(ctx, tx, run.TeamID, principal.User.UserID)
		if err != nil {
			return err
		}
		if !teamRoleCanManageOwnAgents(member.Role) {
			return ErrUnauthorized
		}
		return createPostgresTeamMemexRun(ctx, tx, run)
	})
	if err != nil {
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
	run, err := teamMemexRunFromModel(&row)
	if err != nil {
		return TeamMemexRun{}, err
	}
	attempts, err := listPostgresTeamMemexRunAttempts(ctx, s.gormDB, teamID, runID)
	if err != nil {
		return TeamMemexRun{}, err
	}
	run.Attempts = attempts
	return run, nil
}

func (s *PostgresStore) PublishTeamMemexRun(
	ctx context.Context,
	principal UserPrincipal,
	run TeamMemexRun,
	operations []TeamMemexDocumentOperation,
	now time.Time,
) (TeamMemexRun, error) {
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		member, err := s.activeTeamMember(ctx, tx, run.TeamID, principal.User.UserID)
		if err != nil {
			return err
		}
		if !teamRoleCanManageOwnAgents(member.Role) {
			return ErrUnauthorized
		}
		if err := createPostgresTeamMemexRun(ctx, tx, run); err != nil {
			return err
		}
		for _, operation := range operations {
			if err := applyPostgresTeamMemexOperation(ctx, tx, run.TeamID, operation, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return TeamMemexRun{}, err
	}
	return run, nil
}

func createPostgresTeamMemexRun(
	ctx context.Context,
	tx *gorm.DB,
	run TeamMemexRun,
) error {
	row, err := teamMemexRunModel(run)
	if err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Create(row).Error; err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return err
	}
	for _, attempt := range run.Attempts {
		row, err := teamMemexRunAttemptModel(attempt)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Create(row).Error; err != nil {
			if isUniqueViolation(err) {
				return ErrConflict
			}
			return err
		}
	}
	return nil
}

func listPostgresTeamMemexRunAttempts(
	ctx context.Context,
	db *gorm.DB,
	teamID string,
	runID string,
) ([]TeamMemexRunAttempt, error) {
	var rows []teamMemexRunAttemptRow
	err := db.WithContext(ctx).
		Where("team_id = ? AND run_id = ?", teamID, runID).
		Order("attempt_number ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	attempts := make([]TeamMemexRunAttempt, 0, len(rows))
	for i := range rows {
		attempt, err := teamMemexRunAttemptFromModel(&rows[i])
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, nil
}

func applyPostgresTeamMemexOperation(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	operation TeamMemexDocumentOperation,
	now time.Time,
) error {
	switch operation.Operation {
	case domain.TeamMemexOperationNoOp:
		return nil
	case domain.TeamMemexOperationCreateDoc:
		var existing teamMemexDocumentRow
		err := tx.WithContext(ctx).
			Where("team_id = ? AND path = ?", teamID, operation.Path).
			First(&existing).Error
		if err == nil {
			return ErrConflict
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		row := teamMemexDocumentRow{
			DocumentID: operation.DocumentID,
			TeamID:     teamID,
			Path:       operation.Path,
			Title:      operation.Title,
			Summary:    operation.Summary,
			Tags:       jsonDefault(operation.Tags, "[]"),
			BodyMD:     operation.BodyMD,
			Status:     domain.TeamMemexDocumentStatusActive,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
			if isUniqueViolation(err) {
				return ErrConflict
			}
			return err
		}
		return nil
	case domain.TeamMemexOperationUpdateDoc:
		result := tx.WithContext(ctx).
			Model(&teamMemexDocumentRow{}).
			Where(
				"team_id = ? AND path = ? AND status = ?",
				teamID,
				operation.Path,
				domain.TeamMemexDocumentStatusActive,
			).
			Updates(map[string]any{
				"title":      operation.Title,
				"summary":    operation.Summary,
				"tags_json":  jsonDefault(operation.Tags, "[]"),
				"body_md":    operation.BodyMD,
				"updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	case domain.TeamMemexOperationArchiveDoc:
		result := tx.WithContext(ctx).
			Model(&teamMemexDocumentRow{}).
			Where(
				"team_id = ? AND path = ? AND status = ?",
				teamID,
				operation.Path,
				domain.TeamMemexDocumentStatusActive,
			).
			Updates(map[string]any{
				"status":      domain.TeamMemexDocumentStatusArchived,
				"updated_at":  now,
				"archived_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	default:
		return ErrConflict
	}
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

func (s *MemoryStore) ListTeamMemexDocumentPaths(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.activeTeamMemberLocked(teamID, principal.User.UserID); !ok {
		return nil, ErrNotFound
	}
	team, ok := s.teams[teamID]
	if !ok || team.Status != domain.TeamStatusActive {
		return nil, ErrNotFound
	}
	paths := make([]string, 0)
	for key := range s.teamMemexDocuments {
		if key.TeamID == teamID {
			paths = append(paths, key.Path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func (s *MemoryStore) AuthorizeTeamMemexRun(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	member, ok := s.activeTeamMemberLocked(teamID, principal.User.UserID)
	if !ok {
		return ErrNotFound
	}
	team, ok := s.teams[teamID]
	if !ok || team.Status != domain.TeamStatusActive {
		return ErrNotFound
	}
	if !teamRoleCanManageOwnAgents(member.Role) {
		return ErrUnauthorized
	}
	return nil
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

func (s *MemoryStore) PublishTeamMemexRun(
	ctx context.Context,
	principal UserPrincipal,
	run TeamMemexRun,
	operations []TeamMemexDocumentOperation,
	now time.Time,
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
	nextDocs := make(map[teamMemexDocumentKey]TeamMemexDocument, len(s.teamMemexDocuments))
	for key, document := range s.teamMemexDocuments {
		nextDocs[key] = cloneTeamMemexDocument(document)
	}
	for _, operation := range operations {
		if err := applyMemoryTeamMemexOperation(nextDocs, run.TeamID, operation, now); err != nil {
			return TeamMemexRun{}, err
		}
	}
	s.teamMemexDocuments = nextDocs
	s.teamMemexRuns[run.RunID] = cloneTeamMemexRun(run)
	return cloneTeamMemexRun(run), nil
}

func applyMemoryTeamMemexOperation(
	documents map[teamMemexDocumentKey]TeamMemexDocument,
	teamID string,
	operation TeamMemexDocumentOperation,
	now time.Time,
) error {
	key := teamMemexDocumentKey{TeamID: teamID, Path: operation.Path}
	switch operation.Operation {
	case domain.TeamMemexOperationNoOp:
		return nil
	case domain.TeamMemexOperationCreateDoc:
		if _, ok := documents[key]; ok {
			return ErrConflict
		}
		documents[key] = TeamMemexDocument{
			DocumentID: operation.DocumentID,
			TeamID:     teamID,
			Path:       operation.Path,
			Title:      operation.Title,
			Summary:    operation.Summary,
			Tags:       jsonDefault(operation.Tags, "[]"),
			BodyMD:     operation.BodyMD,
			Status:     domain.TeamMemexDocumentStatusActive,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		return nil
	case domain.TeamMemexOperationUpdateDoc:
		document, ok := documents[key]
		if !ok || document.Status != domain.TeamMemexDocumentStatusActive {
			return ErrNotFound
		}
		document.Title = operation.Title
		document.Summary = operation.Summary
		document.Tags = jsonDefault(operation.Tags, "[]")
		document.BodyMD = operation.BodyMD
		document.UpdatedAt = now
		documents[key] = document
		return nil
	case domain.TeamMemexOperationArchiveDoc:
		document, ok := documents[key]
		if !ok || document.Status != domain.TeamMemexDocumentStatusActive {
			return ErrNotFound
		}
		document.Status = domain.TeamMemexDocumentStatusArchived
		document.UpdatedAt = now
		document.ArchivedAt = &now
		documents[key] = document
		return nil
	default:
		return ErrConflict
	}
}

func cloneTeamMemexDocument(doc TeamMemexDocument) TeamMemexDocument {
	doc.Tags = append(json.RawMessage(nil), doc.Tags...)
	return doc
}

func cloneTeamMemexRun(run TeamMemexRun) TeamMemexRun {
	run.Constraints.AllowedOperations = append([]string(nil), run.Constraints.AllowedOperations...)
	if run.ValidationReport != nil {
		run.ValidationReport = cloneTeamMemexValidationReport(run.ValidationReport)
	}
	run.Attempts = cloneTeamMemexRunAttempts(run.Attempts)
	return run
}

func cloneTeamMemexRunAttempts(
	attempts []TeamMemexRunAttempt,
) []TeamMemexRunAttempt {
	if len(attempts) == 0 {
		return nil
	}
	out := make([]TeamMemexRunAttempt, 0, len(attempts))
	for _, attempt := range attempts {
		out = append(out, cloneTeamMemexRunAttempt(attempt))
	}
	return out
}

func cloneTeamMemexRunAttempt(attempt TeamMemexRunAttempt) TeamMemexRunAttempt {
	attempt.Manifest = cloneTeamMemexManifest(attempt.Manifest)
	attempt.ValidationReport = cloneTeamMemexValidationReport(attempt.ValidationReport)
	return attempt
}

func cloneTeamMemexManifest(manifest TeamMemexManifest) TeamMemexManifest {
	if len(manifest.Operations) == 0 {
		return TeamMemexManifest{}
	}
	operations := make([]domain.TeamMemexManifestOperation, 0, len(manifest.Operations))
	for _, operation := range manifest.Operations {
		operation.Tags = append(json.RawMessage(nil), operation.Tags...)
		operations = append(operations, operation)
	}
	return TeamMemexManifest{Operations: operations}
}

func cloneTeamMemexValidationReport(
	report *domain.TeamMemexValidationReport,
) *domain.TeamMemexValidationReport {
	if report == nil {
		return nil
	}
	cloned := *report
	cloned.Errors = append([]domain.TeamMemexValidationError(nil), report.Errors...)
	cloned.Constraints.AllowedOperations = append(
		[]string(nil),
		report.Constraints.AllowedOperations...,
	)
	return &cloned
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
