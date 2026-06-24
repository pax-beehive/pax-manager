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
	t.Run(
		"Given accepted friends then lookup by counterparty email enforces the boundary",
		func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
			store := NewMemoryStore(func() time.Time { return now })
			alice, err := store.EnsureUser(ctx, "alice@example.com", "", "user")
			require.NoError(t, err)
			bob, err := store.EnsureUser(ctx, "bob@example.com", "", "user")
			require.NoError(t, err)
			carol, err := store.EnsureUser(ctx, "carol@example.com", "", "user")
			require.NoError(t, err)

			accepted, err := store.CreateFriend(ctx, Friend{
				FriendID:        "fr_accepted",
				RequesterUserID: alice.UserID,
				RequesterEmail:  alice.Email,
				RequesterAlias:  "bob",
				RecipientUserID: bob.UserID,
				RecipientEmail:  bob.Email,
				RecipientAlias:  "alice",
				Status:          domain.FriendStatusAccepted,
				CreatedAt:       now,
			})
			require.NoError(t, err)
			_, err = store.CreateFriend(ctx, Friend{
				FriendID:        "fr_pending",
				RequesterUserID: alice.UserID,
				RequesterEmail:  alice.Email,
				RequesterAlias:  "carol",
				RecipientUserID: carol.UserID,
				RecipientEmail:  carol.Email,
				Status:          domain.FriendStatusPending,
				CreatedAt:       now,
			})
			require.NoError(t, err)

			got, err := store.GetAcceptedFriendByEmail(
				ctx,
				UserPrincipal{User: alice},
				" Bob@Example.com ",
			)
			require.NoError(t, err)
			require.Equal(t, accepted.FriendID, got.FriendID)

			got, err = store.GetAcceptedFriendByEmail(
				ctx,
				UserPrincipal{User: bob},
				"alice@example.com",
			)
			require.NoError(t, err)
			require.Equal(t, accepted.FriendID, got.FriendID)

			_, err = store.GetAcceptedFriendByEmail(
				ctx,
				UserPrincipal{User: alice},
				"carol@example.com",
			)
			require.ErrorIs(t, err, ErrNotFound)
		},
	)
	t.Run("Given accepted friends then alias updates only the caller side", func(t *testing.T) {
		ctx := context.Background()
		now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
		store := NewMemoryStore(func() time.Time { return now })
		alice, err := store.EnsureUser(ctx, "alice@example.com", "", "user")
		require.NoError(t, err)
		bob, err := store.EnsureUser(ctx, "bob@example.com", "", "user")
		require.NoError(t, err)
		_, err = store.CreateFriend(ctx, Friend{
			FriendID:        "fr_1",
			RequesterUserID: alice.UserID,
			RequesterEmail:  alice.Email,
			RequesterAlias:  "bob",
			RecipientUserID: bob.UserID,
			RecipientEmail:  bob.Email,
			RecipientAlias:  "alice",
			Status:          domain.FriendStatusAccepted,
			CreatedAt:       now,
		})
		require.NoError(t, err)

		got, err := store.UpdateFriendAlias(ctx, UserPrincipal{User: alice}, "fr_1", "teammate")
		require.NoError(t, err)
		require.Equal(t, "teammate", got.RequesterAlias)
		require.Equal(t, "alice", got.RecipientAlias)

		got, err = store.UpdateFriendAlias(ctx, UserPrincipal{User: bob}, "fr_1", "sender")
		require.NoError(t, err)
		require.Equal(t, "teammate", got.RequesterAlias)
		require.Equal(t, "sender", got.RecipientAlias)
	})
	t.Run("Given removed friends then alias update returns not found", func(t *testing.T) {
		ctx := context.Background()
		now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
		store := NewMemoryStore(func() time.Time { return now })
		alice, err := store.EnsureUser(ctx, "alice@example.com", "", "user")
		require.NoError(t, err)
		bob, err := store.EnsureUser(ctx, "bob@example.com", "", "user")
		require.NoError(t, err)
		_, err = store.CreateFriend(ctx, Friend{
			FriendID:        "fr_1",
			RequesterUserID: alice.UserID,
			RequesterEmail:  alice.Email,
			RequesterAlias:  "bob",
			RecipientUserID: bob.UserID,
			RecipientEmail:  bob.Email,
			RecipientAlias:  "alice",
			Status:          domain.FriendStatusRemoved,
			CreatedAt:       now,
		})
		require.NoError(t, err)

		_, err = store.UpdateFriendAlias(ctx, UserPrincipal{User: alice}, "fr_1", "teammate")

		require.ErrorIs(t, err, ErrNotFound)
	})
}
