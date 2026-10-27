package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/pax-beehive/pax-manager/internal/manager/config"
)

func TestGivenJournalRetentionEnvironmentThenLoadPolicy(t *testing.T) {
	for _, key := range []string{
		"TRANSPORT_JOURNAL_GC_DISABLED", "TRANSPORT_JOURNAL_RETENTION_HOURS",
		"TRANSPORT_JOURNAL_GC_INTERVAL_SECONDS", "TRANSPORT_JOURNAL_GC_BATCH_SIZE",
	} {
		t.Setenv(key, "")
	}
	cfg := config.Load()
	assert.False(t, cfg.TransportJournalGCDisabled)
	assert.Equal(t, 72*time.Hour, cfg.TransportJournalRetention)
	assert.Equal(t, 10*time.Second, cfg.TransportJournalGCInterval)
	assert.Equal(t, 1000, cfg.TransportJournalGCBatchSize)
	t.Setenv("TRANSPORT_JOURNAL_GC_DISABLED", "true")
	t.Setenv("TRANSPORT_JOURNAL_RETENTION_HOURS", "168")
	t.Setenv("TRANSPORT_JOURNAL_GC_INTERVAL_SECONDS", "60")
	t.Setenv("TRANSPORT_JOURNAL_GC_BATCH_SIZE", "500")
	cfg = config.Load()
	assert.True(t, cfg.TransportJournalGCDisabled)
	assert.Equal(t, 168*time.Hour, cfg.TransportJournalRetention)
	assert.Equal(t, time.Minute, cfg.TransportJournalGCInterval)
	assert.Equal(t, 500, cfg.TransportJournalGCBatchSize)
}
