# OpenAI Proxy

Drop-in OpenAI API proxy that issues per-user keys, enforces rate and token quotas, and records every request. Clients change two environment variables — no code changes — and traffic flows through the proxy to the real OpenAI API.

## Features

- 100% compatible with the OpenAI REST API (catch-all `/v1/*`, including streaming, function calling, vision, audio, files)
- Per-key rate limit (RPM), token quota, dollar quota, and expiry
- Web dashboard for issuing, revoking, and inspecting keys
- Per-request usage records with model, tokens, cost, and latency
- Automatic `stream_options.include_usage` injection so streaming requests are billed accurately

## Client Usage

```bash
export OPENAI_API_KEY=sk-pxy-...        # the key issued by the dashboard
export OPENAI_BASE_URL=https://your-proxy.example.com/v1
```

Existing OpenAI SDK code keeps working unchanged.

## Server Usage

```bash
docker run -d -p 8080:8080 \
  -e OPENAI_API_KEY=sk-... \
  -e DATABASE_URL=postgres://... \
  -e REDIS_URL=redis://... \
  -e ADMIN_PASSWORD=... \
  -e SESSION_SECRET=$(openssl rand -hex 32) \
  injundev/openai-proxy
```

Open `http://localhost:8080/login` and sign in with `ADMIN_PASSWORD` to issue keys.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `OPENAI_API_KEY` | — | Real OpenAI key used for all upstream traffic (required) |
| `DATABASE_URL` | — | Postgres connection URL (required) |
| `REDIS_URL` | — | Redis connection URL (required) |
| `ADMIN_PASSWORD` | — | Password for the admin dashboard (required) |
| `SESSION_SECRET` | — | Random string for signing session cookies, 16+ chars (required) |
| `LISTEN_ADDR` | `:8080` | Server listen address |
| `UPSTREAM_URL` | `https://api.openai.com` | Upstream API base URL |
| `KEY_PREFIX` | `sk-pxy-` | Prefix for issued keys |
| `PRICING_PATH` | `pricing.yaml` | Per-1M-token pricing table for cost tracking |
| `STREAM_TIMEOUT` | `30m` | Maximum duration for a single streamed response |
| `VERIFY_CACHE_TTL` | `60s` | How long Redis caches a verified key before re-checking the database |
| `SESSION_TTL` | `24h` | Admin session cookie lifetime |
| `COOKIE_SECURE` | `true` | Set to `false` to allow the admin cookie over plain HTTP |

## Pricing Table

`pricing.yaml` ships with current OpenAI prices. Edit the file and restart to update. Models not listed are recorded with cost `0`; token counts are still tracked.
