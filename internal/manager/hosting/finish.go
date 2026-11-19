package hosting

import "context"

type FinishRequest struct {
	ReservationID string
	Moderation    *ModerationDecision
	InputTokens   int64
	OutputTokens  int64
	RawUsage      []byte
	UpstreamID    string
	ErrorCode     string
	Known         bool
	Started       bool
	Outcome       string
}

// Finish is idempotent and uses only the admitted snapshot. A revoked lease or
// an overdrawn account must not prevent recording the provider's actual usage.
func (s *Service) Finish(ctx context.Context, request *FinishRequest) error {
	if !validFinishRequest(request) {
		return ErrConflict
	}
	return s.Store.WithHostingState(ctx, func(state *State) error {
		r := state.Requests[request.ReservationID]
		if r == nil {
			return ErrNotFound
		}
		if !r.AtomicAdmission {
			return ErrConflict
		}
		if !r.FinishedAt.IsZero() {
			return nil
		}
		record := environmentFor(state, r.EnvironmentID)
		if record == nil || !validAtomicFinish(r, request) {
			return ErrConflict
		}
		cost, tokens, status := atomicFinishCost(r, request)
		adjustAtomicAccounting(state, record, r, cost, tokens)
		r.Status, r.Outcome, r.FinishedAt = status, request.Outcome, s.Now()
		r.GenerationStarted, r.UsageKnown = request.Started, request.Known
		r.ReconciliationRequired = request.Started && !request.Known
		r.InputTokens, r.OutputTokens = request.InputTokens, request.OutputTokens
		r.RawUsage = append([]byte(nil), request.RawUsage...)
		r.UpstreamRequestID, r.ErrorCode = request.UpstreamID, request.ErrorCode
		if r.ReconciliationRequired && r.ErrorCode == "" {
			r.ErrorCode = "usage_unavailable"
		}
		if request.Moderation != nil {
			decision := *request.Moderation
			decision.Categories = append([]string(nil), decision.Categories...)
			decision.PolicyVersion, decision.CheckedAt = reservationPolicyVersion(r), s.Now()
			r.Moderation = &decision
		} else {
			r.Moderation = nil
		}
		return nil
	})
}

func validFinishRequest(request *FinishRequest) bool {
	if request == nil || request.InputTokens < 0 || request.OutputTokens < 0 ||
		request.InputTokens > 1_000_000 || request.OutputTokens > 1_000_000 ||
		len(request.RawUsage) > 4096 || len(request.UpstreamID) > 256 ||
		!ValidSettlementErrorCode(request.ErrorCode) {
		return false
	}
	switch request.Outcome {
	case "completed", "cancelled", "failed", "blocked", "unavailable", "invalid":
	default:
		return false
	}
	if !request.Started {
		return !request.Known && request.InputTokens == 0 && request.OutputTokens == 0 &&
			request.Outcome != "completed"
	}
	return request.Outcome != "blocked" && request.Outcome != "invalid"
}

func validAtomicFinish(r *Reservation, request *FinishRequest) bool {
	if r.Status != "reserved" && (r.Status != "conservative" || !r.ReconciliationRequired) {
		return false
	}
	decision := request.Moderation
	if decision == nil {
		return !request.Started && request.Outcome != "blocked"
	}
	if !validReservationDecision(r, decision) {
		return false
	}
	switch decision.Verdict {
	case "allowed", "skipped_by_policy":
		return request.Outcome != "blocked"
	case "blocked":
		return !request.Started && request.Outcome == "blocked"
	case "unavailable":
		return !request.Started && request.Outcome == "unavailable"
	default:
		return false
	}
}

func atomicFinishCost(r *Reservation, request *FinishRequest) (int64, int64, string) {
	if !request.Started {
		if request.Outcome == "blocked" ||
			(request.Moderation != nil && request.Moderation.Verdict == "unavailable") {
			return 0, 0, "moderation_" + request.Outcome
		}
		return 0, 0, "generation_" + request.Outcome
	}
	if !request.Known {
		return r.Microusd, r.Tokens, "conservative"
	}
	return price(request.InputTokens, r.InputPrice) + price(request.OutputTokens, r.OutputPrice),
		request.InputTokens + request.OutputTokens, "settled"
}

func adjustAtomicAccounting(
	state *State,
	record *EnvironmentRecord,
	r *Reservation,
	cost, tokens int64,
) {
	oldCost, oldTokens := r.ActualMicrousd, r.ActualTokens
	q := &record.Starter.Quota
	if r.Status == "reserved" {
		q.ReservedMicrousd -= r.Microusd
		q.ReservedTokens -= r.Tokens
		state.ReservedMicrousd -= r.Microusd
	}
	if q.PeriodStart.Equal(r.QuotaPeriodStart) {
		q.SpentMicrousd += cost - oldCost
		q.SpentTokens += tokens - oldTokens
	}
	q.Refresh()
	state.SpentMicrousd += cost - oldCost
	if !state.PoolPeriodStart.Equal(r.PoolPeriodStart) {
		// An old provisional charge is already included in the current baseline.
		state.PoolSpentBaselineMicrousd += cost - oldCost
	}
	r.ActualMicrousd, r.ActualTokens = cost, tokens
}

func conserveAtomicReservation(state *State, record *EnvironmentRecord, r *Reservation) {
	adjustAtomicAccounting(state, record, r, r.Microusd, r.Tokens)
	r.Status, r.ErrorCode = "conservative", "usage_unavailable"
	r.ReconciliationRequired = true
}
