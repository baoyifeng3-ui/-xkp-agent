#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

usage() {
  printf 'Usage: sudo install.sh --binary FILE --management-url URL --ca FILE --registration-token TOKEN --display-name NAME --workspace PATH\n'
}
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
binary= management_url= ca_file= registration_token= display_name= workspace=
while (($#)); do
  case "$1" in
    --binary) binary=${2:-}; shift 2;; --management-url) management_url=${2:-}; shift 2;;
    --ca) ca_file=${2:-}; shift 2;; --registration-token) registration_token=${2:-}; shift 2;;
    --display-name) display_name=${2:-}; shift 2;; --workspace) workspace=${2:-}; shift 2;;
    -h|--help) usage; exit 0;; *) die "Unknown argument: $1";;
  esac
done
[[ ${EUID:-$(id -u)} == 0 ]] || die 'Run as root'
# Production installation is deliberately limited to the accepted target.
source /etc/os-release
[[ ${ID:-} == ubuntu ]] || die 'Production installation requires Ubuntu (ID=ubuntu)'
[[ $(uname -m) == x86_64 ]] || die 'Production installation requires amd64/x86_64'
[[ -f "$binary" && -x "$binary" ]] || die 'Agent binary is missing or not executable'
[[ -f "$ca_file" ]] || die 'CA certificate is missing'
[[ "$management_url" == https://* ]] || die 'Management URL must use HTTPS'
[[ "$workspace" == /* ]] || die 'Workspace must be absolute'
[[ -n "$registration_token" && -n "$display_name" ]] || die 'Registration token and display name are required'
[[ "$management_url$display_name$workspace" != *$'\n'* ]] || die 'Arguments must not contain newlines'
getent group docker >/dev/null || die 'Docker group does not exist; install Docker first'
command -v docker >/dev/null || die 'Docker CLI is required'
docker info >/dev/null 2>&1 || die 'Docker daemon is unavailable'
docker_runtimes=$(docker info --format '{{json .Runtimes}}')
[[ "$docker_runtimes" == *'sysbox-runc'* ]] || die 'Docker runtime sysbox-runc is required'
[[ "$docker_runtimes" == *'nvidia'* ]] || die 'NVIDIA Container Runtime is required'
command -v nvidia-smi >/dev/null || die 'NVIDIA driver tools are required'
nvidia-smi >/dev/null 2>&1 || die 'NVIDIA GPU is unavailable'
command -v loginctl >/dev/null || die 'loginctl is required for power control'
[[ -d /etc/polkit-1/rules.d ]] || die 'polkit rules directory is missing; install polkit first'

id xkp-agent >/dev/null 2>&1 || useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin xkp-agent
for group_name in docker video render; do getent group "$group_name" >/dev/null && usermod -aG "$group_name" xkp-agent; done
install -d -m 0700 -o xkp-agent -g xkp-agent /etc/xkp-agent
install -d -m 0750 -o xkp-agent -g xkp-agent "$workspace"
environment_workspace=$workspace/environments
install -d -m 0750 -o xkp-agent -g xkp-agent "$environment_workspace"
install -m 0755 "$binary" /usr/local/bin/xkp-agent
install -m 0644 "$ca_file" /etc/xkp-agent/ca.crt
install -m 0644 "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/xkp-agent-power.rules" \
  /etc/polkit-1/rules.d/60-xkp-agent-power.rules
install -m 0600 -o xkp-agent -g xkp-agent /dev/null /etc/xkp-agent/agent.yml

yaml_escape() { local value=${1//\\/\\\\}; value=${value//\"/\\\"}; printf '%s' "$value"; }
{
  printf 'managementUrl: "%s"\n' "$(yaml_escape "$management_url")"
  printf 'caCertificate: "/etc/xkp-agent/ca.crt"\n'
  printf 'workspacePath: "%s"\n' "$(yaml_escape "$workspace")"
  printf 'environmentWorkspaceRoot: "%s"\n' "$(yaml_escape "$environment_workspace")"
} > /etc/xkp-agent/agent.yml
chown xkp-agent:xkp-agent /etc/xkp-agent/agent.yml
chmod 0600 /etc/xkp-agent/agent.yml

install -d -m 0700 -o xkp-agent -g xkp-agent /run/xkp-agent
token_file=/run/xkp-agent/enrollment.token
printf '%s' "$registration_token" > "$token_file"
chown xkp-agent:xkp-agent "$token_file"; chmod 0600 "$token_file"
unset registration_token
runuser -u xkp-agent -- /usr/local/bin/xkp-agent --config /etc/xkp-agent/agent.yml \
  --enrollment-token-file "$token_file" --display-name "$display_name" --enroll-only
[[ ! -e "$token_file" ]] || die 'Enrollment token was not removed'

service_template="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/xkp-agent.service"
service_temp=$(mktemp)
trap 'rm -f "$service_temp"' EXIT
while IFS= read -r line || [[ -n "$line" ]]; do printf '%s\n' "${line//@@WORKSPACE@@/$workspace}"; done < "$service_template" > "$service_temp"
install -m 0644 "$service_temp" /etc/systemd/system/xkp-agent.service
systemctl daemon-reload
systemctl enable --now xkp-agent.service
"$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/verify.sh"
printf 'XKP Agent installed and enrolled.\n'
