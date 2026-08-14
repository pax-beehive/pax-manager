package manager

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

const permissionObservationTTL = 30 * 24 * time.Hour

type permissionObservationMiddleware struct {
	service *Service
}

func (m permissionObservationMiddleware) HandleACPFrame(
	ctx context.Context,
	frame *acpFrameContext,
	next acpFrameHandler,
) error {
	if frame.direction == acpAgentToUser &&
		frame.requestKind == "session/new" &&
		len(frame.frame.Error) == 0 && frame.agent != nil {
		if err := m.service.observePermissionCatalog(
			ctx,
			frame.agent.agentID,
			frame.frame.Result,
		); err != nil {
			logging.Warn(
				ctx,
				"ACP permission observation cache update failed",
				slog.String("agent_id", frame.agent.agentID),
				logging.Err(err),
			)
		}
	}
	return next(ctx, frame)
}

func (s *Service) observePermissionCatalog(
	ctx context.Context,
	agentID string,
	result json.RawMessage,
) error {
	observed := parseObservedPermissionCatalog(result)
	identity, err := s.store.GetAgentRuntimeIdentity(ctx, agentID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return err
	}
	if !strings.EqualFold(identity.PoolConsistency, "consistent") {
		return nil
	}
	// Persist an empty projection as a negative observation. A successful
	// session/new that no longer exposes permission options is authoritative for
	// this identity and must replace a previously cached native catalog.
	catalogJSON, err := json.Marshal(observed)
	if err != nil {
		return err
	}
	now := s.clock().UTC()
	_, err = s.store.UpsertPermissionObservation(ctx, domain.AgentPermissionObservation{
		AgentID:             agentID,
		IdentityFingerprint: identity.IdentityFingerprint,
		CatalogHash:         fmt.Sprintf("sha256:%x", sha256.Sum256(catalogJSON)),
		Catalog:             observed,
		ObservedAt:          now,
		ExpiresAt:           now.Add(permissionObservationTTL),
	})
	return err
}

type acpSessionConfigOption struct {
	ID           acpTolerantString `json:"id"`
	Name         acpTolerantString `json:"name"`
	Description  acpTolerantString `json:"description"`
	Category     acpTolerantString `json:"category"`
	CurrentValue acpTolerantString `json:"currentValue"`
	Options      []struct {
		Value       acpTolerantString `json:"value"`
		Name        acpTolerantString `json:"name"`
		Description acpTolerantString `json:"description"`
	} `json:"options"`
}

type acpTolerantString string

func (s *acpTolerantString) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		*s = ""
		return nil
	}
	*s = acpTolerantString(value)
	return nil
}

func parseObservedPermissionCatalog(result json.RawMessage) domain.ObservedPermissionCatalog {
	var payload struct {
		ConfigOptions []acpSessionConfigOption `json:"configOptions"`
		Modes         *struct {
			CurrentModeID  acpTolerantString `json:"currentModeId"`
			AvailableModes []struct {
				ID          acpTolerantString `json:"id"`
				Name        acpTolerantString `json:"name"`
				Description acpTolerantString `json:"description"`
			} `json:"availableModes"`
		} `json:"modes"`
	}
	if json.Unmarshal(result, &payload) != nil {
		return domain.ObservedPermissionCatalog{}
	}
	for _, option := range payload.ConfigOptions {
		if !strings.EqualFold(strings.TrimSpace(string(option.Category)), "mode") &&
			!strings.EqualFold(strings.TrimSpace(string(option.ID)), "mode") {
			continue
		}
		catalog := domain.ObservedPermissionCatalog{
			Binding: domain.PermissionBinding{
				Kind:         domain.PermissionBindingConfigOption,
				ConfigID:     strings.TrimSpace(string(option.ID)),
				Category:     strings.TrimSpace(string(option.Category)),
				CurrentValue: strings.TrimSpace(string(option.CurrentValue)),
			},
		}
		catalog.Options = normalizeObservedConfigOptions(option)
		if len(catalog.Options) == 0 {
			continue
		}
		catalog.DefaultChoiceID = observedPermissionChoiceID(
			catalog.Binding,
			catalog.Binding.CurrentValue,
		)
		return catalog
	}
	if payload.Modes == nil {
		return domain.ObservedPermissionCatalog{}
	}
	catalog := domain.ObservedPermissionCatalog{
		Binding: domain.PermissionBinding{
			Kind:         domain.PermissionBindingLegacyMode,
			ConfigID:     "mode",
			Category:     "mode",
			CurrentValue: strings.TrimSpace(string(payload.Modes.CurrentModeID)),
		},
	}
	seen := make(map[string]bool)
	for _, option := range payload.Modes.AvailableModes {
		value := strings.TrimSpace(string(option.ID))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		catalog.Options = append(catalog.Options, domain.ObservedPermissionOption{
			Value:       value,
			Name:        firstNonEmpty(strings.TrimSpace(string(option.Name)), value),
			Description: strings.TrimSpace(string(option.Description)),
		})
	}
	catalog.DefaultChoiceID = observedPermissionChoiceID(
		catalog.Binding,
		catalog.Binding.CurrentValue,
	)
	return catalog
}

func normalizeObservedConfigOptions(option acpSessionConfigOption) []domain.ObservedPermissionOption {
	seen := make(map[string]bool)
	out := make([]domain.ObservedPermissionOption, 0, len(option.Options))
	for _, item := range option.Options {
		value := strings.TrimSpace(string(item.Value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, domain.ObservedPermissionOption{
			Value:       value,
			Name:        firstNonEmpty(strings.TrimSpace(string(item.Name)), value),
			Description: strings.TrimSpace(string(item.Description)),
		})
	}
	return out
}

func observedPermissionChoiceID(binding domain.PermissionBinding, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	switch binding.Kind {
	case domain.PermissionBindingConfigOption:
		configID := strings.TrimSpace(binding.ConfigID)
		if configID == "" {
			configID = "unknown"
		}
		return "agent:config-option:" + configID + ":" + value
	case domain.PermissionBindingLegacyMode:
		return "agent:legacy-mode:" + value
	default:
		return "agent:unknown:" + value
	}
}

func paxAutoApproveCatalogChoice() domain.PermissionCatalogChoice {
	return domain.PermissionCatalogChoice{
		ChoiceID:    domain.PermissionChoicePAXAutoApprove,
		Label:       "Auto approve (PAX)",
		Description: "PAX automatically accepts permission requests from the agent.",
		Kind:        domain.PermissionChoiceKindPAX,
		Risk:        "auto_approve",
	}
}

func (s *Service) resolveAgentPermissionCatalog(
	ctx context.Context,
	agent domain.Agent,
) (domain.AgentPermissionCatalog, error) {
	identity, identityErr := s.store.GetAgentRuntimeIdentity(ctx, agent.AgentID)
	if identityErr != nil && !errors.Is(identityErr, domain.ErrNotFound) {
		return domain.AgentPermissionCatalog{}, identityErr
	}
	hasIdentity := identityErr == nil
	if hasIdentity && isMixedPermissionPool(identity.PoolConsistency) {
		return paxOnlyPermissionCatalog(true), nil
	}
	profiles, err := s.store.ListActivePermissionProfiles(
		ctx,
		agent.OwnerUserID,
		agent.AgentType,
	)
	if err != nil {
		return domain.AgentPermissionCatalog{}, err
	}
	profile, profileFound, uncertain := selectPermissionProfile(profiles, identity, hasIdentity)
	if profileFound {
		if err := validatePermissionProfileDefinition(profile.Definition); err != nil {
			return domain.AgentPermissionCatalog{}, fmt.Errorf(
				"permission profile %s revision %d: %w",
				profile.ProfileID,
				profile.Revision,
				err,
			)
		}
	}
	if hasIdentity && strings.EqualFold(identity.PoolConsistency, "consistent") {
		observation, err := s.store.GetPermissionObservation(
			ctx,
			agent.AgentID,
			identity.IdentityFingerprint,
		)
		if err == nil {
			if observation.ExpiresAt.After(s.clock().UTC()) {
				catalog := catalogFromObservation(observation, profile, profileFound)
				catalog.Stale = uncertain
				return catalog, nil
			}
			uncertain = true
		} else if !errors.Is(err, domain.ErrNotFound) {
			return domain.AgentPermissionCatalog{}, err
		}
	}
	if hasIdentity && !strings.EqualFold(identity.PoolConsistency, "consistent") {
		uncertain = true
	}
	if profileFound {
		catalog, err := catalogFromProfile(profile)
		if err != nil {
			return domain.AgentPermissionCatalog{}, err
		}
		catalog.Stale = uncertain
		return catalog, nil
	}
	return paxOnlyPermissionCatalog(!hasIdentity || uncertain), nil
}

func isMixedPermissionPool(poolConsistency string) bool {
	switch strings.ToLower(strings.TrimSpace(poolConsistency)) {
	case "mixed", "inconsistent":
		return true
	default:
		return false
	}
}

func paxOnlyPermissionCatalog(stale bool) domain.AgentPermissionCatalog {
	return domain.AgentPermissionCatalog{
		CatalogRevision: 1,
		Source:          domain.PermissionCatalogSourcePAXOnly,
		Stale:           stale,
		Choices:         []domain.PermissionCatalogChoice{paxAutoApproveCatalogChoice()},
	}
}

func selectPermissionProfile(
	profiles []domain.AgentPermissionProfile,
	identity domain.AgentRuntimeIdentity,
	hasIdentity bool,
) (domain.AgentPermissionProfile, bool, bool) {
	for _, profile := range profiles {
		matches, uncertain := permissionProfileMatches(profile, identity, hasIdentity)
		if matches {
			return profile, true, uncertain
		}
	}
	return domain.AgentPermissionProfile{}, false, !hasIdentity
}

func permissionProfileMatches(
	profile domain.AgentPermissionProfile,
	identity domain.AgentRuntimeIdentity,
	hasIdentity bool,
) (bool, bool) {
	uncertain := !hasIdentity
	if profile.ACPAgentName != "" {
		if identity.ACPAgentName == "" {
			uncertain = true
		} else if !strings.EqualFold(profile.ACPAgentName, identity.ACPAgentName) {
			return false, false
		}
		if identity.ACPAgentVersion == "" {
			uncertain = true
		}
	}
	matched, versionUncertain := permissionVersionMatches(
		profile.ACPAgentVersionConstraint,
		identity.ACPAgentVersion,
	)
	if !matched {
		return false, false
	}
	uncertain = uncertain || versionUncertain
	if profile.RuntimeName != "" {
		if identity.RuntimeName == "" {
			uncertain = true
		} else if !strings.EqualFold(profile.RuntimeName, identity.RuntimeName) {
			return false, false
		}
		if identity.RuntimeVersion == "" {
			uncertain = true
		}
	}
	matched, versionUncertain = permissionVersionMatches(
		profile.RuntimeVersionConstraint,
		identity.RuntimeVersion,
	)
	return matched, uncertain || versionUncertain
}

func permissionVersionMatches(constraintText, actual string) (bool, bool) {
	constraintText = strings.TrimSpace(constraintText)
	actual = strings.TrimSpace(actual)
	if constraintText == "" || constraintText == "*" {
		return true, false
	}
	if actual == "" {
		return true, true
	}
	constraint, err := semver.NewConstraint(constraintText)
	if err != nil {
		return false, false
	}
	version, err := semver.NewVersion(actual)
	if err != nil {
		return false, false
	}
	return constraint.Check(version), false
}

func catalogFromProfile(
	profile domain.AgentPermissionProfile,
) (domain.AgentPermissionCatalog, error) {
	if err := validatePermissionProfileDefinition(profile.Definition); err != nil {
		return domain.AgentPermissionCatalog{}, fmt.Errorf(
			"permission profile %s revision %d: %w",
			profile.ProfileID,
			profile.Revision,
			err,
		)
	}
	choices := []domain.PermissionCatalogChoice{paxAutoApproveCatalogChoice()}
	for _, choice := range profile.Definition.NativeChoices {
		choices = append(choices, domain.PermissionCatalogChoice{
			ChoiceID:             choice.ChoiceID,
			Label:                choice.Label,
			Description:          choice.Description,
			Kind:                 domain.PermissionChoiceKindAgent,
			Risk:                 choice.Risk,
			RequiresConfirmation: choice.RequiresConfirmation,
		})
	}
	return domain.AgentPermissionCatalog{
		CatalogRevision: profile.Revision,
		Source:          domain.PermissionCatalogSourceProfile,
		DefaultChoiceID: profile.Definition.DefaultChoiceID,
		Choices:         choices,
	}, nil
}

func catalogFromObservation(
	observation domain.AgentPermissionObservation,
	profile domain.AgentPermissionProfile,
	profileFound bool,
) domain.AgentPermissionCatalog {
	profileMapsBinding := profileFound && permissionBindingsCompatible(
		profile.Definition.Binding,
		observation.Catalog.Binding,
	)
	choices := []domain.PermissionCatalogChoice{paxAutoApproveCatalogChoice()}
	for _, option := range observation.Catalog.Options {
		choice := domain.PermissionCatalogChoice{
			ChoiceID:             observedPermissionChoiceID(observation.Catalog.Binding, option.Value),
			Label:                option.Name,
			Description:          option.Description,
			Kind:                 domain.PermissionChoiceKindAgent,
			Risk:                 "unknown",
			RequiresConfirmation: true,
		}
		if profileMapsBinding {
			if profileChoice, ok := permissionProfileChoiceByValue(profile, option.Value); ok {
				choice.ChoiceID = profileChoice.ChoiceID
				choice.Risk = profileChoice.Risk
				choice.RequiresConfirmation = profileChoice.RequiresConfirmation
			}
		}
		choices = append(choices, choice)
	}
	defaultChoiceID := ""
	if profileMapsBinding {
		for _, option := range observation.Catalog.Options {
			if option.Value != observation.Catalog.Binding.CurrentValue {
				continue
			}
			if choice, ok := permissionProfileChoiceByValue(profile, option.Value); ok {
				if choice.RequiresConfirmation {
					defaultChoiceID = ""
				} else {
					defaultChoiceID = choice.ChoiceID
				}
			}
		}
	}
	return domain.AgentPermissionCatalog{
		CatalogRevision: observation.CatalogRevision,
		Source:          domain.PermissionCatalogSourceObserved,
		DefaultChoiceID: defaultChoiceID,
		Choices:         choices,
	}
}

func permissionBindingsCompatible(
	profile domain.PermissionBinding,
	live domain.PermissionBinding,
) bool {
	if profile.Kind != live.Kind {
		return false
	}
	switch profile.Kind {
	case domain.PermissionBindingConfigOption:
		if !strings.EqualFold(
			strings.TrimSpace(profile.ConfigID),
			strings.TrimSpace(live.ConfigID),
		) {
			return false
		}
		profileCategory := strings.TrimSpace(profile.Category)
		return profileCategory == "" || strings.EqualFold(
			profileCategory,
			strings.TrimSpace(live.Category),
		)
	case domain.PermissionBindingLegacyMode:
		return true
	default:
		return false
	}
}

func permissionProfileChoiceByValue(
	profile domain.AgentPermissionProfile,
	value string,
) (domain.PermissionNativeChoice, bool) {
	for _, choice := range profile.Definition.NativeChoices {
		if choice.Value == value {
			return choice, true
		}
	}
	return domain.PermissionNativeChoice{}, false
}

func validatePermissionProfileDefinition(definition domain.PermissionProfileDefinition) error {
	switch definition.Binding.Kind {
	case domain.PermissionBindingConfigOption:
		if strings.TrimSpace(definition.Binding.ConfigID) == "" {
			return errors.New("config_option binding requires config_id")
		}
	case domain.PermissionBindingLegacyMode:
	default:
		return fmt.Errorf("unsupported binding kind %q", definition.Binding.Kind)
	}
	choiceIDs := make(map[string]bool)
	values := make(map[string]bool)
	for _, choice := range definition.NativeChoices {
		if !strings.HasPrefix(choice.ChoiceID, "agent:") || choice.Value == "" {
			return errors.New("native choices require an agent: choice_id and value")
		}
		if choiceIDs[choice.ChoiceID] || values[choice.Value] {
			return errors.New("native choice ids and values must be unique")
		}
		choiceIDs[choice.ChoiceID] = true
		values[choice.Value] = true
	}
	if definition.DefaultChoiceID != "" && !choiceIDs[definition.DefaultChoiceID] {
		return errors.New("default_choice_id must reference a native choice")
	}
	if definition.DefaultChoiceID != "" {
		for _, choice := range definition.NativeChoices {
			if choice.ChoiceID == definition.DefaultChoiceID && choice.RequiresConfirmation {
				return errors.New("default_choice_id cannot require confirmation")
			}
		}
	}
	return nil
}

func resolvePermissionChoiceFromLive(
	choiceID string,
	live domain.ObservedPermissionCatalog,
	profile domain.AgentPermissionProfile,
	profileFound bool,
) (domain.ResolvedPermissionChoice, error) {
	choiceID = strings.TrimSpace(choiceID)
	if choiceID == domain.PermissionChoicePAXAutoApprove {
		return domain.ResolvedPermissionChoice{
			ChoiceID:     choiceID,
			ApprovalMode: domain.SessionApprovalModeAutoApproveAll,
		}, nil
	}
	profileMapsBinding := profileFound && permissionBindingsCompatible(
		profile.Definition.Binding,
		live.Binding,
	)
	for _, option := range live.Options {
		actualChoiceID := observedPermissionChoiceID(live.Binding, option.Value)
		if profileMapsBinding {
			if profileChoice, ok := permissionProfileChoiceByValue(profile, option.Value); ok {
				actualChoiceID = profileChoice.ChoiceID
			}
		}
		if choiceID == actualChoiceID {
			return domain.ResolvedPermissionChoice{
				ChoiceID:     choiceID,
				ApprovalMode: domain.SessionApprovalModeManual,
				Binding:      live.Binding,
				Value:        option.Value,
			}, nil
		}
	}
	return domain.ResolvedPermissionChoice{}, apperr.Error{
		Status:  http.StatusConflict,
		Message: "permission choice is not available in the live ACP session",
	}
}

func (s *Service) resolveConversationPermissionChoice(
	ctx context.Context,
	principal domain.UserPrincipal,
	agentID string,
	choiceID string,
	sessionNewResult json.RawMessage,
) (domain.ResolvedPermissionChoice, error) {
	if choiceID == domain.PermissionChoicePAXAutoApprove {
		return domain.ResolvedPermissionChoice{
			ChoiceID:     choiceID,
			ApprovalMode: domain.SessionApprovalModeAutoApproveAll,
		}, nil
	}
	agent, err := s.store.GetAgent(ctx, principal, agentID)
	if err != nil {
		return domain.ResolvedPermissionChoice{}, err
	}
	identity, identityErr := s.store.GetAgentRuntimeIdentity(ctx, agentID)
	if identityErr != nil && !errors.Is(identityErr, domain.ErrNotFound) {
		return domain.ResolvedPermissionChoice{}, identityErr
	}
	if identityErr == nil && isMixedPermissionPool(identity.PoolConsistency) {
		return domain.ResolvedPermissionChoice{}, apperr.Error{
			Status:  http.StatusConflict,
			Message: "native permission choices are unavailable for a mixed ACP worker pool",
		}
	}
	profiles, err := s.store.ListActivePermissionProfiles(
		ctx,
		agent.OwnerUserID,
		agent.AgentType,
	)
	if err != nil {
		return domain.ResolvedPermissionChoice{}, err
	}
	profile, profileFound, _ := selectPermissionProfile(
		profiles,
		identity,
		identityErr == nil,
	)
	if profileFound {
		if err := validatePermissionProfileDefinition(profile.Definition); err != nil {
			return domain.ResolvedPermissionChoice{}, apperr.Error{
				Status:  http.StatusBadGateway,
				Message: "matched permission profile is invalid",
			}
		}
	}
	live := parseObservedPermissionCatalog(sessionNewResult)
	if live.Binding.Kind == "" || len(live.Options) == 0 {
		return domain.ResolvedPermissionChoice{}, apperr.Error{
			Status:  http.StatusBadGateway,
			Message: "ACP session/new did not return live permission options",
		}
	}
	return resolvePermissionChoiceFromLive(choiceID, live, profile, profileFound)
}

type permissionConfigRequester interface {
	request(
		context.Context,
		string,
		map[string]any,
		<-chan struct{},
	) (conversationResponse, error)
}

func applyResolvedPermissionChoice(
	ctx context.Context,
	runner permissionConfigRequester,
	sessionID string,
	resolved domain.ResolvedPermissionChoice,
) error {
	if resolved.Value == "" {
		return nil
	}
	var method string
	var params map[string]any
	switch resolved.Binding.Kind {
	case domain.PermissionBindingConfigOption:
		if strings.TrimSpace(resolved.Binding.ConfigID) == "" {
			return apperr.Error{
				Status:  http.StatusBadGateway,
				Message: "ACP permission config option has no config id",
			}
		}
		method = "session/set_config_option"
		params = map[string]any{
			"sessionId": sessionID,
			"configId":  resolved.Binding.ConfigID,
			"value":     resolved.Value,
		}
	case domain.PermissionBindingLegacyMode:
		method = "session/set_mode"
		params = map[string]any{
			"sessionId": sessionID,
			"modeId":    resolved.Value,
		}
	default:
		return apperr.Error{
			Status:  http.StatusBadGateway,
			Message: "ACP session/new returned an unsupported permission binding",
		}
	}
	_, err := runner.request(ctx, method, params, nil)
	return err
}

func (s *Service) handleGetAgentPermissionCatalog(
	c context.Context,
	ctx *app.RequestContext,
) {
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	routeUserID := strings.TrimSpace(ctx.Param("user_id"))
	if routeUserID != "" && routeUserID != "self" &&
		routeUserID != principal.User.UserID {
		writeEndpointError(ctx, domain.ErrNotFound)
		return
	}
	agent, err := s.store.GetAgent(c, principal, strings.TrimSpace(ctx.Param("agent_id")))
	if err != nil || agent.OwnerUserID != principal.User.UserID {
		if err == nil {
			err = domain.ErrNotFound
		}
		writeEndpointError(ctx, err)
		return
	}
	catalog, err := s.resolveAgentPermissionCatalog(c, agent)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, catalog)
}
