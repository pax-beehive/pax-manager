package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func newFriendLifecycleStore(t *testing.T) (*MemoryStore, User, User, time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	alice, err := store.EnsureUser(ctx, "alice@example.com", "", "user")
	require.NoError(t, err)
	bob, err := store.EnsureUser(ctx, "bob@example.com", "", "user")
	require.NoError(t, err)
	return store, alice, bob, now
}

func createLifecycleFriend(
	t *testing.T,
	store *MemoryStore,
	friendID string,
	requester User,
	recipient User,
	status string,
	createdAt time.Time,
) {
	t.Helper()
	_, err := store.CreateFriend(context.Background(), Friend{
		FriendID:        friendID,
		RequesterUserID: requester.UserID,
		RequesterEmail:  requester.Email,
		RequesterAlias:  "buddy",
		RecipientUserID: recipient.UserID,
		RecipientEmail:  recipient.Email,
		RecipientAlias:  "pal",
		Status:          status,
		CreatedAt:       createdAt,
	})
	require.NoError(t, err)
}

func TestListFriendsDefaultExcludesRemoved(t *testing.T) {
	ctx := context.Background()
	store, alice, bob, now := newFriendLifecycleStore(t)
	createLifecycleFriend(t, store, "fr_removed", alice, bob, domain.FriendStatusRemoved, now)
	createLifecycleFriend(
		t,
		store,
		"fr_accepted",
		alice,
		bob,
		domain.FriendStatusAccepted,
		now.Add(time.Minute),
	)

	t.Run("Given no status filter then removed friends are hidden", func(t *testing.T) {
		friends, err := store.ListFriends(ctx, ListFriendsFilter{
			Principal: UserPrincipal{User: alice},
		})
		require.NoError(t, err)
		require.Len(t, friends, 1)
		require.Equal(t, "fr_accepted", friends[0].FriendID)
	})

	t.Run("Given status=removed then removed friends are returned", func(t *testing.T) {
		friends, err := store.ListFriends(ctx, ListFriendsFilter{
			Principal: UserPrincipal{User: alice},
			Status:    domain.FriendStatusRemoved,
		})
		require.NoError(t, err)
		require.Len(t, friends, 1)
		require.Equal(t, "fr_removed", friends[0].FriendID)
	})
}

func TestRemoveFriendRequiresActiveStatus(t *testing.T) {
	ctx := context.Background()
	store, alice, bob, now := newFriendLifecycleStore(t)
	createLifecycleFriend(t, store, "fr_removed", alice, bob, domain.FriendStatusRemoved, now)
	createLifecycleFriend(t, store, "fr_blocked", alice, bob, domain.FriendStatusBlocked, now)
	createLifecycleFriend(t, store, "fr_pending", alice, bob, domain.FriendStatusPending, now)

	t.Run("Given a removed friend then removing again returns not found", func(t *testing.T) {
		_, err := store.RemoveFriend(
			ctx,
			UserPrincipal{User: alice},
			"fr_removed",
			now.Add(time.Minute),
		)
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("Given a blocked friend then removing returns not found", func(t *testing.T) {
		_, err := store.RemoveFriend(
			ctx,
			UserPrincipal{User: bob},
			"fr_blocked",
			now.Add(time.Minute),
		)
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("Given a pending friend then removing succeeds", func(t *testing.T) {
		removed, err := store.RemoveFriend(
			ctx,
			UserPrincipal{User: alice},
			"fr_pending",
			now.Add(time.Minute),
		)
		require.NoError(t, err)
		require.Equal(t, domain.FriendStatusRemoved, removed.Status)
	})
}

func TestFriendsBetweenLookupAndCleanup(t *testing.T) {
	ctx := context.Background()
	store, alice, bob, now := newFriendLifecycleStore(t)
	carol, err := store.EnsureUser(ctx, "carol@example.com", "", "user")
	require.NoError(t, err)
	createLifecycleFriend(t, store, "fr_ab_removed", alice, bob, domain.FriendStatusRemoved, now)
	createLifecycleFriend(
		t,
		store,
		"fr_ba_removed",
		bob,
		alice,
		domain.FriendStatusRemoved,
		now.Add(time.Minute),
	)
	createLifecycleFriend(
		t,
		store,
		"fr_ac_pending",
		alice,
		carol,
		domain.FriendStatusPending,
		now.Add(2*time.Minute),
	)
	createLifecycleFriend(
		t,
		store,
		"fr_cb_pending",
		carol,
		bob,
		domain.FriendStatusPending,
		now.Add(3*time.Minute),
	)

	t.Run(
		"Given rows in both directions then lookup returns every row between the pair",
		func(t *testing.T) {
			friends, err := store.ListFriendsBetween(
				ctx,
				UserPrincipal{User: alice},
				" Bob@Example.com ",
			)
			require.NoError(t, err)
			ids := make([]string, 0, len(friends))
			for _, friend := range friends {
				ids = append(ids, friend.FriendID)
			}
			require.ElementsMatch(t, []string{"fr_ab_removed", "fr_ba_removed"}, ids)
		},
	)

	t.Run(
		"Given removed rows then cleanup deletes only removed rows between the pair",
		func(t *testing.T) {
			err := store.DeleteRemovedFriendsBetween(
				ctx,
				UserPrincipal{User: alice},
				"bob@example.com",
			)
			require.NoError(t, err)

			friends, err := store.ListFriendsBetween(
				ctx,
				UserPrincipal{User: alice},
				"bob@example.com",
			)
			require.NoError(t, err)
			require.Empty(t, friends)

			_, err = store.GetFriend(ctx, UserPrincipal{User: alice}, "fr_ac_pending")
			require.NoError(t, err)
			_, err = store.GetFriend(ctx, UserPrincipal{User: carol}, "fr_cb_pending")
			require.NoError(t, err)
		},
	)
}
