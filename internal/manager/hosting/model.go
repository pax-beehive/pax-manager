// Package hosting owns Starter lifecycle, execution leases and model budgets.
package hosting

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

var (
	ErrDisabled            = errors.New("starter_disabled")
	ErrBudget              = errors.New("starter_quota_exhausted")
	ErrPlatformBudget      = errors.New("starter_platform_budget_exhausted")
	ErrConflict            = errors.New("starter_lease_conflict")
	ErrUnauthorized        = errors.New("starter_unauthorized")
	ErrNotFound            = errors.New("starter_not_found")
	ErrRateLimited         = errors.New("starter_rate_limited")
	ErrAccessPaused        = errors.New("starter_access_paused")
	ErrAdminRevision       = errors.New("hosting_admin_revision_conflict")
	ErrAdminUpdate         = errors.New("invalid_hosting_admin_update")
	ErrAdminModelConflict  = errors.New("hosting_model_conflict")
	ErrReasoningEffort     = errors.New("invalid_reasoning_effort")
	ErrInvalidModelRequest = errors.New("invalid_model_request")
	ErrModelDisabled       = errors.New("model_disabled")
)

type Config struct {
	HostKey, TokenSecret, PublicURL, CloudURL, GatewayURL, GatewayKey, Model string
	GrantMicrousd, GrantTokens, GlobalMicrousd                               int64
	InputPrice, OutputPrice                                                  int64 // Microusd per million tokens.
	MaxOutputTokens                                                          int64
	LeaseTTL                                                                 time.Duration
	Models                                                                   []ModelPolicy
	RequestsPerMinute, GlobalRequestsPerMinute                               int
}

func (c *Config) Enabled() bool {
	if len(c.HostKey) < 32 || len(c.TokenSecret) < 32 || len(c.GatewayKey) < 32 ||
		c.GatewayKey == c.HostKey || c.GatewayKey == c.TokenSecret {
		return false
	}
	for _, value := range []string{c.Model} {
		if value == "" {
			return false
		}
	}
	bounds := []struct{ value, min, max int64 }{
		{c.GrantMicrousd, 1, 1_000_000_000_000_000},
		{c.GrantTokens, 0, 1_000_000_000},
		{c.GlobalMicrousd, 0, 1_000_000_000_000_000},
		{c.MaxOutputTokens, 1, 8192},
	}
	for _, bound := range bounds {
		if bound.value < bound.min || bound.value > bound.max {
			return false
		}
	}
	if !c.validModelConfiguration() || c.RequestsPerMinute <= 0 || c.RequestsPerMinute > 100000 ||
		c.GlobalRequestsPerMinute <= 0 || c.GlobalRequestsPerMinute > 1000000 {
		return false
	}
	for _, raw := range []string{c.PublicURL, c.CloudURL, c.GatewayURL} {
		if !validEndpoint(raw) {
			return false
		}
	}
	return validGatewayEndpoint(c.GatewayURL)
}

func validGatewayEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || strings.TrimSpace(raw) != raw || u.ForceQuery {
		return false
	}
	return u.Port() == "" || u.Port() == "443"
}
func validEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" &&
		u.Fragment == ""
}

type Quota struct {
	PeriodStart       time.Time `json:"period_start"`
	ResetsAt          time.Time `json:"resets_at"`
	GrantedMicrousd   int64     `json:"granted_microusd"`
	SpentMicrousd     int64     `json:"spent_microusd"`
	ReservedMicrousd  int64     `json:"reserved_microusd"`
	RemainingMicrousd int64     `json:"remaining_microusd"`
	GrantedTokens     int64     `json:"granted_tokens"` // Zero means no monthly token limit.
	SpentTokens       int64     `json:"spent_tokens"`
	ReservedTokens    int64     `json:"reserved_tokens"`
	RemainingTokens   int64     `json:"remaining_tokens"`
}

func (q *Quota) Refresh() {
	q.RemainingMicrousd = max(0, q.GrantedMicrousd-q.SpentMicrousd-q.ReservedMicrousd)
	q.RemainingTokens = max(0, q.GrantedTokens-q.SpentTokens-q.ReservedTokens)
}

type Example struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
}
type Starter struct {
	EnvironmentID string    `json:"environment_id"`
	OwnerUserID   string    `json:"owner_user_id"`
	Name          string    `json:"name"`
	Plan          string    `json:"plan"`
	State         string    `json:"state"`
	NodeID        string    `json:"node_id,omitempty"`
	AgentID       string    `json:"agent_id,omitempty"`
	Quota         Quota     `json:"quota"`
	Examples      []Example `json:"examples"`
	ErrorCode     string    `json:"error_code,omitempty"`
	AccessPaused  bool      `json:"access_paused"`
	HostID        string    `json:"-"`
	Generation    int64     `json:"-"`
	Job           *Job      `json:"-"`
}

type Job struct {
	Kind                 string    `json:"kind,omitempty"`
	JobID                string    `json:"job_id"`
	EnvironmentID        string    `json:"environment_id"`
	OwnerUserID          string    `json:"owner_user_id"`
	Generation           int64     `json:"generation"`
	LeaseToken           string    `json:"lease_token"`
	LeaseExpiresAt       time.Time `json:"lease_expires_at"`
	Plan                 string    `json:"plan"`
	CloudURL             string    `json:"cloud_url"`
	RegistrationToken    string    `json:"registration_token,omitempty"`
	ModelBaseURL         string    `json:"model_base_url,omitempty"`
	ModelToken           string    `json:"model_token,omitempty"`
	ModelID              string    `json:"model_id,omitempty"`
	ModelMaxOutputTokens int64     `json:"model_max_output_tokens,omitempty"`
	Active               bool      `json:"active"`
	NodeID               string    `json:"node_id,omitempty"`
	AgentID              string    `json:"agent_id,omitempty"`
}

// EnvironmentRecord separates private durable fields from the user-facing view.
type EnvironmentRecord struct {
	Starter      Starter `json:"starter"`
	HostID       string  `json:"host_id"`
	Generation   int64   `json:"generation"`
	Job          *Job    `json:"job"`
	EnrolledNode bool    `json:"enrolled_node,omitempty"`
}
type Reservation struct {
	ID, EnvironmentID, RequestID, Status, UpstreamRequestID string
	Microusd, Tokens, ActualMicrousd, ActualTokens          int64
	InputPrice, OutputPrice                                 int64
	Model                                                   string
	RawUsage                                                json.RawMessage
	CreatedAt                                               time.Time
	Generation                                              int64
	GenerationStarted                                       bool
	AtomicAdmission                                         bool      `json:"atomic_admission,omitempty"`
	Protocol                                                string    `json:"protocol,omitempty"`
	ReasoningEffort                                         string    `json:"reasoning_effort,omitempty"`
	MaxOutputTokens                                         int64     `json:"max_output_tokens,omitempty"`
	Outcome                                                 string    `json:"outcome,omitempty"`
	UsageKnown                                              bool      `json:"usage_known"`
	ReconciliationRequired                                  bool      `json:"reconciliation_required,omitempty"`
	FinishedAt                                              time.Time `json:"finished_at,omitempty"`
	QuotaPeriodStart                                        time.Time `json:"quota_period_start,omitempty"`
	PoolPeriodStart                                         time.Time `json:"pool_period_start,omitempty"`
	Moderation                                              *ModerationDecision
	Policy                                                  *ModelPolicy `json:"policy,omitempty"`
	ErrorCode                                               string       `json:"error_code,omitempty"`
	InputTokens, OutputTokens                               int64
}
type Registration struct {
	OwnerUserID string
	ExpiresAt   time.Time
}
type State struct {
	Environments              map[string]*EnvironmentRecord `json:"environments"`
	Requests                  map[string]*Reservation       `json:"requests"`
	SpentMicrousd             int64                         `json:"spent_microusd"`
	ReservedMicrousd          int64                         `json:"reserved_microusd"`
	PoolPeriodStart           time.Time                     `json:"pool_period_start"`
	PoolResetsAt              time.Time                     `json:"pool_resets_at"`
	PoolSpentBaselineMicrousd int64                         `json:"pool_spent_baseline_microusd"`
	Registrations             map[string]Registration       `json:"-"`
	Admin                     AdminControls                 `json:"admin"`
	SandboxCapacity           *sandboxCapacityReport        `json:"sandbox_capacity,omitempty"`
}

func (s *State) Initialize() {
	if s.Environments == nil {
		s.Environments = map[string]*EnvironmentRecord{}
	}
	if s.Requests == nil {
		s.Requests = map[string]*Reservation{}
	}
	s.Registrations = map[string]Registration{}
	s.Admin.initialize()
}

type Store interface {
	WithHostingState(context.Context, func(*State) error) error
}
type Status struct {
	Enabled             bool      `json:"enabled"`
	EnrolledNode        bool      `json:"enrolled_node,omitempty"`
	PoolAvailable       bool      `json:"pool_available"`
	PoolPeriodStart     time.Time `json:"pool_period_start"`
	PoolResetsAt        time.Time `json:"pool_resets_at"`
	Starter             *Starter  `json:"starter"`
	UserBudgetAvailable bool      `json:"user_budget_available"`
}
type Claim struct {
	Kind            string  `json:"kind,omitempty"`
	HostID          string  `json:"host_id"`
	Capacity        int     `json:"capacity"`
	OwnerUserID     *string `json:"owner_user_id,omitempty"`
	EnvironmentID   *string `json:"environment_id,omitempty"`
	CapacityMissing bool    `json:"-"`
}

type WakeTarget struct {
	NodeID  string `json:"node_id"`
	AgentID string `json:"agent_id"`
}
type Update struct {
	Kind       string `json:"kind,omitempty"`
	HostID     string `json:"host_id"`
	JobID      string `json:"job_id"`
	Generation int64  `json:"generation"`
	LeaseToken string `json:"lease_token"`
	State      string `json:"state"`
	NodeID     string `json:"node_id"`
	AgentID    string `json:"agent_id"`
	ErrorCode  string `json:"error_code"`
}

func secret(prefix string) string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure randomness unavailable")
	}
	return prefix + hex.EncodeToString(b[:])
}
func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func sign(secret, environment string, generation int64) string {
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(fmt.Sprintf("%s:%d", environment, generation)),
	)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func tokenEnvironment(secret, token string) (string, int64, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", 0, ErrUnauthorized
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(parts[0]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return "", 0, ErrUnauthorized
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", 0, ErrUnauthorized
	}
	var environment string
	var generation int64
	payload := strings.Split(string(decoded), ":")
	if len(payload) != 2 {
		return "", 0, ErrUnauthorized
	}
	environment = payload[0]
	if _, err = fmt.Sscan(payload[1], &generation); err != nil {
		return "", 0, ErrUnauthorized
	}
	return environment, generation, nil
}
