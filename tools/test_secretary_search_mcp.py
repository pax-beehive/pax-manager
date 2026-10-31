import io
import json
import os
import subprocess
import sys
import unittest
import urllib.error
from unittest.mock import patch

import secretary_search_mcp as mcp


class FakeResponse(io.BytesIO):
    def __init__(self, data):
        super().__init__(json.dumps(data).encode())


class AdapterTests(unittest.TestCase):
    def setUp(self):
        self.calls = []
        self.payload = {"ref": {"kind": "message", "id": "m"}, "read_receipt": "proof"}
        def open_request(request, timeout):
            self.calls.append((request, timeout))
            return FakeResponse({"code": 200, "message": "ok", "data": self.payload})
        self.client = mcp.ManagerClient("https://manager.test", "test-secret", open_request)
        self.server = mcp.Server(self.client)

    def call(self, name, arguments):
        return self.server.handle({"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": name, "arguments": arguments}})

    def test_configuration_rejects_insecure_or_credential_urls(self):
        for url in ["http://manager.test", "https://user:secret@manager.test", "https://manager.test?key=x", "https://manager.test/#x", "file:///tmp/x", "http://localhost.evil"]:
            with self.subTest(url=url), self.assertRaises(mcp.ConfigurationError):
                mcp.ManagerClient(url, "key")
        for url in ["https://manager.test", "http://localhost:9879", "http://127.0.0.1:9879", "http://[::1]:9879"]:
            mcp.ManagerClient(url, "key")
        with self.assertRaises(mcp.ConfigurationError):
            mcp.ManagerClient("https://manager.test", "")
        self.assertIsNone(mcp.NoRedirect().redirect_request(None, None, 302, "redirect", {}, "https://other.test"))

    def test_search_posts_credentials_only_in_header_and_preserves_data(self):
        result = self.call("search", {"query": "database", "limit": 3})["result"]
        self.assertEqual(self.payload, result["structuredContent"])
        self.assertEqual(self.payload, json.loads(result["content"][0]["text"]))
        request, timeout = self.calls[0]
        self.assertEqual("https://manager.test/api/v1/user/self/search", request.full_url)
        self.assertEqual("Bearer test-secret", request.get_header("Authorization"))
        self.assertNotIn("test-secret", request.data.decode())
        self.assertGreater(timeout, 0)

    def test_read_expand_and_publish_exact_contract(self):
        ref = {"kind": "message", "id": "m", "content_hash": "hash", "canonicalization_version": "v1"}
        self.call("read", {"ref": ref, "view": "last", "before": 1})
        self.assertEqual(ref, json.loads(self.calls[-1][0].data)["ref"])
        self.call("expand", {"message_id": "m", "content_hash": "hash", "canonicalization_version": "v1", "view": "full"})
        self.assertEqual(ref, json.loads(self.calls[-1][0].data)["ref"])
        request = {"summary_id": "s", "subject_type": "topic", "subject_id": "t", "expected_revision": 0, "request_id": "once", "title": "Title", "body": "Summary", "coverage": [{"message_id": "m", "content_hash": "hash", "canonicalization_version": "v1", "read_receipt": "proof"}], "claims": [{"id": "c", "text": "Summary", "evidence": [{"message": {"message_id": "m", "content_hash": "hash", "canonicalization_version": "v1"}, "quote": "quote", "read_receipt": "proof", "view": "full"}]}], "summary_sources": [{"summary_id": "old", "revision": 1}]}
        self.call("publish_summary", request)
        self.assertEqual(request, json.loads(self.calls[-1][0].data))
        self.assertTrue(self.calls[-1][0].full_url.endswith("/search/summaries"))

    def test_invalid_arguments_do_not_send_requests(self):
        cases = [("search", {"query": "x", "owner_user_id": "other"}), ("search", {"query": "x", "limit": True}), ("search", {"query": "x", "limit": 51}), ("search", {"query": "x", "kinds": ["unknown"]}), ("read", {"ref": {"id": "m"}}), ("expand", {"message_id": "m", "view": "full"}), ("expand", {"message_id": "m", "content_hash": "h", "canonicalization_version": "v", "view": "last"}), ("publish_summary", {}), ("unknown", {})]
        for name, args in cases:
            with self.subTest(name=name, args=args):
                result = self.call(name, args)
                self.assertEqual(-32602, result["error"]["code"])
        self.assertEqual([], self.calls)

    def test_backend_errors_never_expose_response_secrets(self):
        for exc in [urllib.error.HTTPError("https://secret", 401, "private-key", {}, io.BytesIO(b'{"message":"private-key"}')), urllib.error.URLError("private-key"), ValueError("private-key")]:
            def fail(*args, **kwargs):
                raise exc
            self.client.opener = fail
            response = self.call("search", {"query": "x"})
            self.assertTrue(response["result"]["isError"])
            self.assertNotIn("private-key", json.dumps(response))
        self.client.opener = lambda *a, **k: FakeResponse({"code": 409, "message": "source version unavailable"})
        self.assertEqual("version_unavailable", self.call("read", {"ref": {"kind": "message", "id": "m"}})["result"]["structuredContent"]["error"]["code"])
        self.client.opener = lambda *a, **k: FakeResponse({"oops": "private-key"})
        self.assertTrue(self.call("search", {"query": "x"})["result"]["isError"])

    def test_protocol_and_stdio(self):
        self.assertIsNone(self.server.handle({"jsonrpc": "2.0", "method": "notifications/initialized"}))
        for method in ["initialize", "ping", "tools/list"]:
            result = self.server.handle({"jsonrpc": "2.0", "id": 3, "method": method, "params": {}})
            self.assertIn("result", result)
        self.assertEqual(-32601, self.server.handle({"jsonrpc": "2.0", "id": 3, "method": "unknown"})["error"]["code"])
        self.assertEqual(-32600, self.server.handle([])["error"]["code"])
        output = io.StringIO()
        self.server.serve(io.StringIO('invalid\n\n{"jsonrpc":"2.0","id":8,"method":"ping"}\n'), output)
        lines = output.getvalue().splitlines()
        self.assertEqual(2, len(lines))
        self.assertEqual(-32700, json.loads(lines[0])["error"]["code"])
        self.assertEqual(8, json.loads(lines[1])["id"])

    def test_size_limits_and_invalid_envelopes(self):
        with patch.object(mcp, "MAX_BYTES", 128):
            with self.assertRaises(mcp.ToolFailure):
                self.client.post("", {"query": "x" * 500})
            self.client.opener = lambda *a, **k: FakeResponse({"code": 200, "data": "x" * 500})
            self.assertTrue(self.call("search", {"query": "x"})["result"]["isError"])
            output = io.StringIO()
            self.server.serve(io.StringIO("x" * 300 + '\n{"jsonrpc":"2.0","id":4,"method":"ping"}\n'), output)
            self.assertEqual(2, len(output.getvalue().splitlines()))
        for envelope in [{"code": 200}, {"code": 403}, {"code": 200, "data": []}]:
            self.client.opener = lambda *a, **k: FakeResponse(envelope)
            response = self.call("search", {"query": "x"})["result"]
            if envelope.get("data") == []:
                self.assertEqual({"data": []}, response["structuredContent"])
            else:
                self.assertTrue(response["isError"])
        for status, detail in [(409, b'{"message":"summary version mismatch"}'), (503, b'not-json')]:
            def fail(*args, **kwargs):
                raise urllib.error.HTTPError("https://secret", status, "secret", {}, io.BytesIO(detail))
            self.client.opener = fail
            self.assertTrue(self.call("search", {"query": "x"})["result"]["isError"])

    def test_protocol_rejects_invalid_calls_and_main_fails_safely(self):
        for method, params in [("initialize", []), ("tools/call", []), ("tools/call", {"name": "search", "arguments": {"query": "x", "session_ids": ["s"] * 51}})]:
            response = self.server.handle({"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
            self.assertEqual(-32602, response["error"]["code"])
        self.assertEqual(-32600, self.server.handle({"jsonrpc": "2.0", "id": {}, "method": "ping"})["error"]["code"])
        self.client.post = lambda *a: (_ for _ in ()).throw(RuntimeError("private-key"))
        self.assertEqual(-32603, self.call("search", {"query": "x"})["error"]["code"])
        with patch.dict(os.environ, {}, clear=True), patch("sys.stderr", new_callable=io.StringIO) as output:
            self.assertEqual(2, mcp.main())
            self.assertIn("PAX_MANAGER_URL", output.getvalue())
        with patch.dict(os.environ, {"PAX_MANAGER_URL": "https://manager.test", "PAX_USER_API_KEY": "private-key"}), patch("sys.stdin", io.StringIO('')), patch("sys.stdout", io.StringIO()):
            self.assertEqual(0, mcp.main())

    def test_actual_stdio_process_smoke(self):
        env = dict(os.environ, PAX_MANAGER_URL="https://manager.test", PAX_USER_API_KEY="test-secret")
        messages = [{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18"}}, {"jsonrpc": "2.0", "method": "notifications/initialized"}, {"jsonrpc": "2.0", "id": 2, "method": "tools/list"}]
        result = subprocess.run([sys.executable, os.path.join(os.path.dirname(__file__), "secretary_search_mcp.py")], input="\n".join(map(json.dumps, messages))+"\n", text=True, capture_output=True, env=env, timeout=5, check=True)
        lines = [json.loads(line) for line in result.stdout.splitlines()]
        self.assertEqual(2, len(lines))
        self.assertEqual(4, len(lines[1]["result"]["tools"]))
        self.assertNotIn("test-secret", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
