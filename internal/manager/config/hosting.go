package config

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

func (c Config) validateHostingModels() error {
	if c.HostingRequestsPerMinute < 0 || c.HostingRequestsPerMinute > 100000 ||
		c.HostingGlobalRequestsPerMinute < 0 || c.HostingGlobalRequestsPerMinute > 1000000 {
		return errors.New("invalid hosting request rate limits")
	}
	if c.HostingModelsFile != "" {
		_, err := hosting.LoadModelRegistry(c.HostingModelsFile)
		if err != nil {
			return err
		}
	}
	return c.validateHostingBudget()
}

func hostingBudgetEnv(name string, fallback, minimum int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum {
		return -1
	}
	return value
}

func (c Config) validateHostingBudget() error {
	if c.HostingHostKey == "" && c.HostingTokenSecret == "" && c.HostingGatewayKey == "" &&
		c.HostingGrantMicrousd == 0 && c.HostingGrantTokens == 0 && c.HostingGlobalMicrousd == 0 {
		return nil
	}
	if c.HostingGrantMicrousd <= 0 || int64(c.HostingGrantMicrousd) > 1_000_000_000_000_000 ||
		c.HostingGrantTokens < 0 || c.HostingGrantTokens > 1_000_000_000 {
		return errors.New("invalid hosting monthly grant")
	}
	if c.HostingGlobalMicrousd < 0 || int64(c.HostingGlobalMicrousd) > 1_000_000_000_000_000 {
		return errors.New("invalid hosting monthly pool budget")
	}
	return nil
}
