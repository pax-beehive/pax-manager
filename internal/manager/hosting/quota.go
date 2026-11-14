package hosting

import (
	"math"
	"time"
)

func (s *Service) refreshPool(state *State) {
	now := s.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	if state.PoolPeriodStart.IsZero() || state.PoolResetsAt.IsZero() {
		// Preserve legacy committed spend until the next calendar month.
		state.PoolPeriodStart, state.PoolResetsAt = start, end
		state.PoolSpentBaselineMicrousd = 0
		return
	}
	if !now.Before(state.PoolResetsAt) && !environmentHasReservation(state, "") {
		state.PoolPeriodStart, state.PoolResetsAt = start, end
		state.PoolSpentBaselineMicrousd = state.SpentMicrousd
	}
}

func poolCommitted(state *State) int64 {
	if state.PoolSpentBaselineMicrousd < 0 ||
		state.SpentMicrousd < state.PoolSpentBaselineMicrousd ||
		state.ReservedMicrousd < 0 {
		return math.MaxInt64
	}
	spent := state.SpentMicrousd - state.PoolSpentBaselineMicrousd
	if spent > math.MaxInt64-state.ReservedMicrousd {
		return math.MaxInt64
	}
	return spent + state.ReservedMicrousd
}

func (s *Service) poolAvailable(state *State) bool {
	return s.Now().Before(state.PoolResetsAt) && poolCommitted(state) < s.poolLimit(state)
}

func (s *Service) reservationFunded(state *State, request *Reservation) bool {
	if request.Microusd == 0 {
		return poolCommitted(state) < s.poolLimit(state)
	}
	return s.poolLimit(state) > 0 && poolCommitted(state) <= s.poolLimit(state)
}

// refreshQuota expires unused user credit independently of the platform pool.
// An old month's in-flight request must settle before replacing its quota.
func (s *Service) refreshQuota(state *State, record *EnvironmentRecord) {
	now := s.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	quota := &record.Starter.Quota
	if quota.PeriodStart.IsZero() || quota.ResetsAt.IsZero() {
		// Adopt legacy balances without turning a restart into another grant.
		quota.PeriodStart, quota.ResetsAt = start, end
	} else if !now.Before(quota.ResetsAt) && !environmentHasReservation(state, record.Starter.EnvironmentID) {
		money, tokens := s.userGrant(state, record.Starter.OwnerUserID)
		*quota = Quota{
			PeriodStart: start, ResetsAt: end,
			GrantedMicrousd: money,
			GrantedTokens:   tokens,
		}
	}
	quota.Refresh()
}

func environmentHasReservation(state *State, environment string) bool {
	for _, request := range state.Requests {
		if (environment == "" || request.EnvironmentID == environment) &&
			request.Status == "reserved" {
			return true
		}
	}
	return false
}
