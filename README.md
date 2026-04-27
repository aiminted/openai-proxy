# OpenAI Proxy

Drop-in OpenAI API proxy that issues per-user keys, enforces rate and token quotas, and records every request. Clients change two environment variables — no code changes — and traffic flows through the proxy to the real OpenAI API. Management lives in a separate Next.js admin at [aiminted/openai-proxy-admin](https://github.com/aiminted/openai-proxy-admin).

## Features

- 100% compatible with the OpenAI REST API (catch-all `/v1/*`, including streaming, function calling, vision, audio, files)
- Per-key rate limit (RPM), token quota, dollar quota, and expiry
- Per-request usage records with model, tokens, cost, and latency
- Automatic `stream_options.include_usage` injection so streaming requests are billed accurately
- JSON admin API with Bearer-token auth and CORS for an external admin UI

## Client Usage

```bash
export OPENAI_API_KEY=sk-pxy-...        # the key issued by the admin
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
  -e CORS_ORIGINS=https://admin.example.com \
  injundev/openai-proxy
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `OPENAI_API_KEY` | — | Real OpenAI key used for all upstream traffic (required) |
| `DATABASE_URL` | — | Postgres connection URL (required) |
| `REDIS_URL` | — | Redis connection URL (required) |
| `ADMIN_PASSWORD` | — | Admin password used for the admin login API (required) |
| `SESSION_SECRET` | — | Random string for signing admin tokens, 16+ chars (required) |
| `CORS_ORIGINS` | — | Comma-separated origins allowed to call `/admin/api/*` (e.g. `https://admin.example.com`). Required for cross-origin admin UI |
| `LISTEN_ADDR` | `:8080` | Server listen address |
| `UPSTREAM_URL` | `https://api.openai.com` | Upstream API base URL |
| `KEY_PREFIX` | `sk-pxy-` | Prefix for issued keys |
| `PRICING_PATH` | `pricing.yaml` | Per-1M-token pricing table for cost tracking |
| `STREAM_TIMEOUT` | `30m` | Maximum duration for a single streamed response |
| `VERIFY_CACHE_TTL` | `60s` | How long Redis caches a verified key before re-checking the database |
| `SESSION_TTL` | `24h` | Admin token lifetime |

## Admin API

All endpoints under `/admin/api/*` require `Authorization: Bearer <token>` except `POST /admin/api/login`.

```
POST   /admin/api/login                    {password}                    → {token, expires_in}
GET    /admin/api/me                                                     → {authenticated}
GET    /admin/api/stats                                                  → {total_keys, active_keys, today_tokens, today_cost_usd}
GET    /admin/api/keys
POST   /admin/api/keys                     {owner, note, expires_at, rpm_limit, token_quota, dollar_quota}
GET    /admin/api/keys/{id}
PATCH  /admin/api/keys/{id}                same body shape as POST
DELETE /admin/api/keys/{id}
POST   /admin/api/keys/{id}/active         {active: true|false}
GET    /admin/api/usage?days=30
GET    /admin/api/usage/{id}/recent?limit=50
```

## Pricing Table

`pricing.yaml` ships with current OpenAI prices. Edit the file and restart to update. Models not listed are recorded with cost `0`; token counts are still tracked.
