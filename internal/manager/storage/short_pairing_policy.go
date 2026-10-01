package storage

import (
	"bytes"
	"fmt"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type shortPairingBudget struct {
	Started time.Time
	Count   int
}

func shortPairingPending(request E2EEPairingRequest, now time.Time) error {
	if request.ProtocolVersion != domain.ShortPairingProtocol {
		return ErrConflict
	}
	if request.CompletedAt != nil || request.CancelledAt != nil || request.RejectedAt != nil {
		return domain.ErrShortPairingEnded
	}
	if request.SupersededAt != nil {
		return domain.ErrE2EEPairingSuperseded
	}
	if !request.ExpiresAt.After(now) {
		return domain.ErrE2EEPairingExpired
	}
	return nil
}

func shortAttemptDeadline(
	request E2EEPairingRequest,
	generation int64,
	now time.Time,
) (time.Time, error) {
	if err := shortPairingPending(request, now); err != nil {
		return time.Time{}, err
	}
	if generation < 0 || generation > 9 {
		return time.Time{}, ErrConflict
	}
	start := request.CreatedAt.Add(time.Duration(generation) * time.Minute)
	deadline := start.Add(90 * time.Second)
	if request.ExpiresAt.Before(deadline) {
		deadline = request.ExpiresAt
	}
	if now.Before(start) || !deadline.After(now) {
		return time.Time{}, domain.ErrE2EEPairingExpired
	}
	return deadline, nil
}
func validShortAttempt(attempt domain.ShortPairingAttempt) bool {
	return attempt.AttemptID != "" && len(attempt.AttemptID) <= 128 &&
		len(attempt.ApproverCapabilityHash) == 32 &&
		len(attempt.ClientHello) > 0 &&
		len(attempt.ClientHello) <= 4096
}
func shortBudgetKeys(request E2EEPairingRequest) []string {
	return []string{
		fmt.Sprintf("account:%s", request.OwnerUserID),
		fmt.Sprintf("agent:%s:%s", request.OwnerUserID, request.AgentID),
	}
}

func shortAdvance(
	request E2EEPairingRequest,
	attempt domain.ShortPairingAttempt,
	capability []byte,
	stage int,
	payload string,
	now time.Time,
) (domain.ShortPairingAttempt, error) {
	if err := shortPairingPending(request, now); err != nil {
		return attempt, err
	}
	if err := validateShortAdvance(request, attempt, capability, stage, payload); err != nil {
		return attempt, err
	}
	previous := ""
	switch stage {
	case 1:
		previous = attempt.RecipientAnswer
	case 2:
		previous = attempt.ClientFinish
	case 3:
		previous = attempt.SecretPayload
	}
	// Replays return committed data, but never extend the original deadline.
	if attempt.Stage >= stage {
		if previous == payload {
			return attempt, nil
		}
		return attempt, ErrConflict
	}
	if attempt.Stage != stage-1 {
		return attempt, ErrConflict
	}
	if !attempt.ExpiresAt.After(now) {
		return attempt, domain.ErrE2EEPairingExpired
	}
	switch stage {
	case 1:
		attempt.RecipientAnswer = payload
	case 2:
		attempt.ClientFinish = payload
	case 3:
		attempt.SecretPayload = payload
		until := now.Add(30 * time.Second)
		if request.ExpiresAt.Before(until) {
			until = request.ExpiresAt
		}
		attempt.ConfirmationExpiresAt = &until
	}
	attempt.Stage = stage
	return attempt, nil
}

func shortApproval(
	request E2EEPairingRequest,
	attempt domain.ShortPairingAttempt,
	caller E2EEPairingRequest,
	now time.Time,
) error {
	if attempt.PairingID != request.PairingID || len(caller.ApprovalCapabilityHash) != 32 ||
		!bytes.Equal(caller.ApprovalCapabilityHash, attempt.ApproverCapabilityHash) {
		return ErrNotFound
	}
	if request.CompletedAt != nil && attempt.Stage == 4 {
		return nil
	}
	if err := shortPairingPending(request, now); err != nil {
		return err
	}
	if attempt.Stage != 3 || attempt.ConfirmationExpiresAt == nil ||
		!attempt.ConfirmationExpiresAt.After(now) {
		return domain.ErrE2EEPairingExpired
	}
	return nil
}

// Only the recipient can cancel; a matched approver can explicitly reject.
func shortEnd(
	request E2EEPairingRequest,
	caller E2EEPairingRequest,
	attempt domain.ShortPairingAttempt,
	capability []byte,
	reason string,
	now time.Time,
) error {
	switch reason {
	case "cancelled":
		if len(capability) != 32 || !bytes.Equal(capability, request.RecipientCapabilityHash) {
			return ErrNotFound
		}
		if request.CancelledAt != nil {
			return nil
		}
		return shortPairingPending(request, now)
	case "rejected":
		caller.ApprovalCapabilityHash = capability
		if request.RejectedAt != nil && attempt.PairingID == request.PairingID &&
			len(capability) == 32 &&
			bytes.Equal(capability, attempt.ApproverCapabilityHash) {
			return nil
		}
		if err := shortPairingPending(request, now); err != nil {
			return err
		}
		return shortApproval(request, attempt, caller, now)
	default:
		return ErrConflict
	}
}

func validateShortAdvance(
	request E2EEPairingRequest,
	attempt domain.ShortPairingAttempt,
	capability []byte,
	stage int,
	payload string,
) error {
	expected := attempt.ApproverCapabilityHash
	if stage == 1 || stage == 3 {
		expected = request.RecipientCapabilityHash
	}
	if len(capability) != 32 || !bytes.Equal(expected, capability) {
		return ErrNotFound
	}
	limit := 4096
	if stage == 3 {
		limit = 8192
	}
	if stage < 1 || stage > 3 || len(payload) == 0 || len(payload) > limit {
		return ErrConflict
	}
	return nil
}
