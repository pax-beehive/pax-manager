package userapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

const (
	NotificationSubscriptionsPath = "/api/v1/user/self/notification-subscriptions"
	NotificationReceiversPath     = "/api/v1/user/self/notifications/receivers"
	NotificationConfigPath        = "/api/v1/user/self/notifications/config"
)

type NotificationHandler struct {
	Registry       notifications.Registry
	Outbox         notifications.Outbox
	SendingEnabled bool
	Principal      func(*http.Request) (domain.UserPrincipal, error)
	Region         string
	PublicKey      string
	KeyID          string
	Now            func() time.Time
}

func (h *NotificationHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	principal, err := h.Principal(r)
	if err != nil {
		notificationResponse(w, 401, "authentication required", nil)
		return
	}
	if r.URL.Path == NotificationSubscriptionsPath && !principal.IsAdmin {
		notificationResponse(w, 403, "administrator access required", nil)
		return
	}
	if h.Registry == nil {
		notificationResponse(w, 503, "notification registry unavailable", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	switch {
	case r.URL.Path == NotificationConfigPath && r.Method == http.MethodGet:
		h.configuration(w)
	case (r.URL.Path == NotificationSubscriptionsPath || r.URL.Path == NotificationReceiversPath) && r.Method == http.MethodGet:
		h.list(ctx, w, r, principal)
	case r.URL.Path == NotificationReceiversPath+"/web-push" && r.Method == http.MethodPut:
		h.register(ctx, w, r, principal)
	case notificationTestRequest(r):
		h.sendTest(ctx, w, r, principal)
	case strings.HasPrefix(r.URL.Path, NotificationReceiversPath+"/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(r.URL.Path, NotificationReceiversPath+"/")
		if id == "" || strings.Contains(id, "/") {
			notificationResponse(w, 404, "receiver not found", nil)
			return
		}
		if err := h.Registry.RevokeNotificationReceiver(ctx, principal.User.UserID, id); err != nil {
			notificationRegistryError(w, err)
			return
		}
		notificationResponse(w, 200, "", map[string]bool{"revoked": true})
	default:
		if r.URL.Path == NotificationSubscriptionsPath || r.URL.Path == NotificationReceiversPath ||
			r.URL.Path == NotificationConfigPath {
			notificationResponse(w, 405, "method not allowed", nil)
		} else {
			notificationResponse(w, 404, "not found", nil)
		}
	}
}

func notificationTestRequest(r *http.Request) bool {
	return r.Method == http.MethodPost &&
		strings.HasPrefix(r.URL.Path, NotificationReceiversPath+"/") &&
		strings.HasSuffix(r.URL.Path, "/test")
}

func (h *NotificationHandler) configuration(w http.ResponseWriter) {
	notificationResponse(w, 200, "", map[string]any{
		"enabled": h.PublicKey != "", "test_enabled": h.SendingEnabled && h.Outbox != nil,
		"turn_completion_enabled": false, "public_key": h.PublicKey,
		"key_id": h.KeyID, "channel": "web_push",
	})
}

func (h *NotificationHandler) sendTest(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	principal domain.UserPrincipal,
) {
	if h.Outbox == nil || !h.SendingEnabled {
		notificationResponse(w, 503, "notification sending is not configured", nil)
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, NotificationReceiversPath+"/"), "/test")
	if id == "" || strings.Contains(id, "/") {
		notificationResponse(w, 404, "receiver not found", nil)
		return
	}
	metadata, err := h.Outbox.EnqueueNotificationTest(ctx, principal.User.UserID, id)
	if err != nil {
		notificationRegistryError(w, err)
		return
	}
	notificationResponse(w, 202, "", map[string]any{"delivery": metadata})
}

func (h *NotificationHandler) list(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	principal domain.UserPrincipal,
) {
	q := r.URL.Query()
	region := h.Region
	if region == "" {
		region = "local"
	}
	filter := notifications.SubscriptionFilter{
		Search:  q.Get("search"),
		UserID:  q.Get("user_id"),
		Status:  notifications.SubscriptionStatus(q.Get("status")),
		Channel: notifications.Channel(q.Get("channel")),
		After:   q.Get("after"),
	}
	if cursor := q.Get("after_" + region); cursor != "" {
		filter.After = cursor
	}
	if value := q.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil {
			notificationResponse(w, 400, "invalid subscription filter", nil)
			return
		}
		filter.Limit = limit
	}
	if r.URL.Path == NotificationReceiversPath {
		filter.UserID = principal.User.UserID
	}
	if err := filter.Validate(); err != nil {
		notificationRegistryError(w, err)
		return
	}
	page, err := h.Registry.ListNotificationSubscriptions(ctx, filter)
	if err != nil {
		notificationRegistryError(w, err)
		return
	}
	notificationResponse(w, 200, "", map[string]any{"regions": []any{map[string]any{
		"registration_enabled": h.PublicKey != "",
		"region":               region, "available": true, "updated_at": h.Now().UTC(), "receivers": page.Receivers, "summary": page.Summary, "next_cursor": page.NextCursor,
	}}})
}

func (h *NotificationHandler) register(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	principal domain.UserPrincipal,
) {
	if h.PublicKey == "" {
		notificationResponse(w, 503, "push registration is not configured", nil)
		return
	}
	var input notifications.Registration
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		notificationResponse(w, 400, "invalid notification registration", nil)
		return
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		notificationResponse(w, 400, "invalid notification registration", nil)
		return
	}
	if input.VAPIDKeyID != h.KeyID {
		notificationResponse(w, 409, "push configuration changed; refresh registration", nil)
		return
	}
	if input.Permission != "granted" {
		input.Enabled = false
	}
	view, err := h.Registry.RegisterNotificationReceiver(ctx, principal.User.UserID, input)
	if err != nil {
		notificationRegistryError(w, err)
		return
	}
	notificationResponse(w, 200, "", map[string]any{"receiver": view})
}

func notificationRegistryError(w http.ResponseWriter, err error) {
	status, message := 503, "notification registry unavailable"
	switch {
	case errors.Is(err, notifications.ErrInvalidRegistration),
		errors.Is(err, notifications.ErrInvalidFilter):
		status, message = 400, err.Error()
	case errors.Is(err, notifications.ErrReceiverConflict),
		errors.Is(err, notifications.ErrReceiverLimit):
		status, message = 409, err.Error()
	case errors.Is(err, notifications.ErrReceiverNotFound):
		status, message = 404, err.Error()
	case errors.Is(err, notifications.ErrReceiverDisabled):
		status, message = 409, err.Error()
	case errors.Is(err, notifications.ErrTestRateLimited):
		w.Header().Set("Retry-After", "30")
		status, message = 429, err.Error()
	}
	notificationResponse(w, status, message, nil)
}

func notificationResponse(w http.ResponseWriter, status int, message string, data any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": status, "message": message, "data": data})
}
