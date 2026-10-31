package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadSearchDefaultsToDisabled(t *testing.T) {
	t.Setenv("SEARCH_READ_RECEIPT_SECRET", "")
	t.Setenv("SEARCH_URL", "")
	t.Setenv("SEARCH_INDEX", "")
	t.Setenv("SEARCH_API_KEY", "")
	cfg := Load()
	assert.Empty(t, cfg.SearchURL)
	assert.Equal(t, "pax-search-v1", cfg.SearchIndex)
	assert.Empty(t, cfg.SearchAPIKey)
	assert.NoError(t, cfg.ValidateSearch())
}

func TestReadReceiptSecretGivenConfiguredWhenLoadedThenValidatesLength(t *testing.T) {
	t.Setenv("SEARCH_URL", "")
	t.Setenv("SEARCH_READ_RECEIPT_SECRET", "short")
	cfg := Load()
	assert.Equal(t, "short", cfg.SearchReadReceiptSecret)
	assert.Error(t, cfg.ValidateSearch())
	cfg.SearchReadReceiptSecret = "01234567890123456789012345678901"
	assert.NoError(t, cfg.ValidateSearch())
}
func TestLoadSearchSettings(t *testing.T) {
	t.Setenv("SEARCH_URL", "http://elasticsearch:9200")
	t.Setenv("SEARCH_INDEX", "custom-search-v1")
	t.Setenv("SEARCH_API_KEY", "private-key")
	cfg := Load()
	assert.Equal(t, "http://elasticsearch:9200", cfg.SearchURL)
	assert.Equal(t, "custom-search-v1", cfg.SearchIndex)
	assert.Equal(t, "private-key", cfg.SearchAPIKey)
	require.NoError(t, cfg.ValidateSearch())
	cfg.SearchURL = "http://name:secret@host"
	assert.Error(t, cfg.ValidateSearch())
	cfg.SearchURL = "http://host:9200"
	cfg.SearchIndex = "bad/index"
	assert.Error(t, cfg.ValidateSearch())
}
