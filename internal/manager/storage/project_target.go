package storage

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type projectTargetRow struct {
	TargetID    string    `gorm:"column:target_id;primaryKey"`
	ProjectID   string    `gorm:"column:project_id"`
	AgentID     string    `gorm:"column:agent_id"`
	DisplayName string    `gorm:"column:display_name"`
	Cwd         string    `gorm:"column:cwd"`
	IsDefault   bool      `gorm:"column:is_default"`
	Enabled     bool      `gorm:"column:enabled"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (projectTargetRow) TableName() string {
	return "project_targets"
}

func projectTargetModel(target ProjectTarget) projectTargetRow {
	return projectTargetRow{
		TargetID:    target.TargetID,
		ProjectID:   target.ProjectID,
		AgentID:     target.AgentID,
		DisplayName: target.DisplayName,
		Cwd:         target.Cwd,
		IsDefault:   target.IsDefault,
		Enabled:     target.Enabled,
		CreatedAt:   target.CreatedAt,
		UpdatedAt:   target.UpdatedAt,
	}
}

func projectTargetFromModel(row projectTargetRow) ProjectTarget {
	return ProjectTarget{
		TargetID:    row.TargetID,
		ProjectID:   row.ProjectID,
		AgentID:     row.AgentID,
		DisplayName: row.DisplayName,
		Cwd:         row.Cwd,
		IsDefault:   row.IsDefault,
		Enabled:     row.Enabled,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

func (s *MemoryStore) CreateProjectTarget(
	_ context.Context,
	principal UserPrincipal,
	projectID string,
	req CreateProjectTargetRequest,
) (ProjectTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.validateMemoryTargetProjectLocked(principal.User.UserID, projectID, true); err != nil {
		return ProjectTarget{}, err
	}
	agentID := strings.TrimSpace(req.AgentID)
	agent, ok := s.agents[agentID]
	if !ok || agent.OwnerUserID != principal.User.UserID {
		return ProjectTarget{}, ErrNotFound
	}
	displayName, cwd, err := normalizeProjectTargetFields(req.DisplayName, req.Cwd)
	if err != nil {
		return ProjectTarget{}, err
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if req.IsDefault && !enabled {
		return ProjectTarget{}, ErrConflict
	}
	for _, target := range s.projectTargets {
		if target.ProjectID == projectID && target.AgentID == agentID && target.Cwd == cwd {
			return target, nil
		}
	}
	targetID, err := newSecret("ptgt")
	if err != nil {
		return ProjectTarget{}, err
	}
	now := s.now().UTC()
	if req.IsDefault {
		s.clearMemoryProjectTargetDefaultLocked(projectID, now)
	}
	target := ProjectTarget{
		TargetID:    targetID,
		ProjectID:   projectID,
		AgentID:     agentID,
		DisplayName: displayName,
		Cwd:         cwd,
		IsDefault:   req.IsDefault,
		Enabled:     enabled,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.projectTargets[targetID] = target
	return target, nil
}

func (s *MemoryStore) ListProjectTargets(
	_ context.Context,
	principal UserPrincipal,
	projectID string,
) ([]ProjectTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.validateMemoryTargetProjectLocked(principal.User.UserID, projectID, false); err != nil {
		return nil, err
	}
	targets := make([]ProjectTarget, 0)
	for _, target := range s.projectTargets {
		if target.ProjectID == projectID {
			targets = append(targets, target)
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].CreatedAt.Equal(targets[j].CreatedAt) {
			return targets[i].TargetID < targets[j].TargetID
		}
		return targets[i].CreatedAt.Before(targets[j].CreatedAt)
	})
	return targets, nil
}

func (s *MemoryStore) GetProjectTarget(
	_ context.Context,
	principal UserPrincipal,
	projectID string,
	targetID string,
) (ProjectTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.validateMemoryTargetProjectLocked(principal.User.UserID, projectID, false); err != nil {
		return ProjectTarget{}, err
	}
	target, ok := s.projectTargets[targetID]
	if !ok || target.ProjectID != projectID {
		return ProjectTarget{}, ErrNotFound
	}
	return target, nil
}

func (s *MemoryStore) UpdateProjectTarget(
	_ context.Context,
	principal UserPrincipal,
	projectID string,
	targetID string,
	req UpdateProjectTargetRequest,
) (ProjectTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.validateMemoryTargetProjectLocked(principal.User.UserID, projectID, true); err != nil {
		return ProjectTarget{}, err
	}
	target, ok := s.projectTargets[targetID]
	if !ok || target.ProjectID != projectID {
		return ProjectTarget{}, ErrNotFound
	}
	if req.AgentID != nil {
		agentID := strings.TrimSpace(*req.AgentID)
		agent, found := s.agents[agentID]
		if !found || agent.OwnerUserID != principal.User.UserID {
			return ProjectTarget{}, ErrNotFound
		}
		target.AgentID = agentID
	}
	if req.DisplayName != nil {
		displayName := strings.TrimSpace(*req.DisplayName)
		if displayName == "" {
			return ProjectTarget{}, ErrConflict
		}
		target.DisplayName = displayName
	}
	if req.Cwd != nil {
		cwd := strings.TrimSpace(*req.Cwd)
		if cwd == "" {
			return ProjectTarget{}, ErrConflict
		}
		target.Cwd = cwd
	}
	if req.Enabled != nil {
		target.Enabled = *req.Enabled
	}
	if req.IsDefault != nil {
		target.IsDefault = *req.IsDefault
	}
	if target.IsDefault && !target.Enabled {
		if req.IsDefault != nil && *req.IsDefault {
			return ProjectTarget{}, ErrConflict
		}
		target.IsDefault = false
	}
	now := s.now().UTC()
	if target.IsDefault {
		s.clearMemoryProjectTargetDefaultLocked(projectID, now)
	}
	target.UpdatedAt = now
	s.projectTargets[targetID] = target
	return target, nil
}

func (s *MemoryStore) validateMemoryTargetProjectLocked(
	ownerUserID string,
	projectID string,
	requireActive bool,
) error {
	project, ok := s.projects[projectID]
	if !ok || project.OwnerUserID != ownerUserID {
		return ErrNotFound
	}
	if requireActive && project.ArchivedAt != nil {
		return ErrConflict
	}
	return nil
}

func (s *MemoryStore) clearMemoryProjectTargetDefaultLocked(projectID string, now time.Time) {
	for targetID, target := range s.projectTargets {
		if target.ProjectID == projectID && target.IsDefault {
			target.IsDefault = false
			target.UpdatedAt = now
			s.projectTargets[targetID] = target
		}
	}
}

func (s *PostgresStore) CreateProjectTarget(
	ctx context.Context,
	principal UserPrincipal,
	projectID string,
	req CreateProjectTargetRequest,
) (ProjectTarget, error) {
	displayName, cwd, err := normalizeProjectTargetFields(req.DisplayName, req.Cwd)
	if err != nil {
		return ProjectTarget{}, err
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if req.IsDefault && !enabled {
		return ProjectTarget{}, ErrConflict
	}
	targetID, err := newSecret("ptgt")
	if err != nil {
		return ProjectTarget{}, err
	}
	now := s.now().UTC()
	target := ProjectTarget{
		TargetID:    targetID,
		ProjectID:   projectID,
		AgentID:     strings.TrimSpace(req.AgentID),
		DisplayName: displayName,
		Cwd:         cwd,
		IsDefault:   req.IsDefault,
		Enabled:     enabled,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	err = s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validatePostgresTargetProject(
			tx,
			principal.User.UserID,
			projectID,
			true,
		); err != nil {
			return err
		}
		if err := validatePostgresTargetAgent(
			tx,
			principal.User.UserID,
			target.AgentID,
		); err != nil {
			return err
		}
		var existing projectTargetRow
		result := tx.
			Where(
				"project_id = ? AND agent_id = ? AND cwd = ?",
				projectID,
				target.AgentID,
				target.Cwd,
			).
			Limit(1).
			Find(&existing)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			target = projectTargetFromModel(existing)
			return nil
		}
		if target.IsDefault {
			if err := clearPostgresProjectTargetDefault(tx, projectID, now); err != nil {
				return err
			}
		}
		return tx.Create(projectTargetModel(target)).Error
	})
	return target, mapGormError(err)
}

func (s *PostgresStore) ListProjectTargets(
	ctx context.Context,
	principal UserPrincipal,
	projectID string,
) ([]ProjectTarget, error) {
	if err := validatePostgresTargetProject(
		s.gormDB.WithContext(ctx),
		principal.User.UserID,
		projectID,
		false,
	); err != nil {
		return nil, mapGormError(err)
	}
	var rows []projectTargetRow
	err := s.gormDB.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("created_at ASC, target_id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, mapGormError(err)
	}
	targets := make([]ProjectTarget, 0, len(rows))
	for _, row := range rows {
		targets = append(targets, projectTargetFromModel(row))
	}
	return targets, nil
}

func (s *PostgresStore) GetProjectTarget(
	ctx context.Context,
	principal UserPrincipal,
	projectID string,
	targetID string,
) (ProjectTarget, error) {
	if err := validatePostgresTargetProject(
		s.gormDB.WithContext(ctx),
		principal.User.UserID,
		projectID,
		false,
	); err != nil {
		return ProjectTarget{}, mapGormError(err)
	}
	var row projectTargetRow
	err := s.gormDB.WithContext(ctx).
		Where("project_id = ? AND target_id = ?", projectID, targetID).
		Take(&row).Error
	return projectTargetFromModel(row), mapGormError(err)
}

func (s *PostgresStore) UpdateProjectTarget(
	ctx context.Context,
	principal UserPrincipal,
	projectID string,
	targetID string,
	req UpdateProjectTargetRequest,
) (ProjectTarget, error) {
	var target ProjectTarget
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validatePostgresTargetProject(
			tx,
			principal.User.UserID,
			projectID,
			true,
		); err != nil {
			return err
		}
		var row projectTargetRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND target_id = ?", projectID, targetID).
			Take(&row).Error
		if err != nil {
			return err
		}
		if req.AgentID != nil {
			agentID := strings.TrimSpace(*req.AgentID)
			if err := validatePostgresTargetAgent(
				tx,
				principal.User.UserID,
				agentID,
			); err != nil {
				return err
			}
			row.AgentID = agentID
		}
		if req.DisplayName != nil {
			displayName := strings.TrimSpace(*req.DisplayName)
			if displayName == "" {
				return ErrConflict
			}
			row.DisplayName = displayName
		}
		if req.Cwd != nil {
			cwd := strings.TrimSpace(*req.Cwd)
			if cwd == "" {
				return ErrConflict
			}
			row.Cwd = cwd
		}
		if req.Enabled != nil {
			row.Enabled = *req.Enabled
		}
		if req.IsDefault != nil {
			row.IsDefault = *req.IsDefault
		}
		if row.IsDefault && !row.Enabled {
			if req.IsDefault != nil && *req.IsDefault {
				return ErrConflict
			}
			row.IsDefault = false
		}
		row.UpdatedAt = s.now().UTC()
		if row.IsDefault {
			if err := clearPostgresProjectTargetDefault(tx, projectID, row.UpdatedAt); err != nil {
				return err
			}
		}
		err = tx.Model(&projectTargetRow{}).
			Where("target_id = ?", row.TargetID).
			Updates(map[string]any{
				"agent_id":     row.AgentID,
				"display_name": row.DisplayName,
				"cwd":          row.Cwd,
				"is_default":   row.IsDefault,
				"enabled":      row.Enabled,
				"updated_at":   row.UpdatedAt,
			}).Error
		if err != nil {
			return err
		}
		target = projectTargetFromModel(row)
		return nil
	})
	return target, mapGormError(err)
}

func validatePostgresTargetProject(
	tx *gorm.DB,
	ownerUserID string,
	projectID string,
	requireActive bool,
) error {
	var row projectRow
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("project_id = ? AND owner_user_id = ?", projectID, ownerUserID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if requireActive && row.ArchivedAt != nil {
		return ErrConflict
	}
	return nil
}

func validatePostgresTargetAgent(tx *gorm.DB, ownerUserID string, agentID string) error {
	var count int64
	err := tx.Table("agents").
		Where(
			"agent_id = ? AND owner_user_id = ? AND deleted_at IS NULL",
			agentID,
			ownerUserID,
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

func clearPostgresProjectTargetDefault(tx *gorm.DB, projectID string, now time.Time) error {
	return tx.Model(&projectTargetRow{}).
		Where("project_id = ? AND is_default = ?", projectID, true).
		Updates(map[string]any{
			"is_default": false,
			"updated_at": now,
		}).Error
}

func normalizeProjectTargetFields(displayName string, cwd string) (string, string, error) {
	displayName = strings.TrimSpace(displayName)
	cwd = strings.TrimSpace(cwd)
	if displayName == "" || cwd == "" {
		return "", "", ErrConflict
	}
	return displayName, cwd, nil
}
