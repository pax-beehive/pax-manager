# Inquiry Access Model Draft

## Summary

This document captures the proposed access model for inquiry, representative
agents, and shared knowledge. The model is intentionally small, but it borrows
from Zanzibar-style relationship grants and IAM-style request evaluation.

The core idea is:

- Use representative agents to model scoped external answering identities.
- Use grants to model which subject can use which resource for which action.
- Use access grants as the single sharing primitive. The first version can rely
  on subject membership; explicit boundaries can be added later if needed.
- Use explicit escalation when an inquiry needs data outside the current
  representative scope.

This is a design draft. It is not an implementation record.

## Goals

- Support `knowledge_capsule`, `inquiry`, and future `task` envelope payloads.
- Let inquiry answers cite or use evidence without leaking private knowledge.
- Let a user's agent answer in a constrained scope on the user's behalf.
- Avoid forcing every knowledge item into a single broad visibility enum.
- Keep team and conversation membership changes from leaving stale access.
- Give future task and Linear integration a reusable access foundation.

## Non-Goals

- No full IAM policy language in the first version.
- No external authorization engine requirement in the first version.
- No automatic cross-scope reads by representative agents.
- No guarantee that representative agents need dedicated runtimes.
- No group chat product surface in the first version, though the model should
  leave room for conversation-scoped grants.

## Terms

- Principal: The subject making an access request. A principal is either a user
  or an agent session.
- Agent session: One auditable run of an agent runtime.
- Representative agent: A fixed-scope, restricted identity that can answer
  inquiry on behalf of a user.
- Resource: A protected object such as knowledge, envelope, task, document, or
  message.
- Action: The operation requested on a resource.
- Grant: A relationship that allows a subject to perform an action on a
  resource.
- Boundary: An optional future constraint that can keep explicit grants valid
  only inside a team, workspace root, conversation, personal scope, or public
  scope.
- Escalation: A child inquiry or delegation from one scope to a narrower or
  more privileged scope.

## Principal Model

Access requests should use one of two principal types:

```text
user
agent_session
```

An agent session is its own principal for authorization and audit. It should not
contain another nested principal field. It should carry enough metadata to
resolve the actor and runtime:

```text
agent_sessions
- session_id
- agent_id
- actor_type              personal_agent | representative_agent | worker
- actor_id                representative_agent_id when actor_type is representative_agent
- created_by_user_id
- created_at
```

For user principals, authorization uses user membership and direct grants. For
agent-session principals, authorization uses the session actor, the agent owner,
and grants on the representative identity when applicable.

## Representative Agent

A representative agent is a permission identity, not necessarily a physical
runtime. It may use a backing agent/runtime, but access is evaluated through the
representative identity and grants.

```text
representative_agent
- representative_agent_id
- profile_id
- backing_agent_id
- represents_type         user | team
- represents_id           user_id or team_id shown as the represented subject
- approval_policy_id
- status                  active | paused
- created_at
```

Representative agents should never silently inherit the backing agent's private
context. They can only use resources allowed by grants to that representative
or to a subject the representative is allowed to act for.

`represents_type` and `represents_id` describe who or what the representative
speaks on behalf of, such as Alice, the HR team, or the company.
- `approval_policy_id` points to the policy used when another representative
  tries to escalate an inquiry into this representative.

For example, an HR representative might have:

```text
represents_type = team
represents_id = hr_team
approval_policy_id = hr_escalation_policy
```

An employee-facing company representative for Alice might have:

```text
represents_type = user
represents_id = alice
```

## Resources

Protected resources should be referenced uniformly:

```text
resource_ref
- type                    knowledge | envelope | task | doc | message |
                          escalation_policy | escalation_rule | agent_profile
- id
```

The first resources that need this model are inquiry envelopes and knowledge
objects used as evidence.

## Actions

The first action set should be:

```text
read
write
answer
cite
manage
```

Action meanings:

- `read`: The principal can read the resource content.
- `write`: The principal can create or update resource content, including
  replies.
- `answer`: The principal can use the resource to form an answer, but does not
  necessarily have permission to quote or expose the original content.
- `cite`: The principal can disclose the resource as evidence to the inquiry
  recipient, subject to boundary and scope checks.
- `manage`: The principal can mutate or administer the resource or its grants.

Evidence must be checked separately for `cite` and `answer`. A resource may be
usable for reasoning but not shareable as evidence.

## Grant Model

Grants express who can do what with a resource. Access grants are the single
sharing primitive for both business resources and configuration resources. Do
not introduce a separate `ScopeControl` table for policy or rule visibility in
the first version.

The first version can omit boundaries from `access_grant`. Team, conversation,
and public grants can rely on the current membership semantics of their subject.
Boundary fields can be added later if explicit user grants need to be scoped to
a company, team root, or conversation.

```text
access_grant
- grant_id
- resource_type
- resource_id
- subject_type            user | team | team_root | representative_agent | conversation | public
- subject_id              user_id, team_id, representative_agent_id, conversation_id, or empty
- action                  read | write | answer | cite | manage
- created_by_user_id
- created_at
- expires_at
- revoked_at
```

Important rule:

```text
A grant is not a permanent pass. It is valid only while its subject still
matches the principal. For example, a team grant follows current team
membership, and a conversation grant follows conversation membership and history
policy.
```

Examples:

```text
knowledge:k1 answer user:alice
knowledge:k2 cite team:infra
knowledge:k3 answer conversation:conv_123
knowledge:k4 read public:*
representative_agent:hr_team write team:acme_employees
escalation_policy:hr_policy read team_root:acme
escalation_policy:hr_policy write team:hr
escalation_policy:hr_policy manage team:hr
```

In the example above, the company/root team can read and reuse the HR policy,
while the HR team can write and manage it. This is represented with multiple
grants, not a separate visibility preset.

## Future Boundaries

Boundaries are a future extension for scoping explicit grants. They are not
required for the first access-grant version. If added later, boundaries validate
whether a grant is still effective for the principal.

```text
public
- Always valid.

personal:user_id
- Valid for the owner and explicitly granted private shares that have not been
  revoked.

team_root:root_team_id
- Valid for current members of the root team, workspace, company, or independent
  team tree.

team:team_id
- Valid for current members of that team.

conversation:conversation_id
- Valid for current conversation members, subject to conversation history
  policy.
```

Even without boundary fields on grants, the first implementation can treat
companies, workspaces, side projects, and independent groups as team roots. A
company is a root team with company semantics, not a separate authorization
primitive.

## Personal, Work, And Life Contexts

This model does not make `work` and `life` first-class authorization types.
They should be mapped onto personal, user-pair, team-root, and team boundaries:

```text
life / personal context
- personal:user_id boundary
- user-pair scopes for one-to-one private relationships
- personal agents can use this context
- representative agents cannot use it unless a sanitized resource is explicitly
  granted to their scope

work context
- team_root or team boundary
- company, workspace, independent project, and side project can all be root
  teams
- team representatives can use work resources only through grants and current
  membership
```

Cross-context access should be explicit. A personal/life agent should not
directly reveal private context to a work representative, and a work
representative should not directly query personal context. Instead, use a
sanitized handoff, inquiry, or escalation.

Example:

```text
The user's life agent knows the user is sick.
The work-facing representative only receives:
"The user is unwell and requests sick leave today."
```

The bridge transfers the minimum necessary disclosure, not raw private context.

## Team Relationship Model

Teams should form a forest, not a single global tree. A root team can represent
a company, workspace, independent project, community, family group, or any other
collaboration boundary.

Recommended team fields:

```text
team
- team_id
- parent_team_id
- root_team_id
- name
- status
```

`root_team_id` should be stored directly. It makes grant checks cheaper and
keeps the first authorization path simple.

Team relationships can include hierarchy and collaboration:

```text
team_relations
- from_team_id
- to_team_id
- relation_type          parent | partner | shared_project | alias
- status                 active | archived
```

Access should not be inferred from team relationships alone:

- Parent-child membership does not automatically grant access to every child
  team resource.
- Overlapping membership does not merge knowledge scopes.
- Partner or shared-project relationships should require explicit grants.
- Inheritance, if enabled, should be an explicit policy on grants or team
  relation records.

The safe default is:

```text
team relationships help routing and suggested grants;
access still requires a grant plus valid subject membership.
```

## Team Membership Semantics

Team grants are grants to the team, not snapshots of members at grant creation
time.

```text
team grant = current members can access
```

If user A was previously in a team and later leaves:

- A no longer receives team-grant access to historical team-only knowledge.
- A does not receive access to knowledge created after leaving.
- A may retain access only through another valid grant, such as a public grant
  or an explicit user grant that has not expired or been revoked. If future
  boundary fields are added, they can also make explicit user grants expire when
  the user leaves a company, team root, or conversation.

Contributor identity is separate from access:

```text
created_by_user_id = A
```

does not imply permanent read access for A.

## Conversation Semantics

Direct private chats and group chats should both use a conversation audience.
Group conversations should not be modeled as a fixed list of user grants:

```text
grant resource -> conversation:conv_123
```

Recommended conversation fields:

```text
conversation
- conversation_id
- conversation_type       direct | group | escalation_thread
- boundary_type           personal | team_root | team | public
- boundary_id
- history_policy          full_history | from_join | none | admin_approved
- status                  active | archived
```

Conversation membership should track join and leave times:

```text
conversation_member
- conversation_id
- user_id
- role                    owner | admin | member
- joined_at
- left_at
```

Conversation history policy controls whether new members can see previous
messages and derived knowledge:

```text
full_history
from_join
none
admin_approved
```

Conversation access should be evaluated by combining:

- The resource grant to `conversation:conversation_id`.
- Current or historical conversation membership.
- The conversation boundary, such as a team root or team.
- The conversation history policy.

For example, a `from_join` conversation can allow a new member to read new
messages while denying old messages and knowledge derived only from old
messages. A `full_history` conversation can grant the new member historical
access once they join. An `admin_approved` conversation should require an
explicit approval before exposing history.

## Inquiry And Escalation

Representative agents cannot directly query outside their scope. If answering
an inquiry requires a narrower or more privileged scope, the representative
should create a child inquiry or escalation.

Example HR flow:

```text
1. Employee asks an HR person's company representative about sensitive HR data.
2. The company representative detects that HR-scoped data is required.
3. It creates a child inquiry to the HR team representative.
4. The HR team representative checks HR-scoped data and policy.
5. The HR team representative may require human approval.
6. It returns a sanitized answer to the company representative.
7. The company representative replies to the original employee.
```

Rules:

- Every cross-boundary data request should be explicit escalation.
- Escalation does not always require human approval.
- Human approval is required for sensitive data, policy commitments, legal,
  HR, security, financial, or other high-risk answers.
- Returned answers and evidence should receive grants appropriate to the final
  audience, often a user pair within a company boundary.

## Escalation Authorization

Escalation is a write to another representative's intake path. A representative
agent that cannot answer with its current grants does not automatically have
permission to create a child inquiry for a narrower or more privileged
audience.

The target representative or team should expose an inbound escalation policy
through grants:

```text
resource: representative_agent:hr_team
action: write
subject: team:acme_employees
```

This means current members of the Acme employees team may request escalation to
the HR team representative. It does not mean the HR team representative must
answer automatically.

The escalation check should validate:

- The requester can ask in the current inquiry audience.
- The source representative can create child inquiries for that audience.
- The target representative accepts `write` from the requester or the
  requester's team.
- The requested purpose is allowed for that target.
- The requester still satisfies membership requirements, such as current company
  or team membership.

Sensitive targets such as HR, security, legal, finance, and customer support
should usually accept escalation requests from broad internal audiences, but
they should reserve the right to require human approval before returning data.

Inbound escalation rules should be maintained by the target representative's
provider. For user-provided representatives, the user maintains the rules. For
team-provided representatives, team owners or a team routine maintain the rules.

Recommended policy shape:

```text
escalation_approval_policy
- policy_id
- maintained_by_type       user | team
- maintained_by_id
- status                   active | paused
- created_at
- updated_at

escalation_rule
- rule_id
- policy_id
- subject_type             user | team | public
- subject_id
- purpose
- requested_action         answer | verify | route | approve
- decision                 auto_accept | needs_approval | deny | needs_clarification
- approver_type            user | team_routine | representative_agent
- approver_id
- priority
- status                   active | paused
```

Team routines are named team-owned intake paths, not individual users:

```text
team_routines
- routine_id
- team_id
- name                     HR intake | Security review | Legal approval
- status                   active | paused
```

The rule decision means:

- `auto_accept`: The target representative can process the escalation without a
  human approval step, while still applying resource grants and boundaries.
- `needs_approval`: The escalation must be routed to the configured approver.
- `deny`: The target representative rejects the escalation.
- `needs_clarification`: The target representative asks the source requester
  for more information before deciding.

The policy should match on requester, purpose, requested action, subject user if
present, and sensitivity. A broad `write` grant only allows the source to knock
on the target door. The approval policy decides whether the door opens, who
reviews it, and what can be returned.

Useful escalation request fields:

```text
escalation_request
- parent_envelope_id
- target_representative_agent_id
- requester_user_id
- subject_user_id
- purpose
- requested_action           answer | verify | route | approve
- sensitivity                low | medium | high
- approval_policy_id
- approval_rule_id
- approver_type
- approver_id
- justification
```

The target representative should return one of:

```text
accepted
declined
needs_human_approval
needs_clarification
```

Returned answers should be sanitized and granted only to the final audience
that is allowed to see them.

## Inquiry Evidence

Inquiry responses should carry evidence when useful, but evidence must be
permission checked.

Recommended response fields:

```text
inquiry_response
- outcome                 answered | declined | needs_clarification | partial
- answer
- selected_ids
- clarifying_questions
- evidence
- confidence              low | medium | high
- answer_mode             auto | draft_approved | human | representative_auto
```

Evidence should distinguish between:

- shareable evidence: The answer can cite or expose it.
- private review evidence: It can help a user's personal agent draft an answer,
  but should not be sent to the requester.
- sensitive evidence: It requires approval or should not be used in an
  automated representative answer.

## Access Check

The authorization check should conceptually look like:

```text
Can(principal, action, resource)
```

Pseudocode:

```text
func Can(principal, action, resource):
    grants = FindGrants(resource, action)

    for grant in grants:
        if grant.revoked_at is set or grant is expired:
            continue
        if !subjectMatches(principal, grant.subject):
            continue
        # Optional future extension:
        # if grant has a boundary and !boundaryAllows(principal, grant.boundary):
        #     continue
        if !representativeScopeAllows(principal, grant):
            continue
        return true

    return false
```

`subjectMatches` handles direct users, team membership, conversation
membership, public grants, and representative-agent grants.

`boundaryAllows` is an optional future extension for explicit grant boundaries.
The first version can rely on `subjectMatches` for team, team-root,
conversation, public, and direct-user grants.

`representativeScopeAllows` prevents representative sessions from using grants
outside their scope, even if the backing runtime or owner would otherwise have
more access.

## Product Notes

- "Representative agent" is the user-facing concept.
- The backing implementation may use a shared runtime or a dedicated runtime.
- Personal agents are private/internal and should not be externally inquiryable
  by default.
- Representative agents are externally inquiryable, but only inside their
  configured scope.
- Scope changes should create a new representative agent or explicit escalation,
  not silently mutate an active session.

## Locked Data Structures

This section fixes the first implementation shape. Names in this section use
singular entity names; a physical database can still follow the repository's
existing table naming convention.

```text
agent_profile        virtual agent, outward identity, persona, and policy
agent_runtime        executable backing runtime; current agents table is legacy runtime
representative_agent deployed outward identity with fixed profile and runtime
agent_session        auditable runtime execution principal
access_grant         single sharing primitive for resources and config
```

### MVP Entities

`agent_profile` is reusable persona/config. It may exist without a runtime and
does not by itself mean the system can answer externally.

```text
agent_profile
- profile_id
- owner_type              user | team
- owner_id                user_id or team_id
- display_name
- description
- card_json               default {}
- instructions_md
- default_model
- tool_policy_json        allowlist/denylist and limits; default {}
- metadata_json           product/UI details; default {}
- status                  active | paused | archived
- created_by_user_id
- created_at
- updated_at
- archived_at
```

`description` should cover the profile's purpose. Keep that intent in one field
unless the product later needs a separate structured category.

`agent_runtime` is the executable backing runtime. The current `agents` table is
mostly an agent runtime already because it stores node, host, api key,
heartbeat, and transport status. Keep the physical table name during migration
if that is safer, but treat it as runtime in the domain model.

```text
agent_runtime
- runtime_id
- node_id                 nullable references nodes(node_id)
- owner_user_id
- runtime_type            codex | claude | hermes | openclaw | custom
- hostname
- machine_type
- os
- api_endpoint
- api_key_hash
- last_heartbeat
- status                  active | paused | archived
- registered_at
- created_at
- updated_at
- metadata_json           default {}
```

`representative_agent` is the deployed outward identity. It fixes one profile,
one runtime, who it represents, and the escalation policy used when another
party asks it to answer.

```text
representative_agent
- representative_agent_id
- profile_id              references agent_profile(profile_id)
- runtime_agent_id        references legacy agents(agent_id)
- represents_type         user | team
- represents_id           user_id or team_id shown as the represented subject
- approval_policy_id      nullable
- status                  active | paused | archived
- created_by_user_id
- created_at
- updated_at
- archived_at
```

Examples:

```text
agent_profile:p1 read team:acme
agent_profile:p1 write user:alice
agent_runtime:rt1 read agent_profile:p1
representative_agent:r1 write team:acme
knowledge:k1 answer representative_agent:r1
knowledge:k1 cite representative_agent:r1
representative_agent:hr write team:acme
```

`agent_session` should remain the execution record, but it needs principal
metadata so access checks can resolve whether the session is acting as a
personal agent, representative agent, or worker.

```text
agent_session add
- conversation_id         nullable references conversation(conversation_id)
- profile_id              nullable references agent_profile(profile_id)
- runtime_agent_id        existing agent_id column, references legacy agents(agent_id)
- representative_agent_id nullable references representative_agent(representative_agent_id)
- created_by_user_id
```

These fields are not a participant list. They identify the principal used for
authorization and audit. Existing session fields already capture runtime state
such as agent, native session, project, status, usage, model, and metadata.

Use `agent_session_participant` to record every subject involved in the session.
This is for audit, display, and later notification/routing logic.

```text
agent_session_participant
- session_id
- participant_type        user | team | agent_profile | representative_agent |
                          agent_runtime | node
- participant_id
- role                    requester | represented_subject | runtime |
                          operator | approver | observer
- added_at
- left_at
```

`access_grant` is the only long-lived access sharing table. Store one action per
row so revocation, expiration, and indexing stay simple.

```text
access_grant
- grant_id
- resource_type           knowledge | envelope | inquiry | inquiry_response |
                          task | doc | message | escalation_policy |
                          escalation_rule | agent_profile |
                          representative_agent | agent_runtime | conversation
- resource_id
- subject_type            user | team | team_root |
                          representative_agent | conversation | public
- subject_id              empty for public
- action                  read | write | answer | cite | manage
- created_by_user_id
- created_at
- expires_at
- revoked_at
- revoked_by_user_id
- revocation_reason
- metadata_json           default {}
```

Use a small MVP action set:

```text
read
write
answer
cite
manage
```

Action meanings:

- `read`: read resource content or view configuration.
- `write`: create or update resource content, including replies.
- `answer`: use the resource to answer without exposing the source.
- `cite`: expose the resource as evidence.
- `manage`: administer the resource, including grants.

`reply`, `view`, and `edit` collapse into `write/read/write` for the first
version. `escalate` can be represented as `write` on the target
`representative_agent` or on an escalation resource when that feature lands.

Recommended indexes:

```text
access_grant(resource_type, resource_id, action, revoked_at, expires_at)
access_grant(subject_type, subject_id, action, revoked_at, expires_at)
unique active grant:
  resource_type, resource_id, subject_type, subject_id, action
  where revoked_at is null
```

`team` should become a forest. Store `root_team_id` directly for cheap access
checks and validate it in application code when parents change.

```text
team add
- parent_team_id          nullable references teams(team_id)
- root_team_id            references teams(team_id)
```

Do not add `team_type` in the first version. Nothing breaks without it because
authorization uses membership and grants, not team category. If product copy
later needs "company" or "project", put that in metadata or add a typed field
after there is behavior attached to it.

`conversation` is the stable audience identity for an actual chat thread. It is
not a pre-created relationship row for every possible pair of users or agents.
Create it lazily when messages exist, and create another conversation when the
user starts a separate topic or thread.

```text
conversation
- conversation_id
- conversation_type       direct | group | agent_thread | escalation_thread
- boundary_type           personal | team_root | team | public
- boundary_id             empty for personal/public
- history_policy          full_history | from_join | none | admin_approved
- status                  active | archived
- created_at
- archived_at
```

```text
conversation_member
- conversation_id
- user_id
- role                    owner | admin | member
- joined_at
- left_at
```

`conversation_member` is human membership only. Do not add a
`representative_agent` as a long-lived conversation member, because that would
let one agent invocation inherit the whole long-running conversation history.

For `conversation_type = direct`, application code should keep active human
membership to two users. A friend record can create, archive, or block a direct
conversation, but authorization still depends on active grants and conversation
membership.

For a long-running human-agent chat, use `conversation_type = agent_thread` and
bind the agent to that specific thread. Multiple `agent_session` rows may attach
to the same conversation over time.

```text
conversation_agent_binding
- binding_id
- conversation_id
- representative_agent_id references representative_agent(representative_agent_id)
- relationship_type       assistant | representative | reviewer | participant
- added_by_user_id
- access_mode             thread_history | from_binding | selected_messages
- status                  active | archived
- created_at
- archived_at
```

When a human asks an agent to help inside a human-human or group conversation,
record a bounded delegation instead of adding the agent as a member:

```text
conversation_agent_invocation
- invocation_id
- conversation_id
- representative_agent_id references representative_agent(representative_agent_id)
- session_id              nullable references agent_session(session_id)
- requested_by_user_id
- access_mode             selected_messages | current_turn |
                          since_invited | full_history_approved
- selected_message_ids_json default []
- status                  active | completed | revoked
- created_at
- expires_at
- revoked_at
```

Agent access should be evaluated as:

```text
human conversation membership
+ active conversation_agent_binding for agent_thread conversations
+ active conversation_agent_invocation from that human or an admin
+ access_grant for the selected messages, turn, or approved history
+ representative_agent profile tool_policy_json
```

This keeps humans as the highest-authority members of the conversation. An agent
can be bound to its own human-agent thread, or asked for help in another
conversation, but it does not automatically become a human member with the same
historical access as the humans.

Conversation count should be proportional to actual threads, not possible
relationships. A user and an agent can have many sessions under one
conversation, or multiple conversations split by topic. Store messages once and
derive participant views from `conversation_member`, `conversation_agent_binding`,
and `conversation_agent_invocation` rather than duplicating the session for each
side.

Private chat examples:

```text
message:m1 read conversation:conv_direct_123
knowledge:k1 answer conversation:conv_direct_123
representative_agent:alice_rep write conversation:conv_direct_123
```

Group chat examples:

```text
message:m2 read conversation:conv_group_456
knowledge:k2 answer conversation:conv_group_456
representative_agent:team_rep write conversation:conv_group_456
```

Human-agent thread example:

```text
conversation:conv_agent_789 type agent_thread
conversation_member:conv_agent_789 user:alice
conversation_agent_binding:conv_agent_789 representative_agent:alice_assistant
agent_session:s1 conversation:conv_agent_789
agent_session:s2 conversation:conv_agent_789
```

### Inquiry Entities

Inquiry can still be carried through envelopes, but the durable inquiry state
should be normalized so escalation and response grants do not depend on parsing
payload JSON.

```text
inquiry
- inquiry_id
- envelope_id             nullable references envelopes(envelope_id)
- parent_inquiry_id       nullable references inquiry(inquiry_id)
- requester_user_id
- subject_user_id         nullable
- source_representative_agent_id nullable references representative_agent(representative_agent_id)
- target_representative_agent_id references representative_agent(representative_agent_id)
- purpose
- question
- sensitivity             low | medium | high
- status                  pending | accepted | declined |
                          needs_clarification | answered | archived
- created_at
- updated_at
- resolved_at
```

```text
inquiry_response
- response_id
- inquiry_id
- representative_agent_id references representative_agent(representative_agent_id)
- outcome                 answered | declined | needs_clarification | partial
- answer_md
- selected_ids_json       default []
- clarifying_questions_json default []
- confidence              low | medium | high
- answer_mode             auto | draft_approved | human | representative_auto
- approved_by_user_id     nullable
- created_at
```

```text
inquiry_response_evidence
- evidence_id
- response_id
- resource_type
- resource_id
- evidence_role           shareable | private_review | sensitive
- action_used             answer | cite
- citation_label
- summary
- created_at
```

If `evidence_role = shareable`, the responder must pass `cite`. If
`evidence_role = private_review`, the responder only needs `answer`, and
the evidence must not be sent to the requester.

### Escalation Entities

These entities are part of the fixed model, but can be implemented after the MVP
if the first inquiry flow only answers within one audience.

```text
escalation_approval_policy
- policy_id
- maintained_by_type       user | team
- maintained_by_id
- status                   active | paused | archived
- created_at
- updated_at
```

```text
escalation_rule
- rule_id
- policy_id
- subject_type             user | team | team_root | conversation | public
- subject_id
- purpose
- requested_action         answer | verify | route | approve
- sensitivity              low | medium | high | any
- decision                 auto_accept | needs_approval | deny |
                           needs_clarification
- approver_type            user | team_routine | representative_agent
- approver_id
- priority
- status                   active | paused | archived
```

```text
team_routine
- routine_id
- team_id
- name
- description
- status                   active | paused | archived
- created_at
- updated_at
```

```text
escalation_request
- escalation_request_id
- parent_inquiry_id
- target_representative_agent_id
- requester_user_id
- subject_user_id
- purpose
- requested_action         answer | verify | route | approve
- sensitivity              low | medium | high
- approval_policy_id
- approval_rule_id
- approver_type
- approver_id
- justification
- status                   pending | accepted | declined |
                           needs_human_approval |
                           needs_clarification | answered | archived
- created_at
- updated_at
- resolved_at
```

### Implementation Order

1. Add `agent_profile`, `representative_agent`, `access_grant`, `conversation`,
   `conversation_member`, `conversation_agent_binding`,
   `conversation_agent_invocation`, the `agent_session` actor fields, and
   `agent_session_participant`.
2. Add team forest fields to `team`.
3. Add normalized `inquiry`, `inquiry_response`, and
   `inquiry_response_evidence`.
4. Implement `Can(principal, action, resource)` against `access_grant`.
5. Add escalation policy/request entities when cross-boundary inquiry is
   enabled.

## Related Models

This draft is inspired by:

- Google Zanzibar and OpenFGA relationship tuples.
- AWS IAM and Cedar request evaluation concepts.
- RBAC for team roles and ABAC for current membership and boundary checks.

The first implementation should stay much smaller than these systems. The
important reusable shape is:

```text
principal + action + resource + grants + representative scope
```

## Open Questions

- Which actions should be required for each inquiry response field?
