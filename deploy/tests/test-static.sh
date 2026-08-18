#!/usr/bin/env bash
set -Eeuo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

for script in "$root/deploy"/*.sh "$root/deploy/tests"/*.sh; do bash -n "$script" || fail "syntax: $script"; done
unit="$root/deploy/xkp-agent.service"
grep -q '^Restart=on-failure' "$unit" || fail 'service must restart on failure'
grep -q '/etc/xkp-agent/agent.yml' "$unit" || fail 'service config path missing'
grep -q '^ProtectSystem=strict' "$unit" || fail 'filesystem hardening missing'
grep -q '^PrivateDevices=false' "$unit" || fail 'NVML device access must remain available'
grep -q 'SupplementaryGroups=.*docker' "$unit" || fail 'Docker socket group access missing'
if grep -Eqi 'registration.?token|enrollment.?token|secret' "$unit"; then fail 'service unit must not contain enrollment secrets'; fi
grep -q 'install -m 0600' "$root/deploy/install.sh" || fail 'installer must create root-only configuration'
grep -q 'ID=ubuntu' "$root/deploy/install.sh" || fail 'installer must require Ubuntu'
grep -q 'x86_64' "$root/deploy/install.sh" || fail 'installer must require amd64'
grep -q -- '--purge-identity' "$root/deploy/uninstall.sh" || fail 'uninstall must preserve identity by default'
printf 'Agent deployment static tests passed.\n'
