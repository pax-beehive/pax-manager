package runtimeconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	SourceDB      = "db"
	SourceEnv     = "env"
	SourceDefault = "default"
)

// Value is the effective value of one entry.
type Value struct {
	Entry     *Entry
	Value     any
	Source    string
	Version   int64
	UpdatedAt time.Time
	UpdatedBy string
	// Row is the stored override when Source is db; nil otherwise.
	Row *domain.RuntimeConfigRow
}

// Resolved is an immutable snapshot. Readers never lock.
type Resolved struct {
	Revision    int64
	entries     []Entry
	values      map[string]Value
	credentials map[string]string
}

// SkippedRow records a database override that was ignored. Reason never
// contains the stored value.
type SkippedRow struct {
	Key    string
	Reason string
}

// EnvLookup returns the environment form of a variable; empty means unset.
type EnvLookup func(name string) string

// resolve builds a snapshot from registry, environment, and overrides. Each
// bad override is skipped on its own so one row cannot block the others.
func resolve(
	entries []Entry,
	env EnvLookup,
	rows []domain.RuntimeConfigRow,
	revision int64,
) (*Resolved, []SkippedRow) {
	byKey := make(map[string]domain.RuntimeConfigRow, len(rows))
	var skipped []SkippedRow
	known := map[string]bool{}
	for i := range entries {
		known[entries[i].Key] = true
	}
	for _, row := range rows {
		if !known[row.Key] {
			skipped = append(skipped, SkippedRow{Key: row.Key, Reason: "key is not registered"})
			continue
		}
		byKey[row.Key] = row
	}
	resolved := &Resolved{
		Revision:    revision,
		entries:     entries,
		values:      make(map[string]Value, len(entries)),
		credentials: map[string]string{},
	}
	if env != nil {
		for _, name := range credentialEnvVars {
			resolved.credentials[name] = env(name)
		}
	}
	for i := range entries {
		entry := &entries[i]
		value := baseValue(entry, env)
		if row, ok := byKey[entry.Key]; ok {
			override, err := rowValue(entry, row)
			if err != nil {
				skipped = append(skipped, SkippedRow{Key: entry.Key, Reason: err.Error()})
			} else {
				stored := row
				value = Value{
					Entry: entry, Value: override, Source: SourceDB,
					Version: row.Version, UpdatedAt: row.UpdatedAt, UpdatedBy: row.UpdatedBy,
					Row: &stored,
				}
			}
		}
		resolved.values[entry.Key] = value
	}
	return resolved, skipped
}

func baseValue(entry *Entry, env EnvLookup) Value {
	if entry.EnvVar != "" && env != nil {
		if raw := env(entry.EnvVar); raw != "" && raw != defaultEnvForm(entry) {
			if parsed, err := parseEnv(entry, raw); err == nil {
				return Value{Entry: entry, Value: parsed, Source: SourceEnv}
			}
		}
	}
	return Value{Entry: entry, Value: entry.Default, Source: SourceDefault}
}

// defaultEnvForm is the environment form that config.Load produces when the
// variable is unset, so such values report the default source.
func defaultEnvForm(entry *Entry) string {
	switch typed := entry.Default.(type) {
	case string:
		return typed
	case bool:
		if entry.fromEnv != nil {
			return ""
		}
		return fmt.Sprint(typed)
	case Feature:
		// VOICE_ADMIN_ONLY defaults to true, which equals the admin stage.
		if typed.Stage == StageAdmin {
			return "true"
		}
	}
	return ""
}

func rowValue(entry *Entry, row domain.RuntimeConfigRow) (any, error) {
	value, err := decodeValue(entry, row.ValueJSON)
	if err != nil {
		return nil, fmt.Errorf("stored value is invalid: %s", err.Error())
	}
	return value, nil
}

// Get returns the effective value of a key, or false when it is unknown.
func (r *Resolved) Get(key string) (Value, bool) {
	value, ok := r.values[key]
	return value, ok
}

// Entries returns registry entries in display order.
func (r *Resolved) Entries() []Entry { return r.entries }

func (r *Resolved) String(key string) string {
	value, _ := r.values[key].Value.(string)
	return value
}

func (r *Resolved) Bool(key string) bool {
	value, _ := r.values[key].Value.(bool)
	return value
}

// Enabled is a pure decision over this snapshot. Unknown features are off.
func (r *Resolved) Enabled(p domain.UserPrincipal, featureKey string) bool {
	value, ok := r.values[FeatureKey(featureKey)]
	if !ok {
		return false
	}
	f, ok := value.Value.(Feature)
	return ok && f.Allows(p)
}

// FeatureStage returns the stage of a feature, used by background jobs to
// skip owner lookups when the answer does not depend on the owner.
func (r *Resolved) FeatureStage(featureKey string) string {
	f, _ := r.values[FeatureKey(featureKey)].Value.(Feature)
	if f.Stage == "" {
		return StageOff
	}
	return f.Stage
}

// Features returns every feature decision for one principal, keyed by short name.
func (r *Resolved) Features(p domain.UserPrincipal) map[string]bool {
	out := map[string]bool{}
	for i := range r.entries {
		entry := &r.entries[i]
		if entry.Kind != KindFeature {
			continue
		}
		out[strings.TrimPrefix(entry.Key, featurePrefix)] = r.Enabled(p, entry.Key)
	}
	return out
}

type SearchConfig struct {
	URL    string
	Index  string
	APIKey string
}

func (r *Resolved) Search() SearchConfig {
	index := r.String("search.index")
	if index == "" {
		index = "pax-search-v1"
	}
	return SearchConfig{
		URL:    r.String("search.url"),
		Index:  index,
		APIKey: r.credentials["SEARCH_API_KEY"],
	}
}

// Fingerprint identifies a configuration without exposing the key.
func (c SearchConfig) Fingerprint() string {
	return fingerprint(c.URL, c.Index, c.APIKey)
}

type VoiceOpenAI struct{ APIKey, URL, Model, Delay string }

type VoiceQwen struct{ APIKey, URL, Model, Language string }

type VoiceSoniox struct{ APIKey, URL, Model string }

type VoiceConfig struct {
	Provider string
	OpenAI   VoiceOpenAI
	Qwen     VoiceQwen
	Soniox   VoiceSoniox
}

func (r *Resolved) Voice() VoiceConfig {
	return VoiceConfig{
		Provider: r.String("voice.provider"),
		OpenAI: VoiceOpenAI{
			APIKey: r.credentials["OPENAI_API_KEY"],
			URL:    r.String("voice.openai.url"),
			Model:  r.String("voice.openai.model"),
			Delay:  r.String("voice.openai.delay"),
		},
		Qwen: VoiceQwen{
			APIKey:   r.credentials["DASHSCOPE_API_KEY"],
			URL:      r.String("voice.qwen.url"),
			Model:    r.String("voice.qwen.model"),
			Language: r.String("voice.qwen.language"),
		},
		Soniox: VoiceSoniox{
			APIKey: r.credentials["SONIOX_API_KEY"],
			URL:    r.String("voice.soniox.url"),
			Model:  r.String("voice.soniox.model"),
		},
	}
}

func (c VoiceConfig) Fingerprint() string {
	return fingerprint(
		c.Provider,
		c.OpenAI.APIKey, c.OpenAI.URL, c.OpenAI.Model, c.OpenAI.Delay,
		c.Qwen.APIKey, c.Qwen.URL, c.Qwen.Model, c.Qwen.Language,
		c.Soniox.APIKey, c.Soniox.URL, c.Soniox.Model,
	)
}

func fingerprint(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(hash, "%d:%s;", len(part), part)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
