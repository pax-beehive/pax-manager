package manager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const shortPairingCapabilityHeader = "X-Pax-Pairing-Capability"

func pairingCapabilityHash(value string) ([]byte, error) {
	raw, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(raw) != 32 {
		return nil, ErrConflict
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

func (s *Service) shortPairingContext(
	c context.Context,
	ctx *app.RequestContext,
) (domain.ShortPairingStore, domain.E2EEPairingRequest, bool) {
	ctx.Header("Cache-Control", "private, no-store")
	principal, agent, ok := s.ownedE2EEAgent(c, ctx)
	if !ok {
		return nil, domain.E2EEPairingRequest{}, false
	}
	store := s.shortPairingStore
	if store == nil {
		writeError(ctx, http.StatusNotImplemented, "short-code pairing unavailable")
		return nil, domain.E2EEPairingRequest{}, false
	}
	request, err := s.store.GetE2EEPairingRequest(
		c,
		principal.User.UserID,
		agent.AgentID,
		ctx.Param("pairing_id"),
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return nil, request, false
	}
	if request.ProtocolVersion != domain.ShortPairingProtocol {
		writeError(ctx, http.StatusConflict, "request does not use short-code pairing")
		return nil, request, false
	}
	return store, request, true
}
func (s *Service) handleShortPairingAttempts(c context.Context, ctx *app.RequestContext) {
	store, request, ok := s.shortPairingContext(c, ctx)
	if !ok {
		return
	}
	capability, err := pairingCapabilityHash(string(ctx.GetHeader(shortPairingCapabilityHeader)))
	if err != nil {
		writeError(ctx, http.StatusNotFound, "pairing capability not found")
		return
	}
	if string(ctx.Method()) == http.MethodGet {
		attempts, e := store.ListShortPairingAttempts(c, request, capability)
		if e != nil {
			writeEndpointError(ctx, e)
			return
		}
		writeData(ctx, http.StatusOK, attempts)
		return
	}
	var input struct {
		AttemptID   string `json:"attempt_id"`
		Generation  int64  `json:"generation"`
		ClientHello string `json:"client_hello"`
	}
	if !decodeShortBody(ctx, &input) || !validE2EEKeyIdentifier(input.AttemptID) {
		writeError(ctx, http.StatusBadRequest, "invalid handshake")
		return
	}
	attempt, err := store.CreateShortPairingAttempt(
		c,
		request,
		domain.ShortPairingAttempt{
			AttemptID:              input.AttemptID,
			Generation:             input.Generation,
			ClientHello:            input.ClientHello,
			ApproverCapabilityHash: capability,
		},
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusCreated, attempt)
}
func (s *Service) handleShortPairingAttempt(c context.Context, ctx *app.RequestContext) {
	store, request, ok := s.shortPairingContext(c, ctx)
	if !ok {
		return
	}
	capability, err := pairingCapabilityHash(string(ctx.GetHeader(shortPairingCapabilityHeader)))
	if err != nil {
		writeError(ctx, http.StatusNotFound, "pairing capability not found")
		return
	}
	var attempt domain.ShortPairingAttempt
	if string(ctx.Method()) == http.MethodGet {
		attempt, err = store.GetShortPairingAttempt(c, request, ctx.Param("attempt_id"), capability)
	} else {
		var input struct {
			Stage   int    `json:"stage"`
			Payload string `json:"payload"`
		}
		if !decodeShortBody(ctx, &input) {
			writeError(ctx, http.StatusBadRequest, "invalid handshake")
			return
		}
		attempt, err = store.AdvanceShortPairingAttempt(c, request, ctx.Param("attempt_id"), capability, input.Stage, input.Payload)
	}
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, attempt)
}
func (s *Service) handleEndShortPairing(c context.Context, ctx *app.RequestContext) {
	store, request, ok := s.shortPairingContext(c, ctx)
	if !ok {
		return
	}
	capability, err := pairingCapabilityHash(string(ctx.GetHeader(shortPairingCapabilityHeader)))
	if err != nil {
		writeError(ctx, http.StatusNotFound, "pairing capability not found")
		return
	}
	var input struct {
		Reason    string `json:"reason"`
		AttemptID string `json:"attempt_id"`
	}
	if !decodeShortBody(ctx, &input) {
		writeError(ctx, http.StatusBadRequest, "invalid terminal transition")
		return
	}
	request.ApprovalAttemptID = input.AttemptID
	if err = store.EndShortPairing(c, request, capability, input.Reason); err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, map[string]string{"status": input.Reason})
}
func decodeShortBody(ctx *app.RequestContext, target any) bool {
	body := ctx.Request.Body()
	if len(body) > 16384 {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && decoder.Decode(new(any)) == io.EOF
}
