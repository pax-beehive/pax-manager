package userapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type harnessAuthClaim struct {
	owner, remote, harness string
	expires                time.Time
}

type harnessAuthPending struct {
	harnessAuthClaim
	method, commandID, previousSessionID string
}

// Only non-secret ownership metadata is retained, for at most the native login deadline.
type harnessAuthNode struct {
	mu      sync.Mutex
	claims  map[string]harnessAuthClaim
	pending map[string]harnessAuthPending
}

type harnessAuthResult struct {
	HarnessAuth *domain.HarnessAuthView `json:"harness_auth"`
	Error       *struct {
		Code string `json:"code"`
	} `json:"error"`
}

func (s *Service) GetNodeHarnessAuthStatus(
	ctx context.Context, meta auth.RequestMetadata, nodeID, harness, sessionID string,
) (int, any, error) {
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	if !validHarnessAuthTarget(nodeID, harness, sessionID) {
		return 0, nil, invalidHarnessAuthRequest()
	}
	remoteID, err := s.harnessAuthRemote(ctx, principal, nodeID)
	if err != nil {
		return 0, nil, err
	}
	node := s.lockHarnessAuthNode(nodeID)
	defer node.mu.Unlock()
	if sessionID != "" && !node.owns(sessionID, principal.User.UserID, remoteID, harness) &&
		!node.hasPending(principal.User.UserID, remoteID, harness) {
		return 0, nil, domain.ErrNotFound
	}
	view, err := s.queryHarnessAuth(ctx, nodeID, harness, sessionID)
	if err != nil {
		return 0, nil, err
	}
	if sessionID != "" && view.SessionID != sessionID {
		return 0, nil, invalidHarnessAuthResponse()
	}
	if view.SessionID != "" &&
		!node.owns(view.SessionID, principal.User.UserID, remoteID, harness) &&
		!node.reconcile(view, principal.User.UserID, remoteID) {
		return 0, nil, unownedHarnessAuthAttempt()
	}
	return http.StatusOK, domain.HarnessAuthData{HarnessAuth: view}, nil
}

func (s *Service) LoginNodeHarness(
	ctx context.Context, meta auth.RequestMetadata, req domain.LoginNodeHarnessRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	if !validHarnessAuthLogin(req) {
		return 0, nil, invalidHarnessAuthRequest()
	}
	remoteID, err := s.harnessAuthRemote(ctx, principal, req.NodeID)
	if err != nil {
		return 0, nil, err
	}
	node := s.lockHarnessAuthNode(req.NodeID)
	defer node.mu.Unlock()
	if req.Operation != "start" {
		if !node.owns(req.SessionID, principal.User.UserID, remoteID, req.Harness) {
			return 0, nil, domain.ErrNotFound
		}
	} else if err = s.checkHarnessAuthStart(ctx, node, principal, remoteID, req); err != nil {
		return 0, nil, err
	}
	view, rejected, err := s.dispatchHarnessAuth(ctx, req)
	if err != nil {
		if rejected && req.Operation == "start" {
			delete(node.pending, req.Harness)
		}
		return 0, nil, err
	}
	if err = node.claimView(view, principal.User.UserID, remoteID, s.clock()); err != nil {
		return 0, nil, err
	}
	delete(node.pending, req.Harness)
	return http.StatusAccepted, domain.HarnessAuthData{HarnessAuth: view}, nil
}

func (s *Service) dispatchHarnessAuth(
	ctx context.Context, req domain.LoginNodeHarnessRequest,
) (domain.HarnessAuthView, bool, error) {
	raw, err := s.nodeControl.Command(ctx, req.NodeID, req.CommandID, map[string]any{
		"command_id": req.CommandID, "type": "harness_auth.login", "harness_auth_login": domain.HarnessAuthLogin{
			Harness: req.Harness, Operation: req.Operation, Method: req.Method, SessionID: req.SessionID, Code: req.Code,
		},
	})
	if err != nil {
		return domain.HarnessAuthView{}, false, harnessAuthTransportError(err)
	}
	var ack struct {
		CommandID string            `json:"command_id"`
		OK        bool              `json:"ok"`
		Result    harnessAuthResult `json:"result"`
		Error     *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &ack) != nil || ack.CommandID != req.CommandID {
		return domain.HarnessAuthView{}, false, invalidHarnessAuthResponse()
	}
	if ack.Error != nil {
		return domain.HarnessAuthView{}, true, harnessAuthDaemonError(ack.Error.Code)
	}
	if !ack.OK {
		return domain.HarnessAuthView{}, false, invalidHarnessAuthResponse()
	}
	view, err := decodeHarnessAuthView(ack.Result, req.Harness)
	if err != nil || view.SessionID == "" ||
		(req.SessionID != "" && view.SessionID != req.SessionID) ||
		(req.Operation == "start" && view.Method != "" && view.Method != harnessAuthMethod(req.Harness, req.Method)) {
		return domain.HarnessAuthView{}, false, invalidHarnessAuthResponse()
	}
	return view, false, nil
}

func (n *harnessAuthNode) claimView(
	view domain.HarnessAuthView,
	owner, remote string,
	now time.Time,
) error {
	if claim, exists := n.claims[view.SessionID]; exists &&
		(claim.owner != owner || claim.remote != remote || claim.harness != view.Harness) {
		return unownedHarnessAuthAttempt()
	}
	if _, exists := n.claims[view.SessionID]; !exists {
		expires := now.Add(5 * time.Minute)
		if pending, exists := n.pending[view.Harness]; exists {
			expires = pending.expires
		}
		if deadline, parseErr := time.Parse(time.RFC3339, view.ExpiresAt); parseErr == nil &&
			deadline.Before(expires) {
			expires = deadline
		}
		n.claims[view.SessionID] = harnessAuthClaim{
			owner,
			remote,
			view.Harness,
			expires,
		}
	}
	return nil
}

func (s *Service) harnessAuthRemote(
	ctx context.Context, principal domain.UserPrincipal, nodeID string,
) (string, error) {
	node, err := s.store.GetNode(ctx, principal, nodeID)
	if err != nil {
		return "", err
	}
	if node.OwnerUserID != principal.User.UserID {
		return "", domain.ErrNotFound
	}
	if s.nodeControl == nil {
		return "", nodeControlUnavailableError()
	}
	remoteID, err := s.nodeControl.RemoteID(node.NodeID)
	if err != nil || remoteID == "" {
		return "", nodeControlUnavailableError()
	}
	return remoteID, nil
}

func (s *Service) lockHarnessAuthNode(nodeID string) *harnessAuthNode {
	value, _ := s.harnessAuthNodes.LoadOrStore(
		nodeID,
		&harnessAuthNode{
			claims:  map[string]harnessAuthClaim{},
			pending: map[string]harnessAuthPending{},
		},
	)
	node := value.(*harnessAuthNode)
	node.mu.Lock()
	for id, claim := range node.claims {
		if !s.clock().Before(claim.expires) {
			delete(node.claims, id)
		}
	}
	for harness, pending := range node.pending {
		if !s.clock().Before(pending.expires) {
			delete(node.pending, harness)
		}
	}
	return node
}

func (n *harnessAuthNode) owns(sessionID, owner, remote, harness string) bool {
	claim, exists := n.claims[sessionID]
	return exists && claim.owner == owner && claim.remote == remote && claim.harness == harness
}

func (n *harnessAuthNode) hasPending(owner, remote, harness string) bool {
	pending, exists := n.pending[harness]
	return exists && pending.owner == owner && pending.remote == remote
}

func (n *harnessAuthNode) reconcile(view domain.HarnessAuthView, owner, remote string) bool {
	pending, exists := n.pending[view.Harness]
	if !exists || !n.hasPending(owner, remote, view.Harness) || view.SessionID == "" ||
		view.SessionID == pending.previousSessionID || view.Method != pending.method {
		return false
	}
	if _, exists = n.claims[view.SessionID]; exists {
		return false
	}
	claim := pending.harnessAuthClaim
	if deadline, err := time.Parse(time.RFC3339, view.ExpiresAt); err == nil &&
		deadline.Before(claim.expires) {
		claim.expires = deadline
	}
	n.claims[view.SessionID] = claim
	delete(n.pending, view.Harness)
	return true
}

func (s *Service) checkHarnessAuthStart(
	ctx context.Context, node *harnessAuthNode, principal domain.UserPrincipal, remoteID string,
	req domain.LoginNodeHarnessRequest,
) error {
	view, err := s.queryHarnessAuth(ctx, req.NodeID, req.Harness, "")
	if err != nil {
		return err
	}
	if activeHarnessAuthState(view.State) &&
		!node.owns(view.SessionID, principal.User.UserID, remoteID, req.Harness) &&
		!node.reconcile(view, principal.User.UserID, remoteID) {
		return unownedHarnessAuthAttempt()
	}
	node.pending[req.Harness] = harnessAuthPending{
		harnessAuthClaim: harnessAuthClaim{
			principal.User.UserID,
			remoteID,
			req.Harness,
			s.clock().Add(5 * time.Minute),
		},
		method: harnessAuthMethod(
			req.Harness,
			req.Method,
		), commandID: req.CommandID, previousSessionID: view.SessionID,
	}
	return nil
}

func (s *Service) queryHarnessAuth(
	ctx context.Context, nodeID, harness, sessionID string,
) (domain.HarnessAuthView, error) {
	raw, err := s.queryNodeControl(ctx, nodeID, map[string]any{
		"type": "harness_auth.status", "harness_auth_status": map[string]any{"harness": harness, "session_id": sessionID},
	})
	if err != nil {
		return domain.HarnessAuthView{}, err
	}
	var result harnessAuthResult
	if json.Unmarshal(raw, &result) != nil {
		return domain.HarnessAuthView{}, invalidHarnessAuthResponse()
	}
	return decodeHarnessAuthView(result, harness)
}

func decodeHarnessAuthView(
	result harnessAuthResult,
	harness string,
) (domain.HarnessAuthView, error) {
	if result.Error != nil {
		return domain.HarnessAuthView{}, harnessAuthDaemonError(result.Error.Code)
	}
	if result.HarnessAuth == nil || result.HarnessAuth.Harness != harness {
		return domain.HarnessAuthView{}, invalidHarnessAuthResponse()
	}
	view := *result.HarnessAuth
	switch view.State {
	case "starting",
		"awaiting_browser",
		"awaiting_code",
		"exchanging",
		"succeeded",
		"failed",
		"expired",
		"cancelled":
		if view.SessionID == "" {
			return domain.HarnessAuthView{}, invalidHarnessAuthResponse()
		}
	case "logged_in", "logged_out":
	default:
		return domain.HarnessAuthView{}, invalidHarnessAuthResponse()
	}
	if view.State != "awaiting_browser" && view.State != "awaiting_code" {
		view.AuthorizationURL, view.UserCode = "", ""
	}
	view.Account = sanitizeHarnessAuthAccount(view.Account)
	if view.State != "logged_in" && view.State != "succeeded" {
		view.Account = nil
	}
	return view, nil
}

func activeHarnessAuthState(state string) bool {
	return state == "starting" || state == "awaiting_browser" || state == "awaiting_code" ||
		state == "exchanging"
}

func validHarnessAuthTarget(nodeID, harness, sessionID string) bool {
	return nodeID != "" && strings.TrimSpace(nodeID) == nodeID &&
		(harness == "claude" || harness == "codex") && len(sessionID) <= 128 &&
		!strings.ContainsAny(sessionID, "\r\n\x00")
}

func validHarnessAuthLogin(req domain.LoginNodeHarnessRequest) bool {
	if !validHarnessAuthTarget(req.NodeID, req.Harness, req.SessionID) || req.CommandID == "" ||
		len(req.CommandID) > 128 || strings.TrimSpace(req.CommandID) != req.CommandID ||
		strings.ContainsAny(req.CommandID, "\r\n\x00") {
		return false
	}
	switch req.Operation {
	case "start":
		return validHarnessAuthStart(req)
	case "submit":
		return req.Harness == "claude" && req.SessionID != "" && req.Method == "" &&
			validHarnessAuthSecret(req.Code)
	case "cancel":
		return req.SessionID != "" && req.Code == "" && req.Method == ""
	}
	return false
}

func validHarnessAuthStart(req domain.LoginNodeHarnessRequest) bool {
	if req.SessionID != "" {
		return false
	}
	if req.Harness == "claude" {
		return req.Code == "" &&
			(req.Method == "" || req.Method == "subscription" || req.Method == "console")
	}
	if req.Method == "api-key" || req.Method == "access-token" {
		return validHarnessAuthSecret(req.Code)
	}
	return req.Code == "" && (req.Method == "" || req.Method == "device")
}

func harnessAuthMethod(harness, method string) string {
	if method != "" {
		return method
	}
	if harness == "codex" {
		return "device"
	}
	return "subscription"
}

func validHarnessAuthSecret(code string) bool {
	return code != "" && len(code) <= 4096 && strings.TrimSpace(code) == code &&
		!strings.ContainsAny(code, "\r\n\x00")
}

func invalidHarnessAuthRequest() error {
	return apperr.Error{Status: http.StatusBadRequest, Message: "invalid harness login request"}
}

func invalidHarnessAuthResponse() error {
	return apperr.Error{
		Status:  http.StatusBadGateway,
		Message: "invalid harness authentication response",
	}
}

func unownedHarnessAuthAttempt() error {
	return apperr.Error{
		Status:  http.StatusConflict,
		Message: "login attempt belongs to another caller or a previous Manager instance; wait for it to expire",
	}
}

func harnessAuthDaemonError(code string) error {
	status := http.StatusBadGateway
	switch code {
	case "invalid_argument":
		status = http.StatusBadRequest
	case "not_found":
		status = http.StatusNotFound
	case "conflict":
		status = http.StatusConflict
	}
	return apperr.Error{
		Status:  status,
		Message: "harness authentication failed: " + safeHarnessAuthErrorCode(code),
	}
}

func safeHarnessAuthErrorCode(code string) string {
	switch code {
	case "invalid_argument", "not_found", "conflict":
		return code
	default:
		return "node_error"
	}
}

func harnessAuthTransportError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return apperr.Error{
			Status:  http.StatusGatewayTimeout,
			Message: "harness authentication request timed out; check status before retrying",
		}
	}
	return nodeControlUnavailableError()
}
