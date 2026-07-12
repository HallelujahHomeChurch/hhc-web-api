# HHC Web API

Website backend and CMS core for `www.alive.org.tw`.

The service owns CMS source records, revisions/publication workflows, weekly bulletin metadata, public projections, and website product orchestration. Public routes read published projections only. Protected admin routes trust sanitized `X-HHC-*` identity headers from `api-gateway` and repeat scope/resource authorization.

There is no separate v1 `cms-api` or `bulletin-api`.

## Local Development

```sh
cp .env.example .env
set -a && source .env && set +a
go run ./cmd/server
```

## Initial Routes

- `GET /health`
- `GET /ready`
- `GET /api/bulletins`
- `GET /api/bulletins/latest`
- `GET /api/bulletins/{issueDate}`
- `GET /api/admin/bulletins`
- `GET /api/admin/bulletins/{issueId}`
- `POST /api/admin/bulletins`
- `POST /api/admin/bulletins/{issueId}/versions`
- `POST /api/admin/bulletins/{issueId}/publish`
- `POST /api/admin/bulletins/{issueId}/unpublish`

Admin writes require `If-Match` after creation. Publish is asynchronous and returns `202`; public visibility changes only after the asset grant workflow completes.
