package config

import (
	"errors"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/search"
)

func (c Config) ValidateSearch() error {
	if c.SearchReadReceiptSecret != "" && len(c.SearchReadReceiptSecret) < 32 {
		return errors.New("SEARCH_READ_RECEIPT_SECRET must contain at least 32 bytes")
	}
	if strings.TrimSpace(c.SearchURL) == "" {
		return nil
	}
	return search.ValidateConfig(c.SearchURL, c.SearchIndex)
}
