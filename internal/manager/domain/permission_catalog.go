package domain

import (
	"context"
	"encoding/json"
	"time"
)

const (
	PermissionChoiceKindPAX   = "pax"
	PermissionChoiceKindAgent = "agent"

	PermissionChoicePAXAutoApprove = "pax:auto_approve"

	PermissionCatalogSourceLive     = "live"
	PermissionCatalogSourceObserved = "observed"
	PermissionCatalogSourceProfile  = "profile"
	PermissionCatalogSourcePAXOnly  = "pax_only"

	PermissionBindingConfigOption = "config_option"
	PermissionBindingLegacyMode   = "legacy_mode"
)

func BuiltInCodexPermissionProfile(createdAt time.Time) AgentPermissionProfile {
	return AgentPermissionProfile{
		ProfileID:                 "codex-acp.permissions",
		Revision:                  1,
		Status:                    "active",
		AgentType:                 "codex",
		ACPAgentName:              "@agentclientprotocol/codex-acp",
		ACPAgentVersionConstraint: "*",
		RuntimeName:               "codex",
		RuntimeVersionConstraint:  "*",
		Priority:                  100,
		Source:                    "builtin",
		CreatedAt:                 createdAt.UTC(),
		Definition: PermissionProfileDefinition{
			Binding: PermissionBinding{
				Kind:     PermissionBindingConfigOption,
				ConfigID: "mode",
				Category: "mode",
			},
			DefaultChoiceID: "agent:mode:agent",
			NativeChoices: []PermissionNativeChoice{
				{
					ChoiceID: "agent:mode:read-only",
					Label:    "Read-only",
					Value:    "read-only",
					Risk:     "read_only",
				},
				{
					ChoiceID: "agent:mode:agent",
					Label:    "Agent",
					Value:    "agent",
					Risk:     "workspace_write",
				},
				{
					ChoiceID:             "agent:mode:agent-full-access",
					Label:                "Agent (full access)",
					Value:                "agent-full-access",
					Risk:                 "host_full_access",
					RequiresConfirmation: true,
				},
			},
		},
	}
}

// AgentRuntimeIdentity is the typed, sanitized identity reported by paxd for
// the ACP process currently backing an agent installation.
type AgentRuntimeIdentity struct {
	AgentID             string    `json:"agent_id"`
	ReportEpoch         string    `json:"report_epoch,omitempty"`
	SchemaVersion       int       `json:"schema_version"`
	ConnectionID        string    `json:"connection_id"`
	ReportGeneration    int64     `json:"report_generation"`
	ProtocolVersion     int       `json:"protocol_version,omitempty"`
	ACPAgentName        string    `json:"acp_agent_name,omitempty"`
	ACPAgentTitle       string    `json:"acp_agent_title,omitempty"`
	ACPAgentVersion     string    `json:"acp_agent_version,omitempty"`
	RuntimeName         string    `json:"runtime_name,omitempty"`
	RuntimeVersion      string    `json:"runtime_version,omitempty"`
	RuntimeBuild        string    `json:"runtime_build,omitempty"`
	RuntimeChannel      string    `json:"runtime_channel,omitempty"`
	IdentityFingerprint string    `json:"identity_fingerprint"`
	CommandFingerprint  string    `json:"command_fingerprint,omitempty"`
	ClientProfileHash   string    `json:"client_profile_hash,omitempty"`
	WorkerResultHash    string    `json:"worker_result_hash,omitempty"`
	PoolConsistency     string    `json:"pool_consistency,omitempty"`
	ObservedAt          time.Time `json:"observed_at"`
}

type PermissionBinding struct {
	Kind         string `json:"kind"`
	ConfigID     string `json:"config_id,omitempty"`
	Category     string `json:"category,omitempty"`
	CurrentValue string `json:"current_value,omitempty"`
}

type PermissionNativeChoice struct {
	ChoiceID             string `json:"choice_id"`
	Label                string `json:"label"`
	Description          string `json:"description,omitempty"`
	Value                string `json:"value"`
	Risk                 string `json:"risk,omitempty"`
	RequiresConfirmation bool   `json:"requires_confirmation,omitempty"`
}

// PermissionProfileDefinition is deliberately declarative. It cannot contain
// arbitrary RPC methods or command metadata.
type PermissionProfileDefinition struct {
	Binding         PermissionBinding        `json:"binding"`
	DefaultChoiceID string                   `json:"default_choice_id,omitempty"`
	NativeChoices   []PermissionNativeChoice `json:"native_choices"`
}

type AgentPermissionProfile struct {
	ProfileID                 string                      `json:"profile_id"`
	Revision                  int64                       `json:"revision"`
	Status                    string                      `json:"status"`
	OwnerUserID               string                      `json:"owner_user_id,omitempty"`
	AgentType                 string                      `json:"agent_type,omitempty"`
	ACPAgentName              string                      `json:"acp_agent_name,omitempty"`
	ACPAgentVersionConstraint string                      `json:"acp_agent_version_constraint,omitempty"`
	RuntimeName               string                      `json:"runtime_name,omitempty"`
	RuntimeVersionConstraint  string                      `json:"runtime_version_constraint,omitempty"`
	Priority                  int                         `json:"priority"`
	Definition                PermissionProfileDefinition `json:"definition"`
	Source                    string                      `json:"source"`
	CreatedAt                 time.Time                   `json:"created_at"`
}

type ObservedPermissionOption struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type ObservedPermissionCatalog struct {
	Binding         PermissionBinding          `json:"binding"`
	DefaultChoiceID string                     `json:"default_choice_id,omitempty"`
	Options         []ObservedPermissionOption `json:"options"`
}

type AgentPermissionObservation struct {
	AgentID             string                    `json:"agent_id"`
	IdentityFingerprint string                    `json:"identity_fingerprint"`
	CatalogRevision     int64                     `json:"catalog_revision"`
	CatalogHash         string                    `json:"catalog_hash"`
	Catalog             ObservedPermissionCatalog `json:"catalog"`
	ObservedAt          time.Time                 `json:"observed_at"`
	ExpiresAt           time.Time                 `json:"expires_at"`
}

type PermissionCatalogChoice struct {
	ChoiceID             string `json:"choice_id"`
	Label                string `json:"label"`
	Description          string `json:"description,omitempty"`
	Kind                 string `json:"kind"`
	Risk                 string `json:"risk,omitempty"`
	RequiresConfirmation bool   `json:"requires_confirmation,omitempty"`
}

type AgentPermissionCatalog struct {
	CatalogRevision int64                     `json:"catalog_revision"`
	Source          string                    `json:"source"`
	Stale           bool                      `json:"stale"`
	DefaultChoiceID string                    `json:"default_choice_id,omitempty"`
	Choices         []PermissionCatalogChoice `json:"choices"`
}

type ResolvedPermissionChoice struct {
	ChoiceID     string
	ApprovalMode string
	Binding      PermissionBinding
	Value        string
}

type PermissionCatalogStore interface {
	UpsertAgentRuntimeIdentity(context.Context, AgentRuntimeIdentity) error
	GetAgentRuntimeIdentity(context.Context, string) (AgentRuntimeIdentity, error)
	InsertPermissionProfile(context.Context, AgentPermissionProfile) error
	ListActivePermissionProfiles(context.Context, string, string) ([]AgentPermissionProfile, error)
	UpsertPermissionObservation(context.Context, AgentPermissionObservation) (AgentPermissionObservation, error)
	GetPermissionObservation(context.Context, string, string) (AgentPermissionObservation, error)
}

func ClonePermissionProfileDefinition(in PermissionProfileDefinition) PermissionProfileDefinition {
	out := in
	out.NativeChoices = append([]PermissionNativeChoice(nil), in.NativeChoices...)
	return out
}

func CloneObservedPermissionCatalog(in ObservedPermissionCatalog) ObservedPermissionCatalog {
	out := in
	out.Options = append([]ObservedPermissionOption(nil), in.Options...)
	return out
}

func MarshalPermissionProfileDefinition(definition PermissionProfileDefinition) (json.RawMessage, error) {
	return json.Marshal(definition)
}
