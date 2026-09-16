# CI baseline repair

## Scope and flow

Issue 027 tracks formatting/lint failures and unavailable Docker Hub MinIO
images in CI. Non-generated Go files are formatted with the existing
`make fmt` target. No generated API changes or regeneration are required.

Storage helpers retain the existing transaction and lock ownership:
node status checks its fence inside the caller transaction; snapshot
reconciliation runs inside the snapshot transaction; memory snapshot
authority changes remain under the store mutex. User API helpers share
maintenance option validation and parse runtime reset acknowledgements.
Encrypted command acknowledgements are handled before event envelopes.

All Compose configurations use quay.io/minio server and client images with
the existing release tags. CI continues to run its original checks and
isolated integration stack. No production services are restarted.

## Verification and operation

Use gvm Go 1.26 locally with GOMAXPROCS=2 and GOFLAGS=-p=1.
Use the normal Go cache and golangci-lint cache on this laptop.

Required checks: `make fmt-check`, `make lint`, `make test-coverage`,
and `make build`. Remote integration CI runs `make integration-test`
and always `make integration-down`. Do not start the local Docker daemon
just for this repair.

Both pinned Quay image manifests were verified with `docker manifest inspect`.
Local formatting, both lint passes, full unit coverage (53.1%), and build pass.
The first remote run restored container startup and exposed an obsolete ACP
runtime assertion. The integration test now drives node-control snapshots,
uses the prompt envelope turn ID, and verifies ACP frames cannot overwrite
snapshot state. Updated integration lint passes; remote rerun is pending. No deployment is included.
