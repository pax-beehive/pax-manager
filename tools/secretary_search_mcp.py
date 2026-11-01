#!/usr/bin/env python3
"""Small stdio MCP adapter for authenticated Manager search and summary APIs."""

import ipaddress
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request

MAX_BYTES = 4 * 1024 * 1024
NATIVE_BASE_PATHS = {"/api/v1/native/" + region for region in ("us", "hk", "client")}


class ConfigurationError(ValueError):
    pass


class InvalidArguments(ValueError):
    pass


class ToolFailure(Exception):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def string(maximum=4096):
    return {"type": "string", "minLength": 1, "maxLength": maximum}


def integer(minimum, maximum):
    return {"type": "integer", "minimum": minimum, "maximum": maximum}


def obj(properties, required):
    return {"type": "object", "properties": properties, "required": required, "additionalProperties": False}


def array(items, maximum=512):
    return {"type": "array", "items": items, "maxItems": maximum}


MESSAGE_REF = obj({"message_id": string(), "content_hash": string(256), "canonicalization_version": string(128), "read_receipt": string(16384)}, ["message_id", "content_hash", "canonicalization_version"])
SUMMARY_REF = obj({"summary_id": string(), "revision": integer(1, 2**63-1)}, ["summary_id", "revision"])
REF = obj({"kind": {"type": "string", "enum": ["message", "session", "summary"]}, "id": string(), "revision": integer(1, 2**63-1), "content_hash": string(256), "canonicalization_version": string(128)}, ["kind", "id"])
EVIDENCE = obj({"message": MESSAGE_REF, "quote": string(1024*1024), "view": {"type": "string", "enum": ["last", "first", "full"]}, "read_receipt": string(16384)}, ["message", "quote"])
CLAIM = obj({"id": string(), "kind": string(), "text": string(1024*1024), "basis": string(), "state": string(), "supersedes": array(string()), "evidence": array(EVIDENCE)}, ["id", "text"])
READ_PROPERTIES = {"ref": REF, "view": {"type": "string", "enum": ["last", "first", "full"]}, "offset": integer(0, 1000000000), "limit": integer(0, 16384), "before": integer(0, 50), "after": integer(0, 50)}

TOOLS = [
    {"name": "search", "description": "Find current Manager messages, session titles, and summaries using whitespace-separated literal keywords across title/content of one object. At most 16 case-insensitively distinct keywords. More matched keywords rank first, then more ASCII identifier-boundary matches, then newest; all-keyword hits precede partial matches. Read hits before quoting them.", "inputSchema": obj({"query": string(256), "kinds": array({"type": "string", "enum": ["message", "session", "summary"]}, 3), "session_ids": array(string(), 50), "cursor": string(16384), "limit": integer(1, 50)}, ["query"])},
    {"name": "read", "description": "Read a source or exact summary revision. Preserve returned refs and read receipts when publishing summaries. An omitted summary revision reads the current version.", "inputSchema": obj(READ_PROPERTIES, ["ref"])},
    {"name": "expand", "description": "Read first/full visible text of one message at its expected version, excluding tools and reasoning. Preserve the returned read receipt.", "inputSchema": obj({"message_id": string(), "content_hash": string(256), "canonicalization_version": string(128), "view": {"type": "string", "enum": ["first", "full"]}, "offset": integer(0, 1000000000), "limit": integer(0, 16384)}, ["message_id", "content_hash", "canonicalization_version", "view"])},
    {"name": "publish_summary", "description": "Publish one immutable summary revision. Coverage includes every covered message version and its read receipt; evidence quotes require matching read receipts. Use a stable request_id for retries and expected_revision for conflicts.", "inputSchema": obj({"summary_id": string(), "subject_type": {"type": "string", "enum": ["fragment", "topic", "direction", "project"]}, "subject_id": string(), "expected_revision": integer(0, 2**63-1), "request_id": string(), "title": {"type": "string", "maxLength": 4096}, "body": string(1024*1024), "claims": array(CLAIM), "coverage": array(MESSAGE_REF), "summary_sources": array(SUMMARY_REF)}, ["summary_id", "subject_type", "subject_id", "expected_revision", "request_id", "title", "body", "coverage"])},
]
for tool in TOOLS:
    tool["annotations"] = {"readOnlyHint": tool["name"] != "publish_summary", "destructiveHint": False, "idempotentHint": True, "openWorldHint": False}


def validate(value, schema):
    kind = schema["type"]
    valid = {"object": isinstance(value, dict), "array": isinstance(value, list), "string": isinstance(value, str), "integer": isinstance(value, int) and not isinstance(value, bool)}[kind]
    if not valid:
        raise InvalidArguments("Tool arguments have an invalid type")
    if "enum" in schema and value not in schema["enum"]:
        raise InvalidArguments("Tool arguments contain an unsupported value")
    if kind == "object":
        if any(key not in value for key in schema["required"]) or any(key not in schema["properties"] for key in value):
            raise InvalidArguments("Tool arguments have missing or unknown fields")
        for key, item in value.items():
            validate(item, schema["properties"][key])
    elif kind == "array":
        if len(value) > schema["maxItems"]:
            raise InvalidArguments("Tool array exceeds its limit")
        for item in value:
            validate(item, schema["items"])
    elif kind == "string":
        if len(value) < schema.get("minLength", 0) or len(value) > schema.get("maxLength", 4096):
            raise InvalidArguments("Tool text exceeds its limits")
    elif value < schema["minimum"] or value > schema["maximum"]:
        raise InvalidArguments("Tool number exceeds its limits")


def api_failure(status, message=""):
    known = {"source version unavailable": ("version_unavailable", "The referenced source version is unavailable; read the source again."), "summary version mismatch": ("version_mismatch", "The summary version changed; read it again before publishing.")}
    if status == 409 and message in known:
        return ToolFailure(*known[message])
    errors = {400: ("invalid_request", "Manager rejected the request."), 401: ("unauthenticated", "Manager authentication failed."), 403: ("forbidden", "Access denied."), 404: ("not_found", "The source was not found or is no longer accessible."), 409: ("version_conflict", "The version changed or is unavailable; read the source again."), 429: ("rate_limited", "Manager rate limit reached; retry later.")}
    return ToolFailure(*errors.get(status, ("unavailable", "Manager is unavailable; retry later.")))


class ManagerClient:
    def __init__(self, base_url, api_key, opener=None):
        try:
            url = urllib.parse.urlsplit(base_url)
            host = url.hostname or ""
            loopback = host.lower() == "localhost"
            if not loopback:
                try:
                    loopback = ipaddress.ip_address(host).is_loopback
                except ValueError:
                    pass
            base_path = url.path.removesuffix("/")
            valid_path = url.path in ("", "/") or (
                base_path in NATIVE_BASE_PATHS
                and url.path in (base_path, base_path + "/")
            )
            valid = (
                (url.scheme == "https" or url.scheme == "http" and loopback)
                and host
                and url.username is None
                and url.password is None
                and not url.query
                and not url.fragment
                and valid_path
                and not any(ord(c) <= 32 or c in "\\?#" for c in base_url)
            )
            _ = url.port
        except ValueError:
            valid = False
        if not valid or not api_key or any(c in api_key for c in "\r\n"):
            raise ConfigurationError("Set PAX_MANAGER_URL to an HTTPS origin or /api/v1/native/{us,hk,client} base (HTTP only on loopback), and PAX_USER_API_KEY to a valid bearer credential.")
        self.base_url = urllib.parse.urlunsplit((url.scheme, url.netloc, base_path, "", ""))
        self.search_url = self.base_url + ("/user/self/search" if base_path else "/api/v1/user/self/search")
        self.api_key = api_key
        self.opener = opener or urllib.request.build_opener(NoRedirect()).open

    def post(self, path, payload):
        raw = json.dumps(payload, ensure_ascii=True).encode()
        if len(raw) > MAX_BYTES:
            raise ToolFailure("invalid_request", "Request exceeds the adapter size limit.")
        request = urllib.request.Request(self.search_url + path, data=raw, headers={"Content-Type": "application/json", "Authorization": "Bearer " + self.api_key}, method="POST")
        try:
            with self.opener(request, timeout=15) as response:
                raw = response.read(MAX_BYTES + 1)
            if len(raw) > MAX_BYTES:
                raise ToolFailure("invalid_response", "Manager response exceeds the adapter size limit.")
            envelope = json.loads(raw)
            if not isinstance(envelope, dict) or not isinstance(envelope.get("code"), int):
                raise ToolFailure("invalid_response", "Manager returned an invalid response.")
            if envelope["code"] < 200 or envelope["code"] >= 300:
                raise api_failure(envelope["code"], envelope.get("message", ""))
            if "data" not in envelope:
                raise ToolFailure("invalid_response", "Manager returned an invalid response.")
            return envelope["data"]
        except urllib.error.HTTPError as exc:
            try:
                detail = json.loads(exc.read(MAX_BYTES + 1))
                message = detail.get("message", "") if isinstance(detail, dict) else ""
            except (ValueError, OSError):
                message = ""
            finally:
                exc.close()
            raise api_failure(exc.code, message) from None
        except (urllib.error.URLError, OSError, ValueError):
            raise ToolFailure("unavailable", "Manager request failed; retry later.") from None


class Server:
    def __init__(self, client):
        self.client = client

    @staticmethod
    def error(request_id, code, message):
        return {"jsonrpc": "2.0", "id": request_id, "error": {"code": code, "message": message}}

    def call(self, params):
        if not isinstance(params, dict):
            raise InvalidArguments("Tool call parameters must be an object")
        tool = next((t for t in TOOLS if t["name"] == params.get("name")), None)
        if tool is None:
            raise InvalidArguments("Unknown tool")
        arguments = params.get("arguments", {})
        validate(arguments, tool["inputSchema"])
        name = tool["name"]
        path = ""
        if name == "expand":
            arguments = dict(arguments)
            arguments["ref"] = {"kind": "message", "id": arguments.pop("message_id"), "content_hash": arguments.pop("content_hash"), "canonicalization_version": arguments.pop("canonicalization_version")}
        if name in ("read", "expand"):
            path = "/read"
        elif name == "publish_summary":
            path = "/summaries"
        try:
            data = self.client.post(path, arguments)
            structured = data if isinstance(data, dict) else {"data": data}
            return {"content": [{"type": "text", "text": json.dumps(data, ensure_ascii=True)}], "structuredContent": structured}
        except ToolFailure as exc:
            error = {"code": exc.code, "message": str(exc)}
            return {"isError": True, "content": [{"type": "text", "text": str(exc)}], "structuredContent": {"error": error}}

    def handle(self, request):
        if not isinstance(request, dict) or request.get("jsonrpc") != "2.0" or not isinstance(request.get("method"), str):
            return self.error(None, -32600, "Invalid JSON-RPC request")
        if "id" not in request:
            return None
        request_id = request["id"]
        if isinstance(request_id, (list, dict, bool)):
            return self.error(None, -32600, "Invalid request ID")
        method = request["method"]
        params = request.get("params", {})
        try:
            if method == "initialize":
                if not isinstance(params, dict):
                    raise InvalidArguments("Initialize parameters must be an object")
                version = params.get("protocolVersion")
                if version not in ("2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"):
                    version = "2025-06-18"
                result = {"protocolVersion": version, "capabilities": {"tools": {}}, "serverInfo": {"name": "pax-manager-secretary-search", "version": "0.1.0"}}
            elif method == "ping":
                result = {}
            elif method == "tools/list":
                result = {"tools": TOOLS}
            elif method == "tools/call":
                result = self.call(params)
            else:
                return self.error(request_id, -32601, "Method not found")
            return {"jsonrpc": "2.0", "id": request_id, "result": result}
        except InvalidArguments as exc:
            return self.error(request_id, -32602, str(exc))
        except Exception:
            return self.error(request_id, -32603, "Internal adapter error")

    def serve(self, source, output):
        while True:
            line = source.readline(MAX_BYTES + 1)
            if not line:
                return
            if len(line) > MAX_BYTES:
                while line and not line.endswith("\n"):
                    line = source.readline(MAX_BYTES + 1)
                response = self.error(None, -32700, "Request exceeds the adapter size limit")
            elif not line.strip():
                continue
            else:
                try:
                    response = self.handle(json.loads(line))
                except (ValueError, RecursionError):
                    response = self.error(None, -32700, "Invalid JSON")
            if response is not None:
                output.write(json.dumps(response, ensure_ascii=True) + "\n")
                output.flush()


def main():
    try:
        client = ManagerClient(os.getenv("PAX_MANAGER_URL", ""), os.getenv("PAX_USER_API_KEY", ""))
    except ConfigurationError as exc:
        print(str(exc), file=sys.stderr)
        return 2
    Server(client).serve(sys.stdin, sys.stdout)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
