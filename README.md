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
