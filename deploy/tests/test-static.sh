#!/usr/bin/env bash
set -Eeuo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

for script in "$root/deploy"/*.sh "$root/deploy/tests"/*.sh; do bash -n "$script" || fail "syntax: $script"; done
unit="$root/deploy/xkp-agent.service"
grep -q '^Restart=on-failure' "$unit" || fail 'service must restart on failure'
grep -q '^User=root' "$unit" || fail 'terminal-capable service must run as root'
grep -q '^Group=root' "$unit" || fail 'terminal-capable service group must be root'
grep -q '/etc/xkp-agent/agent.yml' "$unit" || fail 'service config path missing'
grep -q '^ProtectSystem=strict' "$unit" || fail 'filesystem hardening missing'
grep -q '^PrivateDevices=false' "$unit" || fail 'NVML device access must remain available'
grep -q 'SupplementaryGroups=.*docker' "$unit" || fail 'Docker socket group access missing'
if grep -Eqi 'registration.?token|enrollment.?token|secret' "$unit"; then fail 'service unit must not contain enrollment secrets'; fi
grep -q 'install -m 0600' "$root/deploy/install.sh" || fail 'installer must create root-only configuration'
grep -q 'ID=ubuntu' "$root/deploy/install.sh" || fail 'installer must require Ubuntu'
grep -q 'x86_64' "$root/deploy/install.sh" || fail 'installer must require amd64'
if grep -Eq 'polkit|xkp-agent-power.rules|command -v loginctl' "$root/deploy/install.sh"; then
  fail 'root Agent installer must not require polkit or loginctl'
fi
if grep -Eq 'apt-get.*polkit|policykit-1|polkitd' "$root/deploy/one-click-install.sh"; then
  fail 'one-click installer must not install polkit packages'
fi
grep -q 'docker info' "$root/deploy/install.sh" || fail 'installer must require a reachable Docker daemon'
grep -q 'sysbox-runc' "$root/deploy/install.sh" || fail 'installer must require sysbox-runc'
grep -q 'nvidia' "$root/deploy/install.sh" || fail 'installer must require NVIDIA container runtime'
grep -q 'environmentWorkspaceRoot:' "$root/deploy/install.sh" || fail 'installer must configure environment workspace root'
grep -q 'environmentWorkspaceRoot:' "$root/deploy/verify.sh" || fail 'verification must check environment workspace configuration'
grep -q '/bin/bash' "$root/deploy/verify.sh" || fail 'verification must require the fixed terminal shell'
grep -q 'User=root' "$root/deploy/verify.sh" || fail 'verification must check the systemd root identity'
grep -q 'docker info' "$root/deploy/verify.sh" || fail 'verification must check Docker access as the service account'
grep -q '/usr/bin/loginctl' "$root/internal/power/controller_linux.go" || fail 'power controller must use fixed executable path'
grep -q '/usr/bin/systemctl' "$root/internal/power/controller_linux.go" || fail 'power controller must fall back to systemctl'
grep -q '/usr/sbin/shutdown' "$root/internal/power/controller_linux.go" || fail 'power controller must fall back to shutdown'
grep -q -- '--purge-identity' "$root/deploy/uninstall.sh" || fail 'uninstall must preserve identity by default'
config_example="$root/deploy/agent.yml.example"
grep -q '^managementUrl: "https://' "$config_example" || fail 'production config example must use HTTPS'
grep -q '^development: false' "$config_example" || fail 'production config example must disable development mode'
printf 'Agent deployment static tests passed.\n'
