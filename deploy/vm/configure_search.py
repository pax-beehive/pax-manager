#!/usr/bin/env python3
"""Activate search on one idle Release-managed Manager; retain exact rollback."""
import argparse
import copy
import fcntl
import http.client
import json
import os
import re
import secrets
import socket
import stat
import sys
import time
import urllib.error
import urllib.request
import uuid
from contextlib import contextmanager
from pathlib import Path
from urllib.parse import quote, urlsplit

LABEL = "pax.search.activation"
IMAGE = re.compile(r"sha256:[0-9a-f]{64}")
NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,63}")
SEARCH_KEYS = ("SEARCH_URL", "SEARCH_INDEX", "SEARCH_READ_RECEIPT_SECRET")


class OperationError(Exception):
    """Only fixed, non-secret error codes reach terminal output."""


def require(condition, code):
    if not condition:
        raise OperationError(code)


class UnixHTTP(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect("/var/run/docker.sock")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


class DockerHost:
    def __init__(self, health_port=9879):
        self.health_port = health_port

    def api(self, method, path, body=None, missing=False):
        connection = UnixHTTP("localhost", timeout=45)
        try:
            connection.request(method, "/v1.45" + path,
                               body=None if body is None else json.dumps(body),
                               headers={"Content-Type": "application/json"})
            response = connection.getresponse()
            payload = response.read(8 * 1024 * 1024 + 1)
            if response.status == 404 and missing:
                return None
            require(response.status < 300 and len(payload) <= 8 * 1024 * 1024, "DOCKER_FAILED")
            return json.loads(payload) if payload else None
        except (OSError, ValueError, http.client.HTTPException):
            raise OperationError("DOCKER_FAILED") from None
        finally:
            connection.close()

    def inspect(self, name):
        return self.api("GET", "/containers/" + quote(name, safe="") + "/json", missing=True)

    def stop(self, name):
        self.api("POST", "/containers/" + quote(name, safe="") + "/stop?t=30")

    def rename(self, name, new):
        self.api("POST", "/containers/" + quote(name, safe="") + "/rename?name=" + quote(new, safe=""))

    def create(self, name, body):
        self.api("POST", "/containers/create?name=" + quote(name, safe=""), body)

    def start(self, name):
        self.api("POST", "/containers/" + quote(name, safe="") + "/start")

    def disconnect(self, network, name):
        self.api("POST", "/networks/" + quote(network, safe="") + "/disconnect",
                 {"Container": name, "Force": False})

    def connect(self, network, name, settings):
        self.api("POST", "/networks/" + quote(network, safe="") + "/connect",
                 {"Container": name, "EndpointConfig": settings})

    def healthy(self, timeout):
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        deadline = time.monotonic() + timeout
        while True:
            passed = True
            for path, expected in (("/health", 200), ("/api/v1/user/self/me", 401)):
                try:
                    with opener.open("http://127.0.0.1:" + str(self.health_port) + path, timeout=3) as response:
                        status = response.status
                except urllib.error.HTTPError as error:
                    status = error.code
                    error.close()
                except (OSError, ValueError):
                    status = None
                passed = passed and status == expected
            if passed:
                return True
            if time.monotonic() >= deadline:
                return False
            time.sleep(1)


def endpoint(settings, old_id):
    result = {"Aliases": [alias for alias in settings.get("Aliases") or []
                          if alias not in (old_id, old_id[:12])]}
    for key in ("IPAMConfig", "DriverOpts", "Links", "GwPriority"):
        if settings.get(key) is not None:
            result[key] = copy.deepcopy(settings[key])
    return result


def save_private(path, data):
    temporary = path.with_name("." + path.name + "." + uuid.uuid4().hex)
    try:
        descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "w") as stream:
            json.dump(data, stream, allow_nan=False)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        temporary.unlink(missing_ok=True)


@contextmanager
def idle_agent(agent_state):
    root = Path(agent_state)
    require(root.is_dir(), "AGENT_STATE_MISSING")
    try:
        descriptor = os.open(root / "agent.lock", os.O_RDWR | os.O_NOFOLLOW)
    except OSError:
        raise OperationError("AGENT_LOCK_MISSING") from None
    with os.fdopen(descriptor, "a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise OperationError("AGENT_BUSY") from None
        try:
            path = root / "active.json"
            active = json.loads(path.read_text()) if path.exists() else {}
            require(isinstance(active, dict) and not active, "AGENT_NOT_IDLE")
            yield
        except (ValueError, UnicodeError):
            raise OperationError("AGENT_STATE_INVALID") from None


def options(manager, search_url, index, health_timeout, health_port=9879):
    require(bool(NAME.fullmatch(manager)), "INVALID_MANAGER")
    url = urlsplit(search_url)
    require(url.scheme == "http" and bool(NAME.fullmatch(url.hostname or ""))
            and url.netloc == (url.hostname or "") + ":9200" and not url.path
            and not url.query and not url.fragment, "INVALID_SEARCH_URL")
    require(bool(re.fullmatch(r"[a-z0-9][a-z0-9._-]{0,199}", index)) and index not in (".", ".."), "INVALID_INDEX")
    require(type(health_timeout) is int and 1 <= health_timeout <= 300, "INVALID_HEALTH_TIMEOUT")
    require(type(health_port) is int and 1 <= health_port <= 65535, "INVALID_HEALTH_PORT")


def environment(values):
    require(isinstance(values, list) and all(isinstance(value, str) and "=" in value for value in values), "INVALID_ENV")
    result = dict(value.split("=", 1) for value in values)
    require(len(result) == len(values), "DUPLICATE_ENV")
    return result


def preflight(host, manager, timeout, health_port=9879):
    old = host.inspect(manager)
    require(bool(old) and old["State"]["Running"] and not old.get("Mounts")
            and bool(IMAGE.fullmatch(old["Image"])), "UNSAFE_MANAGER")
    config = old["HostConfig"]
    networks = old["NetworkSettings"]["Networks"]
    require(not config.get("AutoRemove") and config.get("NetworkMode") in networks, "UNSAFE_NETWORK")
    bindings = config.get("PortBindings") or {}
    require(bindings and all(binding.get("HostIp") in ("127.0.0.1", "::1")
            for entries in bindings.values() for binding in entries or []), "UNSAFE_PORTS")
    require(any(binding.get("HostPort") == str(health_port) for binding in bindings.get("9879/tcp", [])), "UNSAFE_PORTS")
    env = environment(old["Config"].get("Env", []))
    require(env.get("CLOUDFLARE_ACCESS_DISABLED") == "false"
            and env.get("ALLOW_LOCAL_USER_HEADER") == "false", "UNSAFE_AUTH")
    require(host.healthy(timeout), "PRECHECK_UNHEALTHY")
    return old


def desired_config(old, operation_id, search_url, index):
    env = environment(old["Config"]["Env"])
    secret = env.get(SEARCH_KEYS[2], "")
    if len(secret) < 32 or not all(33 <= ord(char) <= 126 for char in secret):
        secret = secrets.token_hex(32)
    changes = dict(zip(SEARCH_KEYS, (search_url, index, secret)))
    body = copy.deepcopy(old["Config"])
    body["Env"] = [key + "=" + changes.pop(key, value) for key, value in
                   (entry.split("=", 1) for entry in body["Env"])]
    body["Env"].extend(key + "=" + value for key, value in changes.items())
    body["Image"], body["Hostname"] = old["Image"], ""
    body["Labels"] = dict(body.get("Labels") or {}, **{LABEL: operation_id})
    body["HostConfig"] = copy.deepcopy(old["HostConfig"])
    body["NetworkingConfig"] = {"EndpointsConfig": {
        name: endpoint(settings, old["Id"]) for name, settings in old["NetworkSettings"]["Networks"].items()}}
    return body


def persist(directory, state, phase=None, status=None):
    if phase:
        state["phase"] = phase
    if status:
        state["status"] = status
    save_private(directory / "state.json", state)


def receipt(directory, state):
    return {key: state[key] for key in ("status", "operation_id", "manager", "backup", "failed", "new_id")} | {"state_dir": str(directory)}


def owned(current, state):
    return (current["Image"] == state["old"]["Image"]
            and (current["Config"].get("Labels") or {}).get(LABEL) == state["operation_id"]
            and (state["new_id"] is None or current["Id"] == state["new_id"]))


def restore(host, directory, state, timeout):
    name, old = state["manager"], state["old"]
    current, backup, failed = (host.inspect(value) for value in (name, state["backup"], state["failed"]))
    require(not backup or backup["Id"] == old["Id"], "FOREIGN_CONTAINER")
    require(not failed or owned(failed, state), "FOREIGN_CONTAINER")
    require(not current or current["Id"] == old["Id"] or owned(current, state), "FOREIGN_CONTAINER")
    require((current and current["Id"] == old["Id"]) or backup, "ORIGINAL_MISSING")
    require(not (current and current["Id"] != old["Id"] and failed), "FAILED_NAME_OCCUPIED")
    persist(directory, state, "restoring")
    if current and current["Id"] != old["Id"]:
        if state["new_id"] is None:
            state["new_id"] = current["Id"]
            persist(directory, state)
        if current["State"]["Running"]:
            host.stop(name)
        for network in current["NetworkSettings"]["Networks"]:
            host.disconnect(network, name)
        host.rename(name, state["failed"])
    if backup:
        host.rename(state["backup"], name)
    current = host.inspect(name)
    require(current and current["Id"] == old["Id"], "ORIGINAL_MISSING")
    for network, settings in old["NetworkSettings"]["Networks"].items():
        if network not in current["NetworkSettings"]["Networks"]:
            host.connect(network, name, endpoint(settings, old["Id"]))
    if not current["State"]["Running"]:
        host.start(name)
    require(host.healthy(timeout), "ROLLBACK_UNHEALTHY")
    persist(directory, state, "complete", "rolled_back")
    return receipt(directory, state)


def activate(manager, agent_state, state_dir, search_url, index, health_timeout=240, *, host=None, health_port=9879):
    options(manager, search_url, index, health_timeout, health_port)
    host, directory = host or DockerHost(health_port), Path(state_dir)
    with idle_agent(agent_state):
        old = preflight(host, manager, health_timeout, health_port)
        require(not directory.exists(), "STATE_ALREADY_EXISTS")
        operation_id = uuid.uuid4().hex
        state = {"schema_version": 1, "operation_id": operation_id, "manager": manager,
                 "backup": manager + "-search-before-" + operation_id[:12],
                 "failed": manager + "-search-failed-" + operation_id[:12], "old": old,
                 "desired_config": desired_config(old, operation_id, search_url, index),
                 "new_id": None, "health_port": health_port, "phase": "prepared", "status": "pending"}
        require(not host.inspect(state["backup"]) and not host.inspect(state["failed"]), "NAME_OCCUPIED")
        directory.mkdir(mode=0o700)
        os.chmod(directory, 0o700)
        save_private(directory / "container.before.json", old)
        persist(directory, state)
        try:
            current = host.inspect(manager)
            require(current and all(current.get(key) == old.get(key) for key in
                    ("Id", "Config", "HostConfig", "NetworkSettings")), "RUNTIME_CHANGED")
            persist(directory, state, "switching")
            host.stop(manager)
            host.rename(manager, state["backup"])
            for network in old["NetworkSettings"]["Networks"]:
                host.disconnect(network, state["backup"])
            host.create(manager, state["desired_config"])
            current = host.inspect(manager)
            require(current and owned(current, state), "FOREIGN_CONTAINER")
            state["new_id"] = current["Id"]
            persist(directory, state, "starting")
            host.start(manager)
            current = host.inspect(manager)
            require(current and owned(current, state)
                    and current["Config"]["Env"] == state["desired_config"]["Env"]
                    and current["HostConfig"]["PortBindings"] == old["HostConfig"]["PortBindings"]
                    and set(current["NetworkSettings"]["Networks"]) == set(old["NetworkSettings"]["Networks"]), "RUNTIME_CHANGED")
            persist(directory, state, "health_check")
            require(host.healthy(health_timeout), "HEALTHCHECK_FAILED")
            persist(directory, state, "complete", "activated")
            return receipt(directory, state)
        except Exception as error:
            state["error_code"] = str(error) if isinstance(error, OperationError) else "ACTIVATION_FAILED"
            persist(directory, state)
            try:
                restore(host, directory, state, health_timeout)
            except Exception:
                persist(directory, state, status="rollback_failed")
                raise OperationError("ROLLBACK_FAILED") from None
            raise OperationError("ACTIVATION_ROLLED_BACK") from None


def read_state(directory):
    require(not directory.is_symlink() and directory.is_dir()
            and stat.S_IMODE(directory.stat().st_mode) == 0o700
            and directory.stat().st_uid == os.geteuid(), "UNSAFE_STATE_DIRECTORY")
    path = directory / "state.json"
    require(not path.is_symlink() and path.is_file() and stat.S_IMODE(path.stat().st_mode) == 0o600
            and path.stat().st_uid == os.geteuid(), "UNSAFE_STATE_FILE")
    state = json.loads(path.read_text())
    require(state.get("schema_version") == 1 and bool(re.fullmatch(r"[0-9a-f]{32}", state.get("operation_id", "")))
            and bool(NAME.fullmatch(state.get("manager", "")))
            and bool(IMAGE.fullmatch(state.get("old", {}).get("Image", ""))), "INVALID_STATE")
    for key, suffix in (("backup", "-search-before-"), ("failed", "-search-failed-")):
        require(state.get(key) == state["manager"] + suffix + state["operation_id"][:12], "INVALID_STATE")
    return state


def rollback(agent_state, state_dir, health_timeout=240, *, host=None, health_port=None):
    require(type(health_timeout) is int and 1 <= health_timeout <= 300, "INVALID_HEALTH_TIMEOUT")
    with idle_agent(agent_state):
        directory = Path(state_dir)
        state = read_state(directory)
        port = state.get("health_port", 9879)
        require(type(port) is int and 1 <= port <= 65535
                and (health_port is None or health_port == port), "INVALID_HEALTH_PORT")
        return restore(host or DockerHost(port), directory, state, health_timeout)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("activate", "rollback", "recover"))
    parser.add_argument("--manager", default="pax-manager")
    parser.add_argument("--agent-state", required=True)
    parser.add_argument("--state-dir", required=True)
    parser.add_argument("--search-url", default="http://pax-search:9200")
    parser.add_argument("--index", default="pax-search-v1")
    parser.add_argument("--health-timeout", type=int, default=240)
    parser.add_argument("--health-port", type=int, help="Loopback Manager port; default 9879, recovery uses saved port")
    args = parser.parse_args(argv)
    try:
        if args.command == "activate":
            result = activate(args.manager, args.agent_state, args.state_dir,
                              args.search_url, args.index, args.health_timeout,
                              health_port=9879 if args.health_port is None else args.health_port)
        else:
            result = rollback(args.agent_state, args.state_dir, args.health_timeout, health_port=args.health_port)
        print(json.dumps(result))
        return 0
    except Exception as error:
        print(json.dumps({"status": "error", "code": str(error) if isinstance(error, OperationError) else "OPERATION_FAILED"}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
