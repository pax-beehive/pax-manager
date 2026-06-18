package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type Config struct {
	Port                              string
	DatabaseURL                       string
	RegistrationToken                 string
	RegistrationOwnerEmail            string
	LocalUserID                       string
	AllowLocalUserHeader              bool
	CloudflareAccessDisabled          bool
	CloudflareAccessIssuer            string
	CloudflareAccessAud               string
	CloudflareAccessJWKS              string
	AdminEmails                       map[string]bool
	MaxBodyBytes                      int64
	APIRateLimitPerMinute             int
	APIRateLimitBurst                 int
	RegisterLimitPerMinute            int
	RegisterLimitBurst                int
	PaxdArtifactDownloadTTL           time.Duration
	PaxdArtifactUploadAudience        string
	PaxdArtifactUploadPrincipals      map[string]bool
	PaxdArtifactSigningServiceAccount string
	PaxdArtifactGCSMock               bool
}

func Load() Config {
	return Config{
		Port:                     envDefault("PORT", "9879"),
		DatabaseURL:              os.Getenv("DATABASE_URL"),
		RegistrationToken:        os.Getenv("REGISTRATION_TOKEN"),
		RegistrationOwnerEmail:   os.Getenv("REGISTRATION_TOKEN_OWNER_EMAIL"),
		LocalUserID:              envDefault("LOCAL_USER_ID", "local@example.local"),
		AllowLocalUserHeader:     parseBool(os.Getenv("ALLOW_LOCAL_USER_HEADER")),
		CloudflareAccessDisabled: parseBool(os.Getenv("CLOUDFLARE_ACCESS_DISABLED")),
		CloudflareAccessIssuer:   os.Getenv("CLOUDFLARE_ACCESS_ISSUER"),
		CloudflareAccessAud:      os.Getenv("CLOUDFLARE_ACCESS_AUD"),
		CloudflareAccessJWKS:     os.Getenv("CLOUDFLARE_ACCESS_JWKS_URL"),
		AdminEmails:              parseEmailSet(os.Getenv("ADMIN_EMAILS")),
		MaxBodyBytes:             parseInt64Env("MAX_BODY_BYTES", 1<<20),
		APIRateLimitPerMinute:    parseIntEnv("API_RATE_LIMIT_PER_MINUTE", 300),
		APIRateLimitBurst:        parseIntEnv("API_RATE_LIMIT_BURST", 60),
		RegisterLimitPerMinute:   parseIntEnv("REGISTER_RATE_LIMIT_PER_MINUTE", 30),
		RegisterLimitBurst:       parseIntEnv("REGISTER_RATE_LIMIT_BURST", 10),
		PaxdArtifactDownloadTTL: time.Duration(parseIntEnv(
			"PAXD_ARTIFACT_DOWNLOAD_URL_TTL_SECONDS",
			15*60,
		)) * time.Second,
		PaxdArtifactUploadAudience: os.Getenv("PAXD_ARTIFACT_UPLOAD_AUDIENCE"),
		PaxdArtifactUploadPrincipals: parseStringSet(
			os.Getenv("PAXD_ARTIFACT_UPLOAD_PRINCIPALS"),
		),
		PaxdArtifactSigningServiceAccount: os.Getenv("PAXD_ARTIFACT_SIGNING_SERVICE_ACCOUNT"),
		PaxdArtifactGCSMock:               parseBool(os.Getenv("PAXD_ARTIFACT_GCS_MOCK")),
	}
}

func MergeAdminEmails(extra map[string]bool) map[string]bool {
	out := DefaultAdminEmails()
	for email, enabled := range extra {
		if enabled {
			out[domain.NormalizeEmail(email)] = true
		}
	}
	return out
}

func DefaultAdminEmails() map[string]bool {
	return map[string]bool{
		"toddzheng024@gmail.com":      true,
		"gengcongkai456789@gmail.com": true,
		"zhangjiahang0725@gmail.com":  true,
	}
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func ParseEmailSet(raw string) map[string]bool {
	out := MergeAdminEmails(nil)
	for _, part := range strings.Split(raw, ",") {
		email := domain.NormalizeEmail(part)
		if email != "" {
			out[email] = true
		}
	}
	return out
}

func parseEmailSet(raw string) map[string]bool {
	return ParseEmailSet(raw)
}

func parseStringSet(raw string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		value := strings.TrimSpace(part)
		if value != "" {
			out[value] = true
		}
	}
	return out
}

func parseBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parseIntEnv(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func parseInt64Env(key string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}
