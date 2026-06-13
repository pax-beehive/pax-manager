# Agent Instructions

This repository is a Go service for pax-manager. Keep changes pragmatic,
well-tested, and aligned with the existing module boundaries.

## Documentation and Language

- Code, comments, docs, generated files, and test names must be English and
  ASCII only.
- Do not use emojis.
- Write concise comments only where they clarify non-obvious logic.

## Architecture Practice

- Keep `cmd/manager` as a thin entrypoint.
- Put business logic in `internal/manager/*` packages.
- Keep paxd-facing behavior in `internal/manager/paxd`.
- Keep user-facing behavior in `internal/manager/userapi`.
- Keep authentication and identity resolution in `internal/manager/auth`.
- Keep SQL and in-memory persistence in `internal/manager/storage`.
- Let modules interact through interfaces where that keeps package boundaries
  deep and testable.
- Do not make generated Hertz handlers own business logic. Generated handlers
  should bind transport data and delegate.

## Generated Code

Source of truth:

```text
api/pax_manager.thrift
```

Regenerate derived code with:

```bash
make generate
```

Generated artifacts include:

```text
internal/transport/http/**
internal/manager/openapi_generated.go
internal/manager/openapi_generated.json
internal/manager/*/mocks/**
```

Do not manually edit generated artifacts unless the generator itself is being
changed and the generated output is intentionally updated in the same change.

## Formatting, Linting, and Tests

Before handing off code changes, run the relevant checks:

```bash
make fmt-check
make lint
GOCACHE=/tmp/pax-manager-go-cache go test -count=1 ./...
GOCACHE=/tmp/pax-manager-go-cache go build -o /tmp/pax-manager-build-check ./cmd/manager
```

For Docker-backed end-to-end behavior:

```bash
make integration-test
```

Stop the isolated integration stack with:

```bash
make integration-down
```

`make lint` includes golangci-lint and the integration build tag. `gocyclo`
allows method complexity up to 20.

## Handoff Practice

For substantial architecture, deployment, generated-code, authentication, or
test workflow changes, create a handoff document:

```text
docs/handoff_<YYYYMMDD_HHMMSS>.md
```

The handoff should include:

- a summary of the change
- the relevant module boundaries
- core request and data flows
- generated artifacts and regeneration commands
- local development and deployment commands
- verification commands that were run
- known risks, assumptions, or operational gotchas

Keep handoff docs factual. They should let the next agent continue without
reverse-engineering the entire repository.

## Safety

- Do not revert user changes unless explicitly asked.
- Do not run destructive Git commands.
- Use isolated integration Docker Compose for end-to-end tests so normal local
  volumes are not destroyed.
- Keep production auth defaults safe. Cloudflare Access validation is enabled by
  default; only local development should set `CLOUDFLARE_ACCESS_DISABLED=true`.
