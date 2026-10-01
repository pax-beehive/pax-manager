package domain

import (
	"context"
	"errors"
	"time"
)

var (
	ErrE2EEPairingSuperseded = errors.New(
		"pairing superseded by a newer request; use the latest pairing command",
	)
	ErrE2EEPairingExpired = errors.New("pairing expired; generate a new pairing request")
)

type E2EEPairingRequest struct {
	ProtocolVersion         string     `json:"protocol_version,omitempty"`
	RecipientCapabilityHash []byte     `json:"-"`
	CancelledAt             *time.Time `json:"cancelled_at,omitempty"`
	RejectedAt              *time.Time `json:"rejected_at,omitempty"`
	ApprovalAttemptID       string     `json:"-"`
	ApprovalCapabilityHash  []byte     `json:"-"`
	PairingID               string     `json:"pairing_id"`
	OwnerUserID             string     `json:"-"`
	NodeID                  string     `json:"node_id"`
	AgentID                 string     `json:"agent_id"`
	DeviceID                string     `json:"device_id"`
	DeviceName              string     `json:"device_name"`
	KeyEpoch                int64      `json:"key_epoch"`
	RecipientPublicKey      []byte     `json:"-"`
	SecretCommitment        []byte     `json:"-"`
	CreatedAt               time.Time  `json:"created_at"`
	ExpiresAt               time.Time  `json:"expires_at"`
	SupersededAt            *time.Time `json:"-"`
	CompletedAt             *time.Time `json:"completed_at,omitempty"`
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
	CreateE2EEPairingRequest(
		ctx context.Context,
		request E2EEPairingRequest,
	) (E2EEPairingRequest, error)
	// A superseded request is returned with ErrE2EEPairingSuperseded so a
	// read-only owner status endpoint can describe it. Approval still rejects it.
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
