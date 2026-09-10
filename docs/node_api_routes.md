# Public Node API route matrix

Generated from `internal/manager/nodepolicy/routes.json`.
Regenerate with `python3 deploy/node-public/check.py --write-matrix`.
See [the rollout guide](node_public_rollout.md) for deployment boundaries.

These are the reviewed current machine routes. Auth and owner columns describe
the required boundary, not proof that every cross-tenant case has been audited.
Rate classes are planning metadata for KEV-37/38/39; per-class or distributed
limits are not implemented by this manifest. The existing shared in-memory
API limiter still applies, with a separate registration-start/poll bucket.

| Method | Path | Authentication | Owner boundary | Rate class | Expected caller |
|---|---|---|---|---|---|
| POST | `/api/v1/node/registration/start` | anonymous | Creates pending registration only; browser approval required | bootstrap-start | paxd onboarding |
| POST | `/api/v1/node/registration/poll` | poll-token | Registration ID and hashed poll token; approval and one-time consumption | bootstrap-poll | paxd onboarding |
| POST | `/api/v1/node/register` | registration-token | Owner resolved from registration token | registration | paxd token onboarding |
| POST | `/api/v1/node/agents/register` | node-key-or-registration-token | Authenticated node owner; new node owner from registration token | registration | paxd agent registration |
| POST | `/api/v1/node/status` | node-key | Authenticated node; reported agents must belong to that node | status | paxd status reporter |
| POST | `/api/v1/node/agents/:agent_id/sessions` | node-key | Authenticated node and node-owned agent | status | paxd session reporter |
| GET | `/api/v1/node/mailbox` | node-key | Authenticated node | mailbox | paxd HTTP mailbox fallback |
| GET | `/api/v1/node/agents/:agent_id/mailbox` | node-key | Authenticated node and node-owned agent | mailbox | paxd HTTP mailbox fallback |
| GET | `/api/v1/node/agents/:agent_id/sessions/:session_id/mailbox` | node-key | Authenticated node, node-owned agent and session | mailbox | paxd HTTP mailbox fallback |
| POST | `/api/v1/node/messages/offset` | node-key | Authenticated node mailbox | mailbox-write | paxd HTTP message delivery |
| POST | `/api/v1/node/messages/:message_id/result` | node-key | Message belongs to authenticated node | mailbox-write | paxd HTTP message delivery |
| POST | `/api/v1/node/messages/:message_id/delivered` | node-key | Message belongs to authenticated node | mailbox-write | paxd HTTP message delivery |
| POST | `/api/v1/node/messages/outbound` | node-key | Source agent/session belongs to authenticated node | mailbox-write | paxd HTTP message delivery |
| POST | `/api/v1/node/secrets/resolve` | node-key | Authenticated node owner and requested agent/session scope | secrets | paxd secret resolution |
| POST | `/api/v1/node/secrets/:secret_id/versions` | node-key | Secret belongs to authenticated node owner | secrets | paxd secret writer |
| POST | `/api/v1/node/agents/:agent_id/approvals` | node-key | Authenticated node, node-owned agent and session | approvals | paxd approval integration |
| GET | `/api/v1/node/agents/:agent_id/approvals/:approval_id` | node-key | Approval belongs to authenticated node and agent | approvals | paxd approval polling |
| GET | `/api/v1/node/agents` | node-key | Calling agent belongs to node; results may include authorized owner/team/friend targets | discovery | paxd A2A discovery |
| POST | `/api/v1/node/conversation/deliver` | node-key | Source belongs to node; target authorized by conversation delivery policy | conversation | paxd A2A delivery |
| POST | `/api/v1/node/agents/:agent_id/conversations` | node-key | Source agent belongs to node; target authorized by conversation policy | conversation | agent conversation client |
| GET | `/api/v1/node/agents/:agent_id/e2ee/pairings/:pairing_id` | node-key | Pairing belongs to node-owned agent | e2ee | paxd E2EE pairing |
| POST | `/api/v1/node/agents/:agent_id/e2ee/pairings/:pairing_id/package` | node-key | Pairing belongs to node-owned agent | e2ee | paxd E2EE pairing |
| PUT | `/api/v1/node/artifact-publications/:publication_id` | node-key | Publication/upload and associated agent/session belong to node | artifacts | paxd artifact publisher |
| POST | `/api/v1/node/artifact-publications/:publication_id/prepare` | node-key | Publication/upload and associated agent/session belong to node | artifacts | paxd artifact publisher |
| POST | `/api/v1/node/artifact-publications/:publication_id/failed` | node-key | Publication/upload and associated agent/session belong to node | artifacts | paxd artifact publisher |
| POST | `/api/v1/node/artifact-uploads/:upload_id/complete` | node-key | Publication/upload and associated agent/session belong to node | artifacts | paxd artifact publisher |
| GET | `/api/v1/node/control` | node-key | Query node ID must match authenticated node; runtime fence | tunnel-handshake | paxd control WebSocket |
| GET | `/api/v1/agent/tunnel` | node-key-or-agent-key | Agent belongs to authenticated node, or agent key matches requested agent | tunnel-handshake | paxd ACP WebSocket |
