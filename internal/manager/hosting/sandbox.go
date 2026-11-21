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

const sandboxCapacityFreshness = 20 * time.Second

type SandboxConfig struct {
	HostKey, HostID, CloudURL string
	LeaseTTL                  time.Duration
}

type SandboxStore interface {
	WithSandboxState(context.Context, func(*State) error) error
}

type SandboxService struct {
	store        SandboxStore
	config       SandboxConfig
	configured   bool
	now          func() time.Time
	allowOwner   func(context.Context, string) bool
	validateNode func(context.Context, string, string) error
}

type SandboxStatus struct {
	Sandbox *SandboxView `json:"sandbox"`
}

type SandboxView struct {
	Status     string `json:"status"`
	NodeID     string `json:"node_id,omitempty"`
	ReasonCode string `json:"reason_code,omitempty"`
	Generation int64  `json:"generation,omitempty"`
}

type sandboxCapacityReport struct {
	HostID         string    `json:"host_id"`
	AvailableSlots int       `json:"available_slots"`
	ObservedAt     time.Time `json:"observed_at"`
}

func NewSandbox(
	store SandboxStore,
	config SandboxConfig,
	now func() time.Time,
	allowOwner func(context.Context, string) bool,
	validateNode func(context.Context, string, string) error,
) *SandboxService {
	if config.LeaseTTL <= 0 {
		config.LeaseTTL = 180 * time.Second
	}
	if now == nil {
		now = time.Now
	}
	return &SandboxService{
		store: store, config: config, now: now, allowOwner: allowOwner, validateNode: validateNode,
		configured: store != nil && allowOwner != nil && validateNode != nil && config.valid(),
	}
}

func (config SandboxConfig) valid() bool {
	if len(config.HostKey) < 32 || !hostPattern.MatchString(config.HostID) ||
		!validEndpoint(config.CloudURL) || !validGatewayEndpoint(config.CloudURL) {
		return false
	}
	origin, err := url.Parse(config.CloudURL)
	return err == nil && (origin.EscapedPath() == "" || origin.EscapedPath() == "/")
}

func (s *SandboxService) HostAuthorized(token string) bool {
	return s != nil && s.configured &&
		subtle.ConstantTimeCompare([]byte(token), []byte(s.config.HostKey)) == 1
}

func (s *SandboxService) Status(
	ctx context.Context,
	owner string,
	wake bool,
) (*SandboxStatus, error) {
	if !s.availableTo(ctx, owner) {
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
		now := s.now()
		expireSandbox(record, now)
		if wake && sandboxStopped(record) {
			queueSandbox(state, record)
		}
		result.Sandbox = s.sandboxView(state, record, now)
		return nil
	})
	return result, err
}

func (s *SandboxService) Resume(ctx context.Context, owner, nodeID string) (*SandboxStatus, error) {
	return s.reopen(ctx, owner, nodeID, 0)
}

func (s *SandboxService) Restart(
	ctx context.Context,
	owner, nodeID string,
	generation int64,
) (*SandboxStatus, error) {
	if generation <= 0 {
		return nil, ErrConflict
	}
	return s.reopen(ctx, owner, nodeID, generation)
}

func (s *SandboxService) availableTo(ctx context.Context, owner string) bool {
	return s != nil && s.configured && owner != "" && s.allowOwner(ctx, owner)
}

func (s *SandboxService) reopen(
	ctx context.Context,
	owner, nodeID string,
	restartGeneration int64,
) (*SandboxStatus, error) {
	if !s.availableTo(ctx, owner) || nodeID == "" || s.validateNode(ctx, owner, nodeID) != nil {
		return nil, ErrNotFound
	}
	result := &SandboxStatus{}
	err := s.store.WithSandboxState(ctx, func(state *State) error {
		record := state.Environments[owner]
		if record == nil || record.Starter.OwnerUserID != owner || record.Starter.NodeID != nodeID {
			return ErrNotFound
		}
		now := s.now()
		expireSandbox(record, now)
		if restartGeneration == 0 && sandboxStopped(record) {
			queueSandbox(state, record)
		}
		// A retry of a restart already accepted for this generation only reads the new state.
		if restartGeneration > 0 && restartGeneration == record.Generation &&
			(sandboxStopped(record) || record.Starter.State == "ready") {
			queueSandbox(state, record)
		}
		result.Sandbox = s.sandboxView(state, record, now)
		return nil
	})
	return result, err
}

func sandboxStopped(record *EnvironmentRecord) bool {
	return record.Starter.State == "sleeping" || record.Starter.State == "failed"
}

func queueSandbox(state *State, record *EnvironmentRecord) {
	if record.Job != nil && record.Job.RegistrationToken != "" {
		delete(state.Registrations, hash(record.Job.RegistrationToken))
	}
	record.Generation++
	record.Starter.State = "queued"
	record.Starter.ErrorCode = ""
	record.Job = nil
}

func (s *SandboxService) sandboxView(
	state *State,
	record *EnvironmentRecord,
	now time.Time,
) *SandboxView {
	view := &SandboxView{
		Status:     record.Starter.State,
		NodeID:     record.Starter.NodeID,
		ReasonCode: s.sandboxReason(record, state.SandboxCapacity, now),
	}
	if view.NodeID != "" {
		view.Generation = record.Generation
	}
	return view
}

func (s *SandboxService) sandboxReason(
	record *EnvironmentRecord,
	capacity *sandboxCapacityReport,
	now time.Time,
) string {
	switch record.Starter.State {
	case "failed":
		return sandboxFailureReason(record.Starter.ErrorCode)
	case "queued":
		if capacity == nil || capacity.HostID != s.config.HostID || capacity.AvailableSlots != 0 ||
			!sandboxOnHost(record, s.config.HostID) {
			return ""
		}
		age := now.Sub(capacity.ObservedAt)
		if age >= 0 && age <= sandboxCapacityFreshness {
			return "waiting_for_capacity"
		}
	}
	return ""
}

func sandboxFailureReason(code string) string {
	switch code {
	case "host_environment_limit", "container_exited", "node_connection_timeout":
		return code
	default:
		return "host_start_failed"
	}
}

func expireSandbox(record *EnvironmentRecord, now time.Time) {
	if record.Job != nil && record.Job.Active && !record.Job.LeaseExpiresAt.After(now) {
		record.Job.Active = false
		record.Job.RegistrationToken = ""
		record.Starter.State = "sleeping"
	}
}

func (s *SandboxService) Claim(ctx context.Context, request *Claim) (*Job, error) {
	if s == nil || !s.configured {
		return nil, ErrDisabled
	}
	if request == nil || request.Kind != SandboxKind || request.HostID != s.config.HostID {
		return nil, ErrUnauthorized
	}
	if request.Capacity < 0 || request.Capacity > 100 || request.OwnerUserID != nil ||
		request.EnvironmentID != nil {
		return nil, ErrConflict
	}
	candidates, err := s.sandboxClaimCandidates(ctx, request)
	if err != nil || len(candidates) == 0 {
		return nil, err
	}
	var result *Job
	err = s.store.WithSandboxState(ctx, func(state *State) error {
		now := s.now()
		var next *EnvironmentRecord
		for _, candidate := range candidates {
			record := state.Environments[candidate.owner]
			if !sandboxOnHost(record, request.HostID) || record.Generation != candidate.generation {
				continue
			}
			expireSandbox(record, now)
			if sandboxStarting(record) {
				copy := *record.Job
				result = &copy
				return nil
			}
			if next == nil && request.Capacity > 0 && record.Starter.State == "queued" {
				next = record
			}
		}
		if next != nil {
			result = s.claimSandbox(state, next, now)
		}
		return nil
	})
	return result, err
}

type sandboxClaimCandidate struct {
	owner      string
	generation int64
}

func (s *SandboxService) sandboxClaimCandidates(
	ctx context.Context,
	request *Claim,
) ([]sandboxClaimCandidate, error) {
	var candidates []sandboxClaimCandidate
	err := s.store.WithSandboxState(ctx, func(state *State) error {
		now := s.now()
		if !request.CapacityMissing {
			state.SandboxCapacity = &sandboxCapacityReport{
				HostID: request.HostID, AvailableSlots: request.Capacity, ObservedAt: now,
			}
		}
		for owner, record := range state.Environments {
			if !sandboxOnHost(record, request.HostID) || record.Starter.OwnerUserID != owner {
				continue
			}
			expireSandbox(record, now)
			if sandboxStarting(record) ||
				(request.Capacity > 0 && record.Starter.State == "queued") {
				candidates = append(candidates, sandboxClaimCandidate{
					owner:      owner,
					generation: record.Generation,
				})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Resolving an owner's feature access may need another database connection.
	allowed := candidates[:0]
	for _, candidate := range candidates {
		if s.allowOwner(ctx, candidate.owner) {
			allowed = append(allowed, candidate)
		}
	}
	sort.Slice(allowed, func(i, j int) bool { return allowed[i].owner < allowed[j].owner })
	return allowed, nil
}

func sandboxOnHost(record *EnvironmentRecord, hostID string) bool {
	return record != nil && (record.HostID == "" || record.HostID == hostID)
}

func sandboxStarting(record *EnvironmentRecord) bool {
	return record.Starter.State == "starting" && record.Job != nil && record.Job.Active
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
	if s == nil || !s.configured {
		return time.Time{}, ErrDisabled
	}
	if request == nil || request.Kind != SandboxKind || request.HostID != s.config.HostID {
		return time.Time{}, ErrUnauthorized
	}
	if err := validateSandboxUpdate(request, complete); err != nil {
		return time.Time{}, err
	}
	terminal := complete && request.State != "ready"
	owner, allowed, err := s.sandboxUpdateAccess(ctx, request, terminal)
	if err != nil {
		return time.Time{}, err
	}
	var expiry time.Time
	err = s.store.WithSandboxState(ctx, func(state *State) error {
		now := s.now()
		record, err := matchSandboxLease(state, request, now)
		if err != nil {
			return err
		}
		if !terminal && record.Starter.OwnerUserID != owner {
			return ErrConflict
		}
		if !allowed {
			record.Job.LeaseExpiresAt = now
			expireSandbox(record, now)
			return nil
		}
		expiry = now.Add(s.config.LeaseTTL)
		record.Job.LeaseExpiresAt = expiry
		if request.State == "ready" {
			record.Job.NodeID = request.NodeID
		}
		if terminal {
			record.Job.RegistrationToken = ""
		}
		applyUpdate(record, request, complete)
		if complete && record.Starter.State == "failed" {
			record.Starter.ErrorCode = sandboxFailureReason(request.ErrorCode)
		}
		return nil
	})
	if err == nil && !allowed {
		return time.Time{}, ErrUnauthorized
	}
	return expiry, err
}

func (s *SandboxService) sandboxUpdateAccess(
	ctx context.Context,
	request *Update,
	terminal bool,
) (owner string, allowed bool, err error) {
	// A terminal update can close an active lease after the feature is disabled.
	if terminal {
		return "", true, nil
	}
	owner, err = s.sandboxLeaseOwner(ctx, request)
	if err != nil {
		return "", false, err
	}
	if !s.allowOwner(ctx, owner) {
		return owner, false, nil
	}
	if request.State == "ready" && s.validateNode(ctx, owner, request.NodeID) != nil {
		return "", false, ErrUnauthorized
	}
	return owner, true, nil
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

func (s *SandboxService) sandboxLeaseOwner(ctx context.Context, request *Update) (string, error) {
	var owner string
	err := s.store.WithSandboxState(ctx, func(state *State) error {
		record, err := matchSandboxLease(state, request, s.now())
		if err == nil {
			owner = record.Starter.OwnerUserID
		}
		return err
	})
	return owner, err
}

func matchSandboxLease(
	state *State,
	request *Update,
	now time.Time,
) (*EnvironmentRecord, error) {
	for _, record := range state.Environments {
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
