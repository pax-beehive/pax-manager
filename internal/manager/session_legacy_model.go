package manager

import (
	"context"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// Project an explicitly advertised legacy catalog onto the existing REST model
// selector. Keep the stored ACP options unchanged so writes retain their native
// protocol and a legacy acknowledgement cannot masquerade as a config refresh.
func legacySessionModelOption(config domain.SessionACPConfig) *domain.SessionConfigOption {
	models := config.LegacyModels
	if models == nil || len(models.Available) == 0 {
		return nil
	}
	for _, option := range config.Options {
		if strings.EqualFold(option.Category, domain.SessionConfigCategoryModel) ||
			strings.EqualFold(option.ID, "model") || strings.EqualFold(option.ID, "models") {
			return nil
		}
	}
	option := &domain.SessionConfigOption{
		ID: "model", Name: "Model", Category: domain.SessionConfigCategoryModel,
		Type: "select", CurrentValue: models.CurrentModelID,
		Description: "Switch models while the session is idle.",
	}
	for _, model := range models.Available {
		option.Options = append(option.Options, domain.SessionConfigValue{
			Value: model.ID, Name: model.Name, Description: model.Description,
		})
	}
	return option
}

func (s *Service) requestLegacySessionModel(
	ctx context.Context,
	runner *conversationRunner,
	config domain.SessionACPConfig,
	value any,
) (domain.SessionACPConfig, error) {
	modelID, ok := value.(string)
	if !ok {
		return domain.SessionACPConfig{}, apperr.Error{
			Status: http.StatusBadRequest, Message: "string model value is required",
		}
	}
	_, err := runner.request(ctx, "session/set_model", map[string]any{
		"sessionId": runner.managerSessionID,
		"modelId":   modelID,
	}, nil)
	if err != nil {
		return domain.SessionACPConfig{}, err
	}
	// Legacy set_model returns an empty acknowledgement, not a replacement
	// catalog. Preserve the advertised options and only commit the accepted ID.
	models := *config.LegacyModels
	models.CurrentModelID = modelID
	config.LegacyModels = &models
	config.Source = "session/set_model"
	config.ObservedAt = s.clock().UTC()
	return config, nil
}
