#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
[[ "$(id -u)" == "0" ]] || die '请使用 sudo bash deploy/one-click-install.sh 执行'
root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
binary="$root_dir/dist/xkp-agent-linux-amd64"
ca_file="$root_dir/deploy/ca.crt"
[[ -x "$binary" ]] || { [[ -f "$binary" ]] && chmod 0755 "$binary" || die "找不到 Agent 二进制：$binary"; }
[[ -f "$ca_file" ]] || die "找不到 CA 证书：$ca_file"
[[ -f /etc/os-release ]] || die '无法识别操作系统'
source /etc/os-release
[[ "${ID:-}" == ubuntu ]] || die '正式 Agent 仅支持 Ubuntu'
command -v docker >/dev/null || die '请先安装 Docker'
docker info >/dev/null 2>&1 || die 'Docker 服务未运行'
command -v nvidia-smi >/dev/null || die '未找到 NVIDIA 驱动工具'
install -d -m 0755 /etc/polkit-1/rules.d
if ! dpkg -s policykit-1 >/dev/null 2>&1; then
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y policykit-1
  if apt-cache show polkitd 2>/dev/null | grep -q '^Package: polkitd$'; then
    DEBIAN_FRONTEND=noninteractive apt-get install -y polkitd
  fi
fi
printf '管理平台地址 [https://172.16.33.182:19443]: '; read -r management_url
management_url=${management_url:-https://172.16.33.182:19443}
printf '服务器备注名称 [%s]: ' "$(hostname)"; read -r display_name
display_name=${display_name:-$(hostname)}
printf '工作目录 [/srv/xkp]: '; read -r workspace
workspace=${workspace:-/srv/xkp}
printf '一次性注册凭据: '; read -r -s registration_token; printf '\n'
[[ -n "$registration_token" ]] || die '一次性注册凭据不能为空'
"$root_dir/deploy/install.sh" --binary "$binary" --management-url "$management_url" \
  --ca "$ca_file" --registration-token "$registration_token" \
  --display-name "$display_name" --workspace "$workspace"
unset registration_token
echo 'XKP Agent 安装完成。'
echo '检查命令：sudo systemctl status xkp-agent --no-pager'
