package manager

import (
	"context"
	"errors"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type regionalUserStore interface {
	EnsureRegionalUser(context.Context, string, string, string) (domain.User, error)
}

func EnsureRegionalUser(c context.Context, ctx *app.RequestContext) {
	s := serviceFromContext(ctx)
	ctx.Header("Cache-Control", "no-store")
	if s.cfg.Region == "" {
		writeError(ctx, http.StatusNotFound, "not found")
		return
	}
	assignment, err := auth.VerifyProvisioning(
		ctx.Request.Body(),
		string(ctx.GetHeader("X-Pax-Timestamp")),
		string(ctx.GetHeader("X-Pax-Signature")),
		s.cfg.Region,
		s.cfg.RegionProvisioningSecret,
		s.clock(),
	)
	if err != nil {
		writeError(ctx, http.StatusUnauthorized, "invalid provisioning assertion")
		return
	}
	if s.regionalUsers == nil {
		writeError(ctx, http.StatusServiceUnavailable, "provisioning unavailable")
		return
	}
	role := serviceAdminPolicy{s: s}.RoleForEmail(assignment.IdentityKey)
	_, err = s.regionalUsers.EnsureRegionalUser(c, assignment.UserID, assignment.IdentityKey, role)
	if errors.Is(err, domain.ErrConflict) {
		writeError(ctx, http.StatusConflict, "local user conflicts with directory")
		return
	}
	if err != nil {
		writeError(ctx, http.StatusServiceUnavailable, "provisioning unavailable")
		return
	}
	writeData(ctx, http.StatusOK, assignment)
}
