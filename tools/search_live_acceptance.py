#!/usr/bin/env python3
"""Exercise the local Manager HTTP worker and a genuine stdio MCP child."""

import argparse
import json
import os
import re
import select
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DB = "pax_search_live"
SEARCH = "/api/v1/user/self/search"
DATABASE = "\u6570\u636e\u5e93"
UPDATED = DATABASE + "\u66f4\u65b0"
APPLE = "\u82f9\u679c\u7b14\u8bb0\u672c"


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def sql_literal(value):
    return "'" + value.replace("'", "''") + "'"


def validate_target(base_url, project):
    url = urllib.parse.urlsplit(base_url)
    require(url.scheme == "http" and url.hostname == "127.0.0.1" and url.port
            and not url.path and not url.query and not url.fragment and not url.username,
            "Only an explicit loopback Manager origin is accepted")
    require(re.fullmatch(r"pax-search-live-[a-z0-9-]+", project),
            "Only an isolated pax-search-live Compose project is accepted")


class LocalStack:
    def __init__(self, base_url, project):
        validate_target(base_url, project)
        self.base_url = base_url
        self.project = project

    def request(self, path, payload=None, email=None, key=None, expected=200):
        headers = {"Content-Type": "application/json"}
        if email:
            headers["X-User-Email"] = email
        if key:
            headers["Authorization"] = "Bearer " + key
        data = None if payload is None else json.dumps(payload).encode()
        request = urllib.request.Request(self.base_url + path, data=data, headers=headers)
        try:
            response = urllib.request.urlopen(request, timeout=15)
        except urllib.error.HTTPError as exc:
            response = exc
        with response:
            body = json.load(response)
            require(response.status == expected,
                    "HTTP status for " + path + ": " + str(response.status))
        require(body.get("code") == expected, "Unexpected Manager envelope status")
        return body.get("data")

    def sql(self, statement):
        # No host database connection or user-supplied SQL is used.
        command = ["docker", "compose", "-f", str(ROOT / "docker-compose.search-live.yml"),
                   "-p", self.project, "exec", "-T", "postgres", "psql", "-X",
                   "-v", "ON_ERROR_STOP=1", "-U", "pax", "-d", DB, "-At"]
        result = subprocess.run(command, input=statement, text=True, capture_output=True,
                                check=False, timeout=30, cwd=ROOT)
        require(result.returncode == 0, "Isolated fixture SQL failed: " + result.stderr[-2000:])
        return result.stdout.strip()

    def wait(self):
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            try:
                self.request("/health")
                return
            except (OSError, ValueError, AssertionError):
                time.sleep(0.5)
        raise AssertionError("Local Manager never became healthy")

    def api_key(self, email):
        result = self.request("/api/v1/user/self/api-keys", {"name": "local search acceptance"}, email=email)
        require(bool(result.get("key")), "API key was not issued")
        return result["key"]

    def seed(self):
        owner = self.sql("SELECT user_id FROM users WHERE email='local@example.local';")
        other = self.sql("SELECT user_id FROM users WHERE email='search-other@example.local';")
        require(bool(owner) and bool(other), "Fixture users were not registered by HTTP")
        self.sql("BEGIN;\n"
                 "INSERT INTO agents(agent_id,owner_user_id,hostname,os,api_key_hash) VALUES "
                 "('search-live-agent'," + sql_literal(owner) + ",'local','linux','live-fixture-owner'),"
                 "('search-live-other'," + sql_literal(other) + ",'local','linux','live-fixture-other');\n"
                 "INSERT INTO agent_sessions(agent_id,session_id,session_name) VALUES "
                 "('search-live-agent','search-live-session'," + sql_literal(APPLE) + "),"
                 "('search-live-other','search-live-private','Private session');\n"
                 "INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq) VALUES "
                 "('search-live-m1','search-live-agent','search-live-session','test','inbound','assistant','agent_message_chunk',1),"
                 "('search-live-m2','search-live-agent','search-live-session','test','inbound','user','chat',2),"
                 "('search-live-m3','search-live-agent','search-live-session','test','inbound','assistant','agent_message_chunk',3),"
                 "('search-live-hidden','search-live-other','search-live-private','test','inbound','user','chat',1);\n"
                 "INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES "
                 "('search-live-m1',0,'text'," + sql_literal(DATABASE[:2]) + "),"
                 "('search-live-m1',1,'text'," + sql_literal(DATABASE[2:]) + "),"
                 "('search-live-m2',0,'text'," + sql_literal(APPLE) + "),"
                 "('search-live-m3',0,'text','First visible paragraph.' || E'\\n\\n' || 'Final visible paragraph.'),"
                 "('search-live-hidden',0,'text'," + sql_literal(DATABASE + " PRIVATE") + ");\nCOMMIT;")

    def search(self, key, query, kinds=None, expected_ids=None):
        payload = {"query": query, "kinds": kinds or ["message"], "limit": 50}
        deadline = time.monotonic() + 45
        while True:
            page = self.request(SEARCH, payload, key=key)
            ids = {hit["ref"]["id"] for hit in page["hits"]}
            if expected_ids is None or ids == set(expected_ids):
                return page
            require(time.monotonic() < deadline, "Worker did not synchronize expected " + str(payload))
            time.sleep(0.3)

    def read(self, key, ref, expected=200, **options):
        return self.request(SEARCH + "/read", {"ref": ref, **options}, key=key, expected=expected)


class MCPChild:
    def __init__(self, base_url, key):
        environment = {**os.environ, "PAX_MANAGER_URL": base_url, "PAX_USER_API_KEY": key,
                       "PYTHONDONTWRITEBYTECODE": "1"}
        self.child = subprocess.Popen([sys.executable, str(ROOT / "tools/secretary_search_mcp.py")],
                                      stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                      stderr=subprocess.PIPE, text=True, env=environment)
        self.serial = 0

    def rpc(self, method, params=None):
        self.serial += 1
        request = {"jsonrpc": "2.0", "id": self.serial, "method": method, "params": params or {}}
        self.child.stdin.write(json.dumps(request) + "\n")
        self.child.stdin.flush()
        require(bool(select.select([self.child.stdout], [], [], 20)[0]), "MCP child response timed out")
        response = json.loads(self.child.stdout.readline())
        require(response.get("id") == self.serial and "result" in response,
                "Invalid MCP JSON-RPC response")
        return response["result"]

    def tool(self, name, arguments, error=None):
        result = self.rpc("tools/call", {"name": name, "arguments": arguments})
        if error:
            require(result.get("isError") and result["structuredContent"]["error"]["code"] == error,
                    "MCP did not return the expected safe tool error")
        else:
            require(not result.get("isError"), "MCP tool failed: " + name)
        return result["structuredContent"]

    def close(self):
        self.child.stdin.close()
        try:
            self.child.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.child.kill()
            self.child.wait(timeout=5)
        self.child.stdout.close()
        self.child.stderr.close()


def coverage(read):
    return {"message_id": read["message_id"], "content_hash": read["ref"]["content_hash"],
            "canonicalization_version": read["ref"]["canonicalization_version"],
            "read_receipt": read["read_receipt"]}


def acceptance(stack, key, other):
    stack.search(key, DATABASE, expected_ids=["search-live-m1"])
    stack.search(key, "\u672c", ["session"], [stack.sql("SELECT id FROM agent_sessions WHERE session_id='search-live-session';")])
    stack.search(key, "\u672c", expected_ids=["search-live-m2"])
    require(not stack.search(key, "\u534e\u4e3a\u7b14\u8bb0\u672c")["hits"], "Literal search was too broad")
    stack.search(other, DATABASE, expected_ids=["search-live-hidden"])
    stack.read(other, {"kind": "message", "id": "search-live-m1"}, expected=404)
    print("PASS real HTTP: multipart Chinese content, title, single-character and owner isolation")

    mcp = MCPChild(stack.base_url, key)
    try:
        require(mcp.rpc("initialize", {"protocolVersion": "2025-06-18"})["protocolVersion"] == "2025-06-18", "MCP initialize failed")
        mcp.child.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n")
        mcp.child.stdin.flush()
        require({tool["name"] for tool in mcp.rpc("tools/list")["tools"]} == {"search", "read", "expand", "publish_summary"}, "MCP tools missing")
        hit = mcp.tool("search", {"query": DATABASE, "kinds": ["message"]})["hits"][0]
        read = mcp.tool("read", {"ref": hit["ref"], "view": "last"})
        expanded = mcp.tool("expand", {"message_id": read["message_id"], "content_hash": read["ref"]["content_hash"],
                                      "canonicalization_version": read["ref"]["canonicalization_version"], "view": "full"})
        require(expanded["text"] == DATABASE and expanded["ref"] == read["ref"], "Expand changed message identity")
        request = {"summary_id": "search-live-summary", "subject_type": "topic", "subject_id": "search-live-topic",
                   "expected_revision": 0, "request_id": "search-live-v1", "title": "Search decision",
                   "body": "Use " + DATABASE, "coverage": [coverage(expanded)],
                   "claims": [{"id": "decision", "text": DATABASE, "evidence": [
                       {"message": coverage(expanded), "quote": DATABASE, "view": "full"}]}]}
        invalid = {**request, "request_id": "invalid-proof", "coverage": [{**coverage(expanded), "read_receipt": "forged"}]}
        mcp.tool("publish_summary", invalid, error="invalid_request")
        bad_quote = {**request, "request_id": "invalid-quote", "claims": [{"id": "invalid", "text": "Invalid quote",
                     "evidence": [{"message": coverage(expanded), "quote": "never exposed", "view": "full"}]}]}
        mcp.tool("publish_summary", bad_quote, error="invalid_request")
        v1 = mcp.tool("publish_summary", request)
        require(v1["revision"] == 1 and mcp.tool("publish_summary", request)["revision"] == 1,
                "Immutable publication retry created another version")
        stack.search(key, DATABASE, ["summary"], ["search-live-summary"])
        print("PASS genuine stdio MCP over real HTTP: search/read/expand/publish, receipt and quote validation")

        stack.sql("UPDATE message_parts SET text=" + sql_literal(UPDATED[2:]) +
                  " WHERE message_id='search-live-m1' AND part_index=1;")
        stack.search(key, UPDATED, expected_ids=["search-live-m1"])
        fresh = mcp.tool("read", {"ref": {"kind": "message", "id": "search-live-m1"}, "view": "full"})
        require(fresh["text"] == UPDATED and fresh["ref"]["content_hash"] != expanded["ref"]["content_hash"],
                "A late part update did not change the message version")
        old = mcp.tool("read", {"ref": expanded["ref"], "view": "full"})
        require(old["text"] == DATABASE, "Frozen old source returned current text")
        request2 = {**request, "expected_revision": 1, "request_id": "search-live-v2",
                    "body": "Use " + UPDATED, "coverage": [coverage(fresh)], "claims": []}
        v2 = mcp.tool("publish_summary", request2)
        require(v2["revision"] == 2, "Summary v2 was not published")
        latest = stack.search(key, UPDATED, ["summary"], ["search-live-summary"])
        require(latest["hits"][0]["ref"]["revision"] == 2, "ES did not index the current summary revision")
        historical = mcp.tool("read", {"ref": {"kind": "summary", "id": "search-live-summary", "revision": 1}})
        require(historical["body"] == v1["body"] and historical["coverage"][0]["content_hash"] == expanded["ref"]["content_hash"],
                "Summary v1 lost its exact original source")
        stack.read(other, {"kind": "summary", "id": "search-live-summary", "revision": 1}, expected=404)
        print("PASS running worker: late parts, summary v1/v2, current search and exact historical source")

        # The still-cached ES hit must be rejected against current PG visibility.
        stack.sql("DELETE FROM messages WHERE message_id='search-live-m1';")
        require(not stack.search(key, DATABASE)["hits"], "Deleted message remained visible in search")
        require(not stack.search(key, DATABASE, ["summary"])["hits"], "Summary leaked a deleted source")
        mcp.tool("read", {"ref": expanded["ref"], "view": "full"}, error="not_found")
        mcp.tool("read", {"ref": {"kind": "summary", "id": "search-live-summary", "revision": 1}}, error="not_found")
        pending = "SELECT count(*) FROM search_sync_state WHERE desired_revision > indexed_revision;"
        deadline = time.monotonic() + 45
        while stack.sql(pending) != "0":
            require(time.monotonic() < deadline, "Deletion changes were not acknowledged by the live worker")
            time.sleep(0.3)
        print("PASS deletion: immediate PG revalidation, historical access rules and durable worker acknowledgement")
    finally:
        mcp.close()


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--project", required=True)
    parser.add_argument("--seed-only", action="store_true")
    args = parser.parse_args(argv)
    stack = LocalStack(args.base_url, args.project)
    stack.wait()
    key = stack.api_key("local@example.local")
    other = stack.api_key("search-other@example.local")
    stack.seed()
    if args.seed_only:
        stack.search(key, DATABASE, expected_ids=["search-live-m1"])
        read = stack.read(key, {"kind": "message", "id": "search-live-m1"}, view="full")
        stack.request(SEARCH + "/summaries", {
            "summary_id": "search-live-summary", "subject_type": "topic", "subject_id": "search-live-topic",
            "expected_revision": 0, "request_id": "search-live-seed", "title": "Search decision",
            "body": "Use " + DATABASE, "coverage": [coverage(read)],
        }, key=key)
        stack.search(key, DATABASE, ["summary"], ["search-live-summary"])
        print("Seeded local-only owner and other-user fixtures; API keys are not printed.")
    else:
        acceptance(stack, key, other)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (AssertionError, OSError, ValueError, subprocess.SubprocessError) as error:
        print("FAIL " + str(error), file=sys.stderr)
        raise SystemExit(1) from None
