package hosting

import (
	"context"
	"crypto/subtle"
	"net/url"
	"sort"
	"strings"
	"time"
)

const SandboxKind = "node_only"

type SandboxConfig struct {
	HostKey, HostID, CloudURL string
	Owners                    []string
	LeaseTTL                  time.Duration
}

type SandboxStore interface {
	WithSandboxState(context.Context, func(*State) error) error
}

type SandboxService struct {
	store        SandboxStore
	config       SandboxConfig
	owners       map[string]struct{}
	ordered      []string
	now          func() time.Time
	validateNode func(context.Context, string, string) error
}

type SandboxStatus struct {
	Sandbox *SandboxView `json:"sandbox"`
}

type SandboxView struct {
	Status string `json:"status"`
	NodeID string `json:"node_id,omitempty"`
}

func NewSandbox(
	store SandboxStore,
	config SandboxConfig,
	now func() time.Time,
	validateNode func(context.Context, string, string) error,
) *SandboxService {
	if config.LeaseTTL <= 0 {
		config.LeaseTTL = 180 * time.Second
	}
	if now == nil {
		now = time.Now
	}
	service := &SandboxService{store: store, config: config, now: now, validateNode: validateNode}
	if store == nil || validateNode == nil || !config.valid() {
		return service
	}
	service.owners = make(map[string]struct{}, len(config.Owners))
	for _, owner := range config.Owners {
		if owner != "" && strings.TrimSpace(owner) == owner {
			service.owners[owner] = struct{}{}
		}
	}
	for owner := range service.owners {
		service.ordered = append(service.ordered, owner)
	}
	sort.Strings(service.ordered)
	return service
}

func (config SandboxConfig) valid() bool {
	if len(config.HostKey) < 32 || !hostPattern.MatchString(config.HostID) ||
		!validEndpoint(config.CloudURL) || !validGatewayEndpoint(config.CloudURL) {
		return false
	}
	origin, err := url.Parse(config.CloudURL)
	return err == nil && (origin.EscapedPath() == "" || origin.EscapedPath() == "/")
}

func (s *SandboxService) EnabledFor(owner string) bool {
	if s == nil {
		return false
	}
	_, allowed := s.owners[owner]
	return allowed
}

func (s *SandboxService) HostAuthorized(token string) bool {
	return s != nil && len(s.owners) != 0 &&
		subtle.ConstantTimeCompare([]byte(token), []byte(s.config.HostKey)) == 1
}

func (s *SandboxService) Status(
	ctx context.Context,
	owner string,
	wake bool,
) (*SandboxStatus, error) {
	if !s.EnabledFor(owner) {
		return nil, ErrNotFound
	}
	result := &SandboxStatus{}
	err := s.store.WithSandboxState(ctx, func(state *State) error {
		record := state.Environments[owner]
		if record == nil {
			if !wake {
				return nil
			}
			record = &EnvironmentRecord{Starter: Starter{
				EnvironmentID: "snode-" + hash(owner)[:24],
				OwnerUserID:   owner,
				Plan:          "free",
				State:         "sleeping",
			}}
			state.Environments[owner] = record
		}
		expireSandbox(record, s.now())
		if wake && (record.Starter.State == "sleeping" || record.Starter.State == "failed") {
			record.Generation++
			record.Starter.State = "queued"
			record.Starter.ErrorCode = ""
			record.Job = nil
		}
		result.Sandbox = &SandboxView{Status: record.Starter.State, NodeID: record.Starter.NodeID}
		return nil
	})
	return result, err
}

func expireSandbox(record *EnvironmentRecord, now time.Time) {
	if record.Job != nil && record.Job.Active && !record.Job.LeaseExpiresAt.After(now) {
		record.Job.Active = false
		record.Job.RegistrationToken = ""
		record.Starter.State = "sleeping"
	}
}

func (s *SandboxService) Claim(ctx context.Context, request *Claim) (*Job, error) {
	if s == nil || len(s.owners) == 0 {
		return nil, ErrDisabled
	}
	if request == nil || request.Kind != SandboxKind || request.HostID != s.config.HostID {
		return nil, ErrUnauthorized
	}
	if request.Capacity < 0 || request.Capacity > 100 || request.OwnerUserID != nil ||
		request.EnvironmentID != nil {
		return nil, ErrConflict
	}
	var result *Job
	err := s.store.WithSandboxState(ctx, func(state *State) error {
		now := s.now()
		var next *EnvironmentRecord
		for _, owner := range s.ordered {
			record := state.Environments[owner]
			if record == nil || (record.HostID != "" && record.HostID != request.HostID) {
				continue
			}
			expireSandbox(record, now)
			if record.Starter.State == "starting" && record.Job != nil && record.Job.Active {
				copy := *record.Job
				result = &copy
				return nil
			}
			if next == nil && record.Starter.State == "queued" {
				next = record
			}
		}
		if next != nil && request.Capacity > 0 {
			result = s.claimSandbox(state, next, now)
		}
		return nil
	})
	return result, err
}

func (s *SandboxService) claimSandbox(state *State, record *EnvironmentRecord, now time.Time) *Job {
	record.HostID = s.config.HostID
	record.Starter.State = "starting"
	job := &Job{
		Kind:           SandboxKind,
		JobID:          secret("hjob_"),
		EnvironmentID:  record.Starter.EnvironmentID,
		OwnerUserID:    record.Starter.OwnerUserID,
		Generation:     record.Generation,
		LeaseToken:     secret("lease_"),
		LeaseExpiresAt: now.Add(s.config.LeaseTTL),
		Plan:           "free",
		CloudURL:       strings.TrimSuffix(s.config.CloudURL, "/"),
		Active:         true,
		NodeID:         record.Starter.NodeID,
	}
	if job.NodeID == "" {
		job.RegistrationToken = secret("reg_")
		state.Registrations[hash(job.RegistrationToken)] = Registration{
			OwnerUserID: job.OwnerUserID,
			ExpiresAt:   job.LeaseExpiresAt,
		}
	}
	record.Job = job
	copy := *job
	return &copy
}

func (s *SandboxService) Update(
	ctx context.Context,
	request *Update,
	complete bool,
) (time.Time, error) {
	if s == nil || len(s.owners) == 0 {
		return time.Time{}, ErrDisabled
	}
	if request == nil || request.Kind != SandboxKind || request.HostID != s.config.HostID {
		return time.Time{}, ErrUnauthorized
	}
	if err := validateSandboxUpdate(request, complete); err != nil {
		return time.Time{}, err
	}
	if request.State == "ready" {
		if err := s.validateReadySandbox(ctx, request); err != nil {
			return time.Time{}, err
		}
	}
	var expiry time.Time
	err := s.store.WithSandboxState(ctx, func(state *State) error {
		now := s.now()
		record, err := s.matchSandboxLease(state, request, now)
		if err != nil {
			return err
		}
		expiry = now.Add(s.config.LeaseTTL)
		record.Job.LeaseExpiresAt = expiry
		if request.State == "ready" {
			record.Job.NodeID = request.NodeID
		}
		if request.State == "sleeping" || request.State == "failed" {
			record.Job.RegistrationToken = ""
		}
		applyUpdate(record, request, complete)
		return nil
	})
	return expiry, err
}

func validateSandboxUpdate(request *Update, complete bool) error {
	if request.JobID == "" || request.Generation <= 0 || request.LeaseToken == "" ||
		request.AgentID != "" {
		return ErrConflict
	}
	if !complete {
		if (request.State != "" && request.State != "starting") || request.NodeID != "" {
			return ErrConflict
		}
		return nil
	}
	switch request.State {
	case "ready":
		if request.NodeID != "" {
			return nil
		}
	case "sleeping", "failed":
		if request.NodeID == "" {
			return nil
		}
	}
	return ErrConflict
}

func (s *SandboxService) validateReadySandbox(ctx context.Context, request *Update) error {
	var owner string
	err := s.store.WithSandboxState(ctx, func(state *State) error {
		record, err := s.matchSandboxLease(state, request, s.now())
		if err == nil {
			owner = record.Starter.OwnerUserID
		}
		return err
	})
	if err != nil {
		return err
	}
	if s.validateNode(ctx, owner, request.NodeID) != nil {
		return ErrUnauthorized
	}
	return nil
}

func (s *SandboxService) matchSandboxLease(
	state *State,
	request *Update,
	now time.Time,
) (*EnvironmentRecord, error) {
	for _, owner := range s.ordered {
		record := state.Environments[owner]
		if record == nil || record.Job == nil || record.Job.JobID != request.JobID {
			continue
		}
		job := record.Job
		if job.Kind != SandboxKind || !job.Active || record.HostID != request.HostID ||
			job.Generation != request.Generation || !job.LeaseExpiresAt.After(now) ||
			subtle.ConstantTimeCompare([]byte(job.LeaseToken), []byte(request.LeaseToken)) != 1 {
			return nil, ErrConflict
		}
		if request.State == "ready" && record.Starter.NodeID != "" &&
			record.Starter.NodeID != request.NodeID {
			return nil, ErrUnauthorized
		}
		return record, nil
	}
	return nil, ErrNotFound
}
