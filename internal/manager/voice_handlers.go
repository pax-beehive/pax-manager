package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/logging"
	"github.com/pax-beehive/pax-manager/internal/manager/runtimeconfig"
	"github.com/pax-beehive/pax-manager/internal/manager/voice"
)

// One client frame is a slice of PCM audio; 64 KiB is over a second at 24 kHz.
const voiceMaxClientFrameBytes = 64 << 10

type voiceConfigResponse struct {
	Enabled            bool   `json:"enabled"`
	Provider           string `json:"provider,omitempty"`
	SampleRate         int    `json:"sample_rate,omitempty"`
	Encoding           string `json:"encoding,omitempty"`
	MaxDurationSeconds int    `json:"max_duration_seconds,omitempty"`
}

// voiceRuntime rebuilds the provider only when the effective voice settings
// change, so a configuration update applies to the next dictation session.
type voiceRuntime struct {
	mu          sync.Mutex
	fingerprint string
	built       bool
	provider    voice.Provider
	// build defaults to voice.NewProvider; tests replace it.
	build func(voice.Settings) (voice.Provider, error)
}

// configureVoice validates the startup settings. A bad configuration
// disables voice input instead of failing startup.
func (s *Service) configureVoice() {
	_ = s.voiceProvider()
}

// voiceProvider returns the provider for the current snapshot, or nil.
func (s *Service) voiceProvider() voice.Provider {
	current := s.runtimeConfig.Voice()
	settings := voiceSettings(s.cfg, current)
	key := current.Fingerprint()
	v := &s.voice
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.built && v.fingerprint == key {
		return v.provider
	}
	build := v.build
	if build == nil {
		build = voice.NewProvider
	}
	provider, err := build(settings)
	if err != nil {
		logging.Warn(
			context.Background(),
			"voice input disabled",
			slog.String("provider", settings.Provider),
			logging.Err(err),
		)
		provider = nil
	}
	v.fingerprint, v.built, v.provider = key, true, provider
	return provider
}

// voiceSettings combines live settings with environment-only settings
// (languages and the SeedASR providers).
func voiceSettings(cfg Config, live runtimeconfig.VoiceConfig) voice.Settings {
	return voice.Settings{
		Provider:  live.Provider,
		Languages: cfg.VoiceLanguages,
		OpenAI: voice.OpenAISettings{
			APIKey: live.OpenAI.APIKey,
			URL:    live.OpenAI.URL,
			Model:  live.OpenAI.Model,
			Delay:  live.OpenAI.Delay,
		},
		Qwen: voice.QwenSettings{
			APIKey:   live.Qwen.APIKey,
			URL:      live.Qwen.URL,
			Model:    live.Qwen.Model,
			Language: live.Qwen.Language,
		},
		Soniox: voice.SonioxSettings{
			APIKey:    live.Soniox.APIKey,
			URL:       live.Soniox.URL,
			Model:     live.Soniox.Model,
			Languages: cfg.VoiceLanguages,
		},
		SeedASR: voice.SeedASRSettings{
			URL:        cfg.VoiceSeedASRURL,
			APIKey:     cfg.VoiceSeedASRAPIKey,
			AppKey:     cfg.VoiceSeedASRAppKey,
			AccessKey:  cfg.VoiceSeedASRAccessKey,
			ResourceID: cfg.VoiceSeedASRResourceID,
		},
	}
}

func (s *Service) handleGetVoiceConfig(c context.Context, ctx *app.RequestContext) {
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	ctx.Header("Cache-Control", "no-store")
	provider := s.voiceProvider()
	if !s.voiceAllowed(provider, principal) {
		writeData(ctx, http.StatusOK, voiceConfigResponse{})
		return
	}
	writeData(ctx, http.StatusOK, voiceConfigResponse{
		Enabled:            true,
		Provider:           provider.Name(),
		SampleRate:         provider.SampleRate(),
		Encoding:           voice.Encoding,
		MaxDurationSeconds: int(s.voiceMaxSession() / time.Second),
	})
}

func (s *Service) handleVoiceStream(w http.ResponseWriter, r *http.Request) {
	ctx, logID := httpRequestLogContext(r.Context(), r)
	w.Header().Set(logging.HeaderLogID, logID)
	r = r.WithContext(ctx)
	principal, err := s.auth.Principal(ctx, httpRequestMetadata(r))
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	provider := s.voiceProvider()
	switch {
	case provider == nil:
		writeHTTPError(w, http.StatusServiceUnavailable, "voice input is not configured")
		return
	case !s.voiceAllowed(provider, principal):
		writeHTTPError(w, http.StatusForbidden, "voice input is limited to administrators")
		return
	}
	conn, err := upgrader.Upgrade(w, r, websocketResponseHeader(ctx))
	if err != nil {
		logging.Warn(ctx, "voice stream upgrade failed", logging.Err(err))
		return
	}
	defer func() { _ = conn.Close() }()
	conn.SetReadLimit(voiceMaxClientFrameBytes)
	ctx = logging.With(
		ctx,
		slog.String("owner_user_id", principal.User.UserID),
		slog.String("voice_provider", provider.Name()),
	)
	started := time.Now()
	logging.Info(ctx, "voice stream connected")
	err = voice.Serve(
		ctx,
		conn,
		provider,
		voice.Limits{MaxDuration: s.voiceMaxSession()},
		voiceUserRef(principal.User.UserID),
	)
	elapsed := slog.Duration("elapsed", time.Since(started))
	if err != nil {
		logging.Warn(ctx, "voice stream failed", logging.Err(err), elapsed)
		return
	}
	logging.Info(ctx, "voice stream closed", elapsed)
}

// voiceAllowed follows feature.voice_input, which defaults to
// VOICE_ADMIN_ONLY when no override exists.
func (s *Service) voiceAllowed(provider voice.Provider, principal UserPrincipal) bool {
	return provider != nil &&
		s.runtimeConfig.Feature(principal, runtimeconfig.FeatureVoiceInput)
}

func (s *Service) voiceMaxSession() time.Duration {
	if s.cfg.VoiceMaxSession > 0 {
		return s.cfg.VoiceMaxSession
	}
	return voice.DefaultMaxDuration
}

// voiceUserRef lets the provider attribute abuse to a stable user without
// receiving the user id itself.
func voiceUserRef(userID string) string {
	sum := sha256.Sum256([]byte("pax-voice:" + userID))
	return hex.EncodeToString(sum[:16])
}
