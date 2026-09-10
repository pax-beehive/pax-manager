# Trusted proxy and origin boundary

- Status: partial
- Resolved: -
- Severity: high
- Component: security / deployment
- Tracking: KEV-36

## Problem

Manager accepted client-IP headers from any peer, allowing a caller with origin
access to change rate-limit identity and registration network metadata.

## Resolution

Explicit Cloudflare connector CIDRs gate client IP and location headers.
Default trust is empty, invalid configuration fails validation, and untrusted
requests use the connection address. X-Forwarded-For cannot select a bucket.
Regression tests cover trust, parsing, registration metadata and rate limiting.

## Current note

Code only; production migration, stable connector addressing, privileged ingress
inspection and independent external-origin tests remain outstanding. See
[deployment guidance](../docs/trusted_proxy.md). Existing user authentication
and Node Key checks remain in force.
