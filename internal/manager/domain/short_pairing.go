package domain

import (
	"context"
	"errors"
	"time"
)

const ShortPairingProtocol = "short-code-v2"

var ErrShortPairingLimited = errors.New("pairing attempt limit reached")
var ErrShortPairingEnded = errors.New("pairing has ended")

// Capabilities authenticate relay roles, not possession of the agent root key.
// All secret material carried by the relay is encrypted by the browser peers.
type ShortPairingAttempt struct {
	AttemptID              string     `json:"attempt_id"`
	PairingID              string     `json:"pairing_id"`
	Generation             int64      `json:"generation"`
	ApproverCapabilityHash []byte     `json:"-"`
	ClientHello            string     `json:"client_hello"`
	RecipientAnswer        string     `json:"recipient_answer,omitempty"`
	ClientFinish           string     `json:"client_finish,omitempty"`
	SecretPayload          string     `json:"secret_payload,omitempty"`
	Stage                  int        `json:"stage"`
	CreatedAt              time.Time  `json:"created_at"`
	ExpiresAt              time.Time  `json:"expires_at"`
	ConfirmationExpiresAt  *time.Time `json:"confirmation_expires_at,omitempty"`
}

type ShortPairingStore interface {
	CreateShortPairingAttempt(
		context.Context,
		E2EEPairingRequest,
		ShortPairingAttempt,
	) (ShortPairingAttempt, error)
	ListShortPairingAttempts(
		context.Context,
		E2EEPairingRequest,
		[]byte,
	) ([]ShortPairingAttempt, error)
	GetShortPairingAttempt(
		context.Context,
		E2EEPairingRequest,
		string,
		[]byte,
	) (ShortPairingAttempt, error)
	AdvanceShortPairingAttempt(
		context.Context,
		E2EEPairingRequest,
		string,
		[]byte,
		int,
		string,
	) (ShortPairingAttempt, error)
	EndShortPairing(context.Context, E2EEPairingRequest, []byte, string) error
}
