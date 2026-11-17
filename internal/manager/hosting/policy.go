package hosting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"time"
)

const maxModelRegistryBytes = 1024 * 1024

type ModelPolicy struct {
	ID              string `json:"id"`
	Provider        string `json:"provider"`
	UpstreamModel   string `json:"upstream_model"`
	Moderation      string `json:"moderation"`
	PolicyVersion   string `json:"policy_version"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
	InputPrice      int64  `json:"input_microusd_per_million"`
	OutputPrice     int64  `json:"output_microusd_per_million"`
	Enabled         bool   `json:"enabled"`
}

type ModelRegistry struct {
	PolicyVersion string        `json:"policy_version"`
	DefaultModel  string        `json:"default_model"`
	Models        []ModelPolicy `json:"models"`
}

var modelPolicyIdentifier = regexp.MustCompile(`^[A-Za-z0-9~][A-Za-z0-9._:/@+~=-]{0,255}$`)
var modelPolicyVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var errModelRegistry = errors.New("invalid HOSTING_MODELS_FILE")

// LoadModelRegistry accepts the shared gateway registry shape. Routes belong to
// the gateway; Manager only approves identity, prices, caps and moderation policy.
func LoadModelRegistry(path string) (*ModelRegistry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errModelRegistry
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maxModelRegistryBytes+1))
	if err != nil || len(raw) > maxModelRegistryBytes {
		return nil, errModelRegistry
	}
	return ParseModelRegistry(raw)
}

func ParseModelRegistry(raw []byte) (*ModelRegistry, error) {
	var wire struct {
		PolicyVersion string `json:"policy_version"`
		DefaultModel  string `json:"default_model"`
		Models        []struct {
			ModelPolicy
			Route string `json:"route"`
		} `json:"models"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF ||
		!modelPolicyVersion.MatchString(wire.PolicyVersion) || len(wire.Models) < 1 ||
		len(wire.Models) > 128 {
		return nil, errModelRegistry
	}
	registry := &ModelRegistry{PolicyVersion: wire.PolicyVersion, DefaultModel: wire.DefaultModel}
	for _, entry := range wire.Models {
		policy := entry.ModelPolicy
		if policy.Moderation == "" {
			policy.Moderation = "openai"
		}
		if policy.PolicyVersion == "" {
			policy.PolicyVersion = wire.PolicyVersion
		}
		registry.Models = append(registry.Models, policy)
	}
	if !validModelPolicies(registry.Models, registry.DefaultModel) {
		return nil, errModelRegistry
	}
	return registry, nil
}

func validModelPolicy(policy ModelPolicy) bool {
	return len(policy.ID) <= 128 && modelPolicyIdentifier.MatchString(policy.ID) &&
		len(policy.Provider) <= 64 && modelPolicyIdentifier.MatchString(policy.Provider) &&
		modelPolicyIdentifier.MatchString(policy.UpstreamModel) &&
		modelPolicyVersion.MatchString(policy.PolicyVersion) &&
		(policy.Moderation == "openai" || policy.Moderation == "off") &&
		policy.MaxOutputTokens > 0 && policy.MaxOutputTokens <= 8192 &&
		policy.InputPrice >= 0 && policy.InputPrice <= 1_000_000_000_000 &&
		policy.OutputPrice >= 0 && policy.OutputPrice <= 1_000_000_000_000
}

func validModelPolicies(policies []ModelPolicy, defaultModel string) bool {
	seen := map[string]bool{}
	defaultFound := false
	for _, policy := range policies {
		if !validModelPolicy(policy) || seen[policy.ID] {
			return false
		}
		seen[policy.ID] = true
		if policy.ID == defaultModel {
			defaultFound = true
		}
	}
	return defaultFound
}

func (c *Config) configuredModelPolicy(id string) (ModelPolicy, bool) {
	for _, policy := range c.Models {
		if policy.ID == id {
			return policy, validModelPolicy(policy)
		}
	}
	return ModelPolicy{}, false
}

func (s *Service) admissionPolicy(
	state *State,
	model string,
	maximum int64,
	claimed *ModelPolicy,
) (*ModelPolicy, error) {
	approved, exists := s.effectiveModelPolicy(state, model)
	if !exists && s.legacyPolicyAllowed(state, model) {
		if model != s.Config.Model || maximum != s.Config.MaxOutputTokens ||
			!validLegacyPolicy(claimed, model, maximum) {
			return nil, ErrConflict
		}
		return nil, nil
	}
	if !exists || !approved.Enabled || claimed == nil || approved != *claimed ||
		approved.MaxOutputTokens != maximum {
		return nil, ErrConflict
	}
	return &approved, nil
}

func validLegacyPolicy(claimed *ModelPolicy, model string, maximum int64) bool {
	if claimed == nil {
		return true
	}
	return claimed.ID == model && claimed.Provider == "configured" &&
		modelPolicyIdentifier.MatchString(
			claimed.UpstreamModel,
		) && claimed.Moderation == "openai" &&
		claimed.PolicyVersion == ModerationPolicy && claimed.MaxOutputTokens == maximum &&
		claimed.InputPrice == 0 && claimed.OutputPrice == 0 && claimed.Enabled
}

func (s *Service) currentReservationPolicy(state *State, reservation *Reservation) bool {
	if reservation.Policy == nil {
		return s.legacyPolicyAllowed(state, reservation.Model) &&
			reservation.Model == s.Config.Model
	}
	current, exists := s.effectiveModelPolicy(state, reservation.Policy.ID)
	return exists && current.Enabled && current == *reservation.Policy
}

func (c *Config) validModelConfiguration() bool {
	if len(c.Models) > 0 {
		return validModelPolicies(c.Models, c.Model)
	}
	return c.InputPrice > 0 && c.InputPrice <= 1_000_000_000_000 &&
		c.OutputPrice > 0 && c.OutputPrice <= 1_000_000_000_000
}

func (s *Service) hasZeroPriceModel(state *State) bool {
	for _, policy := range s.runtimePolicies(state) {
		if policy.Enabled && policy.InputPrice == 0 && policy.OutputPrice == 0 &&
			validModelPolicy(policy) {
			return true
		}
	}
	return false
}

// ReserveForPolicy checks the gateway's claimed route policy against Manager's
// own registry. Claimed prices and moderation settings never grant authority.
func (s *Service) ReserveForPolicy(
	ctx context.Context,
	token, requestID string,
	tokens int64,
	model string,
	maximum int64,
	claimed *ModelPolicy,
) (*Reservation, error) {
	selection := &reservationPolicyClaim{model: model, maximum: maximum}
	if claimed != nil {
		copy := *claimed
		selection.policy = &copy
	}
	return s.reserve(ctx, token, requestID, tokens, true, selection)
}

type reservationPolicyClaim struct {
	model   string
	maximum int64
	policy  *ModelPolicy
}

func (s *Service) selectReservationPolicy(
	state *State,
	selection *reservationPolicyClaim,
) (*ModelPolicy, error) {
	if selection != nil {
		return s.admissionPolicy(state, selection.model, selection.maximum, selection.policy)
	}
	policy, exists := s.effectiveModelPolicy(state, s.Config.Model)
	if !exists && s.legacyPolicyAllowed(state, s.Config.Model) {
		return nil, nil
	}
	if !exists || !policy.Enabled {
		return nil, ErrConflict
	}
	return &policy, nil
}

func (s *Service) checkRequestRate(state *State, environment string) error {
	cutoff := s.Now().Add(-time.Minute)
	userCount, total := 0, 0
	for _, request := range state.Requests {
		if !request.CreatedAt.After(cutoff) {
			continue
		}
		total++
		if request.EnvironmentID == environment {
			userCount++
		}
	}
	if userCount >= s.Config.RequestsPerMinute || total >= s.Config.GlobalRequestsPerMinute {
		return ErrRateLimited
	}
	return nil
}

func reservationPolicyVersion(reservation *Reservation) string {
	if reservation.Policy != nil {
		return reservation.Policy.PolicyVersion
	}
	return ModerationPolicy
}

func validReservationRequest(id string, tokens int64) bool {
	return len(id) >= 8 && len(id) <= 128 && tokens > 0 && tokens <= 1_000_000
}

func reservationModerationPermits(reservation *Reservation) bool {
	if reservation.Policy != nil && reservation.Policy.Moderation == "off" {
		return reservation.Moderation.Verdict == "skipped_by_policy" &&
			reservation.Moderation.PolicyVersion == reservation.Policy.PolicyVersion
	}
	return reservation.Moderation.Verdict == "allowed" &&
		reservation.Moderation.PolicyVersion == reservationPolicyVersion(reservation)
}

func validReservationDecision(reservation *Reservation, decision *ModerationDecision) bool {
	if reservation.Policy != nil && decision.PolicyVersion != reservation.Policy.PolicyVersion {
		return false
	}
	if reservation.Policy != nil && reservation.Policy.Moderation == "off" {
		return decision.Verdict == "skipped_by_policy" && decision.Model == "" &&
			decision.InputCount == 0 && len(decision.Categories) == 0 && decision.ProviderRequestID == ""
	}
	return decision.Verdict != "skipped_by_policy"
}
