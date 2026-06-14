# ISSUE-008 Token usage model too narrow vs paxd reporting

**Status:** resolved
**Severity:** medium
**Component:** session / storage
**Found:** 2026-06-13
**Resolved:** 2026-06-14

## Summary

The `agent_sessions` table stores only 3 token fields (`token_input`, `token_output`, `token_total` - via `db/init.sql:70-72`). But paxd's Hermes client reports much finer-grained token data: `input`, `cache_read`, `cache_write`, `output`, `reasoning`, `estimated_cost_usd`, `actual_cost_usd`.

The domain model `TokenUsage` struct (`domain/models.go:166-170`) also only has `Input`, `Output`, `Total`. paxd's `StatusReport.SessionReport.TokenUsage` (`paxd/internal/cloud/client.go:93-101`) has all seven fields.

When paxd reports status, the extra fields are silently dropped by JSON deserialization into the narrower struct.

## Round-trip affected

- [x] Agent registration / connection (status reporting)

## Current behavior

paxd reports:
```json
{
  "token_usage": {
    "input": 1500,
    "cache_read": 200,
    "cache_write": 100,
    "output": 800,
    "reasoning": 300,
    "estimated_cost_usd": 0.015,
    "actual_cost_usd": 0.012
  }
}
```

pax-manager `TokenUsage.UnmarshalJSON` tries to extract `input_tokens`/`output_tokens`/`total_tokens` from the object. The paxd fields (`input`, `output`, `total`) are not matched. So token counts end up as 0.

## Expected behavior

Expand the `agent_sessions` schema and `TokenUsage` domain model to capture all seven fields. At minimum, add `cache_read_tokens`, `cache_write_tokens`, `reasoning_tokens`, and `estimated_cost_usd`.

## Impact

- Cost tracking unavailable in dashboard
- Cache efficiency metrics unavailable
- Reasoning token tracking unavailable (important for reasoning models like DeepSeek)

## Affected code

```
internal/manager/domain/models.go:166-170 - TokenUsage struct
db/init.sql:70-72 - agent_sessions token columns
internal/transport/http/model/paxmanager/api/ - Thrift-generated TokenUsage model
api/pax_manager.thrift - TokenUsage definition
```

## Proposed fix

1. Add columns to `agent_sessions`: `cache_read_tokens`, `cache_write_tokens`, `reasoning_tokens`, `estimated_cost_usd` (ALTER TABLE ADD COLUMN IF NOT EXISTS)
2. Update domain `TokenUsage` with matching fields
3. Update `TokenUsage.UnmarshalJSON` to accept paxd's field names
4. Regenerate Thrift

## Resolution

`TokenUsage` now carries input, output, total, cache read, cache write,
reasoning, estimated cost, and actual cost fields. The PostgreSQL schema stores
those values, and JSON parsing accepts both the existing manager names and the
paxd status report field names.
