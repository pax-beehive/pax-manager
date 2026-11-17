package hosting

import (
	"context"
	"crypto/subtle"
	"regexp"
	"sort"
	"strings"
	"time"
)

type BindingCheck func(context.Context, string, string, string) error
type Service struct {
	Config          Config
	Store           Store
	Now             func() time.Time
	ValidateBinding BindingCheck
}

func New(config *Config, store Store, now func() time.Time, binding BindingCheck) *Service {
	copy := *config
	copy.Models = append([]ModelPolicy(nil), config.Models...)
	if policy, configured := copy.configuredModelPolicy(copy.Model); configured {
		copy.MaxOutputTokens = policy.MaxOutputTokens
	}
	if copy.RequestsPerMinute == 0 {
		copy.RequestsPerMinute = 20
	}
	if copy.GlobalRequestsPerMinute == 0 {
		copy.GlobalRequestsPerMinute = 200
	}
	if copy.LeaseTTL < 120*time.Second {
		copy.LeaseTTL = 180 * time.Second
	}
	if now == nil {
		now = time.Now
	}
	return &Service{Config: copy, Store: store, Now: now, ValidateBinding: binding}
}
func (s *Service) Enabled() bool { return s != nil && s.Store != nil && s.Config.Enabled() }
func (s *Service) HostAuthorized(token string) bool {
	return s.Enabled() && subtle.ConstantTimeCompare([]byte(token), []byte(s.Config.HostKey)) == 1
}

func examples() []Example {
	return []Example{
		{
			ID:     "website",
			Title:  "Build a sample page",
			Prompt: "Read the starter files in /workspace/starter. Build a small HTML project overview and explain how to preview it.",
		},
		{
			ID:     "analysis",
			Title:  "Analyze sample data",
			Prompt: "Read /workspace/starter/sales.csv. Calculate total revenue by product, write a summary to /workspace/analysis.md, and explain the result.",
		},
		{
			ID:     "code",
			Title:  "Improve a small project",
			Prompt: "Inspect the sample project in /workspace/starter, make one useful improvement, and show how you verified it.",
		},
	}
}

func (s *Service) Status(ctx context.Context, owner string, ensure bool) (*Status, error) {
	result := &Status{Enabled: s.Enabled()}
	if !result.Enabled {
		if ensure {
			return nil, ErrDisabled
		}
		return result, nil
	}
	err := s.Store.WithHostingState(ctx, func(state *State) error {
		s.reconcile(state)
		s.refreshPool(state)
		result.PoolAvailable = s.poolAvailable(state)
		result.PoolPeriodStart, result.PoolResetsAt = state.PoolPeriodStart, state.PoolResetsAt
		record := state.Environments[owner]
		if record == nil && ensure {
			money, tokens := s.userGrant(state, owner)
			id := "starter-" + hash(owner)[:24]
			record = &EnvironmentRecord{
				Starter: Starter{
					EnvironmentID: id,
					OwnerUserID:   owner,
					Name:          "Starter",
					Plan:          "free",
					State:         "sleeping",
					Examples:      examples(),
					Quota: Quota{
						GrantedMicrousd: money,
						GrantedTokens:   tokens,
					},
				},
			}
			state.Environments[owner] = record
		}
		if record != nil {
			s.expire(record)
			s.refreshQuota(state, record)
			copy := record.Starter
			result.Starter = &copy
		}
		return nil
	})
	return result, err
}

func (s *Service) expire(record *EnvironmentRecord) {
	if record.Job != nil && record.Job.Active && !record.Job.LeaseExpiresAt.After(s.Now()) {
		record.Job.Active = false
		record.Starter.State = "sleeping"
		record.Starter.ErrorCode = "execution_lease_expired"
	}
}

func (s *Service) Wake(ctx context.Context, owner string) (*Status, error) {
	if _, err := s.Status(ctx, owner, true); err != nil {
		return nil, err
	}
	err := s.Store.WithHostingState(ctx, func(state *State) error {
		record := state.Environments[owner]
		s.refreshPool(state)
		s.expire(record)
		s.refreshQuota(state, record)
		if record.Starter.AccessPaused {
			return ErrAccessPaused
		}
		if !s.poolAvailable(state) {
			return ErrPlatformBudget
		}
		zeroPriceAvailable := s.hasZeroPriceModel(state)
		if record.Starter.Quota.RemainingTokens <= 0 ||
			(record.Starter.Quota.RemainingMicrousd <= 0 && !zeroPriceAvailable) {
			return ErrBudget
		}
		if record.Starter.State == "ready" || record.Starter.State == "starting" ||
			record.Starter.State == "queued" {
			return nil
		}
		record.Generation++
		record.Starter.State = "queued"
		record.Starter.ErrorCode = ""
		record.Job = nil
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Status(ctx, owner, false)
}

var hostPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func (s *Service) Claim(ctx context.Context, request *Claim) (*Job, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if !hostPattern.MatchString(request.HostID) || request.Capacity < 0 || request.Capacity > 100 {
		return nil, ErrConflict
	}
	var result *Job
	err := s.Store.WithHostingState(ctx, func(state *State) error {
		owners := make([]string, 0, len(state.Environments))
		for owner := range state.Environments {
			owners = append(owners, owner)
		}
		sort.Strings(owners)
		for _, owner := range owners {
			record := state.Environments[owner]
			s.expire(record)
			if record.Starter.AccessPaused {
				continue
			}
			if record.HostID != "" && record.HostID != request.HostID {
				continue
			}
			// A lost claim response can be recovered by the same trusted host.
			if record.Job != nil && record.Job.Active && record.Starter.State == "starting" {
				copy := *record.Job
				result = &copy
				return nil
			}
			if record.Starter.State != "queued" || request.Capacity == 0 {
				continue
			}
			record.HostID = request.HostID
			record.Starter.State = "starting"
			job := &Job{
				JobID:                secret("hjob_"),
				EnvironmentID:        record.Starter.EnvironmentID,
				OwnerUserID:          owner,
				Generation:           record.Generation,
				LeaseToken:           secret("lease_"),
				LeaseExpiresAt:       s.Now().Add(s.Config.LeaseTTL),
				Plan:                 "free",
				CloudURL:             strings.TrimRight(s.Config.CloudURL, "/"),
				ModelBaseURL:         strings.TrimRight(s.Config.GatewayURL, "/"),
				ModelID:              s.Config.Model,
				ModelMaxOutputTokens: s.Config.MaxOutputTokens,
				Active:               true,
			}
			job.ModelToken = sign(s.Config.TokenSecret, job.EnvironmentID, job.Generation)
			if record.Starter.NodeID == "" {
				job.RegistrationToken = secret("reg_")
				state.Registrations[hash(job.RegistrationToken)] = Registration{
					OwnerUserID: owner,
					ExpiresAt:   s.Now().Add(24 * time.Hour),
				}
			}
			record.Job = job
			copy := *job
			result = &copy
			return nil
		}
		return nil
	})
	return result, err
}

func (s *Service) Update(ctx context.Context, request *Update, complete bool) (time.Time, error) {
	if !s.Enabled() {
		return time.Time{}, ErrDisabled
	}
	switch request.State {
	case "", "starting", "ready", "sleeping", "failed":
	default:
		return time.Time{}, ErrConflict
	}
	if request.State == "ready" {
		if err := s.validateReady(ctx, request); err != nil {
			return time.Time{}, err
		}
	}
	var expiry time.Time
	err := s.Store.WithHostingState(ctx, func(state *State) error {
		for _, record := range state.Environments {
			job := record.Job
			if job == nil || job.JobID != request.JobID {
				continue
			}
			if !s.leaseMatches(record, request) {
				return ErrConflict
			}
			job.LeaseExpiresAt = s.Now().Add(s.Config.LeaseTTL)
			expiry = job.LeaseExpiresAt
			applyUpdate(record, request, complete)
			return nil
		}
		return ErrNotFound
	})
	return expiry, err
}
func (s *Service) validateReady(ctx context.Context, request *Update) error {
	if request.NodeID == "" || request.AgentID == "" || s.ValidateBinding == nil {
		return ErrConflict
	}
	var owner string
	err := s.Store.WithHostingState(ctx, func(state *State) error {
		for key, record := range state.Environments {
			if record.Job != nil && record.Job.JobID == request.JobID {
				owner = key
				break
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if owner == "" || s.ValidateBinding(ctx, owner, request.NodeID, request.AgentID) != nil {
		return ErrUnauthorized
	}
	return nil
}
func (s *Service) leaseMatches(record *EnvironmentRecord, request *Update) bool {
	job := record.Job
	return job.Active && record.HostID == request.HostID && job.Generation == request.Generation &&
		subtle.ConstantTimeCompare([]byte(job.LeaseToken), []byte(request.LeaseToken)) == 1 &&
		job.LeaseExpiresAt.After(s.Now())
}
func applyUpdate(record *EnvironmentRecord, request *Update, complete bool) {
	job := record.Job
	if request.State == "ready" {
		record.Starter.NodeID = request.NodeID
		record.Starter.AgentID = request.AgentID
		record.Starter.State = "ready"
		record.Starter.ErrorCode = ""
		job.RegistrationToken = ""
	}
	if !complete {
		return
	}
	if request.State != "failed" && request.State != "sleeping" {
		return
	}
	job.Active = false
	record.Starter.State = request.State
	record.Starter.ErrorCode = ""
	if request.State == "failed" {
		record.Starter.ErrorCode = "host_start_failed"
	}
	if request.State == "failed" && request.ErrorCode == "host_capacity" {
		record.Starter.State = "queued"
		record.Starter.ErrorCode = "host_capacity"
		record.Job = nil
		record.Generation++
	}
}

func (s *Service) active(state *State, token string) (*EnvironmentRecord, error) {
	environment, generation, err := tokenEnvironment(s.Config.TokenSecret, token)
	if err != nil {
		return nil, err
	}
	for _, record := range state.Environments {
		if record.Starter.EnvironmentID == environment && record.Generation == generation &&
			record.Job != nil &&
			record.Job.Active &&
			record.Job.LeaseExpiresAt.After(s.Now()) {
			if record.Starter.AccessPaused {
				return nil, ErrAccessPaused
			}
			return record, nil
		}
	}
	return nil, ErrUnauthorized
}

func (s *Service) Reserve(
	ctx context.Context,
	token, requestID string,
	tokens int64,
) (*Reservation, error) {
	return s.reserve(ctx, token, requestID, tokens, false, nil)
}

// ReserveModerated records the pre-generation stage atomically with admission.
func (s *Service) ReserveModerated(
	ctx context.Context,
	token, requestID string,
	tokens int64,
) (*Reservation, error) {
	return s.reserve(ctx, token, requestID, tokens, true, nil)
}

func (s *Service) reserve(
	ctx context.Context,
	token, requestID string,
	tokens int64,
	moderated bool,
	selection *reservationPolicyClaim,
) (*Reservation, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if !validReservationRequest(requestID, tokens) {
		return nil, ErrConflict
	}
	var result *Reservation
	err := s.Store.WithHostingState(ctx, func(state *State) error {
		s.reconcile(state)
		s.refreshPool(state)
		record, err := s.active(state, token)
		if err != nil {
			return err
		}
		policy, err := s.selectReservationPolicy(state, selection)
		if err != nil {
			return err
		}
		key := record.Starter.EnvironmentID + ":" + requestID
		if _, ok := state.Requests[key]; ok {
			return ErrConflict
		}
		if err := s.checkRequestRate(state, record.Starter.EnvironmentID); err != nil {
			return err
		}
		model, inputPrice, outputPrice := s.Config.Model, s.Config.InputPrice, s.Config.OutputPrice
		if policy != nil {
			model, inputPrice, outputPrice = policy.ID, policy.InputPrice, policy.OutputPrice
		}
		s.refreshQuota(state, record)
		q := &record.Starter.Quota
		cost := s.reservationCost(tokens, policy)
		if !s.poolAvailable(state) ||
			poolCommitted(state)+cost > s.poolLimit(state) {
			return ErrPlatformBudget
		}
		if q.RemainingMicrousd < cost || q.RemainingTokens < tokens {
			return ErrBudget
		}
		for _, reservation := range state.Requests {
			if reservation.EnvironmentID == record.Starter.EnvironmentID &&
				reservation.Status == "reserved" {
				return ErrConflict
			}
		}
		result = &Reservation{
			ID:            key,
			EnvironmentID: record.Starter.EnvironmentID,
			RequestID:     requestID,
			Status:        "reserved",
			Microusd:      cost,
			Tokens:        tokens,
			InputPrice:    inputPrice,
			OutputPrice:   outputPrice,
			Model:         model,
			CreatedAt:     s.Now(),
			Generation:    record.Generation,
		}
		if policy != nil {
			snapshot := *policy
			result.Policy = &snapshot
		}
		if moderated {
			result.Moderation = &ModerationDecision{
				Verdict:       "pending",
				Model:         ModerationModel,
				PolicyVersion: reservationPolicyVersion(result),
			}
		}
		state.Requests[key] = result
		q.ReservedMicrousd += cost
		q.ReservedTokens += tokens
		state.ReservedMicrousd += cost
		return nil
	})
	return result, err
}

func price(tokens, rate int64) int64 { return (tokens*rate + 999_999) / 1_000_000 }

func (s *Service) Settle(
	ctx context.Context,
	id string,
	input, output int64,
	raw []byte,
	upstreamID string,
	known bool,
) error {
	return s.SettleWithError(ctx, id, input, output, raw, upstreamID, known, "")
}

// ValidSettlementErrorCode excludes provider text, prompts and credentials.
func ValidSettlementErrorCode(code string) bool {
	switch code {
	case "", "model_upstream_unavailable", "model_upstream_failed", "invalid_model_response",
		"model_stream_failed", "usage_unavailable":
		return true
	default:
		return false
	}
}

func (s *Service) SettleWithError(
	ctx context.Context,
	id string,
	input, output int64,
	raw []byte,
	upstreamID string,
	known bool,
	errorCode string,
) error {
	if !ValidSettlementErrorCode(errorCode) {
		return ErrAdminUpdate
	}
	return s.Store.WithHostingState(ctx, func(state *State) error {
		r := state.Requests[id]
		if r == nil {
			return ErrNotFound
		}
		if r.Moderation != nil && !r.GenerationStarted {
			return ErrConflict
		}
		if r.Status != "reserved" {
			return nil
		}
		var record *EnvironmentRecord
		for _, candidate := range state.Environments {
			if candidate.Starter.EnvironmentID == r.EnvironmentID {
				record = candidate
				break
			}
		}
		if record == nil {
			return ErrNotFound
		}
		cost, tokens := r.Microusd, r.Tokens
		if known && input >= 0 && output >= 0 {
			cost = price(input, r.InputPrice) + price(output, r.OutputPrice)
			tokens = input + output
			r.Status = "settled"
			r.InputTokens, r.OutputTokens = input, output
		} else {
			r.Status = "conservative"
		}
		r.ActualMicrousd = cost
		r.ActualTokens = tokens
		r.RawUsage = append([]byte(nil), raw...)
		r.UpstreamRequestID = upstreamID
		r.ErrorCode = errorCode
		q := &record.Starter.Quota
		q.ReservedMicrousd -= r.Microusd
		q.ReservedTokens -= r.Tokens
		q.SpentMicrousd += cost
		q.SpentTokens += tokens
		q.Refresh()
		state.ReservedMicrousd -= r.Microusd
		state.SpentMicrousd += cost
		return nil
	})
}

func (s *Service) reconcile(state *State) {
	for _, r := range state.Requests {
		if r.Status != "reserved" || s.Now().Before(r.CreatedAt.Add(3*time.Minute)) {
			continue
		}
		for _, record := range state.Environments {
			if record.Starter.EnvironmentID != r.EnvironmentID {
				continue
			}
			if r.Moderation != nil && !r.GenerationStarted {
				releasePreGeneration(state, record, r, "moderation_interrupted")
				break
			}
			r.Status = "conservative"
			r.ErrorCode = "usage_unavailable"
			r.ActualMicrousd = r.Microusd
			r.ActualTokens = r.Tokens
			q := &record.Starter.Quota
			q.ReservedMicrousd -= r.Microusd
			q.ReservedTokens -= r.Tokens
			q.SpentMicrousd += r.Microusd
			q.SpentTokens += r.Tokens
			q.Refresh()
			state.ReservedMicrousd -= r.Microusd
			state.SpentMicrousd += r.Microusd
			break
		}
	}
}
