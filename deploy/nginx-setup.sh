#!/bin/bash
# Prepare nginx on CentOS 7 to reverse-proxy the relay.
# - Installs nginx if missing
# - Generates a 10-year self-signed cert (replace with Let's Encrypt later)
# - Drops our config file into /etc/nginx/conf.d/
# - Opens the firewall ports
#
# Re-run any time safely.
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "ERROR: must run as root (use sudo)" >&2
  exit 1
fi

NGINX_CONF_SRC="$(cd "$(dirname "$0")" && pwd)/nginx-sync.he66.cn.conf"
NGINX_CONF_DST=/etc/nginx/conf.d/sync.he66.cn.conf
SSL_DIR=/etc/nginx/ssl

echo "==> ensuring nginx is installed"
if ! command -v nginx >/dev/null; then
  yum install -y epel-release
  yum install -y nginx
fi

echo "==> ensuring firewalld is open"
if command -v firewall-cmd >/dev/null && systemctl is-active --quiet firewalld; then
  firewall-cmd --permanent --add-service=http
  firewall-cmd --permanent --add-service=https
  firewall-cmd --reload
fi

echo "==> generating self-signed cert (10y) at $SSL_DIR"
mkdir -p "$SSL_DIR"
if [[ ! -f "$SSL_DIR/sync.he66.cn.crt" ]]; then
  openssl req -x509 -nodes -days 3650 \
    -newkey rsa:2048 \
    -keyout "$SSL_DIR/sync.he66.cn.key" \
    -out    "$SSL_DIR/sync.he66.cn.crt" \
    -subj "/CN=sync.he66.cn" \
    -addext "subjectAltName=DNS:sync.he66.cn"
  chmod 0600 "$SSL_DIR/sync.he66.cn.key"
fi
if [[ ! -f "$SSL_DIR/dhparam.pem" ]]; then
  openssl dhparam -out "$SSL_DIR/dhparam.pem" 2048
fi

echo "==> installing nginx config"
install -m 0644 "$NGINX_CONF_SRC" "$NGINX_CONF_DST"

echo "==> testing config"
nginx -t

echo "==> reloading"
systemctl enable nginx
systemctl reload nginx

echo ""
echo "DONE."
echo "Test from outside:"
echo "  curl -k https://sync.he66.cn/healthz   # should print 'ok'"
echo "  curl -k https://sync.he66.cn/v1/stats   # should print JSON"
echo "  wss://sync.he66.cn/v1/rooms/1234/ws?device=test   (in Android client)"
echo ""
echo "NOTE: when you have real DNS for sync.he66.cn, replace the self-signed"
echo "cert with Let's Encrypt:"
echo "  yum install -y certbot python3-certbot-nginx"
echo "  certbot --nginx -d sync.he66.cn"