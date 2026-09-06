package manager

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

type sessionConfigObservationMiddleware struct {
	service *Service
}

func (m sessionConfigObservationMiddleware) HandleACPFrame(
	ctx context.Context,
	frame *acpFrameContext,
	next acpFrameHandler,
) error {
	if frame.direction != acpAgentToUser || frame.agent == nil || len(frame.frame.Error) > 0 {
		return next(ctx, frame)
	}
	source := ""
	payload := json.RawMessage(nil)
	switch frame.requestKind {
	case "session/new":
		source = domain.SessionConfigSourceNew
		payload = frame.frame.Result
	case "session/set_config_option":
		source = domain.SessionConfigSourceSet
		payload = frame.frame.Result
	}
	if frame.frame.Method == "session/update" {
		var params struct {
			Update json.RawMessage `json:"update"`
		}
		if json.Unmarshal(frame.frame.Params, &params) == nil &&
			sessionConfigUpdateType(params.Update) == domain.SessionConfigSourceUpdate {
			source = domain.SessionConfigSourceUpdate
			payload = params.Update
		}
	}
	if source == "" && len(frame.frame.Result) > 0 {
		source = firstNonEmpty(frame.requestKind, domain.SessionConfigSourceResponse)
		payload = frame.frame.Result
	}
	if source != "" && frame.managerSessionID != "" {
		if _, err := m.service.observeSessionACPConfig(
			ctx,
			frame.agent.agentID,
			frame.managerSessionID,
			payload,
			source,
		); err != nil && !errors.Is(err, domain.ErrNotFound) {
			logging.Warn(
				ctx,
				"ACP session config snapshot update failed",
				slog.String("agent_id", frame.agent.agentID),
				slog.String("session_id", frame.managerSessionID),
				logging.Err(err),
			)
		}
	}
	return next(ctx, frame)
}

func sessionConfigUpdateType(raw json.RawMessage) string {
	var value struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value.SessionUpdate)
}

func (s *Service) observeSessionACPConfig(
	ctx context.Context,
	agentID string,
	sessionID string,
	payload json.RawMessage,
	source string,
) (domain.SessionACPConfig, error) {
	config, found := parseSessionACPConfig(payload, source, s.clock().UTC())
	if !found {
		return domain.SessionACPConfig{}, nil
	}
	if err := s.store.UpdateSessionACPConfig(ctx, agentID, sessionID, config); err != nil {
		return domain.SessionACPConfig{}, err
	}
	return config, nil
}

func parseSessionACPConfig(
	raw json.RawMessage,
	source string,
	observedAt time.Time,
) (domain.SessionACPConfig, bool) {
	var payload map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &payload) != nil {
		return domain.SessionACPConfig{}, false
	}
	config := domain.SessionACPConfig{Source: source, ObservedAt: observedAt}
	found := false
	if optionsRaw, ok := payload["configOptions"]; ok {
		var options []json.RawMessage
		if json.Unmarshal(optionsRaw, &options) == nil && options != nil {
			found = true
			for _, optionRaw := range options {
				if option, ok := parseSessionConfigOption(optionRaw); ok {
					config.Options = append(config.Options, option)
				}
			}
		}
	}
	if modelsRaw, ok := payload["models"]; ok && string(modelsRaw) != "null" {
		if models, ok := parseLegacySessionModels(modelsRaw); ok {
			config.LegacyModels = &models
			found = true
			if len(config.Options) == 0 {
				config.Source = domain.SessionConfigSourceLegacy
			}
		}
	}
	return config, found
}

func parseSessionConfigOption(raw json.RawMessage) (domain.SessionConfigOption, bool) {
	var value struct {
		ID           string            `json:"id"`
		ConfigID     string            `json:"configId"`
		Name         string            `json:"name"`
		Description  string            `json:"description"`
		Category     string            `json:"category"`
		Type         string            `json:"type"`
		CurrentValue json.RawMessage   `json:"currentValue"`
		Options      []json.RawMessage `json:"options"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return domain.SessionConfigOption{}, false
	}
	id := firstNonEmpty(strings.TrimSpace(value.ID), strings.TrimSpace(value.ConfigID))
	if id == "" {
		return domain.SessionConfigOption{}, false
	}
	current, ok := sessionConfigCurrentValue(value.CurrentValue)
	if !ok {
		return domain.SessionConfigOption{}, false
	}
	optionType := strings.ToLower(strings.TrimSpace(value.Type))
	if optionType == "" {
		if _, boolean := current.(bool); boolean {
			optionType = "boolean"
		} else {
			optionType = "select"
		}
	}
	option := domain.SessionConfigOption{
		ID:           id,
		Name:         firstNonEmpty(strings.TrimSpace(value.Name), id),
		Description:  strings.TrimSpace(value.Description),
		Category:     strings.TrimSpace(value.Category),
		Type:         optionType,
		CurrentValue: current,
	}
	for _, item := range value.Options {
		option.Options = append(option.Options, parseSessionConfigValues(item, "")...)
	}
	return option, optionType == "select" || optionType == "boolean"
}

func sessionConfigCurrentValue(raw json.RawMessage) (any, bool) {
	var stringValue string
	if json.Unmarshal(raw, &stringValue) == nil {
		return stringValue, true
	}
	var boolValue bool
	if json.Unmarshal(raw, &boolValue) == nil {
		return boolValue, true
	}
	return nil, false
}

func parseSessionConfigValues(
	raw json.RawMessage,
	inheritedGroup string,
) []domain.SessionConfigValue {
	var value struct {
		Value       string            `json:"value"`
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Group       string            `json:"group"`
		GroupID     string            `json:"groupId"`
		Options     []json.RawMessage `json:"options"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	group := firstNonEmpty(
		strings.TrimSpace(value.Name),
		strings.TrimSpace(value.Group),
		strings.TrimSpace(value.GroupID),
		inheritedGroup,
	)
	if strings.TrimSpace(value.Value) != "" {
		return []domain.SessionConfigValue{{
			Value: strings.TrimSpace(value.Value),
			Name: firstNonEmpty(
				strings.TrimSpace(value.Name),
				strings.TrimSpace(value.Value),
			),
			Description: strings.TrimSpace(value.Description),
			Group:       inheritedGroup,
		}}
	}
	out := make([]domain.SessionConfigValue, 0)
	for _, child := range value.Options {
		out = append(out, parseSessionConfigValues(child, group)...)
	}
	return out
}

func parseLegacySessionModels(raw json.RawMessage) (domain.SessionLegacyModels, bool) {
	var value struct {
		CurrentModelID string `json:"currentModelId"`
		Available      []struct {
			ModelID     string `json:"modelId"`
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"availableModels"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return domain.SessionLegacyModels{}, false
	}
	models := domain.SessionLegacyModels{CurrentModelID: strings.TrimSpace(value.CurrentModelID)}
	for _, candidate := range value.Available {
		id := firstNonEmpty(strings.TrimSpace(candidate.ModelID), strings.TrimSpace(candidate.ID))
		if id == "" {
			continue
		}
		models.Available = append(models.Available, domain.SessionModel{
			ID:          id,
			Name:        firstNonEmpty(strings.TrimSpace(candidate.Name), id),
			Description: strings.TrimSpace(candidate.Description),
		})
	}
	return models, models.CurrentModelID != "" || len(models.Available) > 0
}

type setSessionConfigOptionRequest struct {
	Value json.RawMessage `json:"value"`
}

type sessionConfigResponse struct {
	SessionID        string                       `json:"session_id"`
	Options          []domain.SessionConfigOption `json:"options"`
	LegacyModels     *domain.SessionLegacyModels  `json:"legacy_models,omitempty"`
	Source           string                       `json:"source,omitempty"`
	ObservedAt       *time.Time                   `json:"observed_at,omitempty"`
	CanSet           bool                         `json:"can_set"`
	CanForceRefresh  bool                         `json:"can_force_refresh"`
	RefreshSemantics string                       `json:"refresh_semantics,omitempty"`
}

func sessionConfigAPIResponse(session domain.AgentSession) sessionConfigResponse {
	options := session.ACPConfig.Options
	if options == nil {
		options = make([]domain.SessionConfigOption, 0)
	}
	response := sessionConfigResponse{
		SessionID:    session.SessionID,
		Options:      options,
		LegacyModels: session.ACPConfig.LegacyModels,
		Source:       session.ACPConfig.Source,
		CanSet:       session.Transport != domain.SessionTransportE2EE,
	}
	if !session.ACPConfig.ObservedAt.IsZero() {
		observedAt := session.ACPConfig.ObservedAt
		response.ObservedAt = &observedAt
	}
	if session.Transport != domain.SessionTransportE2EE &&
		forceSessionConfigRefreshOption(session.ACPConfig) != nil {
		response.CanForceRefresh = true
		response.RefreshSemantics = "reapply_current_value"
	}
	return response
}

func (s *Service) handleGetSessionConfig(c context.Context, ctx *app.RequestContext) {
	_, session, err := s.authorizeSessionConfig(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, sessionConfigAPIResponse(session))
}

func (s *Service) handleSetSessionConfigOption(c context.Context, ctx *app.RequestContext) {
	_, session, err := s.authorizeSessionConfig(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if session.Transport == domain.SessionTransportE2EE {
		writeEndpointError(
			ctx,
			apperr.Error{
				Status:  http.StatusConflict,
				Message: "encrypted session config is not visible to Manager",
			},
		)
		return
	}
	var req setSessionConfigOptionRequest
	if json.Unmarshal(ctx.Request.Body(), &req) != nil || len(req.Value) == 0 {
		writeEndpointError(
			ctx,
			apperr.Error{Status: http.StatusBadRequest, Message: "value is required"},
		)
		return
	}
	option := sessionConfigOptionByID(session.ACPConfig, strings.TrimSpace(ctx.Param("config_id")))
	if option == nil {
		writeEndpointError(ctx, domain.ErrNotFound)
		return
	}
	value, err := validateSessionConfigValue(*option, req.Value)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	updated, err := s.applySessionConfigOption(c, session, *option, value)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, updated)
}

func (s *Service) handleRefreshSessionConfig(c context.Context, ctx *app.RequestContext) {
	_, session, err := s.authorizeSessionConfig(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if session.Transport == domain.SessionTransportE2EE {
		writeEndpointError(
			ctx,
			apperr.Error{
				Status:  http.StatusConflict,
				Message: "encrypted session config is not visible to Manager",
			},
		)
		return
	}
	option := forceSessionConfigRefreshOption(session.ACPConfig)
	if option == nil {
		writeEndpointError(ctx, apperr.Error{
			Status:  http.StatusConflict,
			Message: "agent does not expose a standard config option that can refresh the catalog",
		})
		return
	}
	updated, err := s.applySessionConfigOption(c, session, *option, option.CurrentValue)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, updated)
}

func (s *Service) authorizeSessionConfig(
	c context.Context,
	ctx *app.RequestContext,
) (domain.UserPrincipal, domain.AgentSession, error) {
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		return domain.UserPrincipal{}, domain.AgentSession{}, err
	}
	routeUserID := strings.TrimSpace(ctx.Param("user_id"))
	if routeUserID != "" && routeUserID != "self" && routeUserID != principal.User.UserID {
		return domain.UserPrincipal{}, domain.AgentSession{}, domain.ErrNotFound
	}
	session, err := s.store.GetSession(c, principal, strings.TrimSpace(ctx.Param("session_id")))
	if err != nil {
		return domain.UserPrincipal{}, domain.AgentSession{}, err
	}
	if session.NodeID != strings.TrimSpace(ctx.Param("node_id")) ||
		session.AgentID != strings.TrimSpace(ctx.Param("agent_id")) {
		return domain.UserPrincipal{}, domain.AgentSession{}, domain.ErrNotFound
	}
	return principal, session, nil
}

func sessionConfigOptionByID(
	config domain.SessionACPConfig,
	id string,
) *domain.SessionConfigOption {
	for index := range config.Options {
		option := &config.Options[index]
		if option.ID == id {
			return option
		}
	}
	return nil
}

func isPermissionSessionConfigOption(option domain.SessionConfigOption) bool {
	return strings.EqualFold(
		strings.TrimSpace(option.Category),
		domain.SessionConfigCategoryMode,
	) ||
		strings.EqualFold(strings.TrimSpace(option.ID), domain.SessionConfigCategoryMode)
}

func validateSessionConfigValue(
	option domain.SessionConfigOption,
	raw json.RawMessage,
) (any, error) {
	if isPermissionSessionConfigOption(option) {
		return nil, apperr.Error{
			Status:  http.StatusConflict,
			Message: "permission mode must be changed through the session permission endpoint",
		}
	}
	switch option.Type {
	case "boolean":
		var value bool
		if json.Unmarshal(raw, &value) != nil {
			return nil, apperr.Error{
				Status:  http.StatusBadRequest,
				Message: "boolean config value is required",
			}
		}
		return value, nil
	case "select":
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return nil, apperr.Error{
				Status:  http.StatusBadRequest,
				Message: "string config value is required",
			}
		}
		for _, candidate := range option.Options {
			if candidate.Value == value {
				return value, nil
			}
		}
		return nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "config value is not advertised by the agent",
		}
	default:
		return nil, apperr.Error{
			Status:  http.StatusConflict,
			Message: "unsupported session config option type",
		}
	}
}

func forceSessionConfigRefreshOption(config domain.SessionACPConfig) *domain.SessionConfigOption {
	for index := range config.Options {
		option := &config.Options[index]
		if isPermissionSessionConfigOption(*option) {
			continue
		}
		if strings.EqualFold(option.Category, domain.SessionConfigCategoryModel) ||
			strings.EqualFold(option.ID, "model") || strings.EqualFold(option.ID, "models") {
			return option
		}
	}
	for index := range config.Options {
		option := &config.Options[index]
		if !isPermissionSessionConfigOption(*option) {
			return option
		}
	}
	return nil
}

func (s *Service) applySessionConfigOption(
	ctx context.Context,
	session domain.AgentSession,
	option domain.SessionConfigOption,
	value any,
) (sessionConfigResponse, error) {
	claimSessionIDs := []string{session.SessionID, session.NativeID, ""}
	agentConn, release, err := s.acpTunnels.claimSessionControlAny(
		session.AgentID,
		session.SessionID,
		claimSessionIDs...,
	)
	if err != nil {
		return sessionConfigResponse{}, err
	}
	defer release()
	runner := conversationRunner{
		service:          s,
		agentConn:        agentConn,
		managerSessionID: session.SessionID,
	}
	params := map[string]any{
		"sessionId": session.SessionID,
		"configId":  option.ID,
		"value":     value,
	}
	if option.Type == "boolean" {
		params["type"] = "boolean"
	} else {
		params["type"] = "id"
	}
	response, err := runner.request(ctx, "session/set_config_option", params, nil)
	if err != nil {
		return sessionConfigResponse{}, err
	}
	config, found := parseSessionACPConfig(
		response.Result,
		domain.SessionConfigSourceSet,
		s.clock().UTC(),
	)
	if !found {
		return sessionConfigResponse{}, apperr.Error{
			Status:  http.StatusBadGateway,
			Message: "ACP session/set_config_option did not return a complete config snapshot",
		}
	}
	if err := s.store.UpdateSessionACPConfig(ctx, session.AgentID, session.SessionID, config); err != nil {
		return sessionConfigResponse{}, err
	}
	session.ACPConfig = config
	if model := sessionConfigCurrentModel(config); model != "" {
		session.Model = model
	}
	return sessionConfigAPIResponse(session), nil
}

func sessionConfigCurrentModel(config domain.SessionACPConfig) string {
	for _, option := range config.Options {
		if !strings.EqualFold(option.Category, domain.SessionConfigCategoryModel) &&
			!strings.EqualFold(option.ID, "model") && !strings.EqualFold(option.ID, "models") {
			continue
		}
		if value, ok := option.CurrentValue.(string); ok {
			return value
		}
	}
	if config.LegacyModels != nil {
		return config.LegacyModels.CurrentModelID
	}
	return ""
}
