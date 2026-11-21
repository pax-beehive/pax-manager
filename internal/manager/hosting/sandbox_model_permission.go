package hosting

import (
	"context"
	"strings"
)

const sandboxModelPermissionTokenPrefix = "sbd_"
const sandboxModelPermissionSubject = "sandbox_model_access"
const sandboxModelRevoked = "revoked"

// A nil permission belongs to the legacy lease-bound flow. An empty lineage is
// an explicit revocation and must never be migrated into a new permission.
type sandboxModelPermission struct {
	Lineage     string `json:"lineage,omitempty"`
	Provisioned bool   `json:"provisioned,omitempty"`
}

type sandboxModelPermissionIdentity struct {
	Owner, Node, Agent, Lineage string
}

func (g *sandboxModelGrant) enabled() bool {
	return g != nil && g.State != sandboxModelRevoked &&
		(g.Permission == nil || g.Permission.Lineage != "")
}

func sandboxModelOwnedNode(sandbox *State, owner string) string {
	if record := sandbox.Environments[owner]; record != nil && record.Starter.OwnerUserID == owner {
		return record.Starter.NodeID
	}
	return ""
}

func sandboxModelPermissionFor(
	prior *sandboxModelGrant, parent sandboxModelParent, harness string,
) *sandboxModelPermission {
	if prior.enabled() && prior.Permission != nil &&
		prior.Parent.NodeID == parent.NodeID && prior.Harness == harness {
		return prior.Permission
	}
	return &sandboxModelPermission{Lineage: secret("")}
}

func sandboxModelCanResume(grant *sandboxModelGrant, parent sandboxModelParent) bool {
	previous := grant.Parent
	return previous.NodeID == parent.NodeID && previous.EnvironmentID == parent.EnvironmentID &&
		previous.HostID == parent.HostID && previous.Generation < parent.Generation
}

func (s *Service) RevokeSandboxModel(ctx context.Context, owner string, target *WakeTarget) error {
	store, ok := s.sandboxModelStore()
	if !ok {
		return ErrDisabled
	}
	if target == nil || target.NodeID == "" || target.AgentID == "" {
		return ErrConflict
	}
	return store.WithSandboxModelStates(ctx, func(account, _ *State) error {
		grant := account.SandboxModelGrants[sandboxModelGrantKey(owner, target.AgentID)]
		if grant == nil || grant.Owner != owner ||
			grant.AgentID != target.AgentID || grant.Parent.NodeID != target.NodeID {
			return ErrNotFound
		}
		grant.Permission = &sandboxModelPermission{}
		grant.State, grant.ReasonCode, grant.Job = sandboxModelRevoked, "", nil
		grant.ModelIDs = nil
		return nil
	})
}

func (s *Service) signSandboxModelPermission(grant *sandboxModelGrant) string {
	subject := strings.Join([]string{
		sandboxModelPermissionSubject, grant.Owner, grant.Parent.NodeID, grant.AgentID,
		grant.Permission.Lineage,
	}, "/")
	return sandboxModelPermissionTokenPrefix + sign(s.Config.TokenSecret, subject, 1)
}

func (s *Service) parseSandboxModelPermission(
	signed string,
) (sandboxModelPermissionIdentity, error) {
	subject, version, err := tokenEnvironment(s.Config.TokenSecret, signed)
	parts := strings.Split(subject, "/")
	if err != nil || version != 1 || len(parts) != 5 ||
		parts[0] != sandboxModelPermissionSubject ||
		parts[1] == "" || parts[2] == "" || parts[3] == "" || parts[4] == "" {
		return sandboxModelPermissionIdentity{}, ErrUnauthorized
	}
	return sandboxModelPermissionIdentity{parts[1], parts[2], parts[3], parts[4]}, nil
}

func (s *Service) withSandboxModelPermission(
	ctx context.Context,
	identity sandboxModelPermissionIdentity,
	run func(*State, *EnvironmentRecord, *sandboxModelGrant) error,
) error {
	store, ok := s.sandboxModelStore()
	if !ok || !s.Sandbox.allowOwner(ctx, identity.Owner) {
		return ErrUnauthorized
	}
	harness, err := s.ModelAgentHarness(ctx, identity.Owner, identity.Node, identity.Agent)
	if err != nil || !supportedSandboxHarness(harness) {
		return ErrUnauthorized
	}
	return store.WithSandboxModelStates(ctx, func(account, sandbox *State) error {
		wallet := account.Environments[identity.Owner]
		grant := account.SandboxModelGrants[sandboxModelGrantKey(identity.Owner, identity.Agent)]
		if wallet == nil || sandboxModelOwnedNode(sandbox, identity.Owner) != identity.Node ||
			!grant.matchesPermission(identity, harness) {
			return ErrUnauthorized
		}
		if wallet.Starter.AccessPaused || account.Admin.Users[identity.Owner].AccessPaused {
			return ErrAccessPaused
		}
		return run(account, wallet, grant)
	})
}

func (g *sandboxModelGrant) matchesPermission(
	identity sandboxModelPermissionIdentity,
	harness string,
) bool {
	return g.enabled() && g.Permission != nil &&
		g.Permission.Lineage == identity.Lineage && g.Owner == identity.Owner &&
		g.Parent.NodeID == identity.Node && g.AgentID == identity.Agent && g.Harness == harness
}
