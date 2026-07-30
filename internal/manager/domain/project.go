package domain

import "time"

// Project is a user-owned logical grouping for reusable workspace targets and sessions.
type Project struct {
	ProjectID       string     `json:"project_id"`
	OwnerUserID     string     `json:"owner_user_id"`
	DisplayName     string     `json:"display_name"`
	ParentProjectID string     `json:"parent_project_id,omitempty"`
	ArchivedAt      *time.Time `json:"archived_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type CreateProjectRequest struct {
	DisplayName     string
	ParentProjectID string
}

type UpdateProjectRequest struct {
	DisplayName     *string
	ParentProjectID *string
}

type ListProjectsFilter struct {
	IncludeArchived bool
}
