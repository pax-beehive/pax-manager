package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

// User-requested local file fetch. The console checks a path and then asks
// the session node to fetch it; accepted fetches reuse the artifact
// publication pipeline (paxd registers, prepares, uploads, and completes).

const (
	maxArtifactFetchPathLength = 8192
	artifactFetchCheckQuery    = "artifact_fetch.check"
	artifactFetchCommand       = "artifact.fetch"
	artifactFetchIDPrefix      = "apub_"
)

var artifactFetchControlTimeout = 10 * time.Second

type artifactFetchDenial struct {
	Code    string `json:"code"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

type artifactFetchCheckData struct {
	Allowed   bool                 `json:"allowed"`
	Path      string               `json:"path"`
	Filename  string               `json:"filename"`
	SizeBytes *int64               `json:"size_bytes,omitempty"`
	Denial    *artifactFetchDenial `json:"denial,omitempty"`
}

type artifactFetchResultData struct {
	Accepted    bool                 `json:"accepted"`
	Publication *ArtifactPublication `json:"publication,omitempty"`
	Denial      *artifactFetchDenial `json:"denial,omitempty"`
}

type artifactFetchTarget struct {
	principal UserPrincipal
	sessionID string
	agentID   string
	nodeID    string
}

type artifactFetchAppliedAck struct {
	filename string
	denial   *artifactFetchDenial
}

func (s *Service) handleCheckArtifactFetch(c context.Context, ctx *app.RequestContext) {
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	path, err := validateArtifactFetchPath(string(ctx.QueryArgs().Peek("path")))
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	target, err := s.resolveArtifactFetchTarget(c, ctx, principal)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	query := map[string]any{
		"type":                 artifactFetchCheckQuery,
		"check_artifact_fetch": map[string]any{"path": path},
	}
	raw, err := s.callArtifactFetchControl(
		c,
		func(callCtx context.Context) (json.RawMessage, error) {
			requestID, err := newArtifactFetchID("ctlqafc_")
			if err != nil {
				return nil, err
			}
			return s.nodeControls.Query(callCtx, target.nodeID, requestID, query)
		},
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	data, err := decodeArtifactFetchCheck(raw)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	attrs := artifactFetchLogAttrs(target, "", path)
	attrs = append(attrs, slog.Bool("allowed", data.Allowed))
	if data.Denial != nil {
		attrs = append(attrs, slog.String("denial_code", data.Denial.Code))
	}
	logging.Info(c, "artifact fetch checked", attrs...)
	writeData(ctx, http.StatusOK, data)
}

func (s *Service) handleCreateArtifactFetch(c context.Context, ctx *app.RequestContext) {
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	var req struct {
		Path *string `json:"path"`
	}
	if err := json.Unmarshal(ctx.Request.Body(), &req); err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Path == nil {
		writeError(ctx, http.StatusBadRequest, "path is required")
		return
	}
	path, err := validateArtifactFetchPath(*req.Path)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	target, err := s.resolveArtifactFetchTarget(c, ctx, principal)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	publicationID, err := newArtifactFetchID(artifactFetchIDPrefix)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	command := map[string]any{
		"command_id": publicationID,
		"type":       artifactFetchCommand,
		"fetch_artifact": map[string]any{
			"publication_id": publicationID,
			"agent_id":       target.agentID,
			"session_id":     target.sessionID,
			"path":           path,
		},
	}
	raw, err := s.callArtifactFetchControl(
		c,
		func(callCtx context.Context) (json.RawMessage, error) {
			return s.nodeControls.Command(callCtx, target.nodeID, publicationID, command)
		},
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	ack, err := decodeArtifactFetchAck(raw, publicationID)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	attrs := artifactFetchLogAttrs(target, publicationID, path)
	if ack.denial != nil {
		attrs = append(attrs,
			slog.String("outcome", "denied"),
			slog.String("denial_code", ack.denial.Code),
		)
		logging.Info(c, "artifact fetch requested", attrs...)
		writeData(ctx, http.StatusOK, artifactFetchResultData{Denial: ack.denial})
		return
	}
	publication, err := s.recordArtifactFetchPublication(c, target, publicationID, path, ack)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	logging.Info(c, "artifact fetch requested", append(attrs,
		slog.String("outcome", "accepted"),
	)...)
	writeData(ctx, http.StatusAccepted, artifactFetchResultData{
		Accepted:    true,
		Publication: &publication,
	})
}

// recordArtifactFetchPublication upserts the publication and stamps the audit
// fields. paxd's worker may have registered the same id first; the upsert is
// idempotent on the source tuple and the mark only touches origin/audit data.
func (s *Service) recordArtifactFetchPublication(
	c context.Context,
	target artifactFetchTarget,
	publicationID string,
	path string,
	ack artifactFetchAppliedAck,
) (ArtifactPublication, error) {
	userID := target.principal.User.UserID
	_, err := s.store.PutArtifactPublication(c, ArtifactPublication{
		PublicationID:     publicationID,
		OwnerUserID:       userID,
		NodeID:            target.nodeID,
		AgentID:           target.agentID,
		SessionID:         target.sessionID,
		Filename:          ack.filename,
		Status:            domain.ArtifactPublicationStatusQueued,
		Origin:            domain.ArtifactPublicationOriginUserFetch,
		RequestedByUserID: userID,
		RequestedPath:     path,
	})
	if errors.Is(err, domain.ErrConflict) {
		return ArtifactPublication{}, apperr.Error{
			Status:  http.StatusBadGateway,
			Message: "paxd registered a conflicting artifact publication",
		}
	}
	if err != nil {
		return ArtifactPublication{}, err
	}
	return s.store.MarkArtifactPublicationUserFetch(c, publicationID, userID, path)
}

func (s *Service) resolveArtifactFetchTarget(
	c context.Context,
	ctx *app.RequestContext,
	principal UserPrincipal,
) (artifactFetchTarget, error) {
	routeUserID := ctx.Param("user_id")
	if routeUserID != "" && routeUserID != "self" && routeUserID != principal.User.UserID {
		return artifactFetchTarget{}, domain.ErrNotFound
	}
	session, err := s.store.GetSession(c, principal, strings.TrimSpace(ctx.Param("session_id")))
	if err != nil {
		return artifactFetchTarget{}, err
	}
	agent, err := s.store.GetAgent(c, principal, session.AgentID)
	if err != nil || agent.OwnerUserID != principal.User.UserID {
		return artifactFetchTarget{}, domain.ErrNotFound
	}
	if session.Transport == domain.SessionTransportE2EE {
		return artifactFetchTarget{}, apperr.Error{
			Status:  http.StatusConflict,
			Message: "local file fetch is not available for encrypted sessions",
		}
	}
	nodeID := firstNonEmpty(session.NodeID, agent.NodeID)
	if nodeID == "" {
		return artifactFetchTarget{}, apperr.Error{
			Status:  http.StatusServiceUnavailable,
			Message: "session is not bound to a paxd node",
		}
	}
	return artifactFetchTarget{
		principal: principal,
		sessionID: session.SessionID,
		agentID:   session.AgentID,
		nodeID:    nodeID,
	}, nil
}

func (s *Service) callArtifactFetchControl(
	c context.Context,
	call func(context.Context) (json.RawMessage, error),
) (json.RawMessage, error) {
	if s.nodeControls == nil {
		return nil, artifactFetchUnavailable()
	}
	callCtx, cancel := context.WithTimeout(c, artifactFetchControlTimeout)
	defer cancel()
	raw, err := call(callCtx)
	if err == nil {
		return raw, nil
	}
	var wireErr nodeControlWireError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return nil, apperr.Error{
			Status:  http.StatusGatewayTimeout,
			Message: "node control request timed out",
		}
	case errors.As(err, &wireErr):
		return nil, artifactFetchBadGateway("paxd rejected the request: " + wireErr.Error())
	default:
		return nil, artifactFetchUnavailable()
	}
}

func decodeArtifactFetchCheck(raw json.RawMessage) (artifactFetchCheckData, error) {
	var result struct {
		Error *nodeControlWireError `json:"error,omitempty"`
		Check *struct {
			Allowed   *bool                `json:"allowed"`
			Path      string               `json:"path"`
			Filename  string               `json:"filename"`
			SizeBytes *int64               `json:"size_bytes"`
			Denial    *artifactFetchDenial `json:"denial"`
		} `json:"artifact_fetch_check,omitempty"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return artifactFetchCheckData{}, artifactFetchBadGateway(
			"paxd returned an invalid artifact fetch check",
		)
	}
	if result.Error != nil {
		return artifactFetchCheckData{}, artifactFetchBadGateway(
			"paxd artifact fetch check failed: " + result.Error.Error(),
		)
	}
	check := result.Check
	if check == nil || check.Allowed == nil {
		return artifactFetchCheckData{}, artifactFetchBadGateway(
			"paxd artifact fetch check is missing a decision",
		)
	}
	data := artifactFetchCheckData{
		Allowed:  *check.Allowed,
		Path:     check.Path,
		Filename: check.Filename,
	}
	if data.Allowed {
		if check.SizeBytes == nil || *check.SizeBytes < 0 ||
			!validArtifactFilename(strings.TrimSpace(check.Filename)) {
			return artifactFetchCheckData{}, artifactFetchBadGateway(
				"paxd returned an invalid allowed artifact fetch check",
			)
		}
		data.SizeBytes = check.SizeBytes
		return data, nil
	}
	if check.Denial == nil || strings.TrimSpace(check.Denial.Code) == "" {
		return artifactFetchCheckData{}, artifactFetchBadGateway(
			"paxd returned a denied artifact fetch check without a denial",
		)
	}
	data.Denial = check.Denial
	return data, nil
}

func decodeArtifactFetchAck(
	raw json.RawMessage,
	publicationID string,
) (artifactFetchAppliedAck, error) {
	var ack struct {
		OK       bool                  `json:"ok"`
		Status   string                `json:"status"`
		TargetID string                `json:"target_id"`
		Error    *nodeControlWireError `json:"error,omitempty"`
		Result   *struct {
			ArtifactFetch *struct {
				PublicationID string `json:"publication_id"`
				Filename      string `json:"filename"`
			} `json:"artifact_fetch"`
		} `json:"result,omitempty"`
	}
	if err := json.Unmarshal(raw, &ack); err != nil {
		return artifactFetchAppliedAck{}, artifactFetchBadGateway(
			"paxd returned an invalid artifact fetch acknowledgement",
		)
	}
	if !ack.OK {
		if ack.Status == "rejected" && ack.Error != nil && strings.TrimSpace(ack.Error.Code) != "" {
			return artifactFetchAppliedAck{denial: &artifactFetchDenial{
				Code:    ack.Error.Code,
				Rule:    ack.Error.Target,
				Message: ack.Error.Message,
			}}, nil
		}
		message := "paxd failed the artifact fetch"
		if ack.Error != nil {
			message += ": " + ack.Error.Error()
		}
		return artifactFetchAppliedAck{}, artifactFetchBadGateway(message)
	}
	if ack.Status != "applied" || ack.Result == nil || ack.Result.ArtifactFetch == nil {
		return artifactFetchAppliedAck{}, artifactFetchBadGateway(
			"paxd returned an unexpected artifact fetch acknowledgement",
		)
	}
	fetch := ack.Result.ArtifactFetch
	filename := strings.TrimSpace(fetch.Filename)
	if fetch.PublicationID != publicationID ||
		(ack.TargetID != "" && ack.TargetID != publicationID) ||
		!validArtifactFilename(filename) {
		return artifactFetchAppliedAck{}, artifactFetchBadGateway(
			"paxd returned an inconsistent artifact fetch acknowledgement",
		)
	}
	return artifactFetchAppliedAck{filename: filename}, nil
}

func validateArtifactFetchPath(path string) (string, error) {
	switch {
	case strings.TrimSpace(path) == "":
		return "", invalidArtifactFetchPath("path is required")
	case len(path) > maxArtifactFetchPathLength:
		return "", invalidArtifactFetchPath("path is too long")
	case !utf8.ValidString(path) || strings.ContainsAny(path, "\x00\r\n"):
		return "", invalidArtifactFetchPath("path is malformed")
	case !strings.HasPrefix(path, "/"):
		return "", invalidArtifactFetchPath("path must be absolute")
	default:
		return path, nil
	}
}

func newArtifactFetchID(prefix string) (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buf[:]), nil
}

func artifactFetchLogAttrs(
	target artifactFetchTarget,
	publicationID string,
	path string,
) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("user_id", target.principal.User.UserID),
		slog.String("node_id", target.nodeID),
		slog.String("agent_id", target.agentID),
		slog.String("session_id", target.sessionID),
		slog.String("path", path),
	}
	if publicationID != "" {
		attrs = append(attrs, slog.String("publication_id", publicationID))
	}
	return attrs
}

func invalidArtifactFetchPath(message string) error {
	return apperr.Error{Status: http.StatusBadRequest, Message: message}
}

func artifactFetchBadGateway(message string) error {
	return apperr.Error{Status: http.StatusBadGateway, Message: message}
}

func artifactFetchUnavailable() error {
	return apperr.Error{
		Status:  http.StatusServiceUnavailable,
		Message: "node control tunnel is not connected",
	}
}
