# HHC Web API

Website backend and CMS core for `www.alive.org.tw`.

The service owns CMS source records, revisions/publication workflows, weekly bulletin metadata, public projections, and website product orchestration. Public routes read published projections only. Protected admin routes require the Azure Container Apps Dapr token, the `api-gateway` Dapr caller identity, sanitized `X-HHC-*` identity headers, and scope/resource authorization.

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
- `GET /api/bulletins/by-number/{issueNumber}`
- `GET /api/bulletins/{issueDate}`
- `GET /api/admin/bulletins`
- `GET /api/admin/bulletins/{issueId}`
- `POST /api/admin/bulletins`
- `POST /api/admin/bulletins/{issueId}/upload-sessions`
- `POST /api/admin/bulletins/{issueId}/assets/{assetId}/complete`
- `POST /api/admin/bulletins/{issueId}/publish`
- `POST /api/admin/bulletins/{issueId}/unpublish`
- `GET /api/news`, `/api/history`, `/api/videos`, `/api/home`
- `GET/POST /api/admin/content/{news|history|videos}`
- `GET/PUT /api/admin/content/{module}/{contentId}`
- `POST /api/admin/content/{module}/{contentId}/{publish|unpublish}`
- `GET /api/admin/content/{module}/{contentId}/revisions`
- `POST /api/admin/content/{module}/{contentId}/revisions/{revision}/restore`
- `/api/admin/campaigns*` and `/api/admin/campaign-schedules*` proxy authorized CMS operators to the private engagement service.
- `GET /api/meetings`, `/api/meetings/{meetingKey}`, and `/api/meeting-occurrences` expose 30-second cached public meeting projections.
- `GET /api/meeting-sync-windows` requires trusted gateway identity and returns only time boundaries; collection access remains authorized by Asset API ACLs.
- `/api/admin/operations/{church-units|resources|meetings}*` requires `cms:read` or `cms:write`; creates require `Idempotency-Key` and mutations require `If-Match`.
- `/priv/meeting-{occurrences|sync-windows}` accepts Dapr callers configured by `OPERATIONS_ALLOWED_CALLER_APP_IDS` (production: `asset-api,hhc-line-function-bot`). The sync-window read also accepts the one configured Asset scan-warmer managed identity through verified internal-ingress workload authentication.

Admin writes require `If-Match` after creation. Publish is asynchronous and returns `202`; public visibility changes only after the asset grant workflow completes. News edits keep the previous published projection live until the replacement asset grant is ready.

Weekly bulletin uploads are orchestrated here, while `asset-api` owns the upload target, private object, ClamAV status, and public read grant. The browser never chooses an asset namespace or owner. PDF uploads are limited to 20 MiB.

News, history, and video content share lifecycle, locale, revision, and public-projection behavior while retaining typed module tables and validation. Public routes never read drafts.

News image uploads are coordinated through content-owned upload routes. Detail and home images are optional; when attached, each image must be owned by that item, pass ClamAV, finish responsive derivative processing, and receive its own public read grant before publication. The public home image falls back to the detail image. Unpublish removes the projection immediately and revokes every public grant asynchronously while retaining private assets for revisions and republishing.

## Production release

GitHub Actions builds one immutable image for the migration job and runtime. The release stops if the manual Container Apps migration job fails. See [`infra/README.md`](infra/README.md).
