#!/usr/bin/env python3
"""Render the reviewed route matrix and probe an existing machine API ingress."""

import argparse
import json
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
MANIFEST = ROOT / "internal/manager/nodepolicy/routes.json"
MATRIX = ROOT / "docs/node_api_routes.md"


def render_matrix(routes):
    lines = [
        "# Public Node API route matrix",
        "",
        "Generated from `internal/manager/nodepolicy/routes.json`.",
        "Regenerate with `python3 deploy/node-public/check.py --write-matrix`.",
        "See [the rollout guide](node_public_rollout.md) for deployment boundaries.",
        "",
        "These are the reviewed current machine routes. Auth and owner columns describe",
        "the required boundary, not proof that every cross-tenant case has been audited.",
        "Rate classes are planning metadata for KEV-37/38/39; per-class or distributed",
        "limits are not implemented by this manifest. The existing shared in-memory",
        "API limiter still applies, with a separate registration-start/poll bucket.",
        "",
        "| Method | Path | Authentication | Owner boundary | Rate class | Expected caller |",
        "|---|---|---|---|---|---|",
    ]
    for route in routes:
        lines.append("| " + " | ".join([
            route["method"], "`" + route["path"] + "`", route["auth"],
            route["owner_boundary"], route["rate_class"], route["caller"],
        ]) + " |")
    return "\n".join(lines) + "\n"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def probe(base, routes, delay):
    parsed = urllib.parse.urlsplit(base)
    if (parsed.scheme not in ("http", "https") or not parsed.hostname
            or parsed.username or parsed.password or parsed.query or parsed.fragment
            or parsed.path not in ("", "/")):
        raise ValueError("base URL must be an HTTP(S) origin without credentials or a path")
    if parsed.scheme != "https" and parsed.hostname not in ("localhost", "127.0.0.1", "::1"):
        raise ValueError("remote probes require HTTPS")
    opener = urllib.request.build_opener(NoRedirect())
    cases = []
    for route in routes:
        path = re.sub(r":[a-z_]+", "probe_missing", route["path"])
        # Empty bootstrap requests fail validation and do not create registrations.
        expected = 400 if route["auth"] in ("anonymous", "poll-token") else 401
        payload = {"hostname": "probe"} if route["auth"] == "registration-token" else {}
        cases.append((route["method"], path, payload, expected))
    cases.extend([
        ("GET", "/api/v1/node/unreviewed", {}, 404),
        ("GET", "/api/v1/agent/unreviewed", {}, 404),
        ("DELETE", "/api/v1/node/status", {}, 404),
    ])
    failures = 0
    for method, path, payload, expected in cases:
        data = None if method == "GET" else json.dumps(payload).encode("ascii")
        request = urllib.request.Request(base.rstrip("/") + path, data=data, method=method)
        request.add_header("Content-Type", "application/json")
        request.add_header("User-Agent", "pax-node-ingress-probe/1")
        try:
            with opener.open(request, timeout=15) as response:
                status = response.status
        except urllib.error.HTTPError as error:
            status = error.code
            error.close()
        except (urllib.error.URLError, TimeoutError):
            status = "connection-failed"
        passed = status == expected
        failures += not passed
        print(f"{'PASS' if passed else 'FAIL'} {method} {path}: {status}, expected {expected}")
        time.sleep(delay)
    print("These credential-free probes do not verify authorized flows or tenant isolation.")
    return int(failures > 0)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--write-matrix", action="store_true")
    parser.add_argument("--probe", metavar="BASE_URL")
    parser.add_argument("--delay", type=float, default=1.0, help="seconds between probes")
    args = parser.parse_args()
    if args.delay < 0:
        parser.error("--delay must be nonnegative")
    routes = json.loads(MANIFEST.read_text())
    matrix = render_matrix(routes)
    if args.write_matrix:
        MATRIX.write_text(matrix)
    elif not MATRIX.exists() or MATRIX.read_text() != matrix:
        print("Route matrix is stale; run with --write-matrix", file=sys.stderr)
        return 1
    if args.probe:
        return probe(args.probe, routes, args.delay)
    print(f"Route matrix is current ({len(routes)} routes).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
