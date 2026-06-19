# Session Identity Boundary Design

## Problem

ACP runtimes return native session ids, often UUIDs. pax-manager must expose a
stable manager session id to users, such as `sess_...`, and persist the ACP id
as `agent_sessions.native_id`.

The intended rule is:

- manager to paxd uses native ids, including reliable transport journal payloads
- pax-manager to users, console, API, websocket, history, approvals, and lists
  uses manager session ids only
- top-level user-facing code should not know native ids exist
- native ids should be visible only inside paxd/native transport code and
  persistence internals

Today that rule is implemented by scattered translations. Examples:

- `acpSessionIDMiddleware` rewrites ACP websocket frames in both directions.
- `canonicalSessionStore` rewrites some business writes before persistence.
- `acp_history` canonicalizes session ids independently.
- storage methods such as `PullMailbox`, `CreateNodeOutboundMessage`, and
  approval helpers translate back to native ids for paxd-facing responses.
- `virtualACPSessionID` duplicates lookup logic during tunnel authentication.

This makes it too easy for a new API, websocket path, history projector, or
approval path to leak a native id.

## Target Shape

Split pax-manager into two explicit halves:

```text
user/API/console boundary
  manager session id only: sess_...
  native_id absent from DTOs and payloads

manager core and durable business state
  manager session id only: sess_...
  agent_sessions.native_id is metadata, not the primary reference

native transport boundary
  paxd/ACP session id on the wire
  manager session id resolved at ingress and translated at egress
```

The important change is not just a helper function. It is that each package
should declare which id language it speaks.

## Proposed Components

### SessionIdentityResolver

Create a small service that owns all session id mapping.

```go
type SessionIdentityResolver interface {
    // Called when paxd/native side reports a session id.
    ResolveNative(ctx context.Context, ownerUserID, nodeID, agentID, nativeID string) (SessionIdentity, error)

    // Called by user-facing APIs to validate and load a manager session.
    ResolveManager(ctx context.Context, principal domain.UserPrincipal, agentID, sessionID string) (SessionIdentity, error)

    // Called only by native egress paths.
    NativeForManager(ctx context.Context, agentID, managerID string) (string, error)
}

type SessionIdentity struct {
    OwnerUserID string
    NodeID      string
    AgentID     string
    ManagerID   string
    NativeID    string
}
```

Rules:

- `ResolveNative` is allowed to create a `sess_...` row if no mapping exists.
- `ResolveManager` must reject unknown or unauthorized sessions.
- `NativeForManager` returns `native_id` when present, otherwise `manager_id`
  for old sessions without a native id.
- All lookups should be scoped by `agent_id`; `node_id` is included when the
  caller has it.

### ManagerSessionStore

Keep persistence canonical. Storage should store business rows using manager ids:

- `agent_sessions.session_id`
- `mailbox.session_id`
- `messages.session_id`
- `agent_approvals.request_session_id`
- `agent_approvals.grant_session_id`
- `secret_access_events.session_id`
- `runtime_state.session_id`

The raw storage package should not translate mailbox or approval records to
native on ordinary reads. If native translation is still needed by paxd-facing
calls, place it behind a native adapter, not in generic storage methods.

### Native Adapter

Introduce a narrow adapter for paxd/native endpoints.

```go
type NativePaxdStore struct {
    store    domain.Store
    sessions SessionIdentityResolver
}

func (s NativePaxdStore) PullMailbox(ctx context.Context, agentID, nativeOrManagerID string, offset int64, limit int) (domain.MailboxPull, error) {
    identity, err := s.sessions.ResolveNativeOrManager(ctx, agentID, nativeOrManagerID)
    if err != nil {
        return domain.MailboxPull{}, err
    }
    pull, err := s.store.PullMailbox(ctx, agentID, identity.ManagerID, offset, limit)
    if err != nil {
        return domain.MailboxPull{}, err
    }
    translateMailboxPullToNative(pull, identity.NativeID)
    return pull, nil
}
```

This makes "native ids on paxd wire" a property of the paxd adapter, not a
property of every storage read.

### ACP Tunnel Boundary

The tunnel should track both ids explicitly.

```go
type ACPTunnelAgent struct {
    agentID      string
    nodeID       string
    ownerUserID  string
    managerSID   string
    nativeSID    string
}
```

Ingress:

1. paxd connects with query `session_id=<native id>`.
2. `ResolveNative` returns or creates `{ManagerID: sess_..., NativeID: uuid}`.
3. hub keys by manager id.
4. logs may include both ids only in internal logs.

User attach:

1. user asks for `session_id=sess_...`.
2. `ResolveManager` validates it.
3. hub claim uses manager id.

Frame direction:

- user to paxd: rewrite all ACP `sessionId` and `session_id` fields to native id
  immediately before journaling and sending to paxd
- paxd to user: journal raw native payload in `transport_journal`, project
  business history using manager id, then rewrite outbound user websocket frame
  to manager id

The transport journal may keep native payloads because it is replay state for
paxd. Business history must never use native ids.

## Package Boundary Recommendation

```text
internal/manager/sessionid
  Resolver and mapping logic.
  Can call store session-specific methods.

internal/manager/userapi
  Manager ids only.
  Reject native-looking ids unless a compatibility path is explicitly enabled.

internal/manager/paxd
  Native ids at request/response boundary.
  Immediately resolve native to manager on ingress.
  Translate manager to native on egress.

internal/manager/storage
  Canonical persistence.
  No user/native presentation decisions in generic methods.

internal/manager/acp_*
  Transport may see native ids.
  Projectors and runtime state receive manager ids.
```

## Migration Plan

1. Add `SessionIdentityResolver`.
   - Move `normalizeReportedSessionInput`, `virtualSessionID`, `nativeSessionID`,
     `virtualACPSessionID`, and session-list lookup logic into this component.
   - Add tests for native-to-manager creation, lookup by existing native id,
     lookup by manager id, and old sessions with empty `native_id`.

2. Stop exposing `NativeID`.
   - Keep `domain.AgentSession.NativeID` as `json:"-"`.
   - Verify generated and hand-written DTOs do not include `native_id` on
     user-facing routes.
   - Keep native id only in internal structs or paxd-specific DTOs if required.

3. Make storage canonical.
   - Remove native translation from generic mailbox/history/approval/list reads.
   - Replace `canonicalSessionStore` with resolver-backed ingress conversion.
   - Keep tables storing manager ids for business rows.

4. Add paxd/native adapters.
   - `PullMailbox` and `PullNodeMailbox` return native ids only through the
     paxd-facing service.
   - `CreateNodeOutboundMessage`, secret access, approval requests, and delivery
     reports resolve native session ids to manager ids before persistence.

5. Refactor ACP tunnel.
   - Store both `managerSID` and `nativeSID` on `ACPTunnelAgent`.
   - Hub keys by manager id.
   - Frame rewriting becomes a boundary operation instead of a broad middleware
     that has to rediscover mappings.

6. Add leak tests.
   - User list/get/history/message APIs never contain the UUID native id.
   - User ACP websocket receives `sess_...` in all `sessionId` fields.
   - paxd mailbox and ACP transport still receive native ids.
   - `messages`, `message_parts`, approvals, and secret audit rows persist
     manager ids.

## Suggested Test Cases

```go
func TestACPUserTunnelDoesNotExposeNativeSessionID(t *testing.T) {
    nativeID := "77921871-8997-4d7c-b3a7-9bdf3dd7c492"
    managerID := "sess_abc"

    // paxd connects with nativeID
    // user connects with managerID
    // paxd sends session/update containing nativeID
    // user receives same frame with managerID and no nativeID substring
}

func TestHistoryProjectionStoresManagerSessionID(t *testing.T) {
    // Given agent_sessions(session_id=sess_abc, native_id=uuid)
    // When a paxd_to_manager transport frame contains uuid
    // Then messages.session_id == sess_abc
}

func TestPaxdMailboxEgressUsesNativeSessionID(t *testing.T) {
    // Given mailbox row stored with sess_abc
    // When paxd pulls mailbox for that session
    // Then response message.session_id and payload.sessionId use uuid
}

func TestUserAPIsNeverSerializeNativeID(t *testing.T) {
    // List sessions, get session, list mailbox, and history.
    // Assert response body does not contain native UUID and no native_id field.
}
```

## Implementation Notes

- Prefer a positive type vocabulary: `ManagerSessionID` and
  `NativeSessionID`. Even simple string aliases make function signatures harder
  to misuse.
- Avoid helper names like `virtualSessionID`; the direction is unclear. Use
  `ManagerForNative` and `NativeForManager`.
- Keep compatibility for existing sessions where `session_id` is already a
  native-looking value, but confine it to resolver migration logic.
- Transport replay should not mutate persisted `transport_journal.payload_json`.
  Rewrite only at the boundary where a frame crosses into or out of the user
  websocket.
- Once this split lands, `canonicalSessionStore` should disappear. A store
  decorator that silently changes ids is exactly the kind of hidden boundary
  that caused the current leakage risk.

