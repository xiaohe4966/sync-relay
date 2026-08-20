#!/bin/bash
# Install squadsync-relay as a systemd service on CentOS 7 / RHEL 7 / RHEL 8+.
# Run as root: sudo bash install.sh
set -euo pipefail

SERVICE_USER=squadsync
SERVICE_DIR=/opt/squadsync-relay
BINARY_SRC="$(cd "$(dirname "$0")" && pwd)/squadsync-relay.linux-amd64"
SERVICE_FILE=/etc/systemd/system/squadsync-relay.service

if [[ $EUID -ne 0 ]]; then
  echo "ERROR: must run as root (use sudo)" >&2
  exit 1
fi
if [[ ! -f "$BINARY_SRC" ]]; then
  echo "ERROR: cannot find $BINARY_SRC" >&2
  echo "Build it first:" >&2
  echo "  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o squadsync-relay.linux-amd64 ." >&2
  exit 1
fi

echo "==> creating user '$SERVICE_USER' (if missing)"
id "$SERVICE_USER" &>/dev/null || useradd --system --no-create-home --shell /sbin/nologin "$SERVICE_USER"

echo "==> installing binary to $SERVICE_DIR"
mkdir -p "$SERVICE_DIR"
install -m 0755 "$BINARY_SRC" "$SERVICE_DIR/squadsync-relay"

echo "==> writing systemd unit at $SERVICE_FILE"
cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=SquadSync WebSocket relay
Documentation=https://github.com/squadsync/relay
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
WorkingDirectory=$SERVICE_DIR
ExecStart=$SERVICE_DIR/squadsync-relay -addr 0.0.0.0:7879
Restart=always
RestartSec=2
LimitNOFILE=65535

# Hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
RestrictRealtime=true
RestrictNamespaces=true

[Install]
WantedBy=multi-user.target
EOF

echo "==> reloading systemd and enabling"
systemctl daemon-reload
systemctl enable squadsync-relay.service
systemctl restart squadsync-relay.service

echo ""
echo "Service status:"
systemctl --no-pager status squadsync-relay.service --lines=0 || true
echo ""
echo "Quick checks:"
sleep 1
curl -fsS http://127.0.0.1:7879/healthz && echo
curl -fsS http://127.0.0.1:7879/v1/stats && echo

echo ""
echo "==> DONE. Listening on 0.0.0.0:7879."
echo "    Logs:  sudo journalctl -u squadsync-relay -f"
echo "    Test:  curl http://127.0.0.1:7879/healthz"