package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/logging"
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

// configureVoice selects this instance's speech recognition provider. A bad
// configuration disables voice input instead of failing startup.
func (s *Service) configureVoice() {
	provider, err := voice.NewProvider(voiceSettings(s.cfg))
	if err != nil {
		logging.Warn(
			context.Background(),
			"voice input disabled",
			slog.String("provider", s.cfg.VoiceProvider),
			logging.Err(err),
		)
		return
	}
	s.voice = provider
}

func voiceSettings(cfg Config) voice.Settings {
	return voice.Settings{
		Provider:  cfg.VoiceProvider,
		Languages: cfg.VoiceLanguages,
		OpenAI: voice.OpenAISettings{
			APIKey: cfg.OpenAIAPIKey,
			URL:    cfg.VoiceOpenAIURL,
			Model:  cfg.VoiceOpenAIModel,
			Delay:  cfg.VoiceOpenAIDelay,
		},
		Qwen: voice.QwenSettings{
			APIKey:   cfg.DashScopeAPIKey,
			URL:      cfg.VoiceQwenURL,
			Model:    cfg.VoiceQwenModel,
			Language: cfg.VoiceQwenLanguage,
		},
		Soniox: voice.SonioxSettings{
			APIKey:    cfg.SonioxAPIKey,
			URL:       cfg.VoiceSonioxURL,
			Model:     cfg.VoiceSonioxModel,
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
	if !s.voiceAllowed(principal) {
		writeData(ctx, http.StatusOK, voiceConfigResponse{})
		return
	}
	writeData(ctx, http.StatusOK, voiceConfigResponse{
		Enabled:            true,
		Provider:           s.voice.Name(),
		SampleRate:         s.voice.SampleRate(),
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
	switch {
	case s.voice == nil:
		writeHTTPError(w, http.StatusServiceUnavailable, "voice input is not configured")
		return
	case !s.voiceAllowed(principal):
		writeHTTPError(w, http.StatusForbidden, "voice input is limited to administrators")
		return
	}
	provider := s.voice
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

func (s *Service) voiceAllowed(principal UserPrincipal) bool {
	return s.voice != nil && (!s.cfg.VoiceAdminOnly || principal.IsAdmin)
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
