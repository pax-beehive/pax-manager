package hosting

import (
	"context"
	"time"
)

const ModerationModel = "omni-moderation-latest"
const ModerationPolicy = "starter-input-v1"

// ModerationDecision deliberately excludes prompts, output text and API keys.
type ModerationDecision struct {
	Verdict           string    `json:"verdict"`
	Model             string    `json:"model"`
	ProviderRequestID string    `json:"provider_request_id,omitempty"`
	Categories        []string  `json:"categories,omitempty"`
	PolicyVersion     string    `json:"policy_version"`
	InputCount        int       `json:"input_count"`
	CheckedAt         time.Time `json:"checked_at"`
}

func environmentFor(state *State, id string) *EnvironmentRecord {
	for _, record := range state.Environments {
		if record.Starter.EnvironmentID == id {
			return record
		}
	}
	return nil
}

func releasePreGeneration(
	state *State,
	record *EnvironmentRecord,
	request *Reservation,
	status string,
) {
	request.Status = status
	request.ActualMicrousd, request.ActualTokens = 0, 0
	q := &record.Starter.Quota
	q.ReservedMicrousd -= request.Microusd
	q.ReservedTokens -= request.Tokens
	q.Refresh()
	state.ReservedMicrousd -= request.Microusd
}

// RecordModeration persists the verdict before the generator is permitted to run.
func (s *Service) RecordModeration(
	ctx context.Context,
	id string,
	decision *ModerationDecision,
) error {
	if decision == nil ||
		(decision.Verdict != "allowed" && decision.Verdict != "blocked" && decision.Verdict != "unavailable" && decision.Verdict != "skipped_by_policy") {
		return ErrConflict
	}
	return s.Store.WithHostingState(ctx, func(state *State) error {
		r := state.Requests[id]
		if r == nil {
			return ErrNotFound
		}
		record := environmentFor(state, r.EnvironmentID)
		if record == nil || r.Status != "reserved" || r.Moderation == nil || r.GenerationStarted ||
			r.Moderation.Verdict != "pending" {
			return ErrConflict
		}
		if !validReservationDecision(r, decision) {
			return ErrConflict
		}
		copy := *decision
		copy.Categories = append([]string(nil), decision.Categories...)
		copy.PolicyVersion, copy.CheckedAt = reservationPolicyVersion(r), s.Now()
		r.Moderation = &copy
		if copy.Verdict != "allowed" && copy.Verdict != "skipped_by_policy" {
			releasePreGeneration(state, record, r, "moderation_"+copy.Verdict)
		}
		return nil
	})
}

// BeginGeneration rechecks the execution lease after the external safety call.
func (s *Service) BeginGeneration(ctx context.Context, id string) error {
	return s.Store.WithHostingState(ctx, func(state *State) error {
		r := state.Requests[id]
		if r == nil {
			return ErrNotFound
		}
		record := environmentFor(state, r.EnvironmentID)
		if record == nil || r.Status != "reserved" || r.GenerationStarted || r.Moderation == nil ||
			!reservationModerationPermits(r) {
			return ErrConflict
		}
		if record.Starter.AccessPaused {
			return ErrAccessPaused
		}
		if !s.currentReservationPolicy(state, r) {
			return ErrConflict
		}
		s.refreshPool(state)
		// An admitted paid hold may consume the last dollar. Zero-price routes
		// still stop when the pool closes, including during the safety check.
		if !s.reservationFunded(state, r) {
			return ErrPlatformBudget
		}
		quota := record.Starter.Quota
		if quota.SpentMicrousd+quota.ReservedMicrousd > quota.GrantedMicrousd ||
			quota.SpentTokens+quota.ReservedTokens > quota.GrantedTokens {
			return ErrBudget
		}
		if record.Generation != r.Generation || record.Job == nil || !record.Job.Active ||
			!record.Job.LeaseExpiresAt.After(s.Now()) {
			return ErrUnauthorized
		}
		r.GenerationStarted = true
		return nil
	})
}

func (s *Service) CancelBeforeGeneration(ctx context.Context, id string) error {
	return s.Store.WithHostingState(ctx, func(state *State) error {
		r := state.Requests[id]
		if r == nil {
			return ErrNotFound
		}
		if r.Status != "reserved" {
			return nil
		}
		record := environmentFor(state, r.EnvironmentID)
		if record == nil || r.GenerationStarted {
			return ErrConflict
		}
		releasePreGeneration(state, record, r, "generation_canceled")
		return nil
	})
}
