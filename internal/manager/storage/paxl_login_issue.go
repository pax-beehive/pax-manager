package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func issuePaxlLoginTx(
	ctx context.Context,
	tx *sql.Tx,
	session PaxlDeviceLoginSession,
	ownerID, keyHash, prefix, apiKey string,
	now time.Time,
) (PaxlDeviceLoginSession, error) {
	keyID, err := newSecret("key")
	if err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_api_keys (key_id, owner_user_id, name, key_hash, prefix, created_at)
		VALUES ($1, $2, 'paxl device login', $3, $4, $5)
	`, keyID, ownerID, keyHash, prefix, now); err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	nodeID, err := newSecret("node")
	if err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	nodeName := firstNonEmpty(session.ClientName, "paxl")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO nodes (
			node_id, owner_user_id, kind, name, hostname, machine_type, os, arch,
			paxd_version, api_endpoint, api_key_hash, status, registered_at, metadata
		)
		VALUES ($1,$2,'paxl',$3,$3,'','unknown','','','',$4,'offline',$5,$6)
	`, nodeID, ownerID, nodeName, "paxl-device:"+nodeID, now,
		nullRaw(json.RawMessage(`{"kind":"paxl"}`))); err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE paxl_device_login_sessions
		SET status = $2, owner_user_id = $3, user_api_key_id = $4, api_key = $5, node_id = $6, approved_at = $7
		WHERE login_id = $1
	`, session.LoginID, domain.PaxlDeviceLoginStatusApproved, ownerID,
		keyID, apiKey, nodeID, now)
	if err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	session, err = scanPaxlDeviceLoginSession(tx.QueryRowContext(ctx, paxlDeviceLoginSelectSQL+`
		WHERE login_id = $1
	`, session.LoginID))
	if err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	return session, nil
}

func (s *MemoryStore) issuePaxlLoginLocked(
	session PaxlDeviceLoginSession,
	ownerID, keyHash, prefix, apiKey string,
	now time.Time,
) (PaxlDeviceLoginSession, error) {
	keyID, err := newSecret("key")
	if err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	userAPIKey := UserAPIKey{
		KeyID: keyID, OwnerUserID: ownerID,
		Name: "paxl device login", Prefix: prefix, CreatedAt: now,
	}
	session.Status = domain.PaxlDeviceLoginStatusApproved
	session.OwnerUserID = ownerID
	session.UserAPIKeyID = userAPIKey.KeyID
	nodeID, err := newSecret("node")
	if err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	session.NodeID = nodeID
	session.APIKey = apiKey
	session.ApprovedAt = &now
	s.userAPIKeys[keyID] = userAPIKey
	s.userAPIKeyHashes[keyHash] = keyID
	s.nodes[nodeID] = Node{
		NodeID:       nodeID,
		OwnerUserID:  ownerID,
		Kind:         "paxl",
		Name:         firstNonEmpty(session.ClientName, "paxl"),
		Hostname:     firstNonEmpty(session.ClientName, "paxl"),
		OS:           "unknown",
		APIEndpoint:  "",
		Status:       "offline",
		RegisteredAt: now,
	}
	s.paxlDeviceLogins[session.LoginID] = session
	return session, nil
}
