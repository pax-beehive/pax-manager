# Trusted Cloudflare proxy boundary

Manager ignores forwarded client identity by default. Configure
`TRUSTED_CLOUDFLARE_PROXY_CIDRS` as a comma-separated list of the immediate
cloudflared connector IPs in CIDR notation, for example
`192.0.2.10/32,2001:db8::10/128` (documentation addresses only).
Invalid CIDRs, IPv4-mapped CIDRs and all-address networks fail configuration
validation. An empty value trusts no proxy.

Only a TCP peer in that list may provide `CF-Connecting-IP`. The value must
be one valid IP, without a port, zone or list; mapped IPv4 is normalized.
Missing or invalid values fall back to the TCP peer. `X-Forwarded-For` is
never used as a fallback. Registration city/country headers are also ignored
for untrusted peers. Node Key and user authentication are unchanged.

## Deployment

Keep application and database host bindings on loopback. Preserve existing
Access checks and the Tunnel route policy. Configure trust before deploying
this binary; otherwise all traffic through a connector shares its rate bucket.

Use the connector address on its connection to Manager, not the public
Cloudflare edge IP ranges. Do not trust the entire shared Docker subnet,
the Docker host gateway, loopback, or Console merely to preserve IP headers.
Those addresses can represent callers that do not sanitize Cloudflare headers.
Console requests will use the Console peer address unless a separately audited
proxy is introduced. This may aggregate Console traffic into one rate bucket.

Assign the connector a stable address on a dedicated network and verify that
only approved peers can use the trusted address. A dynamically reassigned
container address is not a durable trust boundary. Record the network and
exact connector CIDRs in the protected deployment configuration. Recreating
the connector requires validating its identity and Manager reachability.

Before switching production, verify candidate configuration without printing
secrets. Check direct requests with forged CF and forwarded headers, valid
connector traffic, registration previews and both rate limiters. Preserve a
rollback image and environment file. Rolling back this fix restores the former
header-trust behavior; retain network restrictions throughout.

## Remaining KEV-36 work

The trusted-proxy change was deployed on 2026-09-11. See
[production handoff](handoff_20260911_002500.md). This change does not establish
firewall or router rules. Privileged firewall inspection, router forwarding review, independent
external-origin rejection tests and durable automated edge/origin smoke checks
remain necessary before KEV-36 can close.
