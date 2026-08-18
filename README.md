# XKP5.0 Processing Server Agent

The Agent runs on an Ubuntu AMD64 processing server and reports CPU, RAM, disk,
NVIDIA GPU, Docker, and environment status to the XKP5.0 management server.

Build with Go 1.22 or the repository Docker workflow, then install on Ubuntu:

```bash
sudo ./deploy/install.sh \
  --binary ./dist/xkp-agent-linux-amd64 \
  --management-url https://192.168.1.10:19443 \
  --ca ./ca.crt \
  --registration-token 'one-time-token' \
  --display-name 'GPU Server 01' \
  --workspace /srv/xkp
```

The installer consumes the registration token once. Persistent Agent identity is
stored in `/etc/xkp-agent/agent.yml` with mode `0600`. The service connects
outbound; do not expose Docker TCP or an Agent management port to the LAN.

Use `sudo ./deploy/verify.sh` to check the installation. Uninstall preserves the
Agent identity unless both `--purge-identity` and `--confirm-purge` are supplied.

## Build and verify

```bash
go test -race ./...
go vet ./...
GOOS=linux GOARCH=amd64 go build -trimpath -o dist/xkp-agent-linux-amd64 ./cmd/xkp-agent
sha256sum dist/xkp-agent-linux-amd64
```

Windows is supported for development builds only. Production requires Ubuntu
22.04 amd64, systemd, Docker Engine, the NVIDIA driver/NVML for GPU metrics, and a
workspace directory on the intended data filesystem.

## Enrollment and identity

The management URL must use the management server's fixed LAN IP and port 19443.
The supplied CA must validate that exact IP SAN. The registration token is valid
for one use and ten minutes; the installer places it only in a root-only runtime
file and removes it after successful enrollment. It is never embedded in the
systemd unit or persistent YAML.

`/etc/xkp-agent/agent.yml` contains the persistent Agent ID and random credential.
Back up is not required, but preserve it across ordinary upgrades and reinstalls.
Never send this file in support logs. To deliberately create a new identity,
remove the old Agent in XKP5.0 and run uninstall with both identity-purge flags
before requesting another token.

## Operation and recovery

```bash
sudo systemctl status xkp-agent
sudo journalctl -u xkp-agent --since '30 minutes ago'
sudo ./deploy/verify.sh
sudo systemctl restart xkp-agent
```

The Agent reports every five seconds and considers CPU, memory, system disk,
workspace disk, NVIDIA GPU, and Docker independent data sources. A missing GPU,
Docker daemon, or workspace mount is reported as a stable collector error while
available metrics continue to flow. Network failures use capped retry with jitter;
the credential remains local and reconnect is automatic.

This release polls the authenticated command endpoint but does not execute any
server power or container commands. Do not add shell command execution or expose
the Docker socket over TCP as a workaround.

Uninstall while retaining identity:

```bash
sudo ./deploy/uninstall.sh
```

Only for permanent retirement after removing the Agent in the management UI:

```bash
sudo ./deploy/uninstall.sh --purge-identity --confirm-purge
```
