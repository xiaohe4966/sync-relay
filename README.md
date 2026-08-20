# squadsync-relay

Tiny WebSocket fan-out server for SquadSync. Lets two phones in *different*
networks (different Wi-Fi, behind NAT, etc.) still control each other.

## Build

```bash
go build -o squadsync-relay .
```

The only dependency is `github.com/gorilla/websocket`. `go mod tidy` will
fetch it for you on first build.

## Run

```bash
./squadsync-relay                     # listens on :7879
./squadsync-relay -addr 0.0.0.0:7879 # explicit bind
```

Open firewall / port-forward `7879` if you want it reachable from outside.

## Endpoints

| Method | Path                                | Description                       |
|--------|-------------------------------------|-----------------------------------|
| GET    | `/healthz`                         | `ok` (liveness probe)             |
| GET    | `/v1/ping`                          | `pong` (basic latency check)      |
| GET    | `/v1/stats`                         | `{"rooms":N,"clients":M}` JSON    |
| WS     | `/v1/rooms/{room}/ws?device={name}` | Bidirectional fan-out by room     |

The WS path is just a transparent forwarder — the Android client speaks
the same `Wire` JSON over LAN, and the relay does not inspect the payload.

## Deploying behind HTTPS (recommended)

If you put the relay behind nginx / caddy / cloudflare-tunnel, terminate
TLS there and forward `Upgrade` / `Connection: Upgrade` headers:

```nginx
# nginx example
location /squadsync/ {
    proxy_pass http://127.0.0.1:7879/;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_read_timeout 86400;
}
```

The Android client just needs the `wss://your-host/squadsync/v1/...` URL.

---

## Deploying on CentOS 7 (sync.he66.cn)

A pre-built `squadsync-relay.linux-amd64` is included. It's a single 5.6 MB
**statically linked** binary — no glibc / no external libs.

### One-time install

```bash
# 1. Copy these 3 files to the server (e.g. to /root/squadsync/):
#      squadsync-relay.linux-amd64
#      deploy/install.sh
#      deploy/nginx-setup.sh
#      deploy/nginx-sync.he66.cn.conf

# 2. Install the relay as a systemd service
sudo bash install.sh
#   - creates user 'squadsync' (no login)
#   - installs to /opt/squadsync-relay/
#   - sets up /etc/systemd/system/squadsync-relay.service
#   - opens firewall, starts it
#   - prints healthz / stats

# 3. Install nginx + self-signed cert + drop reverse-proxy config
sudo bash nginx-setup.sh
#   - installs nginx if missing
#   - generates a 10-year self-signed cert for sync.he66.cn
#   - drops /etc/nginx/conf.d/sync.he66.cn.conf
#   - opens 80/443 in firewalld
#   - reloads nginx
```

### Verify

```bash
curl -k https://sync.he66.cn/healthz    # → "ok"
curl -k https://sync.he66.cn/v1/stats    # → {"rooms":0,"clients":0}
```

### Replace the self-signed cert with Let's Encrypt (after DNS is set up)

```bash
sudo yum install -y certbot python3-certbot-nginx
sudo certbot --nginx -d sync.he66.cn
```

Certbot rewrites the nginx config to use the new cert path and reloads.

### Operate

```bash
sudo systemctl status squadsync-relay
sudo journalctl -u squadsync-relay -f
sudo systemctl restart squadsync-relay
```

### On the Android client

1. Open SquadSync
2. Expand **配置**
3. Fill **转发服务器 URL** = `wss://sync.he66.cn` (or `ws://<server-IP>:7879` if bypassing nginx)
4. Tap **试连** — dot should turn green when connected
5. Volume / brightness / play / launch-app now go through the relay, so
   peers anywhere on the Internet in the same room receive the commands.

### Cross-compile (if you need to rebuild on a Mac)

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" \
  -o squadsync-relay.linux-amd64 .
```

`CGO_ENABLED=0` keeps the binary statically linked (no glibc dependency),
which is what lets it run on CentOS 7's glibc 2.17.