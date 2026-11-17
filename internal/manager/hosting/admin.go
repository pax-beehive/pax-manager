package hosting

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const maxAdminAudit = 1000

// AdminControls contains operator policies and model identities, never credentials.
type AdminControls struct {
	Revision        int64                         `json:"revision"`
	PoolMicrousd    *int64                        `json:"pool_microusd,omitempty"`
	DefaultMicrousd *int64                        `json:"default_microusd,omitempty"`
	DefaultTokens   *int64                        `json:"default_tokens,omitempty"`
	Users           map[string]AdminUserOverride  `json:"users,omitempty"`
	Models          map[string]AdminModelOverride `json:"models,omitempty"`
	CreatedModels   map[string]ModelPolicy        `json:"created_models,omitempty"`
	Audit           []AdminAudit                  `json:"audit,omitempty"`
}

type AdminUserOverride struct {
	MonthlyMicrousd *int64 `json:"monthly_microusd,omitempty"`
	MonthlyTokens   *int64 `json:"monthly_tokens,omitempty"`
	AccessPaused    bool   `json:"access_paused"`
}

// Base binds an override to its configured identity and safety policy. A changed
// deployment configuration must be explicitly approved again by the operator.
type AdminModelOverride struct {
	Base   ModelPolicy `json:"base"`
	Policy ModelPolicy `json:"policy"`
}

func (a *AdminControls) initialize() {
	if a.Users == nil {
		a.Users = map[string]AdminUserOverride{}
	}
	if a.Models == nil {
		a.Models = map[string]AdminModelOverride{}
	}
	if a.CreatedModels == nil {
		a.CreatedModels = map[string]ModelPolicy{}
	}
}

type AdminQuery struct {
	Limit          int
	UserQuery      string
	UsersAfter     string
	RequestsBefore string
	RequestStatus  string
}

type AdminUpdate struct {
	Revision          int64   `json:"revision"`
	Action            string  `json:"action"`
	TargetID          string  `json:"target_id"`
	MonthlyMicrousd   *int64  `json:"monthly_microusd,omitempty"`
	MonthlyTokens     *int64  `json:"monthly_tokens,omitempty"`
	AccessPaused      *bool   `json:"access_paused,omitempty"`
	Enabled           *bool   `json:"enabled,omitempty"`
	Moderation        *string `json:"moderation,omitempty"`
	MaxOutputTokens   *int64  `json:"max_output_tokens,omitempty"`
	InputPrice        *int64  `json:"input_microusd_per_million,omitempty"`
	OutputPrice       *int64  `json:"output_microusd_per_million,omitempty"`
	ExpectedModelBase string  `json:"expected_model_base,omitempty"`
	Provider          string  `json:"provider,omitempty"`
	UpstreamModel     string  `json:"upstream_model,omitempty"`
}

type AdminSnapshot struct {
	Revision      int64             `json:"revision"`
	Enabled       bool              `json:"enabled"`
	Pool          AdminPool         `json:"pool"`
	Defaults      AdminDefaults     `json:"defaults"`
	Users         []AdminUser       `json:"users"`
	UsersNext     string            `json:"users_next"`
	Requests      []AdminRequest    `json:"requests"`
	RequestsNext  string            `json:"requests_next"`
	Models        []ModelPolicy     `json:"models"`
	ModelWarnings map[string]string `json:"model_warnings,omitempty"`
	ModelBases    map[string]string `json:"model_bases"`
	Audit         []AdminAudit      `json:"audit"`
}

type AdminPool struct {
	MonthlyLimitMicrousd int64     `json:"monthly_limit_microusd"`
	SpentMicrousd        int64     `json:"spent_microusd"`
	ReservedMicrousd     int64     `json:"reserved_microusd"`
	RemainingMicrousd    int64     `json:"remaining_microusd"`
	PeriodStart          time.Time `json:"period_start"`
	ResetsAt             time.Time `json:"resets_at"`
	Available            bool      `json:"available"`
}

type AdminDefaults struct {
	MonthlyMicrousd int64 `json:"monthly_microusd"`
	MonthlyTokens   int64 `json:"monthly_tokens"`
}

type AdminUser struct {
	UserID          string `json:"user_id"`
	EnvironmentID   string `json:"environment_id"`
	Name            string `json:"name"`
	Email           string `json:"email,omitempty"`
	AccessPaused    bool   `json:"access_paused"`
	Quota           Quota  `json:"quota"`
	MonthlyMicrousd int64  `json:"monthly_microusd"`
	MonthlyTokens   int64  `json:"monthly_tokens"`
}

type AdminRequest struct {
	ID                string    `json:"id"`
	RequestID         string    `json:"request_id"`
	EnvironmentID     string    `json:"environment_id"`
	UserID            string    `json:"user_id"`
	Model             string    `json:"model"`
	Provider          string    `json:"provider"`
	UpstreamModel     string    `json:"upstream_model"`
	Status            string    `json:"status"`
	ModerationVerdict string    `json:"moderation_verdict"`
	SpentMicrousd     int64     `json:"spent_microusd"`
	ReservedMicrousd  int64     `json:"reserved_microusd"`
	Tokens            int64     `json:"tokens"`
	InputTokens       int64     `json:"input_tokens"`
	OutputTokens      int64     `json:"output_tokens"`
	CreatedAt         time.Time `json:"created_at"`
	GenerationStarted bool      `json:"generation_started"`
	UpstreamRequestID string    `json:"upstream_request_id"`
	ErrorCode         string    `json:"error_code"`
}

type AdminAudit struct {
	ActorUserID string    `json:"actor_user_id"`
	Action      string    `json:"action"`
	TargetID    string    `json:"target_id"`
	Revision    int64     `json:"revision"`
	CreatedAt   time.Time `json:"created_at"`
}

func (s *Service) poolLimit(state *State) int64 {
	if state.Admin.PoolMicrousd != nil {
		return *state.Admin.PoolMicrousd
	}
	return s.Config.GlobalMicrousd
}

func (s *Service) defaultGrant(state *State) (int64, int64) {
	money, tokens := s.Config.GrantMicrousd, s.Config.GrantTokens
	if state.Admin.DefaultMicrousd != nil {
		money = *state.Admin.DefaultMicrousd
	}
	if state.Admin.DefaultTokens != nil {
		tokens = *state.Admin.DefaultTokens
	}
	return money, tokens
}

func (s *Service) userGrant(state *State, owner string) (int64, int64) {
	money, tokens := s.defaultGrant(state)
	override := state.Admin.Users[owner]
	if override.MonthlyMicrousd != nil {
		money = *override.MonthlyMicrousd
	}
	if override.MonthlyTokens != nil {
		tokens = *override.MonthlyTokens
	}
	return money, tokens
}

func (s *Service) effectiveModelPolicy(state *State, id string) (ModelPolicy, bool) {
	base, exists := s.baseModelPolicy(state, id)
	if !exists {
		return ModelPolicy{}, false
	}
	if s.modelIDConflict(state, id) {
		base.Enabled = false
		return base, true
	}
	if override, ok := state.Admin.Models[id]; ok {
		if override.Base != base {
			base.Enabled = false
			return base, true
		}
		return override.Policy, true
	}
	return base, true
}

// RuntimePolicies requires a live free-environment lease, just like admission.
func (s *Service) RuntimePolicies(ctx context.Context, token string) ([]ModelPolicy, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	var policies []ModelPolicy
	err := s.Store.WithHostingState(ctx, func(state *State) error {
		if _, err := s.active(state, token); err != nil {
			return err
		}
		policies = s.runtimePolicies(state)
		return nil
	})
	return policies, err
}

func (s *Service) runtimePolicies(state *State) []ModelPolicy {
	bases := s.modelBases(state)
	policies := make([]ModelPolicy, 0, len(bases))
	for _, base := range bases {
		policy, _ := s.effectiveModelPolicy(state, base.ID)
		policies = append(policies, policy)
	}
	return policies
}

func (s *Service) AdminSnapshot(ctx context.Context, query AdminQuery) (*AdminSnapshot, error) {
	if s == nil || s.Store == nil {
		return nil, ErrDisabled
	}
	if query.Limit < 0 || query.Limit > 100 || len(query.UserQuery) > 256 ||
		len(query.UsersAfter) > 256 || len(query.RequestsBefore) > 512 ||
		len(query.RequestStatus) > 64 {
		return nil, ErrAdminUpdate
	}
	var snapshot *AdminSnapshot
	err := s.Store.WithHostingState(ctx, func(state *State) error {
		snapshot = s.adminSnapshot(state, query)
		return nil
	})
	return snapshot, err
}

func (s *Service) adminSnapshot(state *State, query AdminQuery) *AdminSnapshot {
	s.reconcile(state)
	s.refreshPool(state)
	money, tokens := s.defaultGrant(state)
	limit := s.poolLimit(state)
	snapshot := &AdminSnapshot{
		Revision: state.Admin.Revision, Enabled: s.Enabled(),
		Defaults: AdminDefaults{MonthlyMicrousd: money, MonthlyTokens: tokens},
		Pool: AdminPool{
			MonthlyLimitMicrousd: limit,
			SpentMicrousd:        max(0, state.SpentMicrousd-state.PoolSpentBaselineMicrousd),
			ReservedMicrousd:     state.ReservedMicrousd,
			RemainingMicrousd:    max(0, limit-poolCommitted(state)),
			PeriodStart:          state.PoolPeriodStart, ResetsAt: state.PoolResetsAt,
			Available: s.Enabled() && s.poolAvailable(state),
		},
		Models: s.runtimePolicies(state), Users: []AdminUser{}, Requests: []AdminRequest{},
		Audit: []AdminAudit{}, ModelBases: map[string]string{},
	}
	for _, base := range s.modelBases(state) {
		snapshot.ModelBases[base.ID] = modelBaseHash(base)
		if s.modelIDConflict(state, base.ID) {
			if snapshot.ModelWarnings == nil {
				snapshot.ModelWarnings = map[string]string{}
			}
			snapshot.ModelWarnings[base.ID] = "model_id_conflict"
		}
	}
	for id, override := range state.Admin.Models {
		base, exists := s.baseModelPolicy(state, id)
		if !exists || override.Base != base {
			if snapshot.ModelWarnings == nil {
				snapshot.ModelWarnings = map[string]string{}
			}
			if snapshot.ModelWarnings[id] == "" {
				snapshot.ModelWarnings[id] = "base_policy_changed"
			}
		}
	}
	if query.Limit == 0 {
		query.Limit = 25
	}
	s.snapshotUsers(state, query, snapshot)
	s.snapshotRequests(state, query, snapshot)
	for i := len(state.Admin.Audit) - 1; i >= 0 && len(snapshot.Audit) < 50; i-- {
		snapshot.Audit = append(snapshot.Audit, state.Admin.Audit[i])
	}
	return snapshot
}

func (s *Service) snapshotUsers(state *State, query AdminQuery, snapshot *AdminSnapshot) {
	owners := make([]string, 0, len(state.Environments))
	for owner := range state.Environments {
		if owner > query.UsersAfter && (query.UserQuery == "" || owner == query.UserQuery) {
			owners = append(owners, owner)
		}
	}
	sort.Strings(owners)
	for _, owner := range owners {
		if len(snapshot.Users) == query.Limit {
			snapshot.UsersNext = snapshot.Users[len(snapshot.Users)-1].UserID
			break
		}
		record := state.Environments[owner]
		s.refreshQuota(state, record)
		money, tokens := s.userGrant(state, owner)
		snapshot.Users = append(snapshot.Users, AdminUser{
			UserID: owner, EnvironmentID: record.Starter.EnvironmentID,
			Name: record.Starter.Name, AccessPaused: record.Starter.AccessPaused,
			Quota: record.Starter.Quota, MonthlyMicrousd: money, MonthlyTokens: tokens,
		})
	}
}

func (s *Service) snapshotRequests(state *State, query AdminQuery, snapshot *AdminSnapshot) {
	requests := make([]*Reservation, 0, len(state.Requests))
	for _, request := range state.Requests {
		record := environmentFor(state, request.EnvironmentID)
		if record == nil ||
			(query.UserQuery != "" && record.Starter.OwnerUserID != query.UserQuery) ||
			(query.RequestStatus != "" && request.Status != query.RequestStatus) {
			continue
		}
		requests = append(requests, request)
	}
	sort.Slice(requests, func(i, j int) bool {
		if requests[i].CreatedAt.Equal(requests[j].CreatedAt) {
			return requests[i].ID > requests[j].ID
		}
		return requests[i].CreatedAt.After(requests[j].CreatedAt)
	})
	start := query.RequestsBefore == ""
	for _, request := range requests {
		if !start {
			start = request.ID == query.RequestsBefore
			continue
		}
		if len(snapshot.Requests) == query.Limit {
			snapshot.RequestsNext = snapshot.Requests[len(snapshot.Requests)-1].ID
			break
		}
		snapshot.Requests = append(snapshot.Requests, safeAdminRequest(state, request))
	}
}

func safeAdminRequest(state *State, request *Reservation) AdminRequest {
	view := AdminRequest{
		ID: request.ID, RequestID: request.RequestID, EnvironmentID: request.EnvironmentID,
		UserID: environmentFor(state, request.EnvironmentID).Starter.OwnerUserID,
		Model:  request.Model, Status: request.Status, SpentMicrousd: request.ActualMicrousd,
		Tokens: request.ActualTokens, InputTokens: request.InputTokens, OutputTokens: request.OutputTokens,
		CreatedAt: request.CreatedAt, GenerationStarted: request.GenerationStarted,
		UpstreamRequestID: request.UpstreamRequestID, ErrorCode: request.ErrorCode,
	}
	if request.Status == "reserved" {
		view.ReservedMicrousd, view.Tokens = request.Microusd, request.Tokens
	}
	if request.Policy != nil {
		view.Provider, view.UpstreamModel = request.Policy.Provider, request.Policy.UpstreamModel
	}
	if request.Moderation != nil {
		view.ModerationVerdict = request.Moderation.Verdict
	}
	return view
}

func (s *Service) AdminUpdate(
	ctx context.Context,
	actor string,
	update *AdminUpdate,
) (*AdminSnapshot, error) {
	if s == nil || s.Store == nil {
		return nil, ErrDisabled
	}
	if !validAdminUpdate(actor, update) {
		return nil, ErrAdminUpdate
	}
	var snapshot *AdminSnapshot
	err := s.Store.WithHostingState(ctx, func(state *State) error {
		state.Admin.initialize()
		if state.Admin.Revision != update.Revision {
			return ErrAdminRevision
		}
		if err := s.applyAdminUpdate(state, update); err != nil {
			return err
		}
		state.Admin.Revision++
		state.Admin.Audit = append(state.Admin.Audit, AdminAudit{
			ActorUserID: actor, Action: update.Action, TargetID: update.TargetID,
			Revision: state.Admin.Revision, CreatedAt: s.Now().UTC(),
		})
		if len(state.Admin.Audit) > maxAdminAudit {
			state.Admin.Audit = append(
				[]AdminAudit(nil),
				state.Admin.Audit[len(state.Admin.Audit)-maxAdminAudit:]...)
		}
		snapshot = s.adminSnapshot(state, AdminQuery{UserQuery: updateUserTarget(update)})
		return nil
	})
	return snapshot, err
}

func updateUserTarget(update *AdminUpdate) string {
	if update.Action == "user" {
		return update.TargetID
	}
	return ""
}

func validAdminUpdate(actor string, update *AdminUpdate) bool {
	if update == nil || actor == "" || len(actor) > 256 || strings.TrimSpace(actor) != actor ||
		update.Revision < 0 || update.Revision == 1<<63-1 || len(update.TargetID) > 256 {
		return false
	}
	if update.MonthlyMicrousd != nil &&
		(*update.MonthlyMicrousd < 0 || *update.MonthlyMicrousd > 1_000_000_000_000_000) {
		return false
	}
	if update.MonthlyTokens != nil &&
		(*update.MonthlyTokens < 0 || *update.MonthlyTokens > 1_000_000_000) {
		return false
	}
	return validAdminAction(update) && validAdminModelBase(update)
}

func validAdminModelBase(update *AdminUpdate) bool {
	if update.Action != "model" {
		return update.ExpectedModelBase == ""
	}
	if len(update.ExpectedModelBase) != 64 {
		return false
	}
	_, err := hex.DecodeString(update.ExpectedModelBase)
	return err == nil
}

func modelBaseHash(base ModelPolicy) string {
	raw, _ := json.Marshal(base)
	return hash(string(raw))
}

func validAdminAction(update *AdminUpdate) bool {
	if update.Action == "model_create" {
		return validAdminModelCreate(update)
	}
	if update.Provider != "" || update.UpstreamModel != "" {
		return false
	}
	return validAdminEditAction(update)
}

func validAdminEditAction(update *AdminUpdate) bool {
	grant := update.MonthlyMicrousd != nil || update.MonthlyTokens != nil
	model := hasAdminModelFields(update)
	switch update.Action {
	case "pool":
		return update.TargetID == "" && update.MonthlyMicrousd != nil &&
			update.MonthlyTokens == nil && update.AccessPaused == nil && !model
	case "defaults":
		return update.TargetID == "" && grant && update.AccessPaused == nil && !model
	case "user":
		return update.TargetID != "" && (grant || update.AccessPaused != nil) && !model
	case "model":
		return update.TargetID != "" && model && !grant && update.AccessPaused == nil
	default:
		return false
	}
}

func hasAdminModelFields(update *AdminUpdate) bool {
	return update.Enabled != nil || update.Moderation != nil || update.MaxOutputTokens != nil ||
		update.InputPrice != nil || update.OutputPrice != nil
}

func intCopy(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (s *Service) applyAdminUpdate(state *State, update *AdminUpdate) error {
	switch update.Action {
	case "pool":
		state.Admin.PoolMicrousd = intCopy(update.MonthlyMicrousd)
	case "defaults":
		if update.MonthlyMicrousd != nil {
			state.Admin.DefaultMicrousd = intCopy(update.MonthlyMicrousd)
		}
		if update.MonthlyTokens != nil {
			state.Admin.DefaultTokens = intCopy(update.MonthlyTokens)
		}
	case "user":
		return s.applyAdminUser(state, update)
	case "model":
		return s.applyAdminModel(state, update)
	case "model_create":
		return s.applyAdminModelCreate(state, update)
	}
	return nil
}

func (s *Service) applyAdminUser(state *State, update *AdminUpdate) error {
	record := state.Environments[update.TargetID]
	if record == nil {
		return ErrNotFound
	}
	s.refreshQuota(state, record)
	override := state.Admin.Users[update.TargetID]
	if update.MonthlyMicrousd != nil {
		override.MonthlyMicrousd = intCopy(update.MonthlyMicrousd)
		record.Starter.Quota.GrantedMicrousd = *update.MonthlyMicrousd
	}
	if update.MonthlyTokens != nil {
		override.MonthlyTokens = intCopy(update.MonthlyTokens)
		record.Starter.Quota.GrantedTokens = *update.MonthlyTokens
	}
	if update.AccessPaused != nil {
		override.AccessPaused = *update.AccessPaused
		record.Starter.AccessPaused = *update.AccessPaused
	}
	record.Starter.Quota.Refresh()
	state.Admin.Users[update.TargetID] = override
	return nil
}

func (s *Service) applyAdminModel(state *State, update *AdminUpdate) error {
	base, exists := s.baseModelPolicy(state, update.TargetID)
	if !exists {
		return ErrNotFound
	}
	if s.modelIDConflict(state, update.TargetID) {
		return ErrAdminModelConflict
	}
	if update.ExpectedModelBase != modelBaseHash(base) {
		return ErrAdminRevision
	}
	policy, _ := s.effectiveModelPolicy(state, update.TargetID)
	if override, ok := state.Admin.Models[update.TargetID]; ok && override.Base != base {
		policy = base
	}
	if update.Enabled != nil {
		policy.Enabled = *update.Enabled
	}
	if update.Moderation != nil {
		policy.Moderation = *update.Moderation
	}
	if update.MaxOutputTokens != nil {
		policy.MaxOutputTokens = *update.MaxOutputTokens
	}
	if update.InputPrice != nil {
		policy.InputPrice = *update.InputPrice
	}
	if update.OutputPrice != nil {
		policy.OutputPrice = *update.OutputPrice
	}
	policy.PolicyVersion = fmt.Sprintf("admin-%d", state.Admin.Revision+1)
	if !validModelPolicy(policy) {
		return ErrAdminUpdate
	}
	state.Admin.Models[update.TargetID] = AdminModelOverride{Base: base, Policy: policy}
	return nil
}
