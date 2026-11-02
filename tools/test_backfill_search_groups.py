import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("backfill_search_groups", Path(__file__).with_name("backfill_search_groups.py"))
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class BackfillSearchGroupsTests(unittest.TestCase):
    def test_bounded_pages_keep_cursor_arguments_literal_and_print_resume_state(self):
        identifier = "id' $(unexpected)"
        pages = [types.SimpleNamespace(stdout=json.dumps({"queued": 2, "after_kind": "message", "after_source_id": identifier})),
                 types.SimpleNamespace(stdout=json.dumps({"queued": 0, "after_kind": "message", "after_source_id": identifier}))]
        output = io.StringIO()
        with patch.dict(os.environ, {"PGDATABASE": "isolated", "PGOPTIONS": "-c search_path=custom_schema"}), patch.object(sys, "argv", ["tool", "--batch-size", "2", "--max-pages", "3"]), patch.object(MODULE.subprocess, "run", side_effect=pages) as run, contextlib.redirect_stdout(output):
            MODULE.main()
        self.assertEqual(2, run.call_count)
        self.assertIn("after_source_id=" + identifier, run.call_args_list[1].args[0])
        self.assertIn("batch_limit=2", run.call_args_list[0].args[0])
        self.assertNotIn("shell", run.call_args.kwargs)
        self.assertEqual(2, len(output.getvalue().splitlines()))
        self.assertIn("statement_timeout=8000", run.call_args.kwargs["env"]["PGOPTIONS"])
        self.assertTrue(run.call_args.kwargs["env"]["PGOPTIONS"].startswith("-c search_path=custom_schema "))

    def test_invalid_bounds_or_missing_connection_fail_before_psql(self):
        for args in (["--batch-size", "0"], ["--batch-size", "1001"], ["--max-pages", "0"], []):
            with patch.dict(os.environ, {}, clear=True), patch.object(sys, "argv", ["tool"] + args), patch.object(MODULE.subprocess, "run") as run, contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                MODULE.main()
            run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
