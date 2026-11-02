#!/usr/bin/env python3
"""Queue bounded existing search projections after deploying group-key support."""

import argparse
import json
import os
from pathlib import Path
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--batch-size", type=int, default=500)
    parser.add_argument("--max-pages", type=int, default=1)
    parser.add_argument("--after-kind", default="")
    parser.add_argument("--after-source-id", default="")
    args = parser.parse_args()
    if not 1 <= args.batch_size <= 1000 or not 1 <= args.max_pages <= 10000:
        parser.error("batch-size must be 1..1000 and max-pages 1..10000")
    if not os.environ.get("PGDATABASE"):
        parser.error("set PGDATABASE (and normal libpq connection variables)")
    migration = Path(__file__).resolve().parent.parent / "db/migrations/20261102_search_group_projection.sql"
    after_kind, after_source = args.after_kind, args.after_source_id
    for _ in range(args.max_pages):
        result = subprocess.run(
            ["psql", "-X", "-q", "-t", "-A", "-v", "ON_ERROR_STOP=1",
             "-v", "after_kind=" + after_kind, "-v", "after_source_id=" + after_source,
             "-v", "batch_limit=" + str(args.batch_size), "-f", str(migration)],
            check=True, capture_output=True, text=True,
            env={**os.environ, "PGOPTIONS": os.environ.get("PGOPTIONS", "") + " -c statement_timeout=8000 -c lock_timeout=1000"},
        )
        page = json.loads(result.stdout)
        print(json.dumps(page), flush=True)
        if page["queued"] == 0:
            break
        after_kind, after_source = page["after_kind"], page["after_source_id"]


if __name__ == "__main__":
    main()
