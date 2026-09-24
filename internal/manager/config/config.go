package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const DefaultPaxdVerificationBaseURL = "https://paxworkspace.net"

type Config struct {
	Port                             string
	DatabaseURL                      string
	RegistrationToken                string
	RegistrationOwnerEmail           string
	LocalUserID                      string
	AllowLocalUserHeader             bool
	CloudflareAccessDisabled         bool
	CloudflareAccessIssuer           string
	CloudflareAccessAud              string
	CloudflareAccessJWKS             string
	CloudflareAccessMigrationEnabled bool
	CloudflareAccessMigrationIssuer  string
	CloudflareAccessMigrationAud     string
	CloudflareAccessMigrationJWKS    string
	AdminEmails                      map[string]bool
	TrustedCloudflareProxyCIDRs      string
	MaxBodyBytes                     int64
	APIRateLimitPerMinute            int
	APIRateLimitBurst                int
	RegisterLimitPerMinute           int
	RegisterLimitBurst               int
	PaxdArtifactDownloadTTL          time.Duration
	ObjectStorageBucket              string
	ObjectStorageRegion              string
	ObjectStorageEndpoint            string
	ObjectStoragePublicEndpoint      string
	ObjectStorageForcePathStyle      bool
	SessionArtifactUploadTTL         time.Duration
	PaxdVerificationBaseURL          string
	TeamMemexExecutor                string
	DeepSeekAPIKey                   string
	DeepSeekBaseURL                  string
	DeepSeekModel                    string
	DeepSeekTimeout                  time.Duration
	DeepSeekMaxTokens                int
	DeepSeekTemperature              float64
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
		CloudflareAccessMigrationEnabled: parseBool(
			os.Getenv("CLOUDFLARE_ACCESS_MIGRATION_ENABLED"),
		),
		CloudflareAccessMigrationIssuer: os.Getenv(
			"CLOUDFLARE_ACCESS_MIGRATION_ISSUER",
		),
		CloudflareAccessMigrationAud: os.Getenv("CLOUDFLARE_ACCESS_MIGRATION_AUD"),
		CloudflareAccessMigrationJWKS: os.Getenv(
			"CLOUDFLARE_ACCESS_MIGRATION_JWKS_URL",
		),
		AdminEmails:                 parseEmailSet(os.Getenv("ADMIN_EMAILS")),
		TrustedCloudflareProxyCIDRs: os.Getenv("TRUSTED_CLOUDFLARE_PROXY_CIDRS"),
		MaxBodyBytes:                parseInt64Env("MAX_BODY_BYTES", 1<<20),
		APIRateLimitPerMinute:       parseIntEnv("API_RATE_LIMIT_PER_MINUTE", 300),
		APIRateLimitBurst:           parseIntEnv("API_RATE_LIMIT_BURST", 60),
		RegisterLimitPerMinute:      parseIntEnv("REGISTER_RATE_LIMIT_PER_MINUTE", 30),
		RegisterLimitBurst:          parseIntEnv("REGISTER_RATE_LIMIT_BURST", 10),
		PaxdArtifactDownloadTTL: time.Duration(parseIntEnv(
			"PAXD_ARTIFACT_DOWNLOAD_URL_TTL_SECONDS",
			15*60,
		)) * time.Second,
		ObjectStorageBucket:         strings.TrimSpace(os.Getenv("OBJECT_STORAGE_BUCKET")),
		ObjectStorageRegion:         envDefault("OBJECT_STORAGE_REGION", "us-east-1"),
		ObjectStorageEndpoint:       strings.TrimSpace(os.Getenv("OBJECT_STORAGE_ENDPOINT")),
		ObjectStoragePublicEndpoint: objectStoragePublicEndpoint(),
		ObjectStorageForcePathStyle: parseBool(os.Getenv("OBJECT_STORAGE_FORCE_PATH_STYLE")),
		SessionArtifactUploadTTL: time.Duration(parseIntEnv(
			"SESSION_ARTIFACT_UPLOAD_URL_TTL_SECONDS",
			15*60,
		)) * time.Second,
		PaxdVerificationBaseURL: envDefault(
			"PAXD_VERIFICATION_BASE_URL",
			DefaultPaxdVerificationBaseURL,
		),
		TeamMemexExecutor: envDefault("TEAM_MEMEX_EXECUTOR", "dry_run"),
		DeepSeekAPIKey:    os.Getenv("DEEPSEEK_API_KEY"),
		DeepSeekBaseURL:   envDefault("DEEPSEEK_BASE_URL", "https://api.deepseek.com"),
		DeepSeekModel:     envDefault("DEEPSEEK_MODEL", "deepseek-v4-flash"),
		DeepSeekTimeout: time.Duration(
			parseIntEnv("DEEPSEEK_TIMEOUT_SECONDS", 120),
		) * time.Second,
		DeepSeekMaxTokens:   parseIntEnv("DEEPSEEK_MAX_TOKENS", 384000),
		DeepSeekTemperature: parseFloatEnv("DEEPSEEK_TEMPERATURE", 0.2),
	}
}

func (c Config) Validate() error {
	if _, err := c.CloudflareProxyPrefixes(); err != nil {
		return err
	}
	if err := c.validateCloudflareAccessMigration(); err != nil {
		return err
	}
	if strings.TrimSpace(c.ObjectStorageBucket) == "" {
		return errors.New("OBJECT_STORAGE_BUCKET is required")
	}
	return nil
}

func (c Config) validateCloudflareAccessMigration() error {
	if !c.CloudflareAccessMigrationEnabled {
		return nil
	}
	if c.CloudflareAccessDisabled {
		return errors.New(
			"CLOUDFLARE_ACCESS_MIGRATION_ENABLED cannot be used when CLOUDFLARE_ACCESS_DISABLED is true",
		)
	}
	if strings.TrimSpace(c.CloudflareAccessIssuer) == "" ||
		strings.TrimSpace(c.CloudflareAccessAud) == "" ||
		strings.TrimSpace(c.CloudflareAccessJWKS) == "" {
		return errors.New(
			"CLOUDFLARE_ACCESS_MIGRATION_ENABLED requires the primary Cloudflare Access issuer, audience, and JWKS URL",
		)
	}
	if strings.TrimSpace(c.CloudflareAccessMigrationIssuer) == "" ||
		strings.TrimSpace(c.CloudflareAccessMigrationAud) == "" ||
		strings.TrimSpace(c.CloudflareAccessMigrationJWKS) == "" {
		return errors.New(
			"CLOUDFLARE_ACCESS_MIGRATION_ENABLED requires the migration Cloudflare Access issuer, audience, and JWKS URL",
		)
	}
	return nil
}

func objectStoragePublicEndpoint() string {
	return envDefault("OBJECT_STORAGE_PUBLIC_ENDPOINT", os.Getenv("OBJECT_STORAGE_ENDPOINT"))
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

func parseFloatEnv(key string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 {
		return fallback
	}
	return v
}

// CloudflareProxyPrefixes returns only explicitly configured proxy networks.
func (c Config) CloudflareProxyPrefixes() ([]netip.Prefix, error) {
	if strings.TrimSpace(c.TrustedCloudflareProxyCIDRs) == "" {
		return nil, nil
	}
	var prefixes []netip.Prefix
	for _, raw := range strings.Split(c.TrustedCloudflareProxyCIDRs, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
			return nil, fmt.Errorf(
				"TRUSTED_CLOUDFLARE_PROXY_CIDRS contains an invalid or unrestricted CIDR",
			)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}
