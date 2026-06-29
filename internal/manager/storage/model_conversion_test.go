package storage

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	dbmodel "github.com/pax-beehive/pax-manager/internal/manager/storage/dal/model"
)

func TestKnowledgeModelConversions(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	archivedAt := now.Add(time.Hour)
	capsule := KnowledgeCapsule{
		CapsuleID:              "cap_1",
		OwnerUserID:            "usr_owner",
		SourceSessionID:        "sess_1",
		SourceAgentID:          "agent_1",
		SourceNodeID:           "node_1",
		CreatedByUserID:        "usr_owner",
		Keyword:                "paxl",
		Title:                  "Paxl handoff",
		Summary:                "Summary",
		Content:                "Content",
		SuggestedSkills:        json.RawMessage(`["go"]`),
		References:             json.RawMessage(`[{"path":"main.go"}]`),
		OpenQuestions:          json.RawMessage(`["q1"]`),
		Risks:                  json.RawMessage(`["risk"]`),
		Redactions:             json.RawMessage(`["secret"]`),
		Status:                 domain.KnowledgeCapsuleStatusArchived,
		Truncated:              true,
		OriginalEstimatedChars: 123,
		CreatedAt:              now,
		ArchivedAt:             &archivedAt,
	}
	require.Equal(t, capsule, knowledgeCapsuleFromModel(knowledgeCapsuleModel(capsule)))
	require.Equal(t, KnowledgeCapsule{}, knowledgeCapsuleFromModel(nil))
	require.Len(t, knowledgeCapsulesFromModels([]*dbmodel.KnowledgeCapsule{
		knowledgeCapsuleModel(capsule),
	}), 1)

	deliveredAt := now.Add(2 * time.Hour)
	injection := SessionKnowledgeInjection{
		InjectionID:         "inj_1",
		OwnerUserID:         "usr_owner",
		CapsuleID:           "cap_1",
		TargetSessionID:     "sess_2",
		TargetAgentID:       "agent_1",
		TargetNodeID:        "node_1",
		CreatedByUserID:     "usr_owner",
		DeliveredAsUserID:   "usr_owner",
		DeliveryMethod:      domain.KnowledgeInjectionDeliveryMailboxSteer,
		DeliveryMessageID:   "msg_1",
		DeliveryMessageType: domain.MessageTypeSystemHandoff,
		Status:              domain.KnowledgeInjectionStatusDelivered,
		CreatedAt:           now,
		DeliveredAt:         &deliveredAt,
		Error:               "",
	}
	require.Equal(t, injection, knowledgeInjectionFromModel(knowledgeInjectionModel(injection)))
	require.Equal(t, SessionKnowledgeInjection{}, knowledgeInjectionFromModel(nil))
	require.Len(t, knowledgeInjectionsFromModels([]*dbmodel.SessionKnowledgeInjection{
		knowledgeInjectionModel(injection),
	}), 1)
}

func TestEnvelopeAndFriendModelConversions(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	acceptedAt := now.Add(time.Hour)
	archivedAt := now.Add(2 * time.Hour)
	envelope := Envelope{
		EnvelopeID:      "env_1",
		SenderUserID:    "usr_sender",
		SenderEmail:     "sender@example.com",
		RecipientUserID: "usr_recipient",
		RecipientEmail:  "recipient@example.com",
		PayloadType:     domain.EnvelopePayloadKnowledgeCapsule,
		PayloadJSON:     json.RawMessage(`{"capsule_id":"cap_1"}`),
		Message:         "handoff",
		Status:          domain.EnvelopeStatusArchived,
		CreatedAt:       now,
		AcceptedAt:      &acceptedAt,
		ArchivedAt:      &archivedAt,
	}
	require.Equal(t, envelope, envelopeFromModel(envelopeModel(envelope)))
	require.Nil(t, envelopeModel(Envelope{}).RecipientUserID)
	require.Equal(t, Envelope{}, envelopeFromModel(nil))
	require.Len(t, envelopesFromModels([]envelopeRow{*envelopeModel(envelope)}), 1)

	removedAt := now.Add(3 * time.Hour)
	friend := Friend{
		FriendID:        "fr_1",
		RequesterUserID: "usr_sender",
		RequesterEmail:  "sender@example.com",
		RequesterAlias:  "sender",
		RecipientUserID: "usr_recipient",
		RecipientEmail:  "recipient@example.com",
		RecipientAlias:  "recipient",
		Status:          domain.FriendStatusRemoved,
		CreatedAt:       now,
		AcceptedAt:      &acceptedAt,
		RemovedAt:       &removedAt,
	}
	require.Equal(t, friend, friendFromModel(friendModel(friend)))
	require.Nil(t, friendModel(Friend{}).RecipientUserID)
	require.Equal(t, Friend{}, friendFromModel(nil))
	require.Len(t, friendsFromModels([]friendRow{*friendModel(friend)}), 1)
}

func TestGormModelConversions(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	role := "admin"
	status := "online"
	osName := "darwin"
	apiEndpoint := "http://localhost:8642"
	raw := `{"ok":true}`

	require.ErrorIs(t, mapGormError(gorm.ErrRecordNotFound), ErrNotFound)
	err := errors.New("db failed")
	require.ErrorIs(t, mapGormError(err), err)
	require.Nil(t, stringPtr(""))
	require.Equal(t, "value", *stringPtr("value"))
	require.Equal(t, "{}", string(rawJSONValue(nil)))
	require.Equal(t, "[]", string(rawJSONArrayValue(nil)))

	user := userFromModel(&dbmodel.User{
		UserID:      "usr_1",
		Email:       "user@example.com",
		DisplayName: "User",
		Role:        &role,
		CreatedAt:   &now,
		LastSeenAt:  &now,
	})
	require.Equal(t, "admin", user.Role)
	require.Equal(t, User{}, userFromModel(nil))

	key := userAPIKeyFromModel(&dbmodel.UserAPIKey{
		KeyID:       "key_1",
		OwnerUserID: "usr_1",
		Name:        "laptop",
		Prefix:      "paxu_123",
		CreatedAt:   &now,
		LastUsedAt:  &now,
	})
	require.Equal(t, "key_1", key.KeyID)
	require.Len(t, userAPIKeysFromModels([]*dbmodel.UserAPIKey{{KeyID: "key_2"}}), 1)
	require.Equal(t, UserAPIKey{}, userAPIKeyFromModel(nil))

	node := nodeFromModel(&dbmodel.Node{
		NodeID:        "node_1",
		OwnerUserID:   "usr_1",
		Name:          "node",
		Hostname:      "host",
		Os:            &osName,
		APIEndpoint:   &apiEndpoint,
		Status:        &status,
		LastHeartbeat: &now,
		RegisteredAt:  &now,
		UserMetadata:  &raw,
		Metadata:      &raw,
	})
	require.True(t, node.Online)
	require.Equal(t, Node{}, nodeFromModel(nil))

	registration := nodeRegistrationSessionFromModel(&dbmodel.NodeRegistrationSession{
		RegistrationID:       "reg_1",
		PairCode:             "PAIR",
		PollTokenHash:        "poll_hash",
		Status:               &status,
		OwnerUserID:          &role,
		NodeID:               &node.NodeID,
		RequestedName:        "node",
		RequestedHostname:    "host",
		RequestedOs:          &osName,
		RequestedAPIEndpoint: &apiEndpoint,
		RequestedMetadata:    &raw,
		ExpiresAt:            now.Add(time.Hour),
		CreatedAt:            &now,
		ApprovedAt:           &now,
		ConsumedAt:           &now,
	})
	require.Equal(t, "reg_1", registration.RegistrationID)
	require.Equal(t, NodeRegistrationSession{}, nodeRegistrationSessionFromModel(nil))
}
