package hosting

import (
	"context"
	"crypto/subtle"
)

func (s *Service) GatewayAuthorized(token string) bool {
	return s != nil && len(s.Config.GatewayKey) >= 32 &&
		subtle.ConstantTimeCompare([]byte(token), []byte(s.Config.GatewayKey)) == 1
}

// Authorize checks the generation and execution lease without reserving budget.
func (s *Service) Authorize(ctx context.Context, token string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	return s.Store.WithHostingState(ctx, func(state *State) error {
		_, err := s.active(state, token)
		return err
	})
}
