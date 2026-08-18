#!/usr/bin/env bash
set -Eeuo pipefail
purge=0; confirm=0
while (($#)); do
  case "$1" in --purge-identity) purge=1; shift;; --confirm-purge) confirm=1; shift;; -h|--help) printf 'Usage: uninstall.sh [--purge-identity --confirm-purge]\n'; exit 0;; *) printf 'Unknown argument: %s\n' "$1" >&2; exit 2;; esac
done
[[ ${EUID:-$(id -u)} == 0 ]] || { printf 'Run as root\n' >&2; exit 1; }
systemctl disable --now xkp-agent.service >/dev/null 2>&1 || true
rm -f /etc/systemd/system/xkp-agent.service /usr/local/bin/xkp-agent
systemctl daemon-reload
if ((purge)); then
  ((confirm)) || { printf 'Identity purge requires --confirm-purge\n' >&2; exit 1; }
  rm -rf /etc/xkp-agent
else
  printf 'Preserved /etc/xkp-agent/agent.yml and CA certificate.\n'
fi
printf 'XKP Agent uninstalled.\n'
