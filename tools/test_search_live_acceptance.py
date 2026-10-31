"""Guard the local acceptance runner's safety and bounded network behavior."""

import io
import json
import subprocess
import unittest
import urllib.error
from unittest import mock

import search_live_acceptance as live


class Response(io.BytesIO):
    def __init__(self, code, data):
        super().__init__(json.dumps({"code": code, "data": data}).encode())
        self.status = code


class LocalAcceptanceTests(unittest.TestCase):
    def setUp(self):
        self.stack = live.LocalStack("http://127.0.0.1:9879", "pax-search-live-tests")

    def test_given_external_targets_when_validated_then_rejected_before_io(self):
        for url in ["https://example.com", "http://127.0.0.1:9879/path", "http://localhost:9879",
                    "http://user@127.0.0.1:9879", "http://127.0.0.1:9879?q=1"]:
            with self.subTest(url=url), self.assertRaises(AssertionError):
                live.LocalStack(url, "pax-search-live-tests")
        for project in ["production", "pax-manager-integration", "pax-search-live-test;false"]:
            with self.subTest(project=project), self.assertRaises(AssertionError):
                live.LocalStack("http://127.0.0.1:9879", project)

    def test_given_sql_fixture_when_run_then_only_named_compose_database_is_used(self):
        with mock.patch.object(live.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, "owner\n", "")) as run:
            self.assertEqual("owner", self.stack.sql("SELECT 1;"))
        command = run.call_args.args[0]
        self.assertIn("pax-search-live-tests", command)
        self.assertIn("pax_search_live", command)
        self.assertEqual("SELECT 1;", run.call_args.kwargs["input"])
        self.assertNotIn("shell", run.call_args.kwargs)
        self.assertEqual("'a''b'", live.sql_literal("a'b"))
        with mock.patch.object(live.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, "", "invalid")):
            with self.assertRaisesRegex(AssertionError, "fixture SQL failed"):
                self.stack.sql("SELECT 1;")

    def test_given_http_calls_when_authenticated_then_key_is_only_in_header(self):
        with mock.patch.object(live.urllib.request, "urlopen", return_value=Response(200, {"hits": []})) as request:
            self.assertEqual({"hits": []}, self.stack.request(live.SEARCH, {"query": "x"}, key="private-key"))
        value = request.call_args.args[0]
        self.assertEqual("Bearer private-key", value.get_header("Authorization"))
        self.assertNotIn("private-key", value.full_url)
        self.assertNotIn(b"private-key", value.data)

    def test_given_expected_denial_when_http_raises_then_envelope_is_verified(self):
        body = io.BytesIO(b'{"code":404,"data":null}')
        error = urllib.error.HTTPError("http://127.0.0.1", 404, "missing", {}, body)
        with mock.patch.object(live.urllib.request, "urlopen", side_effect=error):
            self.assertIsNone(self.stack.request(live.SEARCH + "/read", {}, expected=404))
        with mock.patch.object(live.urllib.request, "urlopen", return_value=Response(503, None)):
            with self.assertRaisesRegex(AssertionError, "HTTP status"):
                self.stack.request(live.SEARCH, {})

    def test_given_worker_delay_when_polling_then_wait_is_bounded(self):
        wrong = {"hits": [{"ref": {"id": "other"}}]}
        right = {"hits": [{"ref": {"id": "wanted"}}]}
        with mock.patch.object(self.stack, "request", side_effect=[wrong, right]), mock.patch.object(live.time, "sleep"):
            self.assertEqual(right, self.stack.search("key", "x", expected_ids=["wanted"]))
        with mock.patch.object(self.stack, "request", return_value=wrong), mock.patch.object(live.time, "monotonic", side_effect=[0, 46]):
            with self.assertRaisesRegex(AssertionError, "did not synchronize"):
                self.stack.search("key", "x", expected_ids=["wanted"])

    def test_given_startup_failures_when_waiting_then_retry_and_timeout(self):
        with mock.patch.object(self.stack, "request", side_effect=[OSError(), {}]), mock.patch.object(live.time, "sleep"):
            self.stack.wait()
        with mock.patch.object(live.time, "monotonic", side_effect=[0, 91]):
            with self.assertRaisesRegex(AssertionError, "never became healthy"):
                self.stack.wait()

    def test_given_api_key_when_issued_then_missing_key_is_rejected(self):
        with mock.patch.object(self.stack, "request", return_value={"key": "secret"}):
            self.assertEqual("secret", self.stack.api_key("owner@example.test"))
        with mock.patch.object(self.stack, "request", return_value={}):
            with self.assertRaisesRegex(AssertionError, "not issued"):
                self.stack.api_key("owner@example.test")

    def test_given_mcp_child_when_calls_fail_then_safe_errors_and_timeout_are_checked(self):
        child = live.MCPChild.__new__(live.MCPChild)
        child.child = mock.Mock()
        child.serial = 0
        child.child.stdout.readline.return_value = '{"id":1,"result":{"tools":[]}}\n'
        with mock.patch.object(live.select, "select", return_value=([child.child.stdout], [], [])):
            self.assertEqual({"tools": []}, child.rpc("tools/list"))
        with mock.patch.object(live.select, "select", return_value=([], [], [])):
            with self.assertRaisesRegex(AssertionError, "timed out"):
                child.rpc("tools/list")
        with mock.patch.object(child, "rpc", return_value={"isError": True, "structuredContent": {"error": {"code": "not_found"}}}):
            self.assertEqual("not_found", child.tool("read", {}, error="not_found")["error"]["code"])
            with self.assertRaisesRegex(AssertionError, "MCP tool failed"):
                child.tool("read", {})
        child.child.wait.side_effect = [subprocess.TimeoutExpired("child", 5), 0]
        child.close()
        child.child.kill.assert_called_once()
        child.child.stdin.close.assert_called_once()


if __name__ == "__main__":
    unittest.main()
