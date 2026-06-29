package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestMemoryKnowledgeCapsuleAndInjectionLifecycle(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, err := store.EnsureUser(ctx, "owner@example.com", "Owner", "user")
	require.NoError(t, err)
	other, err := store.EnsureUser(ctx, "other@example.com", "Other", "user")
	require.NoError(t, err)
	principal := UserPrincipal{User: owner}

	capsule, err := store.CreateKnowledgeCapsule(ctx, KnowledgeCapsule{
		CapsuleID:       "cap_1",
		OwnerUserID:     owner.UserID,
		SourceSessionID: "sess_1",
		SourceAgentID:   "agent_1",
		CreatedByUserID: owner.UserID,
		Keyword:         "paxl",
		Title:           "Paxl handoff",
		Content:         "Use routed injection.",
		Status:          domain.KnowledgeCapsuleStatusActive,
	})
	require.NoError(t, err)
	require.Equal(t, now, capsule.CreatedAt)
	_, err = store.CreateKnowledgeCapsule(ctx, KnowledgeCapsule{
		CapsuleID:   "cap_missing",
		OwnerUserID: "missing",
	})
	require.ErrorIs(t, err, ErrNotFound)

	listed, err := store.ListKnowledgeCapsules(ctx, ListKnowledgeCapsulesFilter{
		Principal: principal,
		Status:    domain.KnowledgeCapsuleStatusActive,
		Keyword:   "paxl",
	})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	_, err = store.GetKnowledgeCapsule(ctx, UserPrincipal{User: other}, "cap_1")
	require.ErrorIs(t, err, ErrNotFound)

	archived, err := store.ArchiveKnowledgeCapsule(ctx, principal, "cap_1", now.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, domain.KnowledgeCapsuleStatusArchived, archived.Status)
	require.NotNil(t, archived.ArchivedAt)

	injection, err := store.CreateKnowledgeInjection(ctx, SessionKnowledgeInjection{
		InjectionID:         "inj_1",
		OwnerUserID:         owner.UserID,
		CapsuleID:           "cap_1",
		TargetSessionID:     "sess_2",
		TargetAgentID:       "agent_1",
		CreatedByUserID:     owner.UserID,
		DeliveryMethod:      domain.KnowledgeInjectionDeliveryMailboxSteer,
		DeliveryMessageType: domain.MessageTypeSystemHandoff,
		Status:              domain.KnowledgeInjectionStatusPending,
	})
	require.NoError(t, err)
	require.Equal(t, now, injection.CreatedAt)
	injections, err := store.ListKnowledgeInjections(ctx, ListKnowledgeInjectionsFilter{
		Principal:       principal,
		TargetSessionID: "sess_2",
	})
	require.NoError(t, err)
	require.Len(t, injections, 1)
}

func TestMemoryFriendLifecycleAndFilters(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	requester, err := store.EnsureUser(ctx, "requester@example.com", "Requester", "user")
	require.NoError(t, err)
	recipient, err := store.EnsureUser(ctx, "recipient@example.com", "Recipient", "user")
	require.NoError(t, err)
	requesterPrincipal := UserPrincipal{User: requester}
	recipientPrincipal := UserPrincipal{User: recipient}

	friend, err := store.CreateFriend(ctx, Friend{
		FriendID:        "fr_1",
		RequesterUserID: requester.UserID,
		RequesterEmail:  requester.Email,
		RequesterAlias:  "requester",
		RecipientEmail:  recipient.Email,
		Status:          domain.FriendStatusPending,
	})
	require.NoError(t, err)
	require.Equal(t, now, friend.CreatedAt)
	received, err := store.ListFriends(ctx, ListFriendsFilter{
		Principal: recipientPrincipal,
		Direction: domain.FriendDirectionReceived,
		Limit:     10,
	})
	require.NoError(t, err)
	require.Len(t, received, 1)

	accepted, err := store.AcceptFriend(
		ctx,
		recipientPrincipal,
		"fr_1",
		"recipient",
		now.Add(time.Minute),
	)
	require.NoError(t, err)
	require.Equal(t, domain.FriendStatusAccepted, accepted.Status)
	require.Equal(t, recipient.UserID, accepted.RecipientUserID)
	got, err := store.GetAcceptedFriendByEmail(ctx, requesterPrincipal, recipient.Email)
	require.NoError(t, err)
	require.Equal(t, "fr_1", got.FriendID)

	updated, err := store.UpdateFriendAlias(ctx, requesterPrincipal, "fr_1", "teammate")
	require.NoError(t, err)
	require.Equal(t, "teammate", updated.RequesterAlias)
	aliasMatches, err := store.ListFriends(ctx, ListFriendsFilter{
		Principal: requesterPrincipal,
		Alias:     "TEAMMATE",
	})
	require.NoError(t, err)
	require.Len(t, aliasMatches, 1)

	removed, err := store.RemoveFriend(ctx, requesterPrincipal, "fr_1", now.Add(2*time.Minute))
	require.NoError(t, err)
	require.Equal(t, domain.FriendStatusRemoved, removed.Status)
	blocked, err := store.BlockFriend(ctx, requesterPrincipal, "fr_1", now.Add(3*time.Minute))
	require.NoError(t, err)
	require.Equal(t, domain.FriendStatusBlocked, blocked.Status)
}

func TestMemoryEnvelopeLifecycleAndFilters(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	sender, err := store.EnsureUser(ctx, "sender@example.com", "Sender", "user")
	require.NoError(t, err)
	recipient, err := store.EnsureUser(ctx, "recipient@example.com", "Recipient", "user")
	require.NoError(t, err)
	senderPrincipal := UserPrincipal{User: sender}
	recipientPrincipal := UserPrincipal{User: recipient}

	envelope, err := store.CreateEnvelope(ctx, Envelope{
		EnvelopeID:     "env_1",
		SenderUserID:   sender.UserID,
		SenderEmail:    sender.Email,
		RecipientEmail: recipient.Email,
		PayloadType:    domain.EnvelopePayloadKnowledgeCapsule,
		PayloadJSON:    json.RawMessage(`{"capsule_id":"cap_1"}`),
		Message:        "handoff",
		Status:         domain.EnvelopeStatusPending,
	})
	require.NoError(t, err)
	require.Equal(t, now, envelope.CreatedAt)
	sent, err := store.ListEnvelopes(ctx, ListEnvelopesFilter{
		Principal: senderPrincipal,
		Direction: domain.EnvelopeDirectionSent,
	})
	require.NoError(t, err)
	require.Len(t, sent, 1)
	received, err := store.ListEnvelopes(ctx, ListEnvelopesFilter{
		Principal: recipientPrincipal,
		Direction: domain.EnvelopeDirectionReceived,
		Status:    domain.EnvelopeStatusPending,
	})
	require.NoError(t, err)
	require.Len(t, received, 1)

	got, err := store.GetEnvelope(ctx, recipientPrincipal, "env_1")
	require.NoError(t, err)
	require.Equal(t, "env_1", got.EnvelopeID)
	accepted, err := store.AcceptEnvelope(ctx, recipientPrincipal, "env_1", now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, recipient.UserID, accepted.RecipientUserID)
	require.Equal(t, domain.EnvelopeStatusAccepted, accepted.Status)
	archived, err := store.ArchiveEnvelope(ctx, recipientPrincipal, "env_1", now.Add(2*time.Minute))
	require.NoError(t, err)
	require.Equal(t, domain.EnvelopeStatusArchived, archived.Status)
}
