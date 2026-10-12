#!/usr/bin/env bash
# PAX device setup. Deployment-owned endpoints; no credentials in this script.
# Define the complete entrypoint before executing a streamed response.
pax_setup_main() (
  set -euo pipefail
  export PAX_DOWNLOAD_URL={{quote .DownloadURL}}
  export PAX_CLOUD_URL={{quote .MachineURL}}
  export PAX_SETUP_AFTER_INSTALL=1
  # An empty value deliberately selects browser sign-in.
  export PAX_REGISTRATION_TOKEN="${PAX_REGISTRATION_TOKEN:-}"
  command -v curl >/dev/null || { echo 'PAX setup requires curl.' >&2; exit 1; }
  local setup_dir installer first_line
  setup_dir=$(mktemp -d)
  trap 'rm -rf "$setup_dir"' EXIT
  installer="$setup_dir/install.sh"
  curl -fsSL --max-redirs 1 "$PAX_DOWNLOAD_URL/api/v1/public/paxd/install.sh" -o "$installer"
  IFS= read -r first_line < "$installer" || true
  case "$first_line" in
    '#!/usr/bin/env bash'|'#!/bin/bash'|'#!/bin/sh') ;;
    *) echo 'Expected a shell installer; the download endpoint returned an empty or non-shell response.' >&2; exit 1 ;;
  esac
  bash -n "$installer"
  bash "$installer"
)
pax_setup_main
