package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const terminalHistoryBenchmarkDatabaseURLEnv = "PAX_MANAGER_BENCHMARK_DATABASE_URL"

var terminalHistoryBenchmarkRunID atomic.Uint64

// BenchmarkPostgresTerminalHistoryChunkPersistence verifies that append cost is
// governed by the active chunk size rather than total terminal history size.
// Run it against an isolated PostgreSQL database; the benchmark creates only a
// connection-local temporary table.
//
// The benchmark intentionally does not assert an absolute latency threshold.
// PostgreSQL fsync behavior, Docker scheduling, and host load make wall-clock
// limits too unstable for a portable test. Errors still fail the benchmark;
// performance regressions should be evaluated from repeated ns/op samples,
// comparing the accumulated_0MiB, accumulated_16MiB, and accumulated_64MiB
// sub-benchmarks with benchstat or an equivalent statistical tool. A healthy
// result stays in the same order of magnitude as accumulated history grows.
//
//	PAX_MANAGER_BENCHMARK_DATABASE_URL=postgres://... \
//	  go test ./internal/manager/storage -run '^$' \
//	  -bench '^BenchmarkPostgresTerminalHistoryChunkPersistence$' \
//	  -benchtime=3s -count=10 > terminal-history.txt
func BenchmarkPostgresTerminalHistoryChunkPersistence(b *testing.B) {
	databaseURL := os.Getenv(terminalHistoryBenchmarkDatabaseURLEnv)
	if databaseURL == "" {
		b.Skipf("set %s to an isolated PostgreSQL database", terminalHistoryBenchmarkDatabaseURLEnv)
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		b.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	b.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if err := prepareTerminalHistoryBenchmarkTable(ctx, db); err != nil {
		b.Fatal(err)
	}

	store := NewPostgresStore(db, time.Now)
	const (
		chunkBytes = 64 * 1024
		deltaBytes = 1024
	)
	delta := strings.Repeat("x", deltaBytes)

	for _, accumulatedMiB := range []int{0, 16, 64} {
		b.Run(fmt.Sprintf("accumulated_%dMiB", accumulatedMiB), func(b *testing.B) {
			messageID := fmt.Sprintf(
				"terminal-benchmark-%d-%d",
				accumulatedMiB,
				terminalHistoryBenchmarkRunID.Add(1),
			)
			prefixParts := accumulatedMiB * 1024 * 1024 / chunkBytes
			if err := seedTerminalHistoryBenchmark(ctx, db, messageID, prefixParts, chunkBytes); err != nil {
				b.Fatal(err)
			}

			b.SetBytes(deltaBytes)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				partIndex := prefixParts + i/(chunkBytes/deltaBytes)
				if err := store.AppendMessagePartText(ctx, messageID, partIndex, delta, nil); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
		})
	}
}

func prepareTerminalHistoryBenchmarkTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TEMP TABLE message_parts (
			message_id TEXT NOT NULL,
			part_index INTEGER NOT NULL,
			part_type TEXT NOT NULL,
			text TEXT,
			payload_json JSONB,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			PRIMARY KEY (message_id, part_index)
		)
	`)
	return err
}

func seedTerminalHistoryBenchmark(
	ctx context.Context,
	db *sql.DB,
	messageID string,
	partCount int,
	chunkBytes int,
) error {
	if partCount == 0 {
		return nil
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO message_parts (
			message_id, part_index, part_type, text, created_at, updated_at
		)
		SELECT $1, part_index, 'text', repeat('x', $3), NOW(), NOW()
		FROM generate_series(0, $2 - 1) AS part_index
	`, messageID, partCount, chunkBytes)
	return err
}
