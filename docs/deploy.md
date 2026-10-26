# Production Deployment Guide: Deploying `exnode` Behind a Reverse Proxy

This guide covers running `exnode` on the public internet behind a TLS-terminating reverse proxy (Caddy or NGINX), configuring persistent checkpoint caching with automated pruning, enforcing per-client rate limits, and securing sensitive internal endpoints.

---

## 1. Architecture Overview

```
                      +---------------------------------------+
                      |               Host                    |
Internet              |                                       |
-------- (HTTPS:443) -> [ Reverse Proxy: Caddy or NGINX ]     |
                      |   - Terminates TLS                    |
                      |   - Strips/sets trusted client IP     |
                      |   - Routes /v1/* to exnode            |
                      |   - Protects /metrics (internal only) |
                      +-------------------+-------------------+
                                          |
                               (HTTP:8080 | docker network)
                                          v
                                   [ exnode ]
                                          |
                               (Persistent Volume)
                                          v
                                [ /var/cache/exnode ]
```

### Key Security & Operational Principles
1. **Never expose `exnode` directly to `0.0.0.0:8080`**: Only bind `exnode` within Docker's internal network or to `127.0.0.1`.
2. **Endpoint exposure**:
   - **Publicly expose**: `/v1/bundle` (proof requests), `/v1/status` (archive tip / RPC lag).
   - **Internal only**: `/metrics` (Prometheus telemetry should be accessible only from internal monitoring networks or scrapers).
3. **Accurate rate limiting**: Configure `EXNODE_CLIENT_IP_HEADER` to match the header populated by your reverse proxy so `exnode` throttles by genuine client IP rather than the proxy's internal IP.
4. **Persistent cache storage**: Mount a dedicated Docker volume or host directory to `/var/cache/exnode` and prune periodically.

---

## 2. Docker Compose Deployment (Caddy with Automatic TLS)

Caddy handles automatic HTTPS certificate provisioning (via Let's Encrypt / ZeroSSL) and reverse proxying with minimal configuration.

### `compose.prod.yml`

```yaml
services:
  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config
    depends_on:
      - exnode

  exnode:
    image: ghcr.io/externalize-labs/exnode:latest
    restart: unless-stopped
    environment:
      EXNODE_NETWORK: public
      EXNODE_RPC: https://mainnet.sorobanrpc.com
      EXNODE_ADDR: :8080
      EXNODE_RATE: 5
      EXNODE_BURST: 20
      EXNODE_CLIENT_IP_HEADER: X-Forwarded-For
      EXNODE_CACHE: /var/cache/exnode
      EXNODE_CORS: "*"
    volumes:
      - exnode-cache:/var/cache/exnode
    healthcheck:
      test: ["CMD", "/exnode", "version"]
      interval: 30s
      timeout: 5s
      retries: 3

volumes:
  caddy_data:
  caddy_config:
  exnode-cache:
```

### `Caddyfile`

```caddyfile
api.example.com {
    # Public API endpoints
    handle /v1/* {
        reverse_proxy exnode:8080 {
            header_up X-Forwarded-For {remote_host}
        }
    }

    # Restrict Prometheus metrics to private network
    handle /metrics {
        @internal remote_ip 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.1
        handle @internal {
            reverse_proxy exnode:8080
        }
        respond "Forbidden" 403
    }

    # Fallback for root or unknown paths
    handle {
        respond "Not Found" 404
    }
}
```

---

## 3. Alternative Reverse Proxy: NGINX with Let's Encrypt

If your infrastructure standardizes on NGINX:

### `nginx.conf`

```nginx
upstream exnode_backend {
    server exnode:8080;
    keepalive 32;
}

server {
    listen 80;
    server_name api.example.com;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl http2;
    server_name api.example.com;

    ssl_certificate /etc/letsencrypt/live/api.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/api.example.com/privkey.pem;

    # Public bundle and status endpoints
    location /v1/ {
        proxy_pass http://exnode_backend;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr; # Overwrite, do not append untrusted headers
        proxy_set_header X-Forwarded-Proto $scheme;

        # Proof generation can take up to a few seconds for large ledgers
        proxy_read_timeout 60s;
        proxy_send_timeout 60s;
    }

    # Internal metrics endpoint
    location /metrics {
        allow 10.0.0.0/8;
        allow 172.16.0.0/12;
        allow 192.168.0.0/16;
        allow 127.0.0.1;
        deny all;

        proxy_pass http://exnode_backend;
    }
}
```

---

## 4. Cache Volume Sizing and Automated Pruning

### Sizing Guidelines
`exnode` caches checkpoint files (`history/...`) fetched from Stellar history archives to avoid repeated downloads when generating proofs.
- **Minimal cache**: 5 GB (serves queries near the current ledger tip).
- **Recommended cache**: 20 GB – 50 GB (supports proof generation over several weeks of historical checkpoints).
- **High throughput / broad historical range**: 100 GB+.

### Automated Cache Pruning
`exnode` includes a built-in `cache prune` subcommand that purges the oldest cached files until the directory is under a specified threshold:

```bash
exnode cache prune --cache /var/cache/exnode --max-size 20GB
```

#### Setting up a Scheduled Host Cron Job
To ensure the Docker volume does not grow unbounded, run a prune job daily:

```bash
# /etc/cron.daily/exnode-cache-prune
#!/bin/sh
docker run --rm \
  -v exnode-cache:/var/cache/exnode \
  ghcr.io/externalize-labs/exnode:latest \
  cache prune --cache /var/cache/exnode --max-size 20GB
```

Make the script executable:
```bash
chmod +x /etc/cron.daily/exnode-cache-prune
```

---

## 5. Rate Limiting and Client IP Headers

`exnode` provides built-in token-bucket rate limiting per client IP address on `/v1/*` routes:
- `--rate`: average requests per second per IP (default: `5.0`; set `0` to disable).
- `--burst`: burst size allowance (default: `20.0`).
- `--client-ip-header`: Header name to extract the client IP from (e.g., `X-Forwarded-For` or `CF-Connecting-IP`).

> **Security Note:** Only specify `--client-ip-header` if your reverse proxy strictly **overwrites** (rather than appends to) this header. If clients can forge this header, they could bypass rate limits.

---

## 6. Verification and Testing

After deploying with `docker compose -f compose.prod.yml up -d`:

1. **Verify TLS and Status Endpoint:**
   ```bash
   curl -I https://api.example.com/v1/status
   # Expected: HTTP/2 200 OK
   ```

2. **Verify Bundle Generation:**
   ```bash
   curl -s "https://api.example.com/v1/status" | jq .
   ```

3. **Verify Protected Metrics:**
   ```bash
   curl -I https://api.example.com/metrics
   # Expected: HTTP/2 403 Forbidden (from external network)
   ```

4. **Verify Rate Limiting:**
   Rapidly querying the `/v1/status` endpoint beyond the burst limit should return `HTTP 429 Too Many Requests`.
