#!/usr/bin/env python3
"""Read-only Ubuntu origin inventory; never print container credentials or commands."""

import json
import subprocess


def command(args):
    try:
        result = subprocess.run(args, capture_output=True, text=True, timeout=15, check=False)
        return result.stdout if result.returncode == 0 else None
    except (OSError, subprocess.TimeoutExpired):
        return None


def inventory():
    report = {
        "hostname": command(["hostname"]),
        "kernel": command(["uname", "-sr"]),
        "cloudflared_version": command(["cloudflared", "--version"]),
        "cloudflared_service": command([
            "systemctl", "show", "cloudflared", "--no-pager",
            "--property=ActiveState,SubState,FragmentPath",
        ]),
        "tcp_listeners": command(["ss", "-lnt"]),
        "containers": [],
    }
    raw = command(["docker", "ps", "--format", "{{json .}}"])
    report["docker_readable"] = raw is not None
    for line in (raw or "").splitlines():
        container = json.loads(line)
        name = container.get("Names", "")
        image = container.get("Image", "")
        if not any(term in (name + " " + image).lower()
                   for term in ("pax", "cloudflared", "postgres", "minio", "redis", "nginx", "caddy")):
            continue
        item = {"name": name, "image": image, "ports": container.get("Ports", "")}
        # Parse locally, and emit only the listed fields. Do not print raw inspect,
        # process arguments, tunnel tokens, database URLs, or the full environment.
        detail = command(["docker", "inspect", container["ID"]])
        if detail:
            obj = json.loads(detail)[0]
            item["network_mode"] = obj.get("HostConfig", {}).get("NetworkMode")
            item["port_bindings"] = obj.get("HostConfig", {}).get("PortBindings")
            item["networks"] = list(obj.get("NetworkSettings", {}).get("Networks", {}))
            item["mounts"] = [
                {key: mount.get(key) for key in ("Type", "Source", "Destination", "RW")}
                for mount in obj.get("Mounts", [])
            ]
            allowed = {"PORT", "CLOUDFLARE_ACCESS_DISABLED", "ALLOW_LOCAL_USER_HEADER"}
            item["auth_flags"] = {}
            for entry in obj.get("Config", {}).get("Env", []):
                key, separator, value = entry.partition("=")
                if separator and key in allowed:
                    item["auth_flags"][key] = value
            labels = obj.get("Config", {}).get("Labels") or {}
            item["compose_files"] = labels.get("com.docker.compose.project.config_files")
            item["compose_directory"] = labels.get("com.docker.compose.project.working_dir")
        report["containers"].append(item)
    return report


if __name__ == "__main__":
    print(json.dumps(inventory(), indent=2))
