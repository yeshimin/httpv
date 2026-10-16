# Configuration

HTTPV uses one versioned gateway document with four ordered collections:

- `upstreams`: HTTP origins that receive proxied traffic.
- `routes`: first-match routing rules that select an upstream.
- `subjects`: independent first-match traffic visualization and control scopes.
- `rules`: runtime block rules, optionally scoped to one subject.

`schema_version` is currently `1`. Unsupported schema versions are rejected
before they can be synchronized to OpenResty.

The default local file is [`config/httpv.json`](../config/httpv.json). An
upstream URL must be an `http` or `https` origin without a path, query, or
fragment. Request paths and query strings are preserved by the proxy.

## Matching

Routes and subjects use the same match fields:

```json
{
  "host": "api.example.test",
  "path_prefix": "/v1/",
  "method": "POST"
}
```

Empty `host` and `method` values match all values. An empty path prefix is
normalized to `/`. Collections are evaluated in file order; put more specific
matches before catch-all entries.

Routing and traffic control are intentionally separate. A request can be
routed without being displayed or gated, and multiple subjects can supervise
different portions of one upstream.

## Upstream pools

An upstream may contain one or more weighted targets:

```json
{
  "id": "orders",
  "name": "Orders",
  "targets": [
    {"id": "orders-a", "url": "http://orders-a:8080", "host_header": "orders", "weight": 3},
    {"id": "orders-b", "url": "http://orders-b:8080", "host_header": "orders", "weight": 1}
  ],
  "max_failures": 1,
  "failure_timeout_ms": 10000
}
```

Selection uses local weighted round-robin. A target returning 502, 503, or 504
is placed into passive cooldown after `max_failures`; later requests skip it
for `failure_timeout_ms`. HTTPV does not automatically replay the failed
request, which avoids duplicating non-idempotent methods. If every target is in
cooldown, requests are attempted across the full pool so recovery remains
possible. Legacy `url` and `host_header` fields are migrated to one target.

## Manual-gate protection

Each subject gate supports bounded waiting and explicit failure behavior:

```json
{
  "enabled": true,
  "timeout_ms": 30000,
  "timeout_action": "block",
  "max_pending": 2000,
  "overflow_action": "reject",
  "failure_action": "allow"
}
```

- `max_pending`: concurrent waiting requests for this subject; `0` is unlimited.
- `overflow_action`: `allow`, `block` (403), or `reject` (503).
- `failure_action`: `allow` for fail-open or `block` for fail-closed when the
  local pending store cannot reserve a decision slot.

The fast path uses an atomic per-subject counter. If a worker exits before it
can release a slot, the next overflow attempt reconciles that counter against
the TTL-backed pending keys before enforcing the limit.

## Detail capture

Each subject also has an opt-in `capture` policy for request/response headers
and bounded body previews. Capture is disabled by default and forces that
subject onto the Lua compatibility path. See [detail capture](detail-capture.md)
for the data path, redaction rules, limits, and TTL behavior.

## Validate and apply

Validate a document without changing the running gateway:

```sh
curl -fsS -X POST \
  -H 'Content-Type: application/json' \
  --data-binary @config/httpv.json \
  http://127.0.0.1:5173/api/config/validate
```

Apply it immediately:

```sh
curl -fsS -X PUT \
  -H 'Content-Type: application/json' \
  --data-binary @config/httpv.json \
  http://127.0.0.1:5173/api/config
```

An accepted document is validated, atomically persisted to the HTTPV runtime
volume, published to the native policy snapshot, and synchronized to OpenResty.
The runtime state takes precedence over the initial file after the first
operator change or restart recovery.

Administrators can perform the same validation, application, revision review,
and rollback from the **Gateway configuration** workbench in the console.

## Revisions and rollback

HTTPV retains the most recent 20 complete configuration snapshots. List them:

```sh
curl -fsS http://127.0.0.1:5173/api/config/revisions
```

Roll back to a retained revision:

```sh
curl -fsS -X POST http://127.0.0.1:5173/api/config/rollback/3
```

Rollback also requires `X-HTTPV-Confirm: rollback:3`. When authentication is
enabled, include the authenticated session cookie and its `X-HTTPV-CSRF` token.

Rollback is append-only: restoring revision 3 creates a new current revision
instead of deleting later history. `GET /api/config`, configuration updates,
and rollback responses expose the current value in the
`X-HTTPV-Config-Revision` response header.

## Native policy compatibility

The current native C policy snapshot accelerates a single managed subject and
up to 32 compatible block rules. Configurations with multiple subjects remain
fully functional but use the Lua compatibility path. This avoids applying one
subject's native rule to another subject while the multi-subject native format
is developed.

## Request ranking retention

The control service counts every distinct HTTP method and path observed during
its current process lifetime. The console requests only the leading 20 entries,
but lower-ranked entries are retained in memory and can be queried later with a
different `limit`:

```sh
curl -fsS 'http://127.0.0.1:5173/api/request-rankings?limit=100'
```

Use `limit=-1` to return all current entries. Rankings contain method, path, and
count only; they do not retain headers or bodies. Counts reset when the control
service restarts. Deployments with unbounded, high-cardinality literal paths
should normalize route parameters before long-running production use or export
the counts to a persistent metrics backend.
