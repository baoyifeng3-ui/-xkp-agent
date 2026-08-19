#!/usr/bin/env bash
set -Eeuo pipefail
config=/etc/xkp-agent/agent.yml
[[ -x /usr/local/bin/xkp-agent ]] || { printf 'Agent binary missing\n' >&2; exit 1; }
[[ -f "$config" ]] || { printf 'Agent configuration missing\n' >&2; exit 1; }
[[ $(stat -c '%a' "$config") == 600 ]] || { printf 'Agent configuration must be 0600\n' >&2; exit 1; }
grep -q '^agentId:' "$config" || { printf 'Agent has not enrolled\n' >&2; exit 1; }
grep -q '^credential:' "$config" || { printf 'Agent credential missing\n' >&2; exit 1; }
grep -q '^environmentWorkspaceRoot:' "$config" || { printf 'Environment workspace root missing\n' >&2; exit 1; }
runuser -u xkp-agent -- docker info >/dev/null 2>&1 || { printf 'Agent cannot access Docker daemon\n' >&2; exit 1; }
docker_runtimes=$(docker info --format '{{json .Runtimes}}')
[[ "$docker_runtimes" == *'sysbox-runc'* ]] || { printf 'sysbox-runc is unavailable\n' >&2; exit 1; }
[[ "$docker_runtimes" == *'nvidia'* ]] || { printf 'NVIDIA Container Runtime is unavailable\n' >&2; exit 1; }
nvidia-smi >/dev/null 2>&1 || { printf 'NVIDIA GPU is unavailable\n' >&2; exit 1; }
systemctl is-enabled --quiet xkp-agent.service
systemctl is-active --quiet xkp-agent.service
printf 'XKP Agent verification passed.\n'
