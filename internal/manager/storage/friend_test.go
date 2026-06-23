package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestMemoryFriendStore(t *testing.T) {
	t.Run(
		"Given a removed friend request when accepting then it returns not found",
		func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
			store := NewMemoryStore(func() time.Time { return now })
			requester, err := store.EnsureUser(ctx, "alice@example.com", "", "user")
			require.NoError(t, err)
			recipient, err := store.EnsureUser(ctx, "bob@example.com", "", "user")
			require.NoError(t, err)
			_, err = store.CreateFriend(ctx, Friend{
				FriendID:        "fr_1",
				RequesterUserID: requester.UserID,
				RequesterEmail:  requester.Email,
				RequesterAlias:  "bob",
				RecipientUserID: recipient.UserID,
				RecipientEmail:  recipient.Email,
				Status:          domain.FriendStatusPending,
				CreatedAt:       now,
			})
			require.NoError(t, err)
			_, err = store.RemoveFriend(
				ctx,
				UserPrincipal{User: requester},
				"fr_1",
				now.Add(time.Minute),
			)
			require.NoError(t, err)

			_, err = store.AcceptFriend(
				ctx,
				UserPrincipal{User: recipient},
				"fr_1",
				"alice",
				now.Add(2*time.Minute),
			)

			require.ErrorIs(t, err, ErrNotFound)
		},
	)
}
