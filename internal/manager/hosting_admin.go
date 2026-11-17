package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

const hostingAdminMaxBody = 8192

func hostingAdminPrincipal(c context.Context, ctx *app.RequestContext) (*Service, string, bool) {
	ctx.Header("Cache-Control", "private, no-store")
	s := serviceFromContext(ctx)
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return s, "", false
	}
	if !principal.IsAdmin {
		writeError(ctx, http.StatusForbidden, "administrator_access_required")
		return s, "", false
	}
	return s, principal.User.UserID, true
}

func GetHostingAdmin(c context.Context, ctx *app.RequestContext) {
	s, _, ok := hostingAdminPrincipal(c, ctx)
	if !ok {
		return
	}
	query, err := s.hostingAdminQuery(c, ctx)
	if err != nil {
		hostingAdminError(ctx, err)
		return
	}
	snapshot, err := s.hosting.AdminSnapshot(c, query)
	s.writeHostingAdmin(c, ctx, snapshot, err)
}

func UpdateHostingAdmin(c context.Context, ctx *app.RequestContext) {
	s, actor, ok := hostingAdminPrincipal(c, ctx)
	if !ok {
		return
	}
	if len(ctx.Request.Body()) > hostingAdminMaxBody {
		writeError(ctx, http.StatusRequestEntityTooLarge, "hosting_admin_request_too_large")
		return
	}
	update, err := decodeHostingAdminUpdate(ctx.Request.Body())
	if err != nil {
		hostingAdminError(ctx, err)
		return
	}
	snapshot, err := s.hosting.AdminUpdate(c, actor, update)
	s.writeHostingAdmin(c, ctx, snapshot, err)
}

func decodeHostingAdminUpdate(body []byte) (*hosting.AdminUpdate, error) {
	var update hosting.AdminUpdate
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&update); err != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, hosting.ErrAdminUpdate
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields["revision"] == nil ||
		fields["action"] == nil {
		return nil, hosting.ErrAdminUpdate
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, hosting.ErrAdminUpdate
		}
	}
	return &update, nil
}

func (s *Service) hostingAdminQuery(
	c context.Context,
	ctx *app.RequestContext,
) (hosting.AdminQuery, error) {
	query := hosting.AdminQuery{
		Limit: 25, UserQuery: strings.TrimSpace(ctx.Query("user_query")),
		UsersAfter: ctx.Query("users_after"), RequestsBefore: ctx.Query("requests_before"),
		RequestStatus: ctx.Query("request_status"),
	}
	if limit := ctx.Query("limit"); limit != "" {
		value, err := strconv.Atoi(limit)
		if err != nil || value < 1 || value > 100 {
			return query, hosting.ErrAdminUpdate
		}
		query.Limit = value
	}
	if len(query.UserQuery) > 320 || len(query.UsersAfter) > 256 ||
		len(query.RequestsBefore) > 256 {
		return query, hosting.ErrAdminUpdate
	}
	if !validHostingRequestStatus(query.RequestStatus) {
		return query, hosting.ErrAdminUpdate
	}
	if strings.Contains(query.UserQuery, "@") {
		user, err := s.store.GetUserByEmail(c, query.UserQuery)
		if errors.Is(err, domain.ErrNotFound) {
			query.UserQuery = "missing-email-user"
		} else if err != nil {
			return query, err
		} else {
			query.UserQuery = user.UserID
		}
	}
	return query, nil
}

func validHostingRequestStatus(status string) bool {
	switch status {
	case "", "reserved", "settled", "conservative", "moderation_blocked",
		"moderation_unavailable", "moderation_interrupted", "generation_canceled":
		return true
	default:
		return false
	}
}

func (s *Service) writeHostingAdmin(
	c context.Context, ctx *app.RequestContext, snapshot *hosting.AdminSnapshot, err error,
) {
	if err != nil {
		hostingAdminError(ctx, err)
		return
	}
	for i := range snapshot.Users {
		user, lookupErr := s.store.GetUser(c, snapshot.Users[i].UserID)
		if lookupErr == nil {
			snapshot.Users[i].Email = user.Email
		}
	}
	writeData(ctx, http.StatusOK, snapshot)
}

func hostingAdminError(ctx *app.RequestContext, err error) {
	switch {
	case errors.Is(err, hosting.ErrAdminRevision):
		writeError(ctx, http.StatusConflict, "hosting_admin_revision_conflict")
	case errors.Is(err, hosting.ErrAdminModelConflict):
		writeError(ctx, http.StatusConflict, "hosting_model_conflict")
	case errors.Is(err, hosting.ErrAdminUpdate):
		writeError(ctx, http.StatusBadRequest, "invalid_hosting_admin_update")
	default:
		hostingError(ctx, err)
	}
}
