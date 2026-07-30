# Logical Projects And Reusable Workspace Targets

## Purpose

A logical Project groups work independently from a repository checkout. A
Project can have nested child Projects and multiple reusable workspace Targets.
Each Target identifies the agent and working-directory intent used to start a
session.

This model deliberately separates three concerns:

- `projects` owns user-visible hierarchy and archive state.
- `project_targets` owns reusable agent/workspace launch settings.
- `agent_sessions.primary_project_id` records the single Project that supplied
  the session's primary context.

Only `projects` and `project_targets` are new tables. `agent_sessions` is an
existing table with the renamed `primary_project_id` column.

## Persistence

```txt
projects
  project_id
  owner_user_id
  display_name
  parent_project_id
  archived_at
  created_at
  updated_at

project_targets
  target_id
  project_id
  agent_id
  display_name
  cwd
  is_default
  enabled
  created_at
  updated_at

agent_sessions
  ...
  primary_project_id
```

The schema intentionally has no database foreign keys between these records.
Ownership and relationship validation happen in the service/storage boundary.
Indexes support owner-scoped Project listing, hierarchy traversal,
Project-target listing, session filtering by primary Project, and at most one
enabled default Target per Project.

Project archive is soft deletion. Archived Projects are hidden from normal
lists but their Targets and historical Sessions remain stored. A Project can
have multiple Targets for the same Agent when their `cwd` values differ.
Disabling a default Target also clears its default state.

`cwd` is stored as user intent. PAX Manager does not expand `~` or require the
path to exist on the manager host. paxd expands and validates the path on the
selected Agent's machine.

## Session Semantics

`primary_project_id` is optional, so projectless Sessions remain valid.

When the existing Conversation endpoint receives `primary_project_id` and
`project_target_id` on a new conversation, it validates the owner, active
Project, enabled Target, and route Agent. It derives `cwd` from the Target,
executes ACP `session/new`, and only then persists the PAX Session with the
requested Project as `primary_project_id`. After creation, the primary Project
is immutable. Periodic Agent reports may update runtime/session projection
fields but cannot overwrite it.

This field is intentionally singular. Future multi-Project classification,
including secretary-agent labels, belongs in a separate many-to-many
association and does not change the primary launch context.

## User API

All routes are owner-scoped by the authenticated `{user_id}` (normally
`self`):

| Method | Path | Behavior |
| --- | --- | --- |
| `POST` | `/api/v1/user/{user_id}/projects` | Create a root or child Project. |
| `GET` | `/api/v1/user/{user_id}/projects` | List active Projects; `include_archived=true` includes archived rows. |
| `GET` | `/api/v1/user/{user_id}/projects/{project_id}` | Get one Project. |
| `PATCH` | `/api/v1/user/{user_id}/projects/{project_id}` | Rename or move a Project; cycles are rejected. |
| `POST` | `/api/v1/user/{user_id}/projects/{project_id}/archive` | Soft-archive a Project. |
| `POST` | `/api/v1/user/{user_id}/projects/{project_id}/targets` | Create a reusable Target. |
| `GET` | `/api/v1/user/{user_id}/projects/{project_id}/targets` | List Project Targets. |
| `GET` | `/api/v1/user/{user_id}/projects/{project_id}/targets/{target_id}` | Get one Target. |
| `PATCH` | `/api/v1/user/{user_id}/projects/{project_id}/targets/{target_id}` | Edit, enable/disable, or make a Target default. |

The flat Session list accepts
`primary_project_id={project_id}` to return the recent Sessions whose primary
context is that Project.

Session creation does not have a Project-specific REST endpoint. The existing
`POST /api/v1/user/{user_id}/nodes/{node_id}/agents/{agent_id}/conversation`
request accepts these optional creation-only fields:

```json
{
  "primary_project_id": "proj_...",
  "project_target_id": "ptgt_..."
}
```

`project_target_id` requires `primary_project_id`. When both are present the
Target's `cwd` is authoritative. Project context cannot be changed on prompts
that identify an existing `session_id`.

The Thrift source of truth is `api/pax_manager.thrift`; generated Hertz models,
routes, and OpenAPI artifacts must change with it.

## Validation Rules

- Project names and Target names must be non-empty.
- A parent Project must belong to the same owner.
- A Project cannot be its own parent or create a hierarchy cycle.
- A Target's Project and Agent must belong to the same owner.
- A disabled Target cannot be used to create a Session.
- At most one enabled Target is default within a Project.
- Archiving and Project/Target mutations are owner-scoped.
- No handler trusts a caller-supplied owner identity.
