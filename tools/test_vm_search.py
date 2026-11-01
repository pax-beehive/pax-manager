"""Exercise the VM search helper through its real Bash entry point."""

import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "deploy" / "vm" / "search.sh"
TRACED_LINES = set()

FAKE_DOCKER = r'''#!/usr/bin/env python3
import json
import os
from pathlib import Path
import sys

path = Path(os.environ["VM_SEARCH_TEST_STATE"])
state = json.loads(path.read_text())
args = sys.argv[1:]
state["calls"].append(args)
code, output = 0, ""
search = state["search_name"]
manager = state["manager_name"]
failure = state.get("fail_command")
if failure and args[:len(failure)] == failure:
    code = 1
elif args[:2] == ["network", "inspect"]:
    code = 0 if state["network"] else 1
    if "--format" in args:
        output = "\n".join(state["members"])
elif args[:2] == ["network", "create"]:
    state["network"] = True
elif args[:2] == ["volume", "inspect"]:
    code = 0 if state["volume"] else 1
elif args[:2] == ["volume", "create"]:
    state["volume"] = True
elif args[:2] == ["container", "inspect"]:
    exists = state["search_exists"] if args[2] == search else state["manager_exists"]
    code = 0 if exists else 1
elif args[:2] == ["network", "connect"]:
    if not state["network"]:
        code = 1
    elif args[3] not in state["members"]:
        state["members"].append(args[3])
elif args[:1] == ["run"]:
    state["search_exists"] = True
    state["running"] = True
    state["members"].append(args[args.index("--name") + 1])
elif args[:1] == ["start"]:
    code = 0 if state["search_exists"] else 1
    state["running"] = state["search_exists"]
elif args[:1] == ["exec"]:
    sequence = state.get("ready_sequence", [])
    ready = sequence.pop(0) if sequence else state["ready"]
    code = 0 if ready and state["running"] else 1
elif args[:1] == ["logs"]:
    output = "synthetic Elasticsearch startup diagnostic"
elif args[:1] == ["stop"]:
    code = 0 if state["search_exists"] else 1
    state["running"] = False
elif args[:1] == ["inspect"]:
    code = 0 if state["search_exists"] else 1
    output = "running" if state["running"] else "exited"
else:
    code, output = 98, "unexpected fake Docker command: " + repr(args)
path.write_text(json.dumps(state))
if output:
    print(output)
sys.exit(code)
'''


class VMSearchTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="pax-vm-search-test-")
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.state_path = self.directory / "state.json"
        docker = self.directory / "docker"
        docker.write_text(FAKE_DOCKER)
        docker.chmod(0o700)
        shell_env = self.directory / "bash-env"
        shell_env.write_text("PS4='+${BASH_SOURCE}:${LINENO}: '\n")
        self.env = dict(os.environ, PATH=str(self.directory) + os.pathsep + os.environ["PATH"],
                        VM_SEARCH_TEST_STATE=str(self.state_path), BASH_ENV=str(shell_env),
                        SEARCH_READY_ATTEMPTS="2", SEARCH_READY_INTERVAL="0")
        for variable in ["SEARCH_CONTAINER", "SEARCH_NETWORK", "SEARCH_VOLUME", "SEARCH_IMAGE",
                         "MANAGER_CONTAINER"]:
            self.env.pop(variable, None)
        self.state = dict(network=False, volume=False, search_exists=False,
                          manager_exists=True, members=[], running=False, ready=True,
                          calls=[], search_name="pax-search", manager_name="pax-manager")

    def run_script(self, action="start", expected=0):
        self.state_path.write_text(json.dumps(self.state))
        command = ["bash", "-x", str(SCRIPT)] + ([] if action is None else [action])
        result = subprocess.run(command, env=self.env,
                                text=True, capture_output=True, timeout=10, check=False)
        self.state = json.loads(self.state_path.read_text())
        TRACED_LINES.update(int(line) for line in re.findall(
            r"^\++" + re.escape(str(SCRIPT)) + r":(\d+):", result.stderr, re.MULTILINE))
        if expected is None:
            self.assertNotEqual(0, result.returncode, result.stderr)
        else:
            self.assertEqual(expected, result.returncode, result.stderr)
        return result

    def commands(self, prefix):
        return [command for command in self.state["calls"] if command[:len(prefix)] == prefix]

    def test_new_container_is_private_persistent_and_ready_before_manager_attach(self):
        self.run_script(None)
        command = self.commands(["run"])[0]
        for expected in ["unless-stopped", "2g", "discovery.type=single-node",
                         "xpack.security.enabled=false", "ES_JAVA_OPTS=-Xms512m -Xmx512m",
                         "pax-search-data:/usr/share/elasticsearch/data",
                         "docker.elastic.co/elasticsearch/elasticsearch:9.5.5"]:
            self.assertIn(expected, command)
        self.assertNotIn("-p", command)
        self.assertNotIn("--publish", command)
        self.assertIn("--health-cmd", command)
        self.assertIn("nofile=65535:65535", command)
        probe = self.commands(["exec"])[0]
        self.assertIn("--max-time", probe)
        self.assertIn("http://localhost:9200/_cluster/health?wait_for_status=yellow&timeout=1s", probe)
        attach = ["network", "connect", "pax-search-private", "pax-manager"]
        self.assertLess(self.state["calls"].index(probe), self.state["calls"].index(attach))

    def test_existing_stopped_container_is_started_and_network_membership_repaired(self):
        self.state.update(network=True, volume=True, search_exists=True)
        self.run_script()
        self.assertEqual([["start", "pax-search"]], self.commands(["start"]))
        self.assertEqual([], self.commands(["run"]))
        self.assertEqual([], self.commands(["network", "create"]))
        self.assertEqual([], self.commands(["volume", "create"]))
        self.assertIn(["network", "connect", "pax-search-private", "pax-search"], self.state["calls"])
        self.assertIn("pax-manager", self.state["members"])

    def test_repeated_start_keeps_existing_members_and_durable_volume(self):
        self.state.update(network=True, volume=True, search_exists=True, running=True,
                          members=["pax-search", "pax-manager"])
        self.run_script()
        self.assertEqual([], self.commands(["network", "connect"]))
        self.assertEqual([], self.commands(["volume", "create"]))
        self.assertEqual([], self.commands(["volume", "rm"]))

    def test_start_without_manager_still_waits_for_search(self):
        self.state["manager_exists"] = False
        self.run_script()
        self.assertTrue(self.commands(["exec"]))
        self.assertNotIn("pax-manager", self.state["members"])

    def test_readiness_retries_transient_failure(self):
        self.state["ready_sequence"] = [False, True]
        self.run_script()
        self.assertEqual(2, len(self.commands(["exec"])))
        self.assertIn("pax-manager", self.state["members"])

    def test_readiness_failure_is_bounded_and_never_attaches_manager(self):
        self.state["ready"] = False
        result = self.run_script(expected=None)
        self.assertEqual(2, len(self.commands(["exec"])))
        self.assertNotIn("pax-manager", self.state["members"])
        self.assertIn("did not become ready", result.stderr)
        self.assertTrue(self.commands(["logs"]))

    def test_attach_stop_and_status_remain_compatible_and_health_reports_readiness(self):
        self.state.update(network=True, volume=True, search_exists=True, running=True,
                          members=["pax-search"])
        self.run_script("attach-manager")
        self.assertIn("pax-manager", self.state["members"])
        self.assertEqual("running", self.run_script("status").stdout.strip())
        self.assertEqual("ready", self.run_script("health").stdout.strip())
        self.run_script("stop")
        self.assertFalse(self.state["running"])
        self.assertTrue(self.state["volume"])
        self.assertEqual("exited", self.run_script("status").stdout.strip())
        self.assertEqual("not ready", self.run_script("health", expected=1).stdout.strip())

    def test_absent_manager_attach_is_a_noop_and_unknown_action_is_rejected(self):
        self.state["manager_exists"] = False
        self.run_script("attach-manager")
        self.assertEqual([], self.commands(["network", "connect"]))
        self.assertIn("Usage:", self.run_script("unknown", expected=2).stderr)

    def test_docker_failure_aborts_before_container_creation(self):
        for prefix in [["network", "create"], ["volume", "create"]]:
            with self.subTest(command=prefix):
                self.state.update(network=False, volume=False, calls=[], fail_command=prefix)
                self.run_script(expected=None)
                self.assertEqual([], self.commands(["run"]))

    def test_failed_diagnostics_preserve_readiness_failure(self):
        self.state.update(ready=False, fail_command=["logs"])
        self.run_script(expected=1)
        self.assertEqual(2, len(self.commands(["exec"])))

    def test_default_wait_parameters_allow_immediate_readiness(self):
        self.env.pop("SEARCH_READY_ATTEMPTS")
        self.env.pop("SEARCH_READY_INTERVAL")
        self.run_script()
        self.assertEqual(1, len(self.commands(["exec"])))

    def test_invalid_wait_limits_are_rejected_before_docker(self):
        for name, value in [("SEARCH_READY_ATTEMPTS", "0"), ("SEARCH_READY_ATTEMPTS", "x"),
                            ("SEARCH_READY_ATTEMPTS", "01"),
                            ("SEARCH_READY_ATTEMPTS", "301"), ("SEARCH_READY_INTERVAL", "-1"),
                            ("SEARCH_READY_INTERVAL", "x"), ("SEARCH_READY_INTERVAL", "61")]:
            with self.subTest(name=name, value=value):
                self.env[name] = value
                self.state["calls"] = []
                self.run_script(expected=2)
                self.assertEqual([], self.state["calls"])
                self.env[name] = "2" if name == "SEARCH_READY_ATTEMPTS" else "0"

    def test_custom_resource_names_are_passed_as_separate_arguments(self):
        self.env.update(SEARCH_CONTAINER="test-search", SEARCH_NETWORK="test-network",
                        SEARCH_VOLUME="test-volume", SEARCH_IMAGE="registry.test/search:fixed",
                        MANAGER_CONTAINER="test-manager")
        self.state.update(search_name="test-search", manager_name="test-manager")
        self.run_script()
        self.assertIn("test-volume:/usr/share/elasticsearch/data", self.commands(["run"])[0])
        self.assertIn(["network", "connect", "test-network", "test-manager"], self.state["calls"])


if __name__ == "__main__":
    result = unittest.main(exit=False)
    print("VM search helper traced source lines: " + ",".join(map(str, sorted(TRACED_LINES))))
    raise SystemExit(not result.result.wasSuccessful())
