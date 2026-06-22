package manager

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	nodeRegistrationTTL          = 5 * time.Minute
	nodeRegistrationPollInterval = 2
	nodePairCodeLength           = 6
)

func StartNodeRegistration(c context.Context, ctx *app.RequestContext) {
	var req StartNodeRegistrationRequest
	decodeBody(ctx, &req)
	service := serviceFromContext(ctx)
	status, data, err := service.StartNodeRegistration(
		c,
		req,
		verificationBaseURL(service.cfg, ctx),
		nodeRegistrationNetwork(ctx),
	)
	writeEndpointResult(ctx, status, data, err)
}

func PollNodeRegistration(c context.Context, ctx *app.RequestContext) {
	var req PollNodeRegistrationRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).PollNodeRegistration(c, req)
	writeEndpointResult(ctx, status, data, err)
}

func ApproveNodeRegistration(c context.Context, ctx *app.RequestContext) {
	pairCode := strings.ToUpper(strings.TrimSpace(ctx.Param("pair_code")))
	status, data, err := serviceFromContext(ctx).ApproveNodeRegistration(c, ctx, pairCode)
	writeEndpointResult(ctx, status, data, err)
}

func GetNodeRegistration(c context.Context, ctx *app.RequestContext) {
	pairCode := strings.ToUpper(strings.TrimSpace(ctx.Param("pair_code")))
	status, data, err := serviceFromContext(ctx).GetNodeRegistration(c, ctx, pairCode)
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) StartNodeRegistration(
	c context.Context,
	req StartNodeRegistrationRequest,
	baseURL string,
	network domain.NodeRegistrationNetworkPreview,
) (int, any, error) {
	if req.Hostname == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "hostname is required"}
	}
	if req.OS == "" {
		req.OS = "unknown"
	}
	now := s.clock().UTC()
	if err := s.store.DeleteStaleNodeRegistrationSessions(c, now); err != nil {
		return 0, nil, err
	}
	registrationID, err := s.secrets.New("nreg")
	if err != nil {
		return 0, nil, apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "could not generate registration id",
		}
	}
	pollToken, err := s.secrets.New("nregpoll")
	if err != nil {
		return 0, nil, apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "could not generate poll token",
		}
	}
	expiresAt := now.Add(nodeRegistrationTTL)
	var pairCode string
	for attempt := 0; attempt < 8; attempt++ {
		pairCode, err = newNodePairCode()
		if err != nil {
			return 0, nil, apperr.Error{
				Status:  http.StatusInternalServerError,
				Message: "could not generate pair code",
			}
		}
		err = s.store.CreateNodeRegistrationSession(c, domain.NodeRegistrationSession{
			RegistrationID: registrationID,
			PairCode:       pairCode,
			PollTokenHash:  s.secrets.Hash(pollToken),
			Status:         domain.NodeRegistrationStatusPending,
			Request:        req.RegisterNodeRequest,
			RequestIP:      network.IPAddress,
			RequestCity:    network.City,
			RequestCountry: network.Country,
			ExpiresAt:      expiresAt,
			CreatedAt:      now,
		})
		if err == nil {
			break
		}
		if !errors.Is(err, ErrConflict) {
			return 0, nil, err
		}
	}
	if err != nil {
		return 0, nil, err
	}
	verificationURI := strings.TrimRight(baseURL, "/") + "/connect.html"
	return http.StatusOK, domain.StartNodeRegistrationResponse{
		RegistrationID:          registrationID,
		PairCode:                pairCode,
		PollToken:               pollToken,
		VerificationURI:         verificationURI,
		VerificationURIComplete: verificationURI + "?code=" + url.QueryEscape(pairCode),
		ExpiresIn:               int64(nodeRegistrationTTL.Seconds()),
		Interval:                nodeRegistrationPollInterval,
		ExpiresAt:               expiresAt.Format(time.RFC3339),
	}, nil
}

func (s *Service) GetNodeRegistration(
	c context.Context,
	ctx *app.RequestContext,
	pairCode string,
) (int, any, error) {
	if pairCode == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "pair code is required"}
	}
	if _, err := s.userPrincipal(c, ctx); err != nil {
		return 0, nil, err
	}
	session, err := s.store.GetNodeRegistrationSession(c, pairCode)
	if err != nil {
		return 0, nil, err
	}
	if session.Status == domain.NodeRegistrationStatusPending &&
		!session.ExpiresAt.After(s.clock().UTC()) {
		session.Status = domain.NodeRegistrationStatusExpired
	}
	return http.StatusOK, domain.NodeRegistrationPreviewResponse{
		RegistrationID: session.RegistrationID,
		PairCode:       session.PairCode,
		Status:         session.Status,
		Request:        session.Request,
		Network: domain.NodeRegistrationNetworkPreview{
			IPAddress: session.RequestIP,
			City:      session.RequestCity,
			Country:   session.RequestCountry,
		},
		ExpiresAt: session.ExpiresAt.Format(time.RFC3339),
		CreatedAt: session.CreatedAt.Format(time.RFC3339),
	}, nil
}

func (s *Service) ApproveNodeRegistration(
	c context.Context,
	ctx *app.RequestContext,
	pairCode string,
) (int, any, error) {
	if pairCode == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "pair code is required"}
	}
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		return 0, nil, err
	}
	session, err := s.store.ApproveNodeRegistrationSession(c, principal, pairCode)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, domain.ApproveNodeRegistrationResponse{
		RegistrationID: session.RegistrationID,
		PairCode:       session.PairCode,
		Status:         session.Status,
		ExpiresAt:      session.ExpiresAt.Format(time.RFC3339),
	}, nil
}

func (s *Service) PollNodeRegistration(
	c context.Context,
	req PollNodeRegistrationRequest,
) (int, any, error) {
	if req.RegistrationID == "" || req.PollToken == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "registration_id and poll_token are required",
		}
	}
	session, err := s.store.PollNodeRegistrationSession(
		c,
		req.RegistrationID,
		s.secrets.Hash(req.PollToken),
	)
	if err != nil {
		return 0, nil, err
	}
	if session.Status != domain.NodeRegistrationStatusApproved {
		return http.StatusOK, domain.PollNodeRegistrationResponse{Status: session.Status}, nil
	}
	apiKey, err := s.secrets.New("pax")
	if err != nil {
		return 0, nil, apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "could not generate api key",
		}
	}
	node, err := s.store.ConsumeNodeRegistrationSession(
		c,
		req.RegistrationID,
		s.secrets.Hash(req.PollToken),
		s.secrets.Hash(apiKey),
	)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, domain.PollNodeRegistrationResponse{
		Status: domain.NodeRegistrationStatusApproved,
		NodeID: node.NodeID,
		APIKey: apiKey,
	}, nil
}

func newNodePairCode() (string, error) {
	return newNodePairCodeWithLength(nodePairCodeLength)
}

func newNodePairCodeWithLength(length int) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var b strings.Builder
	b.Grow(length)
	max := big.NewInt(int64(len(alphabet)))
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(alphabet[n.Int64()])
	}
	return b.String(), nil
}

func verificationBaseURL(cfg Config, ctx *app.RequestContext) string {
	if baseURL := strings.TrimSpace(cfg.PaxdVerificationBaseURL); baseURL != "" {
		return baseURL
	}
	proto := string(ctx.GetHeader("X-Forwarded-Proto"))
	if proto == "" {
		proto = "http"
	}
	host := string(ctx.Host())
	if host == "" {
		host = string(ctx.GetHeader("Host"))
	}
	if host == "" {
		return ""
	}
	return proto + "://" + host
}

func nodeRegistrationNetwork(ctx *app.RequestContext) domain.NodeRegistrationNetworkPreview {
	ipAddress := clientAddress(ctx)
	if ipAddress == "unknown" {
		ipAddress = ""
	}
	return domain.NodeRegistrationNetworkPreview{
		IPAddress: ipAddress,
		City:      strings.TrimSpace(string(ctx.GetHeader("CF-IPCity"))),
		Country:   strings.TrimSpace(string(ctx.GetHeader("CF-IPCountry"))),
	}
}
