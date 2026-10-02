package manager

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type failingRegionalStore struct{}

func (failingRegionalStore) EnsureRegionalUser(
	context.Context,
	string,
	string,
	string,
) (domain.User, error) {
	return domain.User{}, errors.New("offline")
}

func TestRegionalProvisioningGivenWorkerRequestThenOrdinaryRequestsCannotCreateUsers(t *testing.T) {
	secret := strings.Repeat("s", 32)
	store := NewMemoryStore(time.Now)
	srv := newServer(
		Config{Region: "hk", RegionProvisioningSecret: secret, AllowLocalUserHeader: true},
		store,
	)
	t.Cleanup(func() { require.NoError(t, srv.CloseTransportStore(t.Context())) })
	handler := srv.routes()
	call := func(body string, signed bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/internal/users/ensure",
			strings.NewReader(body),
		)
		req.Header.Set("Content-Type", "application/json")
		if signed {
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			mac := hmac.New(sha256.New, []byte(secret))
			_, err := mac.Write([]byte("pax-region-ensure-v1\n" + ts + "\n" + body))
			require.NoError(t, err)
			req.Header.Set("X-Pax-Timestamp", ts)
			req.Header.Set("X-Pax-Signature", hex.EncodeToString(mac.Sum(nil)))
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	body := `{"user_id":"usr_global","identity_key":"owner@example.com","region":"hk"}`
	assert.Equal(t, http.StatusUnauthorized, call(body, false).Code)
	_, err := store.GetUserByEmail(t.Context(), "owner@example.com")
	assert.ErrorIs(t, err, domain.ErrNotFound)
	for range 2 {
		rec := call(body, true)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var result struct {
			Data map[string]string `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
		assert.Equal(t, "usr_global", result.Data["user_id"])
	}
	assert.Equal(
		t,
		http.StatusConflict,
		call(strings.Replace(body, "usr_global", "usr_conflict", 1), true).Code,
	)
	assert.Equal(t, http.StatusUnauthorized, call(strings.Replace(body, "hk", "us", 1), true).Code)
	user, err := store.GetUserByEmail(t.Context(), "owner@example.com")
	require.NoError(t, err)
	assert.Equal(t, "user", user.Role)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/usr_unknown/me", nil)
	req.Header.Set("X-User-Email", "new@example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	_, err = store.GetUserByEmail(t.Context(), "new@example.com")
	assert.ErrorIs(t, err, domain.ErrNotFound)
	srv.regionalUsers = nil
	assert.Equal(t, http.StatusServiceUnavailable, call(body, true).Code)
	srv.regionalUsers = failingRegionalStore{}
	assert.Equal(t, http.StatusServiceUnavailable, call(body, true).Code)
	srv.cfg.Region = ""
	assert.Equal(t, http.StatusNotFound, call(body, true).Code)
}
