package domain

import (
	"context"
	"time"
)

const (
	SessionConfigSourceNew       = "session/new"
	SessionConfigSourceSet       = "session/set_config_option"
	SessionConfigSourceUpdate    = "config_option_update"
	SessionConfigSourceResponse  = "session/response"
	SessionConfigSourceLegacy    = "legacy_models"
	SessionConfigCategoryMode    = "mode"
	SessionConfigCategoryModel   = "model"
	SessionConfigCategoryConfig  = "model_config"
	SessionConfigCategoryThought = "thought_level"
)

// SessionConfigValue is one selectable value advertised by an ACP agent.
type SessionConfigValue struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Group       string `json:"group,omitempty"`
}

// SessionConfigOption is a normalized ACP session config option. CurrentValue
// is limited to the string and boolean values defined by ACP v1.
type SessionConfigOption struct {
	ID           string               `json:"id"`
	Name         string               `json:"name"`
	Description  string               `json:"description,omitempty"`
	Category     string               `json:"category,omitempty"`
	Type         string               `json:"type"`
	CurrentValue any                  `json:"current_value"`
	Options      []SessionConfigValue `json:"options,omitempty"`
}

type SessionModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionLegacyModels preserves the never-stabilized models response used by
// older agents. It is intentionally read-only; ACP config options are the
// standard model-selection mechanism.
type SessionLegacyModels struct {
	CurrentModelID string         `json:"current_model_id,omitempty"`
	Available      []SessionModel `json:"available,omitempty"`
}

type SessionACPConfig struct {
	Options      []SessionConfigOption `json:"options,omitempty"`
	LegacyModels *SessionLegacyModels  `json:"legacy_models,omitempty"`
	Source       string                `json:"source"`
	ObservedAt   time.Time             `json:"observed_at"`
}

type SessionACPConfigStore interface {
	UpdateSessionACPConfig(
		ctx context.Context,
		agentID string,
		sessionID string,
		config SessionACPConfig,
	) error
}
