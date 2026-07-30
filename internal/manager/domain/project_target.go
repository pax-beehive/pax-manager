package domain

import "time"

type ProjectTarget struct {
	TargetID    string    `json:"target_id"`
	ProjectID   string    `json:"project_id"`
	AgentID     string    `json:"agent_id"`
	DisplayName string    `json:"display_name"`
	Cwd         string    `json:"cwd"`
	IsDefault   bool      `json:"is_default"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateProjectTargetRequest struct {
	AgentID     string `json:"agent_id"`
	DisplayName string `json:"display_name"`
	Cwd         string `json:"cwd"`
	IsDefault   bool   `json:"is_default,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

type UpdateProjectTargetRequest struct {
	AgentID     *string `json:"agent_id,omitempty"`
	DisplayName *string `json:"display_name,omitempty"`
	Cwd         *string `json:"cwd,omitempty"`
	IsDefault   *bool   `json:"is_default,omitempty"`
	Enabled     *bool   `json:"enabled,omitempty"`
}

type CreateProjectTargetSessionRequest struct {
	SessionName string `json:"name,omitempty"`
}
