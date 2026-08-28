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
// (either form), consulting a per-agent cache before falling back to a single
// ListAgentSessions read. The mapping is stable for a session's lifetime, so
// caching it removes the per-frame session query that otherwise runs twice for
// every inbound frame (session-id middleware + history canonicalization) — a
// dominant cost under high-volume terminal output.
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
	sessions, err := store.ListAgentSessions(
		ctx,
		domain.UserPrincipal{User: domain.User{UserID: a.ownerUserID}},
		a.agentID,
	)
	if err != nil {
		return acpSessionAliases{}, false
	}
	for _, session := range sessions {
		if session.SessionID == sessionID || session.NativeID == sessionID {
			aliases := acpSessionAliases{
				managerID: session.SessionID,
				nativeID:  firstNonEmpty(session.NativeID, session.SessionID),
			}
			a.cacheSessionAliases(aliases)
			return aliases, true
		}
	}
	// Only positive mappings are cached: a frame that arrives before its session
	// row is durable must be re-resolved on the next frame, never pinned as
	// "unknown".
	return acpSessionAliases{}, false
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
