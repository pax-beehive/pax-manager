package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

// WithHostingState serializes small-pool admission and budget reservations. The
// PostgreSQL row lock also fences concurrent manager processes, not just threads.
func (s *PostgresStore) WithHostingState(
	ctx context.Context,
	run func(*hosting.State) error,
) error {
	return s.withHostingState(ctx, "hosting_state", run)
}

// WithSandboxState uses a separate document so Starter writes from older
// Manager instances cannot drop sandbox Nodes or their leases.
func (s *PostgresStore) WithSandboxState(
	ctx context.Context,
	run func(*hosting.State) error,
) error {
	return s.withHostingState(ctx, "sandbox_node_state", run)
}

func (s *PostgresStore) withHostingState(
	ctx context.Context,
	table string,
	run func(*hosting.State) error,
) error {
	return s.withModelStates(ctx, table, "", func(state, _ *hosting.State) error {
		return run(state)
	})
}

func (s *PostgresStore) WithSandboxModelStates(
	ctx context.Context,
	run func(account, sandbox *hosting.State) error,
) error {
	return s.withModelStates(ctx, "hosting_state", "sandbox_node_state", run)
}

func (s *PostgresStore) withModelStates(
	ctx context.Context,
	table, sandboxTable string,
	run func(account, sandbox *hosting.State) error,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	state, err := readHostingState(ctx, tx, table)
	if err != nil {
		return err
	}
	var sandbox *hosting.State
	if sandboxTable != "" {
		sandbox, err = readHostingState(ctx, tx, sandboxTable)
		if err != nil {
			return err
		}
	}
	if err = run(state, sandbox); err != nil {
		return err
	}
	for key, registration := range state.Registrations {
		if _, err = tx.ExecContext(ctx, `INSERT INTO agent_registration_tokens(token_hash,owner_user_id,expires_at,created_at) VALUES ($1,$2,$3,NOW()) ON CONFLICT(token_hash) DO NOTHING`, key, registration.OwnerUserID, registration.ExpiresAt); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	write := fmt.Sprintf("UPDATE %s SET document=$1::jsonb WHERE id=1", table)
	if _, err = tx.ExecContext(ctx, write, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}

func readHostingState(ctx context.Context, tx *sql.Tx, table string) (*hosting.State, error) {
	insert := fmt.Sprintf(
		"INSERT INTO %s (id,document) VALUES (1,'{}'::jsonb) ON CONFLICT (id) DO NOTHING",
		table,
	)
	if _, err := tx.ExecContext(ctx, insert); err != nil {
		return nil, err
	}
	var raw []byte
	read := fmt.Sprintf("SELECT document FROM %s WHERE id=1 FOR UPDATE", table)
	if err := tx.QueryRowContext(ctx, read).Scan(&raw); err != nil {
		return nil, err
	}
	return decodeHostingState(raw)
}

func decodeHostingState(raw []byte) (*hosting.State, error) {
	var state hosting.State
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &state); err != nil {
			return nil, err
		}
	}
	state.Initialize()
	return &state, nil
}

func (s *MemoryStore) WithHostingState(_ context.Context, run func(*hosting.State) error) error {
	return s.withHostingState(&s.hostingState, run)
}

func (s *MemoryStore) WithSandboxState(_ context.Context, run func(*hosting.State) error) error {
	return s.withHostingState(&s.sandboxNodeState, run)
}

func (s *MemoryStore) WithSandboxModelStates(
	_ context.Context,
	run func(account, sandbox *hosting.State) error,
) error {
	return s.withHostingState(&s.hostingState, func(account *hosting.State) error {
		sandbox, err := decodeHostingState(s.sandboxNodeState)
		if err != nil {
			return err
		}
		return run(account, sandbox)
	})
}

func (s *MemoryStore) withHostingState(
	document *json.RawMessage,
	run func(*hosting.State) error,
) error {
	s.hostingMu.Lock()
	defer s.hostingMu.Unlock()
	state, err := decodeHostingState(*document)
	if err != nil {
		return err
	}
	if err := run(state); err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, registration := range state.Registrations {
		if _, ok := s.users[registration.OwnerUserID]; !ok {
			return ErrNotFound
		}
		if _, exists := s.regTokens[key]; exists {
			continue
		}
		expiry := registration.ExpiresAt
		s.regTokens[key] = registrationToken{
			OwnerUserID: registration.OwnerUserID,
			ExpiresAt:   &expiry,
		}
	}
	*document = raw
	return nil
}
