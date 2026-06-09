# task_bridge — Incorporated ClickUp API v2 reference

**Revision:** 1
**Last modified:** 2026-06-09T00:00:00Z
**Status:** P1 skeleton. Every endpoint the engine incorporates is documented
here (§11.4.8). Items marked `UNCONFIRMED:` are verified against the
`raksul/go-clickup` source (P1) or by live API probe (P2) before use — never
guessed (§11.4.6).

Base URL: `https://api.clickup.com/api/v2`
Auth header: `Authorization: <personal_token>` (NOT `Bearer`; token begins `pk_`,
never expires). The token is injected by the consumer and never logged.

## Tasks

| Op | Method + path | Notes |
|---|---|---|
| Create | `POST /list/{list_id}/task` | confirmed |
| List | `GET /list/{list_id}/task` | 100/page, `page` 0-indexed, `date_updated_gt` (Unix ms) incremental key |
| Get | `GET /task/{task_id}` | `UNCONFIRMED:` verify via go-clickup `tasks.go` (P1) |
| Update | `PUT /task/{task_id}` | does NOT update custom fields |
| Delete | `DELETE /task/{task_id}` | `UNCONFIRMED:` verify P1; only under AllowRemoteDelete |
| Set custom field | `POST /task/{task_id}/field/{field_id}` | `UNCONFIRMED:` verify via go-clickup `custom_fields.go` (P1) |

## Folders / Lists

| Op | Method + path | Notes |
|---|---|---|
| Get folders | `GET /space/{space_id}/folder` | confirmed |
| Get lists | `GET /folder/{folder_id}/list` | `UNCONFIRMED:` verify P1 |
| Folderless lists | `GET /space/{space_id}/list` | `UNCONFIRMED:` verify P1 |

URL→ID resolution: extract numeric segments from the injected `CLICKUP_FOLDER` /
`CLICKUP_BOARD`, validate each candidate against the API, keep the resolved one
(P2.2 — probe, not guess).

## Webhooks

| Op | Method + path | Notes |
|---|---|---|
| Create | `POST /team/{team_id}/webhook` | `UNCONFIRMED:` verify P1; one location filter (folder_id) |
| Events | — | `taskCreated/Updated/Deleted/StatusUpdated/AssigneeUpdated/TagUpdated/PriorityUpdated/Moved` |
| Signature | `X-Signature` header | HMAC-SHA256, hex, of the raw body, keyed by `webhook.secret`; verify before acting |

## Rate limits

100 req/min/token (Free/Unlimited/Business). Headers `X-RateLimit-Limit /
-Remaining / -Reset`. 429 over-limit. The engine reads the headers at runtime
(token-bucket throttle) rather than hardcoding the tier.
