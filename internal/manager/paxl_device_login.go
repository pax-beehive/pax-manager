package manager

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	paxlDeviceLoginTTL          = 10 * time.Minute
	paxlDeviceLoginPollInterval = 2
	paxlUserCodeLength          = 6
)

func StartPaxlDeviceLogin(c context.Context, ctx *app.RequestContext) {
	var req StartPaxlDeviceLoginRequest
	decodeBody(ctx, &req)
	service := serviceFromContext(ctx)
	status, data, err := service.StartPaxlDeviceLogin(
		c,
		req,
		verificationBaseURL(service.cfg, ctx),
	)
	writeEndpointResult(ctx, status, data, err)
}

func PollPaxlDeviceLogin(c context.Context, ctx *app.RequestContext) {
	ctx.Header("Cache-Control", "no-store")
	var req PollPaxlDeviceLoginRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).PollPaxlDeviceLogin(c, req)
	writeEndpointResult(ctx, status, data, err)
}

func ApprovePaxlDeviceLogin(c context.Context, ctx *app.RequestContext) {
	userCode := strings.ToUpper(strings.TrimSpace(ctx.Param("user_code")))
	status, data, err := serviceFromContext(ctx).ApprovePaxlDeviceLogin(c, ctx, userCode)
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) StartPaxlDeviceLogin(
	c context.Context,
	req StartPaxlDeviceLoginRequest,
	baseURL string,
) (int, any, error) {
	if req.Protocol != "" && req.Protocol != domain.PaxlDeviceLoginProtocolClientCommit {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "unsupported login protocol",
		}
	}
	now := s.clock().UTC()
	if err := s.store.DeleteStalePaxlDeviceLoginSessions(c, now); err != nil {
		return 0, nil, err
	}
	loginID, err := s.secrets.New("paxllogin")
	if err != nil {
		return 0, nil, apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "could not generate login id",
		}
	}
	pollToken, err := s.secrets.New("paxlpoll")
	if err != nil {
		return 0, nil, apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "could not generate poll token",
		}
	}
	expiresAt := now.Add(paxlDeviceLoginTTL)
	var userCode string
	for attempt := 0; attempt < 8; attempt++ {
		userCode, err = newNodePairCodeWithLength(paxlUserCodeLength)
		if err != nil {
			return 0, nil, apperr.Error{
				Status:  http.StatusInternalServerError,
				Message: "could not generate user code",
			}
		}
		err = s.store.CreatePaxlDeviceLoginSession(c, domain.PaxlDeviceLoginSession{
			Protocol:      req.Protocol,
			LoginID:       loginID,
			UserCode:      userCode,
			PollTokenHash: s.secrets.Hash(pollToken),
			Status:        domain.PaxlDeviceLoginStatusPending,
			ClientName:    strings.TrimSpace(req.ClientName),
			ExpiresAt:     expiresAt,
			CreatedAt:     now,
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
	verificationURI := strings.TrimRight(baseURL, "/") + "/paxl-login.html"
	return http.StatusOK, domain.StartPaxlDeviceLoginResponse{
		Protocol:                req.Protocol,
		Region:                  s.cfg.Region,
		LoginID:                 loginID,
		UserCode:                userCode,
		PollToken:               pollToken,
		VerificationURI:         verificationURI,
		VerificationURIComplete: verificationURI + "?code=" + url.QueryEscape(userCode),
		ExpiresIn:               int64(paxlDeviceLoginTTL.Seconds()),
		Interval:                paxlDeviceLoginPollInterval,
		ExpiresAt:               expiresAt.Format(time.RFC3339),
	}, nil
}

func (s *Service) ApprovePaxlDeviceLogin(
	c context.Context,
	ctx *app.RequestContext,
	userCode string,
) (int, any, error) {
	if userCode == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "user code is required"}
	}
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		return 0, nil, err
	}
	if s.cfg.Region != "" {
		proof, err := auth.VerifyPaxlLoginApproval(
			ctx.Request.Body(),
			string(ctx.GetHeader("X-Pax-Login-Timestamp")),
			string(ctx.GetHeader("X-Pax-Login-Signature")),
			s.cfg.Region,
			s.cfg.RegionProvisioningSecret,
			userCode,
			s.clock(),
		)
		if err != nil || proof.IdentityKey != principal.User.Email {
			return 0, nil, ErrUnauthorized
		}
		if proof.Purpose == "admin" {
			if !principal.IsAdmin {
				return 0, nil, apperr.Error{
					Status:  http.StatusForbidden,
					Message: "regional administrator permission is required",
				}
			}
		} else if proof.UserID != principal.User.UserID {
			return 0, nil, ErrUnauthorized
		}
	}
	key, err := s.secrets.New("paxu")
	if err != nil {
		return 0, nil, apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "could not generate api key",
		}
	}
	session, err := s.store.ApprovePaxlDeviceLoginSession(
		c, principal, userCode, s.secrets.Hash(key), s.secrets.Prefix(key), key,
	)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, domain.ApprovePaxlDeviceLoginResponse{
		LoginID:   session.LoginID,
		UserCode:  session.UserCode,
		Status:    session.Status,
		NodeID:    session.NodeID,
		ExpiresAt: session.ExpiresAt.Format(time.RFC3339),
	}, nil
}

func (s *Service) PollPaxlDeviceLogin(
	c context.Context,
	req PollPaxlDeviceLoginRequest,
) (int, any, error) {
	if req.LoginID == "" || req.PollToken == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "login_id and poll_token are required",
		}
	}
	var session domain.PaxlDeviceLoginSession
	var err error
	if req.Action != "" {
		session, err = s.updatePaxlLogin(c, req)
	} else {
		session, err = s.store.PollPaxlDeviceLoginSession(c, req.LoginID, s.secrets.Hash(req.PollToken))
	}
	if err != nil {
		return 0, nil, err
	}
	if session.Protocol == domain.PaxlDeviceLoginProtocolClientCommit {
		return s.clientCommitLoginResponse(c, session)
	}
	if session.Status != domain.PaxlDeviceLoginStatusApproved {
		return http.StatusOK, domain.PollPaxlDeviceLoginResponse{Status: session.Status}, nil
	}
	consumed, err := s.store.ConsumePaxlDeviceLoginSession(
		c,
		req.LoginID,
		s.secrets.Hash(req.PollToken),
	)
	if err != nil {
		return 0, nil, err
	}
	user, err := s.store.GetUser(c, consumed.OwnerUserID)
	if err != nil {
		return 0, nil, err
	}
	keys, err := s.store.ListUserAPIKeys(c, domain.UserPrincipal{User: user})
	if err != nil {
		return 0, nil, err
	}
	var keyMeta *domain.UserAPIKey
	for i := range keys {
		if keys[i].KeyID == consumed.UserAPIKeyID {
			keyMeta = &keys[i]
			break
		}
	}
	return http.StatusOK, domain.PollPaxlDeviceLoginResponse{
		Status:     domain.PaxlDeviceLoginStatusApproved,
		APIKey:     consumed.APIKey,
		NodeID:     consumed.NodeID,
		UserAPIKey: keyMeta,
		User:       &user,
	}, nil
}

func (s *Service) updatePaxlLogin(
	c context.Context,
	req PollPaxlDeviceLoginRequest,
) (domain.PaxlDeviceLoginSession, error) {
	if req.Action != "commit" && req.Action != "ack" && req.Action != "cancel" {
		return domain.PaxlDeviceLoginSession{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "invalid login action",
		}
	}
	update := domain.PaxlDeviceLoginUpdate{
		LoginID:        req.LoginID,
		PollTokenHash:  s.secrets.Hash(req.PollToken),
		ExpectedUserID: req.ExpectedUserID,
		Action:         req.Action,
	}
	if req.Action == "commit" {
		key, err := s.secrets.New("paxu")
		if err != nil {
			return domain.PaxlDeviceLoginSession{}, err
		}
		update.APIKey, update.KeyHash, update.KeyPrefix = key, s.secrets.Hash(
			key,
		), s.secrets.Prefix(
			key,
		)
	}
	return s.store.UpdatePaxlDeviceLoginSession(c, update)
}

func (s *Service) clientCommitLoginResponse(
	c context.Context,
	session domain.PaxlDeviceLoginSession,
) (int, any, error) {
	response := domain.PollPaxlDeviceLoginResponse{Status: session.Status, Region: s.cfg.Region}
	if session.Status != domain.PaxlDeviceLoginStatusConfirmed &&
		session.Status != domain.PaxlDeviceLoginStatusApproved {
		return http.StatusOK, response, nil
	}
	user, err := s.store.GetUser(c, session.OwnerUserID)
	if err != nil {
		return 0, nil, err
	}
	response.User = &user
	if session.Status == domain.PaxlDeviceLoginStatusApproved {
		keys, err := s.store.ListUserAPIKeys(c, domain.UserPrincipal{User: user})
		if err != nil {
			return 0, nil, err
		}
		for i := range keys {
			if keys[i].KeyID == session.UserAPIKeyID && keys[i].RevokedAt == nil {
				response.UserAPIKey = &keys[i]
			}
		}
		if response.UserAPIKey == nil {
			return 0, nil, ErrUnauthorized
		}
		response.APIKey, response.NodeID = session.APIKey, session.NodeID
	}
	return http.StatusOK, response, nil
}
