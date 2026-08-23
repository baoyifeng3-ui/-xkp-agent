#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
[[ "$(id -u)" == "0" ]] || die '请使用 sudo bash one-click-install.sh 执行'

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
agent_root=$(cd "$script_dir/.." && pwd)
binary="$agent_root/dist/xkp-agent-linux-amd64"
ca_file="$script_dir/ca.crt"

if [[ -f "$script_dir/install.sh" ]]; then
  sed -i 's/\r$//' "$script_dir/install.sh"
fi
[[ -f /etc/os-release ]] || die '无法识别操作系统'
source /etc/os-release
[[ "${ID:-}" == 'ubuntu' ]] || die '正式 Agent 仅支持 Ubuntu'
[[ "$(uname -m)" == 'x86_64' ]] || die '正式 Agent 仅支持 amd64/x86_64'

if [[ ! -d /etc/polkit-1/rules.d ]]; then
  echo '正在安装 polkit 依赖...'
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y policykit-1 polkitd
  install -d -m 0755 /etc/polkit-1/rules.d
fi

[[ -f "$binary" ]] || die "找不到 Agent 二进制：$binary"
chmod 0755 "$binary"
[[ -f "$ca_file" ]] || die "找不到 CA 证书：$ca_file"

default_url='https://172.16.33.182:19443'
default_name=$(hostname)
default_workspace='/srv/xkp'
printf '管理平台地址 [%s]: ' "$default_url"; read -r management_url
management_url=${management_url:-$default_url}
printf '服务器备注名称 [%s]: ' "$default_name"; read -r display_name
display_name=${display_name:-$default_name}
printf '工作目录 [%s]: ' "$default_workspace"; read -r workspace
workspace=${workspace:-$default_workspace}
printf '一次性注册凭据: '; read -r -s registration_token; printf '\n'
[[ -n "$registration_token" ]] || die '一次性注册凭据不能为空'

bash "$script_dir/install.sh" \
  --binary "$binary" \
  --management-url "$management_url" \
  --ca "$ca_file" \
  --registration-token "$registration_token" \
  --display-name "$display_name" \
  --workspace "$workspace"

unset registration_token
echo 'Agent 一键安装完成。'
echo '检查状态：systemctl status xkp-agent --no-pager'
