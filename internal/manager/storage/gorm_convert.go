package storage

import (
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	dbmodel "github.com/pax-beehive/pax-manager/internal/manager/storage/dal/model"
)

func mapGormError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func timeValue(v *time.Time) time.Time {
	if v == nil {
		return time.Time{}
	}
	return *v
}

func userFromModel(row *dbmodel.User) User {
	if row == nil {
		return User{}
	}
	return User{
		UserID:      row.UserID,
		Email:       row.Email,
		DisplayName: row.DisplayName,
		Role:        stringValue(row.Role),
		CreatedAt:   timeValue(row.CreatedAt),
		LastSeenAt:  row.LastSeenAt,
	}
}

func userAPIKeyFromModel(row *dbmodel.UserAPIKey) UserAPIKey {
	if row == nil {
		return UserAPIKey{}
	}
	return UserAPIKey{
		KeyID:       row.KeyID,
		OwnerUserID: row.OwnerUserID,
		Name:        row.Name,
		Prefix:      row.Prefix,
		CreatedAt:   timeValue(row.CreatedAt),
		LastUsedAt:  row.LastUsedAt,
		RevokedAt:   row.RevokedAt,
	}
}

func userAPIKeysFromModels(rows []*dbmodel.UserAPIKey) []UserAPIKey {
	out := make([]UserAPIKey, 0, len(rows))
	for _, row := range rows {
		out = append(out, userAPIKeyFromModel(row))
	}
	return out
}

func rawJSONPtr(raw json.RawMessage) *string {
	if len(raw) == 0 {
		return nil
	}
	value := string(raw)
	return &value
}

func rawJSONValue(raw *string) json.RawMessage {
	if raw == nil || *raw == "" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(*raw)
}

func nodeFromModel(row *dbmodel.Node) Node {
	if row == nil {
		return Node{}
	}
	node := Node{
		NodeID:        row.NodeID,
		OwnerUserID:   row.OwnerUserID,
		Name:          row.Name,
		Hostname:      row.Hostname,
		MachineType:   row.MachineType,
		OS:            stringValue(row.Os),
		Arch:          row.Arch,
		PaxdVersion:   row.PaxdVersion,
		APIEndpoint:   stringValue(row.APIEndpoint),
		Status:        stringValue(row.Status),
		LastHeartbeat: row.LastHeartbeat,
		RegisteredAt:  timeValue(row.RegisteredAt),
		Metadata:      rawJSONValue(row.Metadata),
	}
	node.Online = node.Status == "online"
	return node
}

func nodeRegistrationSessionFromModel(
	row *dbmodel.NodeRegistrationSession,
) NodeRegistrationSession {
	if row == nil {
		return NodeRegistrationSession{}
	}
	return NodeRegistrationSession{
		RegistrationID: row.RegistrationID,
		PairCode:       row.PairCode,
		PollTokenHash:  row.PollTokenHash,
		Status:         stringValue(row.Status),
		OwnerUserID:    stringValue(row.OwnerUserID),
		NodeID:         stringValue(row.NodeID),
		Request: RegisterNodeRequest{
			Name:        row.RequestedName,
			Hostname:    row.RequestedHostname,
			MachineType: row.RequestedMachineType,
			OS:          stringValue(row.RequestedOs),
			Arch:        row.RequestedArch,
			PaxdVersion: row.RequestedPaxdVersion,
			APIEndpoint: stringValue(row.RequestedAPIEndpoint),
			Metadata:    rawJSONValue(row.RequestedMetadata),
		},
		ExpiresAt:  row.ExpiresAt,
		CreatedAt:  timeValue(row.CreatedAt),
		ApprovedAt: row.ApprovedAt,
		ConsumedAt: row.ConsumedAt,
	}
}
