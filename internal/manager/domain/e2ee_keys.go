package domain

import (
	"context"
	"time"
)

type E2EEPairingRequest struct {
	PairingID          string     `json:"pairing_id"`
	OwnerUserID        string     `json:"-"`
	NodeID             string     `json:"node_id"`
	AgentID            string     `json:"agent_id"`
	DeviceID           string     `json:"device_id"`
	DeviceName         string     `json:"device_name"`
	KeyEpoch           int64      `json:"key_epoch"`
	RecipientPublicKey []byte     `json:"-"`
	SecretCommitment   []byte     `json:"-"`
	CreatedAt          time.Time  `json:"created_at"`
	ExpiresAt          time.Time  `json:"expires_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

type E2EEKeyPackage struct {
	PairingID                string    `json:"pairing_id"`
	OwnerUserID              string    `json:"-"`
	NodeID                   string    `json:"node_id"`
	AgentID                  string    `json:"agent_id"`
	DeviceID                 string    `json:"device_id"`
	KeyEpoch                 int64     `json:"key_epoch"`
	RecipientPublicKey       []byte    `json:"-"`
	SenderEphemeralPublicKey []byte    `json:"-"`
	Nonce                    []byte    `json:"-"`
	Ciphertext               []byte    `json:"-"`
	CreatedAt                time.Time `json:"created_at"`
}

type E2EEKeyDistributionStore interface {
	CreateE2EEPairingRequest(ctx context.Context, request E2EEPairingRequest) (E2EEPairingRequest, error)
	GetE2EEPairingRequest(
		ctx context.Context,
		ownerUserID string,
		agentID string,
		pairingID string,
	) (E2EEPairingRequest, error)
	ListPendingE2EEPairingRequests(
		ctx context.Context,
		ownerUserID string,
		agentID string,
		limit int,
	) ([]E2EEPairingRequest, error)
	CompleteE2EEPairing(
		ctx context.Context,
		request E2EEPairingRequest,
		keyPackage E2EEKeyPackage,
	) (E2EEKeyPackage, error)
	GetE2EEKeyPackage(
		ctx context.Context,
		ownerUserID string,
		agentID string,
		deviceID string,
		keyEpoch int64,
	) (E2EEKeyPackage, error)
}
