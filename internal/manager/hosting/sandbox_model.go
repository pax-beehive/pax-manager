package hosting

import (
	"context"
	"crypto/subtle"
	"slices"
	"strings"
	"time"
)

const SandboxModelKind = "sandbox_model"

const sandboxModelWorkerFreshness = 30 * time.Second
const sandboxModelTokenPrefix = "sbm_"

type SandboxModelAccess struct {
	NodeID    string              `json:"node_id"`
	Harnesses []string            `json:"harnesses"`
	State     string              `json:"state"`
	Agents    []SandboxModelAgent `json:"agents"`
}

type SandboxModelAgent struct {
	AgentID    string `json:"agent_id"`
	State      string `json:"state"`
	ReasonCode string `json:"reason_code,omitempty"`
}

type sandboxModelWorker struct {
	HostID    string    `json:"host_id"`
	Harnesses []string  `json:"harnesses"`
	SeenAt    time.Time `json:"seen_at"`
}

type sandboxModelGrant struct {
	Owner      string             `json:"owner"`
	Parent     sandboxModelParent `json:"parent"`
	Generation int64              `json:"generation"`
	AgentID    string             `json:"agent_id"`
	Harness    string             `json:"harness"`
	State      string             `json:"state"`
	ReasonCode string             `json:"reason_code,omitempty"`
	Job        *Job               `json:"job,omitempty"`
}

type sandboxModelParent struct {
	NodeID        string `json:"node_id"`
	EnvironmentID string `json:"environment_id"`
	HostID        string `json:"host_id"`
	Generation    int64  `json:"generation"`
	JobID         string `json:"job_id"`
}

type sandboxModelStore interface {
	WithSandboxModelStates(context.Context, func(account, sandbox *State) error) error
}

func (s *Service) sandboxModelStore() (sandboxModelStore, bool) {
	if !s.Enabled() || s.Sandbox == nil || !s.Sandbox.configured || s.ModelAgentHarness == nil {
		return nil, false
	}
	store, ok := s.Store.(sandboxModelStore)
	return store, ok
}

func (s *Service) sandboxModelParent(
	sandbox *State,
	owner string,
	now time.Time,
) (sandboxModelParent, time.Time, bool) {
	record := sandbox.Environments[owner]
	if record == nil || record.Job == nil || record.Starter.State != "ready" ||
		record.Starter.OwnerUserID != owner || record.HostID != s.Sandbox.config.HostID ||
		record.Starter.NodeID == "" || record.Starter.EnvironmentID == "" || record.Generation < 1 {
		return sandboxModelParent{}, time.Time{}, false
	}
	job := record.Job
	if !job.Active || !job.LeaseExpiresAt.After(now) || job.Kind != SandboxKind ||
		job.JobID == "" ||
		job.OwnerUserID != owner ||
		job.EnvironmentID != record.Starter.EnvironmentID ||
		job.Generation != record.Generation ||
		job.NodeID != record.Starter.NodeID {
		return sandboxModelParent{}, time.Time{}, false
	}
	return sandboxModelParent{
		NodeID: record.Starter.NodeID, EnvironmentID: record.Starter.EnvironmentID,
		HostID: record.HostID, Generation: record.Generation, JobID: job.JobID,
	}, job.LeaseExpiresAt, true
}

func supportedSandboxHarness(harness string) bool {
	switch harness {
	case "pi", "codex", "claude-code", "kimi", "opencode", "hermes", "dsh", "dscode", "openclaw":
		return true
	default:
		return false
	}
}

func sandboxWorkerHarnesses(state *State, host string, now time.Time) []string {
	worker := state.SandboxModelWorker
	if worker == nil || worker.HostID != host {
		return nil
	}
	if age := now.Sub(worker.SeenAt); age < 0 || age > sandboxModelWorkerFreshness {
		return nil
	}
	return worker.Harnesses
}

func sandboxGrantState(grant *sandboxModelGrant, parent sandboxModelParent, now time.Time) string {
	if grant == nil || grant.Parent != parent {
		return "available"
	}
	if grant.Job != nil && (!grant.Job.Active || !grant.Job.LeaseExpiresAt.After(now)) {
		return "failed"
	}
	return grant.State
}

func sandboxModelFailureReason(state, code string) string {
	if state == "sleeping" && code == "" {
		return "lease_ended"
	}
	switch code {
	case "host_capacity", "host_environment_limit", "container_exited", "node_connection_timeout",
		"lease_ended", "lease_expired", "acp_probe_timeout", "acp_session_failed":
		return code
	default:
		return "model_setup_failed"
	}
}

func sandboxModelGrantKey(owner, agent string) string {
	return owner + "/" + agent
}

// IncludeSandboxModelAccess projects the AWS option without rebinding legacy Starter.
func (s *Service) IncludeSandboxModelAccess(
	ctx context.Context,
	owner string,
	status *Status,
) error {
	store, ok := s.sandboxModelStore()
	if !ok || !s.Sandbox.allowOwner(ctx, owner) {
		return nil
	}
	return store.WithSandboxModelStates(ctx, func(account, sandbox *State) error {
		now := s.Now()
		parent, _, ok := s.sandboxModelParent(sandbox, owner, now)
		if !ok {
			return nil
		}
		harnesses := sandboxWorkerHarnesses(account, parent.HostID, now)
		agents := sandboxModelAgents(account, owner, parent, now)
		if len(harnesses) == 0 && len(agents) == 0 {
			return nil
		}
		status.SandboxModelAccess = &SandboxModelAccess{
			NodeID: parent.NodeID, Harnesses: append([]string{}, harnesses...),
			State: "available", Agents: agents,
		}
		if account.Environments[owner] == nil {
			wallet := s.newStarter(account, owner)
			s.refreshQuota(account, wallet)
			status.UserBudgetAvailable = s.userBudgetAvailable(account, wallet.Starter.Quota)
		}
		if account.Admin.Users[owner].AccessPaused {
			status.UserBudgetAvailable = false
		}
		return nil
	})
}

func sandboxModelAgents(
	account *State, owner string, parent sandboxModelParent, now time.Time,
) []SandboxModelAgent {
	agents := []SandboxModelAgent{}
	for _, grant := range account.SandboxModelGrants {
		if grant == nil || grant.Owner != owner || grant.Parent != parent {
			continue
		}
		view := SandboxModelAgent{
			AgentID: grant.AgentID, State: sandboxGrantState(grant, parent, now),
		}
		if view.State == "failed" {
			view.ReasonCode = sandboxModelFailureReason(grant.State, grant.ReasonCode)
			if grant.Job != nil && grant.Job.Active && !grant.Job.LeaseExpiresAt.After(now) {
				view.ReasonCode = "lease_expired"
			}
		}
		agents = append(agents, view)
	}
	slices.SortFunc(agents, func(a, b SandboxModelAgent) int {
		return strings.Compare(a.AgentID, b.AgentID)
	})
	return agents
}

// WakeSandboxModel handles an AWS target independently from the legacy Pi row.
// A false match means the caller may continue through the legacy handler.
func (s *Service) WakeSandboxModel(
	ctx context.Context,
	owner string,
	target *WakeTarget,
) (bool, error) {
	store, ok := s.sandboxModelStore()
	if !ok {
		return false, nil
	}
	var matched bool
	if err := store.WithSandboxModelStates(ctx, func(_, sandbox *State) error {
		record := sandbox.Environments[owner]
		matched = record != nil && record.Starter.NodeID != "" && record.Starter.NodeID == target.NodeID
		return nil
	}); err != nil || !matched {
		return matched, err
	}
	if !s.Sandbox.allowOwner(ctx, owner) {
		return true, ErrUnauthorized
	}
	harness, err := s.ModelAgentHarness(ctx, owner, target.NodeID, target.AgentID)
	if err != nil || !supportedSandboxHarness(harness) {
		return true, ErrUnauthorized
	}
	err = store.WithSandboxModelStates(ctx, func(account, sandbox *State) error {
		now := s.Now()
		parent, _, ok := s.sandboxModelParent(sandbox, owner, now)
		if !ok || parent.NodeID != target.NodeID {
			return ErrConflict
		}
		key := sandboxModelGrantKey(owner, target.AgentID)
		prior := account.SandboxModelGrants[key]
		switch sandboxGrantState(prior, parent, now) {
		case "queued", "starting", "ready":
			return nil
		}
		if !slices.Contains(sandboxWorkerHarnesses(account, parent.HostID, now), harness) {
			return ErrDisabled
		}
		if _, err := s.sandboxModelWallet(account, owner); err != nil {
			return err
		}
		generation := int64(1)
		if prior != nil {
			generation = prior.Generation + 1
		}
		account.SandboxModelGrants[key] = &sandboxModelGrant{
			Owner: owner, Parent: parent, Generation: generation, AgentID: target.AgentID,
			Harness: harness, State: "queued",
		}
		return nil
	})
	return true, err
}

func (s *Service) sandboxModelWallet(account *State, owner string) (*EnvironmentRecord, error) {
	s.refreshPool(account)
	wallet := account.Environments[owner]
	if wallet == nil {
		wallet = s.newStarter(account, owner)
		account.Environments[owner] = wallet
	}
	s.refreshQuota(account, wallet)
	if wallet.Starter.AccessPaused || account.Admin.Users[owner].AccessPaused {
		return nil, ErrAccessPaused
	}
	if !s.poolAvailable(account) {
		return nil, ErrPlatformBudget
	}
	if !s.userBudgetAvailable(account, wallet.Starter.Quota) {
		return nil, ErrBudget
	}
	return wallet, nil
}

type sandboxModelCandidate struct {
	Owner      string
	Parent     sandboxModelParent
	AgentID    string
	Harness    string
	Generation int64
}

func (s *Service) ClaimSandboxModel(ctx context.Context, request *Claim) (*Job, error) {
	store, ok := s.sandboxModelStore()
	if !ok {
		return nil, ErrDisabled
	}
	if request == nil || request.Kind != SandboxModelKind ||
		request.HostID != s.Sandbox.config.HostID {
		return nil, ErrUnauthorized
	}
	harnesses, ok := validatedSandboxHarnesses(request.SupportedHarnesses, request.Capacity)
	if !ok || request.CapacityMissing || request.SupportedHarnesses == nil ||
		request.Capacity < 0 || request.Capacity > 100 ||
		request.OwnerUserID != nil || request.EnvironmentID != nil {
		return nil, ErrConflict
	}
	candidates, err := s.sandboxModelCandidates(ctx, store, request, harnesses)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if !s.sandboxModelAgentAllowed(ctx, candidate) {
			continue
		}
		job, err := s.claimSandboxModel(ctx, store, candidate, request.Capacity)
		if job != nil || err != nil {
			return job, err
		}
	}
	return nil, nil
}

func validatedSandboxHarnesses(reported []string, capacity int) ([]string, bool) {
	if (len(reported) == 0 && capacity != 0) || len(reported) > 9 {
		return nil, false
	}
	harnesses := slices.Clone(reported)
	slices.Sort(harnesses)
	for index, harness := range harnesses {
		if !supportedSandboxHarness(harness) || (index > 0 && harness == harnesses[index-1]) {
			return nil, false
		}
	}
	return harnesses, true
}

func (s *Service) sandboxModelCandidates(
	ctx context.Context,
	store sandboxModelStore,
	request *Claim,
	harnesses []string,
) ([]sandboxModelCandidate, error) {
	var candidates []sandboxModelCandidate
	err := store.WithSandboxModelStates(ctx, func(account, sandbox *State) error {
		now := s.Now()
		account.SandboxModelWorker = &sandboxModelWorker{request.HostID, harnesses, now}
		for _, grant := range account.SandboxModelGrants {
			if grant == nil || !slices.Contains(harnesses, grant.Harness) {
				continue
			}
			parent, _, ok := s.sandboxModelParent(sandbox, grant.Owner, now)
			if !ok || grant.Parent != parent {
				continue
			}
			state := sandboxGrantState(grant, parent, now)
			if state == "starting" || (state == "queued" && request.Capacity > 0) {
				candidates = append(candidates, sandboxModelCandidate{
					grant.Owner, parent, grant.AgentID, grant.Harness, grant.Generation,
				})
			}
		}
		return nil
	})
	slices.SortFunc(candidates, func(a, b sandboxModelCandidate) int {
		if order := strings.Compare(a.Owner, b.Owner); order != 0 {
			return order
		}
		return strings.Compare(a.AgentID, b.AgentID)
	})
	return candidates, err
}

func (s *Service) sandboxModelAgentAllowed(
	ctx context.Context,
	candidate sandboxModelCandidate,
) bool {
	if !s.Sandbox.allowOwner(ctx, candidate.Owner) {
		return false
	}
	harness, err := s.ModelAgentHarness(
		ctx, candidate.Owner, candidate.Parent.NodeID, candidate.AgentID,
	)
	return err == nil && harness == candidate.Harness
}

func (s *Service) claimSandboxModel(
	ctx context.Context,
	store sandboxModelStore,
	candidate sandboxModelCandidate,
	capacity int,
) (*Job, error) {
	var result *Job
	err := store.WithSandboxModelStates(ctx, func(account, sandbox *State) error {
		now := s.Now()
		parent, parentExpiry, ok := s.sandboxModelParent(sandbox, candidate.Owner, now)
		grant := account.SandboxModelGrants[sandboxModelGrantKey(candidate.Owner, candidate.AgentID)]
		if !ok || parent != candidate.Parent || !matchesSandboxCandidate(grant, candidate) {
			return nil
		}
		if !slices.Contains(sandboxWorkerHarnesses(account, parent.HostID, now), grant.Harness) {
			return nil
		}
		if _, err := s.sandboxModelWallet(account, candidate.Owner); err != nil {
			return nil
		}
		switch sandboxGrantState(grant, parent, now) {
		case "starting":
			replayed := *grant.Job
			result = &replayed
		case "queued":
			if capacity > 0 {
				result = s.startSandboxModel(account, grant, now, parentExpiry)
			}
		}
		return nil
	})
	return result, err
}

func matchesSandboxCandidate(grant *sandboxModelGrant, candidate sandboxModelCandidate) bool {
	return grant != nil && grant.Owner == candidate.Owner && grant.Parent == candidate.Parent &&
		grant.Generation == candidate.Generation &&
		grant.AgentID == candidate.AgentID &&
		grant.Harness == candidate.Harness
}

func (s *Service) startSandboxModel(
	account *State,
	grant *sandboxModelGrant,
	now, parentExpiry time.Time,
) *Job {
	grant.State = "starting"
	allocation := strings.Join([]string{grant.Owner, grant.Parent.NodeID, grant.AgentID}, "/")
	job := &Job{
		Kind: SandboxModelKind, JobID: secret("hjob_"),
		EnvironmentID: "smodel-" + hash(allocation)[:24], OwnerUserID: grant.Owner,
		Generation: grant.Generation, LeaseToken: secret("lease_"),
		LeaseExpiresAt: sandboxModelExpiry(now, s.Config.LeaseTTL, parentExpiry),
		Plan:           "free", CloudURL: strings.TrimRight(s.Sandbox.config.CloudURL, "/"),
		ModelBaseURL: strings.TrimRight(s.Config.GatewayURL, "/"),
		ModelID:      s.Config.Model, ModelMaxOutputTokens: s.defaultModelOutputLimit(account),
		Active: true, NodeID: grant.Parent.NodeID, AgentID: grant.AgentID, Harness: grant.Harness,
		SandboxEnvironmentID: grant.Parent.EnvironmentID, SandboxGeneration: grant.Parent.Generation,
	}
	job.ModelToken = s.signSandboxModel(grant, job.JobID)
	grant.Job = job
	result := *job
	return &result
}

func sandboxModelExpiry(now time.Time, ttl time.Duration, parentExpiry time.Time) time.Time {
	expires := now.Add(ttl)
	if parentExpiry.Before(expires) {
		return parentExpiry
	}
	return expires
}

func (s *Service) UpdateSandboxModel(
	ctx context.Context,
	request *Update,
	complete bool,
) (time.Time, error) {
	store, ok := s.sandboxModelStore()
	if !ok {
		return time.Time{}, ErrDisabled
	}
	if request == nil || request.Kind != SandboxModelKind ||
		request.HostID != s.Sandbox.config.HostID {
		return time.Time{}, ErrUnauthorized
	}
	if !validSandboxModelUpdate(request, complete) {
		return time.Time{}, ErrConflict
	}
	candidate, err := s.findSandboxModelCandidate(ctx, request)
	if err != nil {
		return time.Time{}, err
	}
	terminal := complete && request.State != "ready"
	if !terminal && !s.sandboxModelAgentAllowed(ctx, candidate) {
		return time.Time{}, ErrUnauthorized
	}
	var expiry time.Time
	err = store.WithSandboxModelStates(ctx, func(account, sandbox *State) error {
		grant := account.SandboxModelGrants[sandboxModelGrantKey(candidate.Owner, candidate.AgentID)]
		now := s.Now()
		if !matchesSandboxCandidate(grant, candidate) ||
			!matchesSandboxModelJob(grant, request, now) {
			return ErrConflict
		}
		if terminal {
			grant.Job.Active = false
			grant.State = "failed"
			grant.ReasonCode = sandboxModelFailureReason(request.State, request.ErrorCode)
			expiry = grant.Job.LeaseExpiresAt
			return nil
		}
		parent, parentExpiry, ok := s.sandboxModelParent(sandbox, candidate.Owner, now)
		if !ok || parent != candidate.Parent {
			return ErrUnauthorized
		}
		if _, err := s.sandboxModelWallet(account, candidate.Owner); err != nil {
			return err
		}
		if complete {
			if grant.AgentID != request.AgentID || parent.NodeID != request.NodeID {
				return ErrUnauthorized
			}
			grant.State = "ready"
		}
		expiry = sandboxModelExpiry(now, s.Config.LeaseTTL, parentExpiry)
		grant.Job.LeaseExpiresAt = expiry
		return nil
	})
	return expiry, err
}

func validSandboxModelUpdate(request *Update, complete bool) bool {
	if request.JobID == "" || request.LeaseToken == "" || request.Generation <= 0 {
		return false
	}
	if !complete {
		return (request.State == "" || request.State == "starting") &&
			request.NodeID == "" && request.AgentID == ""
	}
	switch request.State {
	case "ready":
		return request.NodeID != "" && request.AgentID != ""
	case "failed", "sleeping":
		return request.NodeID == "" && request.AgentID == ""
	default:
		return false
	}
}

func matchesSandboxModelJob(grant *sandboxModelGrant, request *Update, now time.Time) bool {
	if grant == nil || grant.Job == nil {
		return false
	}
	job := grant.Job
	return job.Kind == SandboxModelKind && job.JobID == request.JobID &&
		grant.Parent.HostID == request.HostID && job.Generation == request.Generation &&
		job.Active && job.LeaseExpiresAt.After(now) &&
		subtle.ConstantTimeCompare([]byte(job.LeaseToken), []byte(request.LeaseToken)) == 1
}

func (s *Service) findSandboxModelCandidate(
	ctx context.Context,
	request *Update,
) (sandboxModelCandidate, error) {
	var found sandboxModelCandidate
	err := s.Store.WithHostingState(ctx, func(account *State) error {
		now := s.Now()
		for _, grant := range account.SandboxModelGrants {
			if matchesSandboxModelJob(grant, request, now) {
				found = sandboxModelCandidate{
					grant.Owner,
					grant.Parent,
					grant.AgentID,
					grant.Harness,
					grant.Generation,
				}
				return nil
			}
		}
		return ErrConflict
	})
	return found, err
}

func (s *Service) signSandboxModel(grant *sandboxModelGrant, jobID string) string {
	subject := strings.Join(
		[]string{SandboxModelKind, grant.Owner, grant.Parent.NodeID, grant.AgentID, jobID},
		"/",
	)
	return sandboxModelTokenPrefix + sign(s.Config.TokenSecret, subject, grant.Generation)
}

func (s *Service) withActiveToken(
	ctx context.Context,
	token string,
	spend bool,
	run func(*State, *EnvironmentRecord, *sandboxModelGrant) error,
) error {
	signedModel, managed := strings.CutPrefix(token, sandboxModelTokenPrefix)
	if !managed {
		return s.Store.WithHostingState(ctx, func(state *State) error {
			wallet, err := s.active(state, token)
			if err != nil {
				return err
			}
			return run(state, wallet, nil)
		})
	}
	return s.withActiveSandboxModel(ctx, signedModel, spend, run)
}

func (s *Service) withActiveSandboxModel(
	ctx context.Context,
	signed string,
	spend bool,
	run func(*State, *EnvironmentRecord, *sandboxModelGrant) error,
) error {
	store, ok := s.sandboxModelStore()
	if !ok {
		return ErrUnauthorized
	}
	subject, generation, err := tokenEnvironment(s.Config.TokenSecret, signed)
	parts := strings.Split(subject, "/")
	if err != nil || len(parts) != 5 || parts[0] != SandboxModelKind {
		return ErrUnauthorized
	}
	owner, node, agent, jobID := parts[1], parts[2], parts[3], parts[4]
	if !s.Sandbox.allowOwner(ctx, owner) {
		return ErrUnauthorized
	}
	harness, err := s.ModelAgentHarness(ctx, owner, node, agent)
	if err != nil || !supportedSandboxHarness(harness) {
		return ErrUnauthorized
	}
	return store.WithSandboxModelStates(ctx, func(account, sandbox *State) error {
		now := s.Now()
		parent, _, ok := s.sandboxModelParent(sandbox, owner, now)
		grant := account.SandboxModelGrants[sandboxModelGrantKey(owner, agent)]
		wallet := account.Environments[owner]
		if !ok || wallet == nil || parent.NodeID != node ||
			!matchesSandboxCandidate(
				grant,
				sandboxModelCandidate{owner, parent, agent, harness, generation},
			) {
			return ErrUnauthorized
		}
		if grant.Job == nil || grant.Job.JobID != jobID {
			return ErrUnauthorized
		}
		switch sandboxGrantState(grant, parent, now) {
		case "ready":
		case "starting":
			if spend {
				return ErrUnauthorized
			}
		default:
			return ErrUnauthorized
		}
		if wallet.Starter.AccessPaused || account.Admin.Users[owner].AccessPaused {
			return ErrAccessPaused
		}
		return run(account, wallet, grant)
	})
}
