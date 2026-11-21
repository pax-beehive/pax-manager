package hosting

import (
	"context"
	"crypto/subtle"
)

func (s *Service) GatewayAuthorized(token string) bool {
	return s != nil && len(s.Config.GatewayKey) >= 32 &&
		subtle.ConstantTimeCompare([]byte(token), []byte(s.Config.GatewayKey)) == 1
}

// Authorize checks the active lease or model permission without reserving budget.
func (s *Service) Authorize(ctx context.Context, token string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	return s.withActiveToken(
		ctx,
		token,
		false,
		func(*State, *EnvironmentRecord, *sandboxModelGrant) error {
			return nil
		},
	)
}
