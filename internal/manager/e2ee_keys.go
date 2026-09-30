package manager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	e2eePairingLifetime        = 10 * time.Minute
	e2eePairingPublicKeyBytes  = 65
	e2eePairingCommitmentBytes = 32
	e2eePairingNonceBytes      = 12
	e2eeWrappedRootMinBytes    = 48
)

type createE2EEPairingRequest struct {
	PairingID          string `json:"pairing_id"`
	DeviceID           string `json:"device_id"`
	DeviceName         string `json:"device_name"`
	KeyEpoch           int64  `json:"key_epoch"`
	RecipientPublicKey string `json:"recipient_public_key"`
	SecretCommitment   string `json:"secret_commitment"`
}

type completeE2EEPairingRequest struct {
	SenderEphemeralPublicKey string `json:"sender_ephemeral_public_key"`
	Nonce                    string `json:"nonce"`
	Ciphertext               string `json:"ciphertext"`
}

type e2eePairingResponse struct {
	PairingID          string     `json:"pairing_id"`
	NodeID             string     `json:"node_id"`
	AgentID            string     `json:"agent_id"`
	DeviceID           string     `json:"device_id"`
	DeviceName         string     `json:"device_name"`
	KeyEpoch           int64      `json:"key_epoch"`
	RecipientPublicKey string     `json:"recipient_public_key"`
	SecretCommitment   string     `json:"secret_commitment"`
	CreatedAt          time.Time  `json:"created_at"`
	ExpiresAt          time.Time  `json:"expires_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

type e2eeKeyPackageResponse struct {
	PairingID                string    `json:"pairing_id"`
	NodeID                   string    `json:"node_id"`
	AgentID                  string    `json:"agent_id"`
	DeviceID                 string    `json:"device_id"`
	KeyEpoch                 int64     `json:"key_epoch"`
	RecipientPublicKey       string    `json:"recipient_public_key"`
	SenderEphemeralPublicKey string    `json:"sender_ephemeral_public_key"`
	Nonce                    string    `json:"nonce"`
	Ciphertext               string    `json:"ciphertext"`
	CreatedAt                time.Time `json:"created_at"`
}

func (s *Service) handleCreateE2EEPairing(c context.Context, ctx *app.RequestContext) {
	principal, agent, ok := s.ownedE2EEAgent(c, ctx)
	if !ok {
		return
	}
	var input createE2EEPairingRequest
	if err := json.Unmarshal(ctx.Request.Body(), &input); err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid pairing request")
		return
	}
	if input.KeyEpoch == 0 {
		input.KeyEpoch = 1
	}
	recipientPublicKey, err := decodeE2EEKeyField(
		input.RecipientPublicKey, e2eePairingPublicKeyBytes,
	)
	if err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid recipient public key")
		return
	}
	secretCommitment, err := decodeE2EEKeyField(
		input.SecretCommitment, e2eePairingCommitmentBytes,
	)
	if err != nil || !validE2EEKeyIdentifier(input.PairingID) ||
		!validE2EEKeyIdentifier(input.DeviceID) || input.KeyEpoch < 1 ||
		len(input.DeviceName) > 256 {
		writeError(ctx, http.StatusBadRequest, "invalid pairing request")
		return
	}
	now := s.clock().UTC()
	created, err := s.store.CreateE2EEPairingRequest(c, domain.E2EEPairingRequest{
		PairingID: input.PairingID, OwnerUserID: principal.User.UserID,
		NodeID: agent.NodeID, AgentID: agent.AgentID, DeviceID: input.DeviceID,
		DeviceName: input.DeviceName, KeyEpoch: input.KeyEpoch,
		RecipientPublicKey: recipientPublicKey, SecretCommitment: secretCommitment,
		CreatedAt: now, ExpiresAt: now.Add(e2eePairingLifetime),
	})
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusCreated, encodeE2EEPairingResponse(created))
}

// Request completion and browser delivery are separate: an approved request does
// not expire while its browser is offline. GET exposes state without reapproving.
func (s *Service) handleGetUserE2EEPairing(c context.Context, ctx *app.RequestContext) {
	ctx.Header("Cache-Control", "private, no-store")
	principal, agent, ok := s.ownedE2EEAgent(c, ctx)
	if !ok {
		return
	}
	request, err := s.store.GetE2EEPairingRequest(
		c,
		principal.User.UserID,
		agent.AgentID,
		ctx.Param("pairing_id"),
	)
	if err != nil && !errors.Is(err, domain.ErrE2EEPairingSuperseded) {
		writeEndpointError(ctx, err)
		return
	}
	status := "pending"
	switch {
	case request.CompletedAt != nil:
		status = "approved"
	case request.SupersededAt != nil:
		status = "superseded"
	case !request.ExpiresAt.After(s.clock().UTC()):
		status = "expired"
	}
	writeData(ctx, http.StatusOK, struct {
		e2eePairingResponse
		Status string `json:"status"`
	}{encodeE2EEPairingResponse(request), status})
}

func (s *Service) handleListE2EEPairings(c context.Context, ctx *app.RequestContext) {
	principal, agent, ok := s.ownedE2EEAgent(c, ctx)
	if !ok {
		return
	}
	requests, err := s.store.ListPendingE2EEPairingRequests(
		c, principal.User.UserID, agent.AgentID, 50,
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	responses := make([]e2eePairingResponse, 0, len(requests))
	for _, request := range requests {
		responses = append(responses, encodeE2EEPairingResponse(request))
	}
	writeData(ctx, http.StatusOK, responses)
}

func (s *Service) handleCompleteUserE2EEPairing(c context.Context, ctx *app.RequestContext) {
	principal, agent, ok := s.ownedE2EEAgent(c, ctx)
	if !ok {
		return
	}
	s.completeE2EEPairing(
		c, ctx, principal.User.UserID, agent.NodeID, agent.AgentID, ctx.Param("pairing_id"),
	)
}

func (s *Service) handleGetE2EEKeyPackage(c context.Context, ctx *app.RequestContext) {
	principal, agent, ok := s.ownedE2EEAgent(c, ctx)
	if !ok {
		return
	}
	keyEpoch, err := strconv.ParseInt(string(ctx.Query("key_epoch")), 10, 64)
	if err != nil || keyEpoch < 1 {
		writeError(ctx, http.StatusBadRequest, "invalid key epoch")
		return
	}
	keyPackage, err := s.store.GetE2EEKeyPackage(
		c, principal.User.UserID, agent.AgentID, ctx.Param("device_id"), keyEpoch,
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, encodeE2EEKeyPackageResponse(keyPackage))
}

func (s *Service) handleGetNodeE2EEPairing(c context.Context, ctx *app.RequestContext) {
	node, agent, ok := s.nodeE2EEAgent(c, ctx)
	if !ok {
		return
	}
	request, err := s.store.GetE2EEPairingRequest(
		c, node.OwnerUserID, agent.AgentID, ctx.Param("pairing_id"),
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, encodeE2EEPairingResponse(request))
}

func (s *Service) handleCompleteNodeE2EEPairing(c context.Context, ctx *app.RequestContext) {
	node, agent, ok := s.nodeE2EEAgent(c, ctx)
	if !ok {
		return
	}
	s.completeE2EEPairing(
		c, ctx, node.OwnerUserID, node.NodeID, agent.AgentID, ctx.Param("pairing_id"),
	)
}

func (s *Service) completeE2EEPairing(
	c context.Context,
	ctx *app.RequestContext,
	ownerUserID string,
	nodeID string,
	agentID string,
	pairingID string,
) {
	request, err := s.store.GetE2EEPairingRequest(c, ownerUserID, agentID, pairingID)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if request.NodeID != nodeID {
		writeEndpointError(ctx, ErrNotFound)
		return
	}
	var input completeE2EEPairingRequest
	if err := json.Unmarshal(ctx.Request.Body(), &input); err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid key package")
		return
	}
	senderPublicKey, err := decodeE2EEKeyField(
		input.SenderEphemeralPublicKey, e2eePairingPublicKeyBytes,
	)
	if err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid sender public key")
		return
	}
	nonce, err := decodeE2EEKeyField(input.Nonce, e2eePairingNonceBytes)
	if err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid key package nonce")
		return
	}
	ciphertext, err := base64.StdEncoding.Strict().DecodeString(input.Ciphertext)
	if err != nil || len(ciphertext) < e2eeWrappedRootMinBytes || len(ciphertext) > 4096 {
		writeError(ctx, http.StatusBadRequest, "invalid key package ciphertext")
		return
	}
	keyPackage, err := s.store.CompleteE2EEPairing(c, request, domain.E2EEKeyPackage{
		PairingID: request.PairingID, OwnerUserID: request.OwnerUserID,
		NodeID: request.NodeID, AgentID: request.AgentID, DeviceID: request.DeviceID,
		KeyEpoch: request.KeyEpoch, RecipientPublicKey: request.RecipientPublicKey,
		SenderEphemeralPublicKey: senderPublicKey, Nonce: nonce, Ciphertext: ciphertext,
		CreatedAt: s.clock().UTC(),
	})
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusCreated, encodeE2EEKeyPackageResponse(keyPackage))
}

func (s *Service) ownedE2EEAgent(
	c context.Context,
	ctx *app.RequestContext,
) (domain.UserPrincipal, domain.Agent, bool) {
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return domain.UserPrincipal{}, domain.Agent{}, false
	}
	routeUserID := ctx.Param("user_id")
	if routeUserID != "" && routeUserID != "self" && routeUserID != principal.User.UserID {
		writeEndpointError(ctx, ErrNotFound)
		return domain.UserPrincipal{}, domain.Agent{}, false
	}
	agent, err := s.store.GetAgent(c, principal, ctx.Param("agent_id"))
	if err != nil || agent.OwnerUserID != principal.User.UserID {
		writeEndpointError(ctx, ErrNotFound)
		return domain.UserPrincipal{}, domain.Agent{}, false
	}
	return principal, agent, true
}

func (s *Service) nodeE2EEAgent(
	c context.Context,
	ctx *app.RequestContext,
) (domain.Node, domain.Agent, bool) {
	node := nodeFromContext(ctx)
	agent, err := s.store.GetNodeAgent(c, node.NodeID, ctx.Param("agent_id"))
	if err != nil || agent.OwnerUserID != node.OwnerUserID {
		writeEndpointError(ctx, ErrNotFound)
		return domain.Node{}, domain.Agent{}, false
	}
	return node, agent, true
}

func decodeE2EEKeyField(value string, expectedBytes int) ([]byte, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != expectedBytes {
		return nil, ErrConflict
	}
	return decoded, nil
}

func validE2EEKeyIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	return !strings.ContainsAny(value, " \t\r\n/\\")
}

func encodeE2EEPairingResponse(request domain.E2EEPairingRequest) e2eePairingResponse {
	return e2eePairingResponse{
		PairingID: request.PairingID, NodeID: request.NodeID, AgentID: request.AgentID,
		DeviceID: request.DeviceID, DeviceName: request.DeviceName, KeyEpoch: request.KeyEpoch,
		RecipientPublicKey: base64.StdEncoding.EncodeToString(request.RecipientPublicKey),
		SecretCommitment:   base64.StdEncoding.EncodeToString(request.SecretCommitment),
		CreatedAt:          request.CreatedAt, ExpiresAt: request.ExpiresAt, CompletedAt: request.CompletedAt,
	}
}

func encodeE2EEKeyPackageResponse(keyPackage domain.E2EEKeyPackage) e2eeKeyPackageResponse {
	return e2eeKeyPackageResponse{
		PairingID: keyPackage.PairingID, NodeID: keyPackage.NodeID,
		AgentID: keyPackage.AgentID, DeviceID: keyPackage.DeviceID,
		KeyEpoch:           keyPackage.KeyEpoch,
		RecipientPublicKey: base64.StdEncoding.EncodeToString(keyPackage.RecipientPublicKey),
		SenderEphemeralPublicKey: base64.StdEncoding.EncodeToString(
			keyPackage.SenderEphemeralPublicKey,
		),
		Nonce:      base64.StdEncoding.EncodeToString(keyPackage.Nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(keyPackage.Ciphertext),
		CreatedAt:  keyPackage.CreatedAt,
	}
}
