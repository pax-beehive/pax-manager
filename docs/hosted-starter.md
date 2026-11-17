# Hosted Starter control plane

Starter gives each authenticated owner one stable logical environment and a
monthly model allowance, subject to a monthly operator budget pool. Ensure
does not allocate compute. Wake queues work; trusted pax-host
workers claim it, run the owner's paxd/Pi environment, and report real owned
node/Agent IDs before Console can open the existing session workbench.

## Enable deliberately

Hosting is disabled unless the store implements the hosting transaction interface
and its host/token/gateway service secrets, endpoints, model and valid limits
are configured. User GET
returns `data.enabled=false`; mutations return 503. Existing users/devices are
unaffected. Apply the additive `db/hosting.sql` migration through the usual deployment
workflow. Manager stores no generation-provider or moderation key. Those belong
to the independent pax-gateway service. Do not put provider, moderation, host or
gateway service keys in a browser, image, Terraform or logs. The gateway service
key must be separate from the host key and tenant-token signing secret.

```text
HOSTING_HOST_KEY=REPLACE_WITH_SEPARATE_RANDOM_32_PLUS_CHARACTER_HOST_SECRET
HOSTING_TOKEN_SECRET=REPLACE_WITH_SEPARATE_RANDOM_32_PLUS_CHARACTER_MODEL_SIGNING_SECRET
HOSTING_PUBLIC_URL=https://hosting-us.example.com
HOSTING_CLOUD_URL=https://node-us.example.com
HOSTING_GATEWAY_URL=https://model-us.example.com/v1
HOSTING_GATEWAY_KEY=REPLACE_WITH_SEPARATE_RANDOM_32_PLUS_CHARACTER_GATEWAY_SECRET
HOSTING_MODEL=REPLACE_WITH_FIXED_TEXT_CHAT_MODEL
HOSTING_GRANT_MICROUSD=5000000
HOSTING_GRANT_TOKENS=200000
HOSTING_GLOBAL_MICROUSD=0
HOSTING_INPUT_MICROUSD_PER_MILLION=REPLACE_WITH_REVIEWED_RATE
HOSTING_OUTPUT_MICROUSD_PER_MILLION=REPLACE_WITH_REVIEWED_RATE
HOSTING_MAX_OUTPUT_TOKENS=2048
HOSTING_REQUESTS_PER_MINUTE=20
HOSTING_GLOBAL_REQUESTS_PER_MINUTE=200
```

Amounts use integer microusd (1,000,000 = USD 1). The default user allowance is
USD5 per UTC calendar month and 200,000 total input/output tokens, shared across
all models and harnesses. Both limits remain configurable. Unused allowance
expires instead of accumulating, and skipped months do not accumulate grants.
Existing records without period metadata retain their current balances until
the next month; a restart or configuration edit does not grant more current-month
credit. Cross-month in-flight calls settle against the old allowance before the
next allowance becomes available (normally within the three-minute orphan bound).

`HOSTING_GLOBAL_MICROUSD` is the platform ceiling for each UTC calendar month,
including in-flight reserved money. It defaults to0 (paused). Set the desired
monthly amount explicitly before deployment; for example, `100000000` is USD100
per month. Unused headroom expires, and skipped months do not accumulate money.
Only a new UTC month restores the monthly allowance; restarts and new signups
do not. Exhaustion
blocks new free model calls, including configured zero-price routes; setting the
ceiling to0 pauses the pool without disabling its account/lease service. Within
the current month only an explicit operator increase adds headroom. Released
unused holds are not a top-up. At a month boundary, all prior reservations must
settle/cancel/reconcile before new-month admission resumes; this normally defers
the rollover by at most the three-minute orphan window.
There is no automatic payment, supplier-wallet refill or public top-up API.
Malformed, negative or overflowing budget environment values reject startup.

The global cap is configured model-accounting admission, not an AWS billing alert. Set rates
to reviewed upper prices for the exact supplier/model; cached input is conservatively
priced at the configured input rate. Changing a rate does not retroactively change
an admitted request's recorded model/rates.

`HOSTING_PUBLIC_URL` must be a direct regional hosting origin. Its ingress must
allow exactly these POST routes to reach Manager, with the endpoint's own bearer
authentication, without requiring browser cookies or a Cloudflare browser login:

```text
/api/v1/hosting/jobs/claim
/api/v1/hosting/jobs/heartbeat
/api/v1/hosting/jobs/complete
```

The independent gateway additionally calls exactly
`POST /api/v1/hosting/gateway/accounting` with the separate
`HOSTING_GATEWAY_KEY`. Restrict this route to the gateway service in ingress
where practical; handler bearer authentication remains mandatory. Do not add it
to browser/native-user routing. The old Manager model-generation route returns
410 `model_gateway_moved` and never falls back to a provider.

Do not point a worker at a global node-routing origin that expects an existing
node key, or assume a new registration token is such a key. `HOSTING_CLOUD_URL`
must be the corresponding direct regional node API origin accepting the existing
owner-bound registration-token flow. Deployment/Cloudflare origin provisioning is
an operator step; adding Manager handlers alone does not open an external ingress.

## User API

All successful control calls use the existing envelope
`{code:200,message:"ok",data:...}`. Errors use the HTTP status in `code`. All
responses containing Starter/credentials are no-store.

| Method and path | Behavior |
| --- | --- |
| GET /api/v1/starter | Read authenticated owner's Starter or null, without granting |
| POST /api/v1/starter | Idempotently ensure owner's Starter and grant, without compute |
| POST /api/v1/starter/wake | Ensure and queue an owned environment after checking budget |

The owner always comes from existing authentication. Request bodies cannot choose
another user or promote a plan. `pool_available` reports whether the monthly
pool has uncommitted headroom and is accepting new requests; it never exposes
the operator's balance. `pool_period_start` and `pool_resets_at` are UTC RFC3339
timestamps for the current platform budget period. Availability
can be false even while the user's monthly allowance remains positive. An enabled
response contains `starter` with stable
environment ID, owner, name, plan, state, quota and examples. `node_id`/`agent_id`
are present after a verified ready report. Quota contains integer
`granted/spent/reserved/remaining_microusd` and the corresponding `_tokens` fields,
plus UTC RFC3339 `period_start` and `resets_at`. Spent/reserved quota counters
describe the current displayed period. Global lifetime counters never reset;
a durable period baseline measures only the platform's current-month spend.
States are sleeping, queued, starting, ready and failed. An expired execution lease
changes the observed state to sleeping. Capacity rejection is retryable queued
work, not a permanent broken Agent. The current version has no autonomous idle
detector, paid entitlement API or user-controlled Delegate promotion.

## Trusted host protocol

POST `/api/v1/hosting/jobs/claim` with a host-only bearer key and
`{host_id,capacity}`. `data.job` is null or:

```text
job_id, environment_id, owner_user_id, generation, lease_token,
lease_expires_at, plan, cloud_url, registration_token (initial allocation only),
model_base_url, model_token, model_id, model_max_output_tokens, active
```

Environment IDs are stable lowercase slugs. The first host is pinned; neither a
timeout nor another host's free capacity authorizes moving the user's disk. Claim
retry on the same host recovers an already-starting allocation. Registration-token
creation and job assignment are committed in one storage transaction. Plain
limited allocation credentials are private durable control data, not public status.

POST heartbeat/complete with `{host_id,job_id,generation,lease_token,state,node_id,
agent_id,error_code}`. Heartbeat returns `{permitted:true,lease_expires_at}`.
Leases last 180 seconds and must be renewed while the environment executes.
Ready completion keeps the lease active. Sleeping/failed completion revokes it;
`failed` with `host_capacity` requeues on the same host. Old/expired generations,
wrong hosts and stale secrets receive a conflict. Ready IDs must refer to a real
node and Agent belonging to the canonical owner, with matching node binding.

The host must stop a guest when authorization expires or is lost, including after
host process restart. Model credentials are signed for environment+generation and
validated against the current active execution lease on every call. Renewing an
execution lease keeps the credential valid; a new wake rotates the generation.
The shared host key grants trusted platform-host authority and must never enter a
tenant. This first version does not provide per-host credential enrollment or
cross-host data migration/fencing.

## Model gateway

The image uses `HOSTING_GATEWAY_URL` as its Chat Completions base URL and its
limited model token as the API key. A root-owned host-worker configuration pins
the same exact `model_gateway_url`; a job cannot select another gateway. The
independent gateway replaces the tenant credential with its provider key. Manager
only handles account, lease and durable accounting operations; it never receives
model prompts or calls a model supplier. Caller-provided routes/keys cannot select
the subsidized upstream. Only operator-approved text models, one completion,
bounded client function tools, 128 messages, 48,000 request bytes and each
selected model's approved output ceiling are accepted. Image/audio, server tools, unknown cost-bearing parameters and provider
storage are rejected. The pinned Pi 0.75.3 generated request is a regression
fixture. SSE always requests final usage and preserves tool/content chunks.

Admission atomically reserves a conservative input byte/framing bound plus maximum
output in both tokens and microusd, against owner and platform budgets. One
in-flight model request per environment is permitted. Input framing uses a
conservative text bound, not ACP cumulative/context usage. This gateway is for a
reviewed text-only model; do not enable a provider with extra unmetered server-side
features or reasoning charges outside the output ceiling.

Bounded numeric supplier usage, request identifier, model and configured price
snapshot are recorded without prompts or output text. Exact valid usage releases excess
reservation. Missing/ambiguous usage or an interrupted upstream call consumes the
reserved allowance conservatively. A client disconnect does not refund the call;
the gateway drains upstream within its 120-second deadline. SSE terminal DONE is
sent only after settlement. After 3 minutes, the next status/admission operation
reconciles an orphan reservation: a moderation-stage request that was never
marked as starting generation releases its hold without charge; a request marked
as starting generation retains conservative accounting. An idempotent reservation
ID cannot invoke the provider twice. Exhausted owner allowance returns 402;
unavailable platform allowance returns 503, keeping the distinction visible to
Console.

## Approved model pool

Set `HOSTING_MODELS_FILE=/etc/pax/models.json` to enable the approved multi-model
pool. Use the same operator-reviewed JSON on the gateway as `GATEWAY_MODELS_FILE`.
Manager accepts the gateway-only `route` field but does not use it to choose an
upstream. File entries approve model identity, provider, actual configured
upstream model, moderation mode, policy version, output ceiling and nonnegative
input/output prices. No provider credentials belong in this file.

```json
{
  "policy_version": "pool-v1",
  "default_model": "example/provider-a",
  "models": [
    {
      "id": "example/provider-a",
      "provider": "provider-a",
      "upstream_model": "vendor/model-a",
      "route": "example/provider-a",
      "moderation": "openai",
      "max_output_tokens": 2048,
      "input_microusd_per_million": 1000000,
      "output_microusd_per_million": 2000000,
      "enabled": true
    },
    {
      "id": "example/provider-b",
      "provider": "provider-b",
      "upstream_model": "vendor/model-b",
      "route": "example/provider-b",
      "moderation": "off",
      "max_output_tokens": 512,
      "input_microusd_per_million": 0,
      "output_microusd_per_million": 0,
      "enabled": true
    }
  ]
}
```

Replace placeholder identities and rates with reviewed deployments; trial-pool
membership does not mean the supplier provides free inference. Missing per-model
moderation defaults to `openai`, and missing per-model version inherits the
required top-level version. Missing `enabled` leaves an entry disabled. Duplicate
IDs, unknown modes, negative prices and malformed files fail configuration.
The file is loaded at startup; deploy matching versions on Manager and gateway.
Change the policy version when changing a route or its safety/cost policy.

Administrators can also register persistent models through
`PATCH /api/v1/user/self/hosting-admin` with `action: model_create`. Registration
accepts an alias, one of `openrouter`, `openai`, or `anthropic`, an upstream
model identity, and explicit moderation, cap and prices. New models are always
disabled and stored in the existing hosting-state document. Existing model
controls can enable or adjust their policy later. Registration does not add
credentials, change the platform budget, or establish a provider connection.
See `docs/handoff_20261116_182600.md` for the request and deployment contract.

With a registry, its default model and cap replace the legacy single-model
settings. All models consume the same existing user/global grants and the same
request-rate limits. Zero-price models still consume token allowance. Exhausted
money credits do not prevent wake when an enabled zero-price policy exists and
tokens remain; this does not silently select that model or bypass an overdrawn
platform money cap. Choose an enabled default for working new host sessions.
Disabling the default is permitted as a kill switch; it does not disable other
models, audit persistence or settlement of already-started calls.

The gateway sends its selected policy on admission. Manager compares every field
with its own approved registry and snapshots that policy and its prices in the
reservation. Disabled/unapproved IDs and forged prices or moderation modes fail
admission. Before generation, Manager rechecks the selected model is enabled and
its full policy still matches the snapshot. A changed policy rejects generation
and the gateway releases the pre-generation hold. Settlement keeps the admitted
price snapshot even after an operator update.

Only an explicit approved `off` snapshot may record `skipped_by_policy`; it must
have the exact version and no invented checker model/categories/result ID.
Unreviewed input is never recorded as `allowed`. A checker outage never changes
an `openai` policy into `off`. Without `HOSTING_MODELS_FILE`, implicit legacy
requests retain required OpenAI moderation and the original single-model rates;
a claimed zero-price legacy policy cannot change those authoritative rates.
A registered identity uses its approved persisted policy before legacy fallback.

The gateway exposes approved enabled models in `/v1/models`. The current hosted
Pi image initially advertises the configured default only; populating every
harness's model picker from that catalog is separate work. Users may explicitly
select another approved model through a compatible gateway client. Native
Responses and Anthropic Messages are provided by the independent gateway's
bounded text/client-tool adapters. See its `docs/native-protocols.md` for public
bases, unsupported opaque history and model-specific commissioning limits.

## Input moderation

The independent pax-gateway service applies the selected server-approved model
policy. Missing moderation settings default to `openai`. Those requests pass
OpenAI `omni-moderation-latest` through the fixed `https://api.openai.com/v1/moderations` endpoint. The generation provider and
client cannot choose or override that endpoint. Authentication, execution-lease
validation and single-request budget reservation happen before moderation. The
execution lease is checked again after moderation and before generation begins.

The checker covers the complete submitted text context, not only the latest user
message: prior messages, tool results, assistant function-call arguments, tool
descriptions, JSON Schema text and free-form schema keys. JSON-escaped text is
decoded for inspection. Bounded text chunks with overlap are submitted as an array
of strings, with at most 8,000 bytes per chunk. Every submitted input must receive
a valid result; a missing result does not count as approval.

| Outcome | Gateway behavior |
| --- | --- |
| Approved model policy explicitly uses `off` | Persist `skipped_by_policy` and its exact approved version, then revalidate the policy and lease |
| All required-check results valid and unflagged | Persist the verdict, revalidate the policy and lease, then permit generation |
| Any result flagged or category flag true | Return 403 `moderation_blocked`; do not call the generation provider |
| Checker timeout, 429/5xx, redirect, invalid or incomplete response | Return 503 `moderation_unavailable`; do not call the generation provider |
| Verdict/accounting cannot be persisted | Do not permit generation |

The checker has a 10-second deadline and does not follow redirects. A blocked or
unavailable check releases its pre-generation reservation with zero generation
charge. This does not refund a generation call that has already been admitted;
the conservative settlement rules above still apply after generation starts.
Moderation is not skipped when unavailable. One in-flight request per environment
is enforced, but blocked or checker-failure requests do not consume generation
quota. They still count toward durable rolling request limits: 20 admitted
requests per user and 200 platform-wide per 60 seconds by default, across all
models. Exceeding either returns 429 `starter_rate_limited`; a gateway restart
does not reset the ledger count. Limits are configurable with the two request
rate variables above. No automatic strike counter or account suspension is
implemented. A single flag blocks that request. These controls are not
comprehensive abuse prevention.

The gateway sends verdict and numeric usage metadata over its service-authenticated
accounting endpoint. The existing request record in `hosting_state` stores the verdict, requested or
returned moderation model, flagged category names, provider request ID, timestamp,
input count and policy version. It does not store another copy of the prompts,
tool output, generated text or moderation API key. These records support later
investigation; they are not proof that content is lawful or that an account is
safe from supplier enforcement.

OpenAI currently documents the moderation endpoint as free to use, subject to
rate limits. Classification signals cover supported harm categories; this first
layer is not output moderation, a complete terms/legal-compliance check, a
malicious-code detector, a tool-permission system or a supported-region check.
The text-only gateway does not add image, audio or other media forwarding. Do not
send known or suspected CSAM to this endpoint; follow the provider's dedicated
child-safety handling guidance instead.

User-supplied keys may access public model APIs directly. The platform subsidy
token only works through Pax gateway and cannot spend outside its policy. No
blanket public-egress restriction is introduced. Private-network, cloud-metadata,
resource and host-isolation restrictions remain separate safeguards. Self-hosting
a model changes supplier dependency but does not remove infrastructure risks.

Before rollout, tell users that when a model policy requires OpenAI moderation,
submitted text context, including relevant file or tool content, is sent to
OpenAI even when another supplier performs generation. Explicit `off` policies
skip that checker; provider-native safeguards can still apply. Review this processing in the application's privacy notice
and data handling policy. Keep the key server-side and verify the real account's
endpoint permissions, region eligibility and rate limits during commissioning.

Official references: [Moderation guide](https://developers.openai.com/api/docs/guides/moderation)
and [Create moderation API](https://developers.openai.com/api/reference/resources/moderations/methods/create).

## Persistence and scale

Memory storage is for development. PostgreSQL stores one locked JSONB hosting
document plus ordinary registration-token rows. A transaction row lock serializes
grants, leases and budget admission across Manager processes. It is intentionally
a small-pool implementation: request audit history grows and each operation reads
the document. Before broad rollout, migrate to normalized per-owner and per-request
tables with bounded retention and maintain the same atomic budget guarantees.
There is no automatic destructive retention or grant reset.

## Verification

Use installed Go through gvm; do not alter go.mod/go.work to select a toolchain.

```sh
source /Users/gengcongkai/.gvm/scripts/gvm
gvm list
gvm use go1.26
GOWORK=off GOCACHE=/tmp/pax-manager-go-cache go test ./...
PAX_HOST_CONTRACT_DRIVER=/absolute/path/pax-host/tests/worker_contract_driver.py \
  GOWORK=off GOCACHE=/tmp/pax-manager-go-cache \
  go test ./internal/manager -run TestStarterHostWorkerContract -count=1
```

The Python contract driver uses a real loopback Manager HTTP server and actual
pax-host ControlClient/Worker, with Docker/disk execution replaced by a fake host.
It checks claim, renewal, ready, stop/sleep/wake, identity retention and generation
rotation. It does not prove gVisor, EC2 or supplier behavior. Optional isolated
PostgreSQL tests use `PAX_MANAGER_HOSTING_TEST_DATABASE_URL`; they create and remove
a private test schema and exercise two independent store pools, rollback and
cross-process admission serialization. Never point them at production.

The complete user acceptance still requires a real Linux/runsc host, externally
reachable regional origins, the built paxd/paxl/Pi image, a real limited supplier
account, two user accounts, and the host isolation/recovery checks in pax-host.
