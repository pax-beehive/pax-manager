# paxd Device Onboarding Design

## Summary

paxd should support an interactive onboarding flow for users who do not already
have a node registration token. The flow should look like a standard device-code
login:

1. The user runs `paxd connect --cloud-url <url>`.
2. paxd prints a verification URL and short code.
3. The user opens the URL, logs in, enters the code, and approves the node.
4. paxd polls until approval, then registers the node and stores the node API
   key locally.
5. Optional `--run` starts the daemon immediately after successful registration.

This flow is an onboarding wrapper around the existing node registration path.
It should not introduce a second long-lived node credential type.

## Goals

- Remove the need for users to manually mint and copy a node registration token.
- Keep browser authentication user-owned through Cloudflare Access or local user
  auth.
- Keep the node API key local to paxd; do not show it in the browser.
- Reuse existing `/api/v1/node/register` behavior for final node creation.
- Keep the flow auditable and revocable.

## Proposed Flow

### 1. Start Device Onboarding

```http
POST /api/v1/device/node-login/start
Content-Type: application/json

{
  "hostname": "workstation.local",
  "machine_type": "mac",
  "os": "darwin",
  "arch": "arm64",
  "paxd_version": "0.1.0"
}
```

Response:

```json
{
  "device_code": "secret-polling-token",
  "user_code": "ABCD-EFGH",
  "verification_uri": "https://pax.example.com/device",
  "verification_uri_complete": "https://pax.example.com/device?code=ABCD-EFGH",
  "expires_in": 600,
  "interval": 2
}
```

`device_code` is a secret held by paxd for polling. `user_code` is the short
code the browser user enters.

### 2. User Approval

The user opens the verification URL and logs in. The page asks for the short
code, shows the requested node metadata, and asks the user to approve or deny
the connection.

On approval, pax-manager creates or attaches a one-time node registration token
owned by the logged-in user.

### 3. Poll For Result

```http
POST /api/v1/device/node-login/poll
Content-Type: application/json

{
  "device_code": "secret-polling-token"
}
```

Possible statuses:

```text
authorization_pending
slow_down
expired
denied
approved
```

Approved response:

```json
{
  "status": "approved",
  "registration_token": "reg_..."
}
```

The registration token is one-time use and should be consumed by the existing
node registration endpoint.

### 4. Register Node

paxd calls the existing endpoint:

```http
POST /api/v1/node/register
X-Registration-Token: reg_...
Content-Type: application/json

{
  "hostname": "workstation.local",
  "machine_type": "mac",
  "os": "darwin",
  "arch": "arm64",
  "paxd_version": "0.1.0"
}
```

Response:

```json
{
  "node_id": "node_...",
  "api_key": "paxn_..."
}
```

paxd writes the returned node identity and API key into local config and SQLite,
then exits or starts `run` when `--run` was supplied.

## CLI Shape

```bash
paxd connect --cloud-url https://pax.example.com
paxd connect --cloud-url https://pax.example.com --run
```

The terminal output should include the URL and code, and may try to open the
browser as a convenience. The command should continue to work without browser
auto-open.

## Security Notes

- Device sessions expire quickly, for example after 10 minutes.
- Polling must be rate limited. `slow_down` tells paxd to increase the interval.
- A device session is single-use after approval, denial, expiry, or successful
  registration token consumption.
- The browser should never display or receive the long-lived node API key.
- The node API key remains a local machine credential stored by paxd.
- All approval and registration events should be auditable by owner user,
  hostname, user agent, and timestamp.

## Open Questions

- Should the browser approval create a new node immediately, or only mint a
  one-time registration token for paxd to consume?
- Should `paxd connect --run` install or update a service, or only run in the
  current terminal?
- Should users be able to name the node during browser approval?
- Should device onboarding support selecting an existing node to rotate the
  local credential, or only create new nodes?
