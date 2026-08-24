#!/usr/bin/env bash
set -Eeuo pipefail
root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root_dir"
command -v go >/dev/null || { echo 'Go 1.22+ is required' >&2; exit 1; }
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/xkp-agent-linux-amd64 ./cmd/xkp-agent
if command -v file >/dev/null && file dist/xkp-agent-linux-amd64 | grep -qi 'dynamically linked'; then
  echo 'ERROR: Agent must be statically linked' >&2
  exit 1
fi
echo 'Static Ubuntu-compatible Agent built.'
