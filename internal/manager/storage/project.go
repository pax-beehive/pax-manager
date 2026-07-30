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

type projectRow struct {
	ProjectID       string     `gorm:"column:project_id;primaryKey"`
	OwnerUserID     string     `gorm:"column:owner_user_id"`
	DisplayName     string     `gorm:"column:display_name"`
	ParentProjectID *string    `gorm:"column:parent_project_id"`
	ArchivedAt      *time.Time `gorm:"column:archived_at"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

func (projectRow) TableName() string {
	return "projects"
}

func projectModel(project Project) projectRow {
	return projectRow{
		ProjectID:       project.ProjectID,
		OwnerUserID:     project.OwnerUserID,
		DisplayName:     project.DisplayName,
		ParentProjectID: stringPtrOrNil(project.ParentProjectID),
		ArchivedAt:      project.ArchivedAt,
		CreatedAt:       project.CreatedAt,
		UpdatedAt:       project.UpdatedAt,
	}
}

func projectFromModel(row projectRow) Project {
	return Project{
		ProjectID:       row.ProjectID,
		OwnerUserID:     row.OwnerUserID,
		DisplayName:     row.DisplayName,
		ParentProjectID: stringFromPtr(row.ParentProjectID),
		ArchivedAt:      row.ArchivedAt,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func (s *MemoryStore) CreateProject(
	_ context.Context,
	principal UserPrincipal,
	req CreateProjectRequest,
) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		return Project{}, ErrConflict
	}
	ownerUserID := principal.User.UserID
	if req.ParentProjectID != "" {
		parent, ok := s.projects[req.ParentProjectID]
		if !ok || parent.OwnerUserID != ownerUserID || parent.ArchivedAt != nil {
			return Project{}, ErrNotFound
		}
	}
	projectID, err := newSecret("proj")
	if err != nil {
		return Project{}, err
	}
	now := s.now().UTC()
	project := Project{
		ProjectID:       projectID,
		OwnerUserID:     ownerUserID,
		DisplayName:     displayName,
		ParentProjectID: req.ParentProjectID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	s.projects[project.ProjectID] = project
	return project, nil
}

func (s *MemoryStore) ListProjects(
	_ context.Context,
	principal UserPrincipal,
	filter ListProjectsFilter,
) ([]Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	projects := make([]Project, 0)
	for _, project := range s.projects {
		if project.OwnerUserID != principal.User.UserID {
			continue
		}
		if !filter.IncludeArchived && project.ArchivedAt != nil {
			continue
		}
		projects = append(projects, project)
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].CreatedAt.Equal(projects[j].CreatedAt) {
			return projects[i].ProjectID < projects[j].ProjectID
		}
		return projects[i].CreatedAt.Before(projects[j].CreatedAt)
	})
	return projects, nil
}

func (s *MemoryStore) GetProject(
	_ context.Context,
	principal UserPrincipal,
	projectID string,
) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	project, ok := s.projects[projectID]
	if !ok || project.OwnerUserID != principal.User.UserID {
		return Project{}, ErrNotFound
	}
	return project, nil
}

func (s *MemoryStore) UpdateProject(
	_ context.Context,
	principal UserPrincipal,
	projectID string,
	req UpdateProjectRequest,
) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	project, ok := s.projects[projectID]
	if !ok || project.OwnerUserID != principal.User.UserID {
		return Project{}, ErrNotFound
	}
	if project.ArchivedAt != nil {
		return Project{}, ErrConflict
	}
	if req.DisplayName != nil {
		displayName := strings.TrimSpace(*req.DisplayName)
		if displayName == "" {
			return Project{}, ErrConflict
		}
		project.DisplayName = displayName
	}
	if req.ParentProjectID != nil {
		parentProjectID := *req.ParentProjectID
		if err := s.validateMemoryProjectParentLocked(
			principal.User.UserID,
			projectID,
			parentProjectID,
		); err != nil {
			return Project{}, err
		}
		project.ParentProjectID = parentProjectID
	}
	project.UpdatedAt = s.now().UTC()
	s.projects[projectID] = project
	return project, nil
}

func (s *MemoryStore) ArchiveProject(
	_ context.Context,
	principal UserPrincipal,
	projectID string,
) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	project, ok := s.projects[projectID]
	if !ok || project.OwnerUserID != principal.User.UserID {
		return Project{}, ErrNotFound
	}
	if project.ArchivedAt == nil {
		now := s.now().UTC()
		project.ArchivedAt = &now
		project.UpdatedAt = now
		s.projects[projectID] = project
	}
	return project, nil
}

func (s *MemoryStore) validateMemoryProjectParentLocked(
	ownerUserID string,
	projectID string,
	parentProjectID string,
) error {
	if parentProjectID == "" {
		return nil
	}
	currentID := parentProjectID
	for currentID != "" {
		if currentID == projectID {
			return ErrConflict
		}
		current, ok := s.projects[currentID]
		if !ok || current.OwnerUserID != ownerUserID {
			return ErrNotFound
		}
		if currentID == parentProjectID && current.ArchivedAt != nil {
			return ErrNotFound
		}
		currentID = current.ParentProjectID
	}
	return nil
}

func (s *PostgresStore) CreateProject(
	ctx context.Context,
	principal UserPrincipal,
	req CreateProjectRequest,
) (Project, error) {
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		return Project{}, ErrConflict
	}
	projectID, err := newSecret("proj")
	if err != nil {
		return Project{}, err
	}
	now := s.now().UTC()
	project := Project{
		ProjectID:       projectID,
		OwnerUserID:     principal.User.UserID,
		DisplayName:     displayName,
		ParentProjectID: req.ParentProjectID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	err = s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validatePostgresProjectParent(
			tx,
			project.OwnerUserID,
			project.ProjectID,
			project.ParentProjectID,
		); err != nil {
			return err
		}
		return tx.Create(projectModel(project)).Error
	})
	return project, mapGormError(err)
}

func (s *PostgresStore) ListProjects(
	ctx context.Context,
	principal UserPrincipal,
	filter ListProjectsFilter,
) ([]Project, error) {
	query := s.gormDB.WithContext(ctx).
		Where("owner_user_id = ?", principal.User.UserID).
		Order("created_at ASC, project_id ASC")
	if !filter.IncludeArchived {
		query = query.Where("archived_at IS NULL")
	}
	var rows []projectRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, mapGormError(err)
	}
	projects := make([]Project, 0, len(rows))
	for _, row := range rows {
		projects = append(projects, projectFromModel(row))
	}
	return projects, nil
}

func (s *PostgresStore) GetProject(
	ctx context.Context,
	principal UserPrincipal,
	projectID string,
) (Project, error) {
	var row projectRow
	err := s.gormDB.WithContext(ctx).
		Where("project_id = ? AND owner_user_id = ?", projectID, principal.User.UserID).
		Take(&row).Error
	return projectFromModel(row), mapGormError(err)
}

func (s *PostgresStore) UpdateProject(
	ctx context.Context,
	principal UserPrincipal,
	projectID string,
	req UpdateProjectRequest,
) (Project, error) {
	var project Project
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row projectRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND owner_user_id = ?", projectID, principal.User.UserID).
			Take(&row).Error
		if err != nil {
			return err
		}
		if row.ArchivedAt != nil {
			return ErrConflict
		}
		if req.DisplayName != nil {
			displayName := strings.TrimSpace(*req.DisplayName)
			if displayName == "" {
				return ErrConflict
			}
			row.DisplayName = displayName
		}
		if req.ParentProjectID != nil {
			if err := validatePostgresProjectParent(
				tx,
				principal.User.UserID,
				projectID,
				*req.ParentProjectID,
			); err != nil {
				return err
			}
			row.ParentProjectID = stringPtrOrNil(*req.ParentProjectID)
		}
		row.UpdatedAt = s.now().UTC()
		if err := tx.Model(&projectRow{}).
			Where("project_id = ?", row.ProjectID).
			Updates(map[string]any{
				"display_name":      row.DisplayName,
				"parent_project_id": row.ParentProjectID,
				"updated_at":        row.UpdatedAt,
			}).Error; err != nil {
			return err
		}
		project = projectFromModel(row)
		return nil
	})
	return project, mapGormError(err)
}

func (s *PostgresStore) ArchiveProject(
	ctx context.Context,
	principal UserPrincipal,
	projectID string,
) (Project, error) {
	var project Project
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row projectRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND owner_user_id = ?", projectID, principal.User.UserID).
			Take(&row).Error
		if err != nil {
			return err
		}
		if row.ArchivedAt == nil {
			now := s.now().UTC()
			row.ArchivedAt = &now
			row.UpdatedAt = now
			if err := tx.Model(&projectRow{}).
				Where("project_id = ?", row.ProjectID).
				Updates(map[string]any{
					"archived_at": row.ArchivedAt,
					"updated_at":  row.UpdatedAt,
				}).Error; err != nil {
				return err
			}
		}
		project = projectFromModel(row)
		return nil
	})
	return project, mapGormError(err)
}

func validatePostgresProjectParent(
	tx *gorm.DB,
	ownerUserID string,
	projectID string,
	parentProjectID string,
) error {
	if parentProjectID == "" {
		return nil
	}
	currentID := parentProjectID
	for currentID != "" {
		if currentID == projectID {
			return ErrConflict
		}
		var row projectRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND owner_user_id = ?", currentID, ownerUserID).
			Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if currentID == parentProjectID && row.ArchivedAt != nil {
			return ErrNotFound
		}
		currentID = stringFromPtr(row.ParentProjectID)
	}
	return nil
}
