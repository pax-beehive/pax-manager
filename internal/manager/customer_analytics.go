package manager

import (
	"context"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const routeCustomerAnalytics = "/api/v1/user/self/customer-analytics"
const routeCustomerVisit = "/api/v1/user/self/customer-visit"

func (s *Service) handleCustomerAnalytics(c context.Context, ctx *app.RequestContext) {
	ctx.Header("Cache-Control", "private, no-store")
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if !principal.IsAdmin {
		writeError(ctx, http.StatusForbidden, "administrator access required")
		return
	}
	if s.customerAnalytics == nil {
		writeError(ctx, http.StatusServiceUnavailable, "analytics unavailable")
		return
	}
	// Serialize refreshes, not individual users. Cache only metadata after auth.
	s.customerAnalyticsMu.Lock()
	defer s.customerAnalyticsMu.Unlock()
	now := s.clock().UTC()
	if s.customerAnalyticsAt.IsZero() || now.Sub(s.customerAnalyticsAt) >= 10*time.Second {
		queryCtx, cancel := context.WithTimeout(c, 5*time.Second)
		rows, queryErr := s.customerAnalytics.CustomerAnalytics(queryCtx)
		cancel()
		if queryErr != nil {
			writeError(ctx, http.StatusServiceUnavailable, "analytics unavailable")
			return
		}
		s.customerAnalyticsRows = rows
		s.customerAnalyticsAt = now
	}
	rows := make([]domain.CustomerAnalytics, 0, len(s.customerAnalyticsRows))
	for _, row := range s.customerAnalyticsRows {
		if row.Email == "local@example.local" {
			continue
		}
		row.IsAdmin = serviceAdminPolicy{s: s}.IsAdmin(row.Email)
		rows = append(rows, row)
	}
	region := s.cfg.Region
	if region == "" {
		region = "local"
	}
	writeData(ctx, http.StatusOK, map[string]any{"regions": []any{map[string]any{
		"region": region, "available": true, "updated_at": s.customerAnalyticsAt, "users": rows,
	}}})
}

func (s *Service) handleCustomerVisit(c context.Context, ctx *app.RequestContext) {
	ctx.Header("Cache-Control", "private, no-store")
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if s.customerAnalytics == nil {
		writeError(ctx, http.StatusServiceUnavailable, "activity unavailable")
		return
	}
	if err = s.customerAnalytics.RecordCustomerVisit(c, principal.User.UserID, s.clock().UTC()); err != nil {
		writeError(ctx, http.StatusServiceUnavailable, "activity unavailable")
		return
	}
	writeData(ctx, http.StatusOK, map[string]bool{"recorded": true})
}
