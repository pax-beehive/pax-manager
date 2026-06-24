package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestMemoryEnvelopeStore(t *testing.T) {
	t.Run("Given a sent envelope then sender can list it from outbox", func(t *testing.T) {
		ctx := context.Background()
		now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
		store := NewMemoryStore(func() time.Time { return now })
		sender, err := store.EnsureUser(ctx, "sender@example.com", "", "user")
		require.NoError(t, err)
		recipient, err := store.EnsureUser(ctx, "recipient@example.com", "", "user")
		require.NoError(t, err)
		_, err = store.CreateEnvelope(ctx, Envelope{
			EnvelopeID:      "env_1",
			SenderUserID:    sender.UserID,
			SenderEmail:     sender.Email,
			RecipientUserID: recipient.UserID,
			RecipientEmail:  recipient.Email,
			PayloadType:     domain.EnvelopePayloadKnowledgeCapsule,
			Status:          domain.EnvelopeStatusPending,
			CreatedAt:       now,
		})
		require.NoError(t, err)

		outbox, err := store.ListEnvelopes(ctx, ListEnvelopesFilter{
			Principal: UserPrincipal{User: sender},
			Direction: domain.EnvelopeDirectionSent,
		})
		require.NoError(t, err)
		require.Len(t, outbox, 1)
		require.Equal(t, "env_1", outbox[0].EnvelopeID)

		inbox, err := store.ListEnvelopes(ctx, ListEnvelopesFilter{
			Principal: UserPrincipal{User: sender},
		})
		require.NoError(t, err)
		require.Empty(t, inbox)

		received, err := store.ListEnvelopes(ctx, ListEnvelopesFilter{
			Principal: UserPrincipal{User: recipient},
		})
		require.NoError(t, err)
		require.Len(t, received, 1)

		got, err := store.GetEnvelope(ctx, UserPrincipal{User: sender}, "env_1")
		require.NoError(t, err)
		require.Equal(t, "env_1", got.EnvelopeID)
	})

	t.Run(
		"Given a recipient accepts an envelope then sender outbox shows accepted",
		func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
			acceptedAt := now.Add(time.Minute)
			store := NewMemoryStore(func() time.Time { return now })
			sender, err := store.EnsureUser(ctx, "sender@example.com", "", "user")
			require.NoError(t, err)
			recipient, err := store.EnsureUser(ctx, "recipient@example.com", "", "user")
			require.NoError(t, err)
			_, err = store.CreateEnvelope(ctx, Envelope{
				EnvelopeID:      "env_1",
				SenderUserID:    sender.UserID,
				SenderEmail:     sender.Email,
				RecipientUserID: recipient.UserID,
				RecipientEmail:  recipient.Email,
				PayloadType:     domain.EnvelopePayloadKnowledgeCapsule,
				Status:          domain.EnvelopeStatusPending,
				CreatedAt:       now,
			})
			require.NoError(t, err)

			_, err = store.AcceptEnvelope(ctx, UserPrincipal{User: recipient}, "env_1", acceptedAt)
			require.NoError(t, err)
			outbox, err := store.ListEnvelopes(ctx, ListEnvelopesFilter{
				Principal: UserPrincipal{User: sender},
				Direction: domain.EnvelopeDirectionSent,
			})

			require.NoError(t, err)
			require.Len(t, outbox, 1)
			require.Equal(t, domain.EnvelopeStatusAccepted, outbox[0].Status)
			require.NotNil(t, outbox[0].AcceptedAt)
			require.True(t, outbox[0].AcceptedAt.Equal(acceptedAt))
		},
	)

	t.Run(
		"Given an already accepted envelope then accepting again returns not found",
		func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
			store := NewMemoryStore(func() time.Time { return now })
			sender, err := store.EnsureUser(ctx, "sender@example.com", "", "user")
			require.NoError(t, err)
			recipient, err := store.EnsureUser(ctx, "recipient@example.com", "", "user")
			require.NoError(t, err)
			acceptedAt := now.Add(time.Minute)
			_, err = store.CreateEnvelope(ctx, Envelope{
				EnvelopeID:      "env_1",
				SenderUserID:    sender.UserID,
				SenderEmail:     sender.Email,
				RecipientUserID: recipient.UserID,
				RecipientEmail:  recipient.Email,
				PayloadType:     domain.EnvelopePayloadKnowledgeCapsule,
				Status:          domain.EnvelopeStatusAccepted,
				CreatedAt:       now,
				AcceptedAt:      &acceptedAt,
			})
			require.NoError(t, err)

			_, err = store.AcceptEnvelope(
				ctx,
				UserPrincipal{User: recipient},
				"env_1",
				now.Add(2*time.Minute),
			)

			require.ErrorIs(t, err, ErrNotFound)
		},
	)
}
