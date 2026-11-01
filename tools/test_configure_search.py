"""First search activation preserves application identity and recovery."""
import copy
import importlib.util
import json
import os
import tempfile
import unittest
import io
import urllib.error
from unittest.mock import Mock, patch
from pathlib import Path

MODULE = Path(__file__).resolve().parents[1] / "deploy/vm/configure_search.py"
spec = importlib.util.spec_from_file_location("configure_search", MODULE)
tool = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tool)
IMAGE = "sha256:" + "a" * 64


def original():
    return {"Id": "old-id", "Image": IMAGE, "State": {"Running": True}, "Mounts": [],
            "Config": {"Image": "mutable:tag", "Hostname": "old-id", "Env": [
                "DATABASE_URL=private", "CLOUDFLARE_ACCESS_DISABLED=false",
                "ALLOW_LOCAL_USER_HEADER=false", "UNCHANGED=yes"], "Labels": {"keep": "yes"},
                "Cmd": ["/manager"], "Entrypoint": ["/app/manager"]},
            "HostConfig": {"NetworkMode": "backend", "AutoRemove": False,
                           "RestartPolicy": {"Name": "unless-stopped"},
                           "PortBindings": {"9879/tcp": [{"HostIp": "127.0.0.1", "HostPort": "9879"}]}},
            "NetworkSettings": {"Networks": {
                "backend": {"Aliases": ["pax-manager", "old-id"], "IPAMConfig": None},
                "origin": {"Aliases": ["origin-manager"], "IPAMConfig": {"IPv4Address": "172.30.1.4"},
                           "DriverOpts": {"test": "value"}, "GwPriority": 1},
                "pax-search-private": {"Aliases": ["pax-manager"], "IPAMConfig": None}}}}


class FakeHost:
    def __init__(self):
        self.containers = {"pax-manager": original()}
        self.calls = []
        self.failure = None
        self.restore_failure = False

    def inspect(self, name):
        return copy.deepcopy(self.containers.get(name))

    def stop(self, name):
        self.calls.append(("stop", name))
        self.containers[name]["State"]["Running"] = False

    def rename(self, name, new):
        self.calls.append(("rename", name, new))
        self.containers[new] = self.containers.pop(name)

    def disconnect(self, network, name):
        self.calls.append(("disconnect", network, name))
        self.containers[name]["NetworkSettings"]["Networks"].pop(network, None)

    def connect(self, network, name, endpoint):
        self.calls.append(("connect", network, name))
        self.containers[name]["NetworkSettings"]["Networks"][network] = copy.deepcopy(endpoint)

    def create(self, name, body):
        self.calls.append(("create", name))
        self.containers[name] = {"Id": "new-id", "Image": body["Image"], "State": {"Running": False},
            "Config": {key: copy.deepcopy(value) for key, value in body.items()
                       if key not in ("HostConfig", "NetworkingConfig")},
            "HostConfig": copy.deepcopy(body["HostConfig"]), "Mounts": [],
            "NetworkSettings": {"Networks": copy.deepcopy(body["NetworkingConfig"]["EndpointsConfig"])}}
        if self.failure == "response_lost":
            raise tool.OperationError("DOCKER_FAILED")

    def start(self, name):
        self.calls.append(("start", name))
        if self.containers[name]["Id"] == "old-id" and self.restore_failure:
            raise tool.OperationError("DOCKER_FAILED")
        if self.containers[name]["Id"] == "new-id" and self.failure == "start":
            raise tool.OperationError("DOCKER_FAILED")
        self.containers[name]["State"]["Running"] = True

    def healthy(self, timeout):
        current = self.containers.get("pax-manager")
        return bool(current and current["State"]["Running"] and
                    not (current["Id"] == "new-id" and self.failure == "health"))


class ActivationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.agent = self.root / "agent"
        self.agent.mkdir()
        (self.agent / "agent.lock").touch()
        (self.agent / "active.json").write_text("{}")
        self.state = self.root / "operation"
        self.host = FakeHost()

    def activate(self, **changes):
        args = dict(manager="pax-manager", agent_state=self.agent, state_dir=self.state,
                    search_url="http://pax-search:9200", index="pax-search-v1", health_timeout=240)
        args.update(changes)
        return tool.activate(host=self.host, **args)

    def rollback(self):
        return tool.rollback(self.agent, self.state, 240, host=self.host)

    def saved(self):
        return json.loads((self.state / "state.json").read_text())

    def test_given_live_manager_when_activated_then_only_search_environment_changes(self):
        before = original()
        result = self.activate()
        after = self.host.inspect("pax-manager")
        self.assertEqual("activated", result["status"])
        self.assertEqual(IMAGE, after["Config"]["Image"])
        self.assertEqual(before["HostConfig"], after["HostConfig"])
        self.assertEqual(before["Config"]["Cmd"], after["Config"]["Cmd"])
        self.assertEqual(before["Config"]["Entrypoint"], after["Config"]["Entrypoint"])
        self.assertEqual(before["Config"]["Env"], after["Config"]["Env"][:4])
        env = dict(value.split("=", 1) for value in after["Config"]["Env"])
        self.assertEqual("http://pax-search:9200", env["SEARCH_URL"])
        self.assertEqual("pax-search-v1", env["SEARCH_INDEX"])
        self.assertEqual(64, len(env["SEARCH_READ_RECEIPT_SECRET"]))
        self.assertEqual(set(before["NetworkSettings"]["Networks"]), set(after["NetworkSettings"]["Networks"]))
        self.assertEqual({"IPv4Address": "172.30.1.4"}, after["NetworkSettings"]["Networks"]["origin"]["IPAMConfig"])
        self.assertNotIn("old-id", after["NetworkSettings"]["Networks"]["backend"]["Aliases"])
        self.assertEqual(0o700, self.state.stat().st_mode & 0o777)
        self.assertEqual(0o600, (self.state / "state.json").stat().st_mode & 0o777)
        self.assertEqual(0o600, (self.state / "container.before.json").stat().st_mode & 0o777)
        self.assertNotIn(env["SEARCH_READ_RECEIPT_SECRET"], json.dumps(result))
        backup = self.host.inspect(result["backup"])
        self.assertEqual("old-id", backup["Id"])
        self.assertFalse(backup["State"]["Running"])

    def test_given_valid_receipt_secret_when_activated_then_reuses_it(self):
        secret = "previous-" + "x" * 40
        self.host.containers["pax-manager"]["Config"]["Env"].extend([
            "SEARCH_URL=http://old:9200", "SEARCH_INDEX=old", "SEARCH_READ_RECEIPT_SECRET=" + secret])
        self.activate()
        env = self.host.inspect("pax-manager")["Config"]["Env"]
        self.assertEqual(1, sum(value.startswith("SEARCH_URL=") for value in env))
        self.assertIn("SEARCH_READ_RECEIPT_SECRET=" + secret, env)

    def test_given_active_or_uncertain_release_when_activating_then_refuses_without_mutation(self):
        for active in [{"task": {"id": "x"}}, {"blocked": True}, {"request_id": "uncertain"}]:
            with self.subTest(active=active):
                (self.agent / "active.json").write_text(json.dumps(active))
                with self.assertRaisesRegex(tool.OperationError, "AGENT_NOT_IDLE"):
                    self.activate()
                self.assertEqual([], self.host.calls)
                self.assertFalse(self.state.exists())

    def test_given_busy_release_lock_when_activating_then_refuses(self):
        import fcntl
        with (self.agent / "agent.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaisesRegex(tool.OperationError, "AGENT_BUSY"):
                self.activate()
        self.assertEqual([], self.host.calls)

    def test_given_unsafe_manager_when_activating_then_refuses_before_stopping(self):
        for kind in ["mount", "public", "auth", "image", "stopped", "network", "duplicate_env"]:
            with self.subTest(kind=kind):
                manager = original()
                if kind == "mount": manager["Mounts"] = [{"Source": "/private"}]
                if kind == "public": manager["HostConfig"]["PortBindings"]["9879/tcp"][0]["HostIp"] = "0.0.0.0"
                if kind == "auth": manager["Config"]["Env"][1] = "CLOUDFLARE_ACCESS_DISABLED=true"
                if kind == "image": manager["Image"] = "mutable:tag"
                if kind == "stopped": manager["State"]["Running"] = False
                if kind == "network": manager["HostConfig"]["NetworkMode"] = "host"
                if kind == "duplicate_env": manager["Config"]["Env"].append("UNCHANGED=shadow")
                self.host.containers["pax-manager"] = manager
                with self.assertRaises(tool.OperationError): self.activate()
                self.assertEqual([], self.host.calls)

    def test_given_failed_new_service_when_activating_then_restores_exact_old_and_retains_failed(self):
        for failure in ["start", "health", "response_lost"]:
            with self.subTest(failure=failure):
                self.setUp()
                self.host.failure = failure
                with self.assertRaisesRegex(tool.OperationError, "ACTIVATION_ROLLED_BACK"):
                    self.activate()
                state = self.saved()
                self.assertEqual("rolled_back", state["status"])
                self.assertEqual("old-id", self.host.inspect("pax-manager")["Id"])
                self.assertTrue(self.host.inspect("pax-manager")["State"]["Running"])
                self.assertEqual(set(original()["NetworkSettings"]["Networks"]), set(self.host.inspect("pax-manager")["NetworkSettings"]["Networks"]))
                self.assertEqual("new-id", self.host.inspect(state["failed"])["Id"])

    def test_given_activated_service_when_rollback_requested_then_restores_original_idempotently(self):
        self.activate()
        self.assertEqual("rolled_back", self.rollback()["status"])
        self.assertEqual("old-id", self.host.inspect("pax-manager")["Id"])
        calls = len(self.host.calls)
        self.rollback()
        self.assertEqual(calls, len(self.host.calls))

    def test_given_foreign_replacement_when_recovering_then_refuses_to_stop_it(self):
        self.activate()
        self.host.containers["pax-manager"]["Id"] = "foreign-id"
        calls = len(self.host.calls)
        with self.assertRaisesRegex(tool.OperationError, "FOREIGN_CONTAINER"):
            self.rollback()
        self.assertEqual(calls, len(self.host.calls))

    def test_given_failed_restore_when_activating_then_keeps_recovery_state(self):
        self.host.failure = "health"
        self.host.restore_failure = True
        with self.assertRaisesRegex(tool.OperationError, "ROLLBACK_FAILED"):
            self.activate()
        self.assertEqual("rollback_failed", self.saved()["status"])
        self.host.restore_failure = False
        self.assertEqual("rolled_back", self.rollback()["status"])

    def test_given_crash_after_old_rename_when_recovering_then_starts_old_with_all_networks(self):
        self.activate()
        state = self.saved()
        self.host.containers.pop("pax-manager")
        self.assertEqual("rolled_back", self.rollback()["status"])
        self.assertEqual("old-id", self.host.inspect("pax-manager")["Id"])

    def test_given_invalid_cli_values_when_activating_then_refuses(self):
        for change in [{"manager": "../bad"}, {"search_url": "http://user:secret@host:9200"},
                       {"search_url": "https://public.example"}, {"index": "UPPER"},
                       {"health_timeout": 0}, {"health_timeout": 301},
                       {"health_port": 0}, {"health_port": 65536}, {"index": "a" * 201}]:
            with self.subTest(change=change):
                with self.assertRaises(tool.OperationError): self.activate(**change)
                self.assertEqual([], self.host.calls)

    def test_given_dynamic_port_when_activating_then_preserves_it_for_recovery(self):
        self.host.containers["pax-manager"]["HostConfig"]["PortBindings"]["9879/tcp"][0]["HostPort"] = "55045"
        self.activate(health_port=55045)
        self.assertEqual(55045, self.saved()["health_port"])
        with self.assertRaisesRegex(tool.OperationError, "INVALID_HEALTH_PORT"):
            tool.rollback(self.agent, self.state, host=self.host, health_port=9879)
        self.assertEqual("rolled_back", self.rollback()["status"])

    def test_given_invalid_old_secret_when_activating_then_rotates_only_secret(self):
        self.host.containers["pax-manager"]["Config"]["Env"].append("SEARCH_READ_RECEIPT_SECRET=" + " " * 40)
        self.activate()
        env = tool.environment(self.saved()["desired_config"]["Env"])
        self.assertEqual(64, len(env["SEARCH_READ_RECEIPT_SECRET"]))
        self.assertNotIn(" ", env["SEARCH_READ_RECEIPT_SECRET"])

    def test_given_missing_agent_state_or_lock_when_activating_then_refuses(self):
        with self.assertRaisesRegex(tool.OperationError, "AGENT_STATE_MISSING"):
            self.activate(agent_state=self.root / "missing")
        (self.agent / "agent.lock").unlink()
        with self.assertRaisesRegex(tool.OperationError, "AGENT_LOCK_MISSING"):
            self.activate()

    def test_given_invalid_agent_json_when_activating_then_refuses(self):
        (self.agent / "active.json").write_text("invalid")
        with self.assertRaisesRegex(tool.OperationError, "AGENT_STATE_INVALID"):
            self.activate()

    def test_given_nonprivate_operation_state_when_recovering_then_refuses(self):
        self.activate()
        os.chmod(self.state, 0o755)
        with self.assertRaisesRegex(tool.OperationError, "UNSAFE_STATE_DIRECTORY"):
            self.rollback()
        os.chmod(self.state, 0o700)
        os.chmod(self.state / "state.json", 0o644)
        with self.assertRaisesRegex(tool.OperationError, "UNSAFE_STATE_FILE"):
            self.rollback()

    def test_given_invalid_operation_identity_when_recovering_then_refuses(self):
        self.activate()
        state = self.saved()
        for key, value in [("schema_version", 2), ("operation_id", "foreign"), ("backup", "foreign")]:
            changed = dict(state, **{key: value})
            tool.save_private(self.state / "state.json", changed)
            with self.assertRaisesRegex(tool.OperationError, "INVALID_STATE"):
                self.rollback()

    def test_given_foreign_backup_or_failed_container_when_recovering_then_leaves_all_untouched(self):
        self.activate()
        for name in [self.saved()["backup"], self.saved()["failed"]]:
            with self.subTest(name=name):
                previous = self.host.containers.get(name)
                self.host.containers[name] = dict(original(), Id="foreign")
                calls = len(self.host.calls)
                with self.assertRaisesRegex(tool.OperationError, "FOREIGN_CONTAINER"):
                    self.rollback()
                self.assertEqual(calls, len(self.host.calls))
                if previous: self.host.containers[name] = previous
                else: self.host.containers.pop(name)

    def test_given_missing_original_when_recovering_then_refuses(self):
        self.activate()
        self.host.containers.pop(self.saved()["backup"])
        with self.assertRaisesRegex(tool.OperationError, "ORIGINAL_MISSING"):
            self.rollback()

    def test_given_crash_at_each_switch_boundary_when_recovering_then_original_is_restored(self):
        for method in ["stop", "rename", "disconnect", "create", "start"]:
            with self.subTest(method=method):
                self.setUp()
                operation = getattr(self.host, method)
                interrupted = [False]
                def crash(*args):
                    result = operation(*args)
                    if not interrupted[0]:
                        interrupted[0] = True
                        raise KeyboardInterrupt()
                    return result
                setattr(self.host, method, crash)
                with self.assertRaises(KeyboardInterrupt): self.activate()
                setattr(self.host, method, operation)
                self.assertEqual("rolled_back", self.rollback()["status"])
                self.assertEqual("old-id", self.host.inspect("pax-manager")["Id"])
                self.assertEqual(original()["Config"]["Env"], self.host.inspect("pax-manager")["Config"]["Env"])

    def test_given_existing_operation_directory_when_activating_then_requires_explicit_recovery(self):
        self.state.mkdir()
        with self.assertRaisesRegex(tool.OperationError, "STATE_ALREADY_EXISTS"):
            self.activate()
        self.assertEqual([], self.host.calls)

    def test_given_cli_activate_and_recover_when_run_then_prints_only_safe_receipts(self):
        args = ["--agent-state", str(self.agent), "--state-dir", str(self.state)]
        with patch.object(tool, "DockerHost", return_value=self.host), patch("sys.stdout", new_callable=io.StringIO) as output:
            self.assertEqual(0, tool.main(["activate", *args]))
            self.assertEqual("activated", json.loads(output.getvalue())["status"])
            self.assertNotIn("DATABASE_URL", output.getvalue())
        with patch.object(tool, "DockerHost", return_value=self.host), patch("sys.stdout", new_callable=io.StringIO) as output:
            self.assertEqual(0, tool.main(["recover", *args]))
            self.assertEqual("rolled_back", json.loads(output.getvalue())["status"])
        with patch("sys.stderr", new_callable=io.StringIO) as output:
            self.assertEqual(1, tool.main(["activate", *args, "--search-url", "invalid-secret-value"]))
            self.assertNotIn("invalid-secret-value", output.getvalue())


class DockerAdapterTests(unittest.TestCase):
    def test_given_docker_requests_when_dispatched_then_paths_and_payloads_are_bounded(self):
        connection = Mock()
        response = connection.getresponse.return_value
        response.status, response.read.return_value = 200, b'{"Id":"id"}'
        with patch.object(tool, "UnixHTTP", return_value=connection):
            host = tool.DockerHost()
            self.assertEqual({"Id": "id"}, host.inspect("manager/a"))
            self.assertIn("manager%2Fa", connection.request.call_args.args[1])
            host.stop("manager")
            host.rename("manager", "backup")
            host.create("manager", {"Image": IMAGE})
            host.start("manager")
            host.disconnect("private", "manager")
            host.connect("private", "manager", {"Aliases": ["manager"]})
            payload = json.loads(connection.request.call_args.kwargs["body"])
            self.assertEqual({"Container": "manager", "EndpointConfig": {"Aliases": ["manager"]}}, payload)
            response.status = 404
            self.assertIsNone(host.inspect("missing"))
            response.status, response.read.return_value = 204, b""
            host.start("manager")
            self.assertTrue(connection.close.called)

    def test_given_docker_failures_when_dispatched_then_no_response_body_is_reported(self):
        for status, payload in [(500, b"SECRET"), (200, b"not-json"), (200, b"x" * (8 * 1024 * 1024 + 1))]:
            with self.subTest(status=status, size=len(payload)):
                connection = Mock()
                connection.getresponse.return_value.status = status
                connection.getresponse.return_value.read.return_value = payload
                with patch.object(tool, "UnixHTTP", return_value=connection):
                    with self.assertRaisesRegex(tool.OperationError, "^DOCKER_FAILED$"):
                        tool.DockerHost().inspect("manager")
        with patch.object(tool, "UnixHTTP") as connection:
            connection.return_value.request.side_effect = OSError("SECRET")
            with self.assertRaisesRegex(tool.OperationError, "^DOCKER_FAILED$"):
                tool.DockerHost().inspect("manager")

    def test_given_fixed_health_paths_when_ready_then_accepts_only_200_and_401(self):
        opener = Mock()
        response = Mock()
        response.__enter__ = Mock(return_value=Mock(status=200))
        response.__exit__ = Mock(return_value=False)
        denied = urllib.error.HTTPError("http://local", 401, "denied", {}, None)
        opener.open.side_effect = [response, denied]
        with patch.object(tool.urllib.request, "build_opener", return_value=opener):
            self.assertTrue(tool.DockerHost(55045).healthy(1))
        self.assertEqual("http://127.0.0.1:55045/health", opener.open.call_args_list[0].args[0])
        self.assertEqual("http://127.0.0.1:55045/api/v1/user/self/me", opener.open.call_args_list[1].args[0])

    def test_given_unavailable_health_when_timeout_expires_then_returns_false(self):
        opener = Mock()
        opener.open.side_effect = OSError("unavailable")
        with patch.object(tool.urllib.request, "build_opener", return_value=opener), \
                patch.object(tool.time, "monotonic", side_effect=[0, 0, 2]), \
                patch.object(tool.time, "sleep") as sleep:
            self.assertFalse(tool.DockerHost().healthy(1))
            sleep.assert_called_once_with(1)
        self.assertIsNone(tool.NoRedirect().redirect_request(None))

    def test_given_unix_socket_when_connecting_then_uses_local_docker_and_timeout(self):
        with patch.object(tool.socket, "socket") as socket:
            connection = tool.UnixHTTP("localhost", timeout=7)
            connection.connect()
            socket.return_value.settimeout.assert_called_once_with(7)
            socket.return_value.connect.assert_called_once_with("/var/run/docker.sock")


if __name__ == "__main__":
    unittest.main()
