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
grep -q 'org.freedesktop.login1.power-off' "$root/deploy/xkp-agent-power.rules" || fail 'power policy missing'
grep -q 'xkp-agent-power.rules' "$root/deploy/install.sh" || fail 'installer must deploy power policy'
grep -q 'command -v loginctl' "$root/deploy/install.sh" || fail 'installer must require loginctl'
grep -q 'docker info' "$root/deploy/install.sh" || fail 'installer must require a reachable Docker daemon'
grep -q 'sysbox-runc' "$root/deploy/install.sh" || fail 'installer must require sysbox-runc'
grep -q 'nvidia' "$root/deploy/install.sh" || fail 'installer must require NVIDIA container runtime'
grep -q 'environmentWorkspaceRoot:' "$root/deploy/install.sh" || fail 'installer must configure environment workspace root'
grep -q 'environmentWorkspaceRoot:' "$root/deploy/verify.sh" || fail 'verification must check environment workspace configuration'
grep -q 'docker info' "$root/deploy/verify.sh" || fail 'verification must check Docker access as the service account'
grep -q '/etc/polkit-1/rules.d' "$root/deploy/install.sh" || fail 'installer must require polkit'
grep -q '/usr/bin/loginctl' "$root/internal/power/controller_linux.go" || fail 'power controller must use fixed executable path'
grep -q -- '--purge-identity' "$root/deploy/uninstall.sh" || fail 'uninstall must preserve identity by default'
printf 'Agent deployment static tests passed.\n'
