package hosting

import "context"

type PrepareRequest struct {
	Token           string
	RequestID       string
	Model           string
	InputTokens     int64
	MaxOutputTokens int64
	Protocol        string
	ReasoningEffort *string
}

type PreparedRequest struct {
	ReservationID   string      `json:"reservation_id"`
	Policy          ModelPolicy `json:"policy"`
	MaxOutputTokens int64       `json:"max_output_tokens"`
	Tokens          int64       `json:"tokens"`
}

// Prepare fixes the route and holds both budgets in one serialized admission.
// No later lease, balance or policy change revokes this admitted request.
func (s *Service) Prepare(ctx context.Context, request *PrepareRequest) (*PreparedRequest, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if request == nil || !validReservationRequest(request.RequestID, 1) ||
		request.InputTokens < 0 || request.InputTokens > 1_000_000 ||
		request.MaxOutputTokens < 0 || request.MaxOutputTokens > 8192 ||
		!validProtocol(request.Protocol) {
		return nil, ErrInvalidModelRequest
	}
	var prepared *PreparedRequest
	err := s.withActiveToken(
		ctx,
		request.Token,
		true,
		func(state *State, record *EnvironmentRecord, _ *sandboxModelGrant) error {
			s.reconcile(state)
			s.refreshPool(state)
			policy, err := s.atomicAdmissionPolicy(state, request)
			if err != nil {
				return err
			}
			maximum := request.MaxOutputTokens
			if maximum == 0 {
				maximum = policy.MaxOutputTokens
			}
			if maximum > policy.MaxOutputTokens || request.InputTokens+maximum > 1_000_000 {
				return ErrInvalidModelRequest
			}
			r, err := s.prepareReservation(state, record, request, policy, maximum)
			if err != nil {
				return err
			}
			prepared = &PreparedRequest{
				ReservationID: r.ID, Policy: policy, MaxOutputTokens: maximum, Tokens: r.Tokens,
			}
			return nil
		},
	)
	return prepared, err
}

func validProtocol(protocol string) bool {
	return protocol == "chat" || protocol == "responses" || protocol == "messages"
}

func (s *Service) atomicAdmissionPolicy(
	state *State,
	request *PrepareRequest,
) (ModelPolicy, error) {
	policy, exists := s.effectiveModelPolicy(state, request.Model)
	if !exists && request.Model == s.Config.Model && s.legacyPolicyAllowed(state, request.Model) {
		policy = ModelPolicy{
			ID: request.Model, Provider: "configured", UpstreamModel: request.Model,
			Moderation: "openai", PolicyVersion: ModerationPolicy,
			MaxOutputTokens: s.Config.MaxOutputTokens,
			InputPrice:      s.Config.InputPrice, OutputPrice: s.Config.OutputPrice, Enabled: true,
		}
		exists = true
	}
	if !exists || !validModelPolicy(policy) {
		return ModelPolicy{}, ErrConflict
	}
	if !policy.Enabled {
		return ModelPolicy{}, ErrModelDisabled
	}
	if !validAdmissionReasoning(policy, request.Protocol, request.ReasoningEffort) {
		return ModelPolicy{}, ErrReasoningEffort
	}
	return policy, nil
}

// Keep this exact capability matrix aligned with Gateway's reasoning.go.
func validAdmissionReasoning(policy ModelPolicy, protocol string, effort *string) bool {
	if effort == nil {
		return true
	}
	if protocol != "chat" || policy.Provider != "openrouter" {
		return false
	}
	switch policy.UpstreamModel {
	case "openai/gpt-6.1-sol", "openai/gpt-6.2-astra", "anthropic/claude-opus-5.5":
	default:
		return false
	}
	switch *effort {
	case "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func (s *Service) prepareReservation(
	state *State,
	record *EnvironmentRecord,
	request *PrepareRequest,
	policy ModelPolicy,
	maximum int64,
) (*Reservation, error) {
	key := record.Starter.EnvironmentID + ":" + request.RequestID
	if _, exists := state.Requests[key]; exists {
		return nil, ErrConflict
	}
	if err := s.checkRequestRate(state, record.Starter.EnvironmentID); err != nil {
		return nil, err
	}
	s.refreshQuota(state, record)
	tokens := request.InputTokens + maximum
	cost := price(request.InputTokens, policy.InputPrice) + price(maximum, policy.OutputPrice)
	if err := s.checkAtomicHold(state, record, cost, tokens); err != nil {
		return nil, err
	}
	r := &Reservation{
		ID: key, EnvironmentID: record.Starter.EnvironmentID, RequestID: request.RequestID,
		Status: "reserved", Microusd: cost, Tokens: tokens,
		InputPrice: policy.InputPrice, OutputPrice: policy.OutputPrice, Model: policy.ID,
		CreatedAt: s.Now(), Generation: record.Generation, AtomicAdmission: true,
		Protocol: request.Protocol, MaxOutputTokens: maximum, Policy: &policy,
		QuotaPeriodStart: record.Starter.Quota.PeriodStart, PoolPeriodStart: state.PoolPeriodStart,
		Moderation: &ModerationDecision{
			Verdict: "pending", Model: ModerationModel, PolicyVersion: policy.PolicyVersion,
		},
	}
	if request.ReasoningEffort != nil {
		r.ReasoningEffort = *request.ReasoningEffort
	}
	state.Requests[key] = r
	record.Starter.Quota.ReservedMicrousd += cost
	record.Starter.Quota.ReservedTokens += tokens
	record.Starter.Quota.Refresh()
	state.ReservedMicrousd += cost
	return r, nil
}

func (s *Service) checkAtomicHold(
	state *State,
	record *EnvironmentRecord,
	cost, tokens int64,
) error {
	if !s.poolAvailable(state) || poolCommitted(state)+cost > s.poolLimit(state) {
		return ErrPlatformBudget
	}
	q := &record.Starter.Quota
	if q.RemainingMicrousd < cost || (q.GrantedTokens != 0 && q.RemainingTokens < tokens) {
		return ErrBudget
	}
	if environmentHasReservation(state, record.Starter.EnvironmentID) {
		return ErrConflict
	}
	return nil
}
