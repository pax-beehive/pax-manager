package userapi_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
	userapimocks "github.com/pax-beehive/pax-manager/internal/manager/userapi/mocks"
)

func TestCreateRegistrationToken(t *testing.T) {
	t.Run(
		"Given a non-admin principal when minting for another user then it returns forbidden before loading that user",
		func(t *testing.T) {
			ctx := context.Background()
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().
				Principal(ctx, auth.RequestMetadata{}).
				Return(userPrincipal("usr_self", false), nil).
				Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.CreateRegistrationToken(
				ctx,
				auth.RequestMetadata{},
				domain.CreateRegistrationTokenRequest{OwnerEmail: "other@example.com"},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusForbidden, appErr.Status)
		},
	)

	t.Run(
		"Given an admin principal when minting for an owner email then it stores the hashed token with expiration",
		func(t *testing.T) {
			ctx := context.Background()
			meta := auth.NewRequestMetadata(map[string]string{"Cf-Access-Jwt-Assertion": "jwt"})
			principal := userPrincipal("usr_admin", true)
			owner := domain.User{UserID: "usr_owner", Email: "owner@example.com"}
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			secrets := userapimocks.NewMockSecretIssuer(t)

			principals.EXPECT().Principal(ctx, meta).Return(principal, nil).Once()
			store.EXPECT().GetUserByEmail(ctx, "owner@example.com").Return(owner, nil).Once()
			secrets.EXPECT().New("reg").Return("reg_raw", nil).Once()
			secrets.EXPECT().Hash("reg_raw").Return("hashed_reg").Once()
			store.EXPECT().
				CreateRegistrationToken(ctx, "usr_owner", "hashed_reg", mock.MatchedBy(func(expiresAt *time.Time) bool {
					return expiresAt != nil && expiresAt.Equal(fixedUserNow().Add(2*time.Hour))
				})).
				Return(nil).
				Once()

			svc := userapi.NewService(store, fixedUserClock, principals, secrets)
			status, data, err := svc.CreateRegistrationToken(
				ctx,
				meta,
				domain.CreateRegistrationTokenRequest{
					OwnerEmail:       "owner@example.com",
					ExpiresInSeconds: 7200,
				},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusCreated, status)
			resp := data.(domain.CreateRegistrationTokenResponse)
			require.Equal(t, "reg_raw", resp.Token)
			require.Equal(t, "usr_owner", resp.OwnerUserID)
			require.NotNil(t, resp.ExpiresAt)
			require.True(t, resp.ExpiresAt.Equal(fixedUserNow().Add(2*time.Hour)))
		},
	)
}

func TestCreateUserAPIKey(t *testing.T) {
	t.Run(
		"Given a valid principal when creating an API key then it stores only hash and prefix and returns the raw key once",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			secrets := userapimocks.NewMockSecretIssuer(t)
			keyMeta := domain.UserAPIKey{
				KeyID:       "key_1",
				OwnerUserID: "usr_self",
				Name:        "laptop",
				Prefix:      "paxu_123",
			}

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			secrets.EXPECT().New("paxu").Return("paxu_1234567890", nil).Once()
			secrets.EXPECT().Hash("paxu_1234567890").Return("hashed_key").Once()
			secrets.EXPECT().Prefix("paxu_1234567890").Return("paxu_123").Once()
			store.EXPECT().
				CreateUserAPIKey(ctx, principal, "laptop", "hashed_key", "paxu_123").
				Return(keyMeta, nil).
				Once()

			svc := userapi.NewService(store, fixedUserClock, principals, secrets)
			status, data, err := svc.CreateUserAPIKey(
				ctx,
				auth.RequestMetadata{},
				domain.CreateUserAPIKeyRequest{Name: "laptop"},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusCreated, status)
			require.Equal(
				t,
				domain.CreateUserAPIKeyResponse{APIKey: keyMeta, Key: "paxu_1234567890"},
				data,
			)
		},
	)
}

func TestMailbox(t *testing.T) {
	t.Run(
		"Given missing agent ID when creating mailbox message then it returns bad request",
		func(t *testing.T) {
			ctx := context.Background()
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().
				Principal(ctx, auth.RequestMetadata{}).
				Return(userPrincipal("usr_self", false), nil).
				Once()

			svc := userapi.NewService(
				userapimocks.NewMockStore(t),
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.CreateMailboxMessage(
				ctx,
				auth.RequestMetadata{},
				domain.CreateMailboxRequest{Message: "hello"},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given no mailbox list limit when listing mailbox then it uses the default limit",
		func(t *testing.T) {
			ctx := context.Background()
			principal := userPrincipal("usr_self", false)
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)

			principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Once()
			store.EXPECT().
				ListMailbox(ctx, mock.MatchedBy(func(filter domain.MailboxFilter) bool {
					return filter.Principal == principal && filter.AgentID == "agent_1" &&
						filter.Limit == 50
				})).
				Return([]domain.MailboxMessage{{MessageID: "msg_1"}}, nil).
				Once()

			svc := userapi.NewService(
				store,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.ListMailbox(ctx, auth.RequestMetadata{}, "agent_1", "", "", 0)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(
				t,
				map[string]any{"messages": []domain.MailboxMessage{{MessageID: "msg_1"}}},
				data,
			)
		},
	)
}

func TestRevokeUserAPIKey(t *testing.T) {
	t.Run("Given an empty key ID when revoking then it returns not found", func(t *testing.T) {
		ctx := context.Background()
		principals := userapimocks.NewMockPrincipalResolver(t)
		principals.EXPECT().
			Principal(ctx, auth.RequestMetadata{}).
			Return(userPrincipal("usr_self", false), nil).
			Once()

		svc := userapi.NewService(
			userapimocks.NewMockStore(t),
			fixedUserClock,
			principals,
			userapimocks.NewMockSecretIssuer(t),
		)
		_, _, err := svc.RevokeUserAPIKey(ctx, auth.RequestMetadata{}, "")

		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusNotFound, appErr.Status)
	})
}

func userPrincipal(userID string, admin bool) domain.UserPrincipal {
	return domain.UserPrincipal{
		User:    domain.User{UserID: userID, Email: userID + "@example.com"},
		IsAdmin: admin,
	}
}

func fixedUserClock() time.Time {
	return fixedUserNow()
}

func fixedUserNow() time.Time {
	return time.Date(2026, 6, 12, 15, 0, 0, 0, time.UTC)
}
