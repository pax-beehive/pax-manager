package manager

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type analyticsFixture struct {
	rows    []domain.CustomerAnalytics
	calls   int
	visited string
	err     error
}

func (f *analyticsFixture) CustomerAnalytics(context.Context) ([]domain.CustomerAnalytics, error) {
	f.calls++
	return f.rows, f.err
}
func (f *analyticsFixture) RecordCustomerVisit(_ context.Context, id string, _ time.Time) error {
	f.visited = id
	return f.err
}

func TestCustomerAnalyticsGivenAdminThenAuthorizesBeforeSharedCache(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(time.Now)
	svc := newServer(
		Config{
			AllowLocalUserHeader: true,
			AdminEmails:          map[string]bool{"admin@example.invalid": true},
		},
		store,
	)
	t.Cleanup(func() { require.NoError(t, svc.CloseTransportStore(t.Context())) })
	svc.clock = func() time.Time { return now }
	fixture := &analyticsFixture{rows: []domain.CustomerAnalytics{
		{
			Email: "admin@example.invalid",
		}, {Email: "normal@example.invalid"}, {Email: "local@example.local"},
	}}
	svc.customerAnalytics = fixture
	handler := svc.routes()
	call := func(method, path, email string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if email != "" {
			r.Header.Set("X-User-Email", email)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	assert.Equal(t, 401, call(http.MethodGet, routeCustomerAnalytics, "").Code)
	assert.Equal(
		t,
		403,
		call(http.MethodGet, routeCustomerAnalytics, "normal@example.invalid").Code,
	)
	assert.Zero(t, fixture.calls)
	for range 2 {
		w := call(http.MethodGet, routeCustomerAnalytics, "admin@example.invalid")
		assert.Equal(t, 200, w.Code, w.Body.String())
		assert.Contains(t, w.Header().Get("Cache-Control"), "no-store")
		assert.NotContains(t, w.Body.String(), "local@example.local")
		assert.Contains(t, w.Body.String(), `"is_admin":true`)
		assert.Contains(t, w.Body.String(), `"region":"local"`)
	}
	assert.Equal(t, 1, fixture.calls)
	assert.Equal(
		t,
		403,
		call(http.MethodGet, routeCustomerAnalytics, "normal@example.invalid").Code,
	)
	now = now.Add(11 * time.Second)
	fixture.err = errors.New("secret database error")
	w := call(http.MethodGet, routeCustomerAnalytics, "admin@example.invalid")
	assert.Equal(t, 503, w.Code)
	assert.NotContains(t, w.Body.String(), "secret database")
	fixture.err = nil
	svc.cfg.Region = "us"
	assert.Equal(t, 200, call(http.MethodGet, routeCustomerAnalytics, "admin@example.invalid").Code)
	assert.Equal(t, 401, call(http.MethodPost, routeCustomerVisit, "").Code)
	assert.Equal(t, 200, call(http.MethodPost, routeCustomerVisit, "normal@example.invalid").Code)
	user, err := store.GetUserByEmail(t.Context(), "normal@example.invalid")
	require.NoError(t, err)
	assert.Equal(t, user.UserID, fixture.visited)
	fixture.err = errors.New("offline")
	assert.Equal(t, 503, call(http.MethodPost, routeCustomerVisit, "normal@example.invalid").Code)
	svc.customerAnalytics = nil
	assert.Equal(t, 503, call(http.MethodPost, routeCustomerVisit, "normal@example.invalid").Code)
	assert.Equal(t, 503, call(http.MethodGet, routeCustomerAnalytics, "admin@example.invalid").Code)
}
