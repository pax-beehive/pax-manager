package manager

import (
	"context"
	"strconv"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/runtimeconfig"
)

type runtimeConfigProvider interface {
	RuntimeConfig() domain.RuntimeConfigRepository
}

// runtimeConfigRegistry is the production registry. Package tests replace it
// so pre-existing behavior tests are not gated by the admin-stage defaults.
var runtimeConfigRegistry = runtimeconfig.DefaultRegistry

func newRuntimeConfigStore(cfg Config, store any) *runtimeconfig.Store {
	var repository domain.RuntimeConfigRepository
	if provider, ok := store.(runtimeConfigProvider); ok {
		repository = provider.RuntimeConfig()
	}
	return runtimeconfig.New(runtimeconfig.Options{
		Repository: repository,
		Env:        runtimeConfigEnv(cfg),
		Region:     cfg.Region,
		Registry:   runtimeConfigRegistry(),
	})
}

// runtimeConfigEnv exposes the already-loaded Config as the environment
// layer, so resolution without overrides matches config.Load exactly. The
// *_API_KEY values are environment-only credentials, never overridable.
func runtimeConfigEnv(cfg Config) runtimeconfig.EnvLookup {
	values := map[string]string{
		"SEARCH_URL":                    cfg.SearchURL,
		"SEARCH_INDEX":                  cfg.SearchIndex,
		"SEARCH_API_KEY":                cfg.SearchAPIKey,
		"VOICE_PROVIDER":                cfg.VoiceProvider,
		"VOICE_ADMIN_ONLY":              strconv.FormatBool(cfg.VoiceAdminOnly),
		"VOICE_OPENAI_URL":              cfg.VoiceOpenAIURL,
		"VOICE_OPENAI_MODEL":            cfg.VoiceOpenAIModel,
		"VOICE_OPENAI_DELAY":            cfg.VoiceOpenAIDelay,
		"OPENAI_API_KEY":                cfg.OpenAIAPIKey,
		"VOICE_QWEN_URL":                cfg.VoiceQwenURL,
		"VOICE_QWEN_MODEL":              cfg.VoiceQwenModel,
		"VOICE_QWEN_LANGUAGE":           cfg.VoiceQwenLanguage,
		"DASHSCOPE_API_KEY":             cfg.DashScopeAPIKey,
		"VOICE_SONIOX_URL":              cfg.VoiceSonioxURL,
		"VOICE_SONIOX_MODEL":            cfg.VoiceSonioxModel,
		"SONIOX_API_KEY":                cfg.SonioxAPIKey,
		"TRANSPORT_JOURNAL_GC_DISABLED": strconv.FormatBool(cfg.TransportJournalGCDisabled),
	}
	return func(name string) string { return values[name] }
}

// featureForOwner decides a feature for a background job owner. Stages that
// do not depend on the owner skip the user lookup.
func (s *Service) featureForOwner(ctx context.Context, ownerUserID, feature string) bool {
	snapshot := s.runtimeConfig.Snapshot()
	switch snapshot.FeatureStage(feature) {
	case runtimeconfig.StageGA:
		return ownerUserID != ""
	case runtimeconfig.StageOff:
		return false
	}
	if ownerUserID == "" || s.store == nil {
		return false
	}
	user, err := s.store.GetUser(ctx, ownerUserID)
	if err != nil {
		return false
	}
	principal := domain.UserPrincipal{
		User:    user,
		IsAdmin: serviceAdminPolicy{s: s}.IsAdmin(user.Email),
	}
	return snapshot.Enabled(principal, feature)
}
