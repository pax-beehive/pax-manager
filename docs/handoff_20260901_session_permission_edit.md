# Session permission editing handoff

## Contract

`POST /api/v1/user/{user_id}/nodes/{node_id}/agents/{agent_id}/sessions/{session_id}/permission`

```json
{"permission_choice_id":"agent:legacy-mode:accept_edits"}
```

The endpoint is for an existing durable plaintext ACP session. It authorizes
the exact user, node, agent, and session binding; resolves the requested choice
against the current runtime identity and unexpired permission observation;
claims the session ACP tunnel; applies `session/set_mode` or
`session/set_config_option`; and only then persists the effective PAX config.

PAX auto-approve updates the PAX approval policy without issuing a native ACP
configuration request. Mixed pools, stale observations, unknown choices,
missing native session bindings, and unavailable tunnels fail without
persisting a false permission selection.

## Verification

- Permission choice resolution is covered against a current observed catalog.
- ACP method selection remains covered for config-option and legacy-mode
  bindings, including propagation of ACP failures.
- Storage retains `permission_choice_id` when an effective native selection is
  supplied and clears it for legacy approval-only PATCH updates.
