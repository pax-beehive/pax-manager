package manager

import (
	"context"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// acpSessionAliases is the stable translation between a session's public
// manager ID and its native ACP ID. Both aliases resolve to the same entry so a
// frame carrying either form hits the cache.
type acpSessionAliases struct {
	managerID string
	nativeID  string
}

// resolveSessionAliases returns the manager/native aliases for any session ID
// (either form), consulting a per-agent cache before an authorized lookup.
func (a *ACPTunnelAgent) resolveSessionAliases(
	ctx context.Context,
	store domain.Store,
	sessionID string,
) (acpSessionAliases, bool) {
	if a == nil || sessionID == "" {
		return acpSessionAliases{}, false
	}

	a.sessionAliasMu.RLock()
	cached, ok := a.sessionAliases[sessionID]
	a.sessionAliasMu.RUnlock()
	if ok {
		return cached, true
	}

	if store == nil {
		return acpSessionAliases{}, false
	}
	session, err := resolveStoreSessionAliases(ctx, store, a.ownerUserID, a.agentID, sessionID)
	if err != nil {
		return acpSessionAliases{}, false
	}
	aliases := acpSessionAliases{
		managerID: session.SessionID,
		nativeID:  firstNonEmpty(session.NativeID, session.SessionID),
	}
	a.cacheSessionAliases(aliases)
	return aliases, true
}

// canonicalSessionID resolves any session ID to its public manager ID via the
// cache, falling back to the raw ID when the mapping is not yet known.
func (a *ACPTunnelAgent) canonicalSessionID(
	ctx context.Context,
	store domain.Store,
	sessionID string,
) string {
	if aliases, ok := a.resolveSessionAliases(ctx, store, sessionID); ok {
		return aliases.managerID
	}
	return sessionID
}

// primeSessionAliases records a freshly bound manager/native pair (from
// session/new) so the very first frame that follows is a cache hit.
func (a *ACPTunnelAgent) primeSessionAliases(managerID, nativeID string) {
	if a == nil || managerID == "" || nativeID == "" {
		return
	}
	a.cacheSessionAliases(acpSessionAliases{managerID: managerID, nativeID: nativeID})
}

func (a *ACPTunnelAgent) cacheSessionAliases(aliases acpSessionAliases) {
	if aliases.managerID == "" && aliases.nativeID == "" {
		return
	}
	a.sessionAliasMu.Lock()
	defer a.sessionAliasMu.Unlock()
	if a.sessionAliases == nil {
		a.sessionAliases = make(map[string]acpSessionAliases)
	}
	if aliases.managerID != "" {
		a.sessionAliases[aliases.managerID] = aliases
	}
	if aliases.nativeID != "" {
		a.sessionAliases[aliases.nativeID] = aliases
	}
}
