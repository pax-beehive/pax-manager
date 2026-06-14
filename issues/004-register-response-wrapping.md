# ISSUE-004 Register response field naming mismatch

**Status:** open
**Severity:** high
**Component:** api
**Found:** 2026-06-13
**Resolved:** -

## Summary

`POST /api/agent/register` returns Thrift-generated camelCase fields (`agentId`, `apiKey`) but the paxd client expects snake_case (`agent_id`, `api_key`). This will cause paxd to fail parsing the registration response.

## Round-trip affected

- [x] Agent registration / connection

## Current behavior

pax-manager response (from `domain.RegisterAgentResponse`):
```json
{"agent_id": "agent_xxx", "api_key": "pax_xxx"}
```

Wait - checking `domain/models.go:103-106`:
```go
type RegisterAgentResponse struct {
    AgentID string `json:"agent_id"`
    APIKey  string `json:"api_key"`
}
```

The Go struct tags use `json:"agent_id"` and `json:"api_key"` which is correct snake_case.

However, the response goes through `writeEndpointResult` -> `writeData` -> `ctx.JSON(status, apiResponse{Data: data, ...})`. The `Data` field is `any`. Hertz JSON serialization uses the struct tags. So the response should actually be:
```json
{"data": {"agent_id": "...", "api_key": "..."}, "code": 201, "message": "ok"}
```

**But paxd expects the response at the top level:**
```go
// paxd/internal/cloud/client.go:139-143
type RegisterResponse struct {
    AgentID string `json:"agent_id"`
    APIKey  string `json:"api_key"`
}
var regResp RegisterResponse
json.NewDecoder(resp.Body).Decode(&regResp)
```

paxd decodes the **entire body** into `RegisterResponse`. But the body is wrapped: `{"data": {...}, "code": 201, "message": "ok"}`. So paxd's `AgentID` and `APIKey` will be empty strings.

## Expected behavior

Either:
A. pax-manager returns the register response unwrapped (flat JSON, no `{data, code, message}` envelope) for this specific endpoint
B. paxd client parses the envelope first, then extracts the data field

## Impact

**Blocks agent registration from paxd.** The agent will receive a 201 response but fail to extract agent_id and api_key.

## Affected code

```
pax-manager: internal/manager/paxd/service.go:98-101 - returns RegisterAgentResponse wrapped in {data,code,message}
pax-manager: internal/manager/server.go:179-181 - writeData wraps in apiResponse
```

## Proposed fix

Option A is simpler: in `paxd.RegisterAgent`, return the response without wrapping through `writeData`. Either use a raw `ctx.JSON(http.StatusCreated, resp)` or add a `writeDataUnwrapped` helper.
