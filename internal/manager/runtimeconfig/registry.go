// Package runtimeconfig resolves Manager settings and feature gates from
// database overrides, environment values, and code defaults, in that order.
//
// The registry in this file is the source of truth: keys that are not
// registered are never accepted, and every entry declares its type, default,
// validation, secrecy, and whether a change applies without a restart.
package runtimeconfig

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type Kind string

const (
	KindFeature Kind = "feature"
	KindSetting Kind = "setting"
)

type Apply string

const (
	ApplyLive    Apply = "live"
	ApplyRestart Apply = "restart"
)

const (
	TypeString   = "string"
	TypeURL      = "url"
	TypeBool     = "bool"
	TypeInt      = "int"
	TypeDuration = "duration"
	TypeEnum     = "enum"
	TypeFeature  = "feature"
)

// ResolvedGroup maps keys to typed values (string, bool, int64, or Feature)
// for one group. Testers receive the effective values plus unsaved overrides.
type ResolvedGroup map[string]any

func (g ResolvedGroup) String(key string) string {
	value, _ := g[key].(string)
	return value
}

type TestResult struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Message   string `json:"message"`
}

type Entry struct {
	Key         string
	Kind        Kind
	Group       string
	Label       string
	Description string
	Type        string
	Options     []string
	EnvVar      string
	Default     any
	Apply       Apply
	Validate    func(v any) error
	Tester      func(ctx context.Context, v ResolvedGroup) TestResult
	Regions     []string

	// fromEnv converts the environment value to the typed value. Entries
	// whose environment form differs from their value type set it.
	fromEnv func(raw string) (any, error)
}

func (e *Entry) visibleIn(region string) bool {
	if len(e.Regions) == 0 || region == "" {
		return true
	}
	for _, candidate := range e.Regions {
		if candidate == region {
			return true
		}
	}
	return false
}

// Feature keys without the "feature." prefix, as used by requireFeature and
// the user features endpoint.
const (
	FeatureSecretary         = "secretary"
	FeatureNotifications     = "notifications"
	FeatureCollaboration     = "collaboration"
	FeatureKnowledgeCapsules = "knowledge_capsules"
	FeatureBrowserControl    = "browser_control"
	FeatureAndroidControl    = "android_control"
	FeatureVoiceInput        = "voice_input"
)

const featurePrefix = "feature."

// FeatureKey returns the registry key for a short feature name.
func FeatureKey(name string) string {
	if strings.HasPrefix(name, featurePrefix) {
		return name
	}
	return featurePrefix + name
}

// Credentials are read from the environment only. They are never stored in
// the database, listed by the admin API, or accepted as overrides; the
// snapshot carries them so consumers can fingerprint their clients.
var credentialEnvVars = []string{
	"SEARCH_API_KEY",
	"OPENAI_API_KEY",
	"DASHSCOPE_API_KEY",
	"SONIOX_API_KEY",
}

var voiceProviders = []string{"openai", "qwen", "soniox"}

var openAIDelays = []string{"minimal", "low", "medium", "high", "xhigh"}

// DefaultRegistry returns a fresh copy of the registered entries.
func DefaultRegistry() []Entry {
	entries := []Entry{
		feature(FeatureSecretary, "Secretary",
			"Secretary automation, tasks, guidance, and summary browsing."),
		feature(FeatureNotifications, "Notifications",
			"Push notifications (Android app and Chrome only)."),
		feature(FeatureCollaboration, "Collaboration",
			"Teams, friends, envelopes, and team knowledge."),
		feature(FeatureKnowledgeCapsules, "Knowledge capsules",
			"Knowledge capsules and injections in sessions."),
		feature(FeatureBrowserControl, "Browser control",
			"Watching and taking over the browser on a device."),
		feature(FeatureAndroidControl, "Android control",
			"Watching and taking over Android on a device."),
		voiceInputFeature(),

		setting("search.url", "search", "Search URL",
			"Elasticsearch base URL. Empty disables workspace search.",
			TypeURL, "SEARCH_URL", "", validateSearchURL),
		setting("search.index", "search", "Search index",
			"Elasticsearch index name.",
			TypeString, "SEARCH_INDEX", "pax-search-v1", validateSearchIndex),

		enum("voice.provider", "voice", "Voice provider",
			"Speech recognition provider for voice input.",
			"VOICE_PROVIDER", voiceProviders),
		setting("voice.openai.url", "voice", "OpenAI realtime URL",
			"Overrides the OpenAI realtime transcription endpoint.",
			TypeURL, "VOICE_OPENAI_URL", "", validateOptionalURL),
		setting("voice.openai.model", "voice", "OpenAI transcription model",
			"OpenAI transcription model name.",
			TypeString, "VOICE_OPENAI_MODEL", "", validateShortText),
		enum("voice.openai.delay", "voice", "OpenAI transcription delay",
			"Latency and accuracy trade-off passed to OpenAI. Empty uses low.",
			"VOICE_OPENAI_DELAY", openAIDelays),
		setting("voice.qwen.url", "voice", "Qwen realtime URL",
			"Overrides the DashScope realtime endpoint.",
			TypeURL, "VOICE_QWEN_URL", "", validateOptionalURL),
		setting("voice.qwen.model", "voice", "Qwen model",
			"DashScope realtime ASR model name.",
			TypeString, "VOICE_QWEN_MODEL", "", validateShortText),
		setting("voice.qwen.language", "voice", "Qwen language",
			"Language hint for Qwen recognition.",
			TypeString, "VOICE_QWEN_LANGUAGE", "", validateShortText),
		setting("voice.soniox.url", "voice", "Soniox URL",
			"Overrides the Soniox realtime endpoint.",
			TypeURL, "VOICE_SONIOX_URL", "", validateOptionalURL),
		setting("voice.soniox.model", "voice", "Soniox model",
			"Soniox realtime model name.",
			TypeString, "VOICE_SONIOX_MODEL", "", validateShortText),

		setting("transport.journal_gc_disabled", "transport", "Disable journal cleanup",
			"Pauses transport journal retention cleanup.",
			TypeBool, "TRANSPORT_JOURNAL_GC_DISABLED", false, nil),
	}
	for i := range entries {
		if entries[i].Group == "search" {
			entries[i].Tester = testSearch
		}
	}
	return entries
}

func feature(name, label, description string) Entry {
	return Entry{
		Key:         FeatureKey(name),
		Kind:        KindFeature,
		Group:       "features",
		Label:       label,
		Description: description,
		Type:        TypeFeature,
		Default:     Feature{Stage: StageAdmin},
		Apply:       ApplyLive,
		Validate:    validateFeature,
	}
}

// voiceInputFeature follows VOICE_ADMIN_ONLY when no override exists:
// true (the default) keeps voice input admin-only, false opens it to all users.
func voiceInputFeature() Entry {
	entry := feature(FeatureVoiceInput, "Voice input", "Dictation in the composer.")
	entry.EnvVar = "VOICE_ADMIN_ONLY"
	entry.fromEnv = func(raw string) (any, error) {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "1", "true", "yes", "on":
			return Feature{Stage: StageAdmin}, nil
		default:
			return Feature{Stage: StageGA}, nil
		}
	}
	return entry
}

func setting(
	key, group, label, description, typ, envVar string,
	fallback any,
	validate func(any) error,
) Entry {
	return Entry{
		Key:         key,
		Kind:        KindSetting,
		Group:       group,
		Label:       label,
		Description: description,
		Type:        typ,
		EnvVar:      envVar,
		Default:     fallback,
		Apply:       ApplyLive,
		Validate:    validate,
	}
}

func enum(key, group, label, description, envVar string, options []string) Entry {
	entry := setting(key, group, label, description, TypeEnum, envVar, "", nil)
	entry.Options = append([]string(nil), options...)
	return entry
}

func validateShortText(v any) error {
	text, _ := v.(string)
	if len(text) > 200 {
		return errors.New("must be at most 200 characters")
	}
	if strings.ContainsAny(text, "\r\n") {
		return errors.New("must be a single line")
	}
	return nil
}

func validateOptionalURL(v any) error {
	text, _ := v.(string)
	if text == "" {
		return nil
	}
	return validateURL(text, "http", "https", "ws", "wss")
}

func validateFeature(v any) error {
	f, ok := v.(Feature)
	if !ok {
		return fmt.Errorf("must be a feature object")
	}
	return f.validate()
}
