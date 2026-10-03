# Configuration

HTTPV uses one versioned gateway document with four ordered collections:

- `upstreams`: HTTP origins that receive proxied traffic.
- `routes`: first-match routing rules that select an upstream.
- `subjects`: independent first-match traffic visualization and control scopes.
- `rules`: runtime block rules, optionally scoped to one subject.

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

## Native policy compatibility

The current native C policy snapshot accelerates a single managed subject and
up to 32 compatible block rules. Configurations with multiple subjects remain
fully functional but use the Lua compatibility path. This avoids applying one
subject's native rule to another subject while the multi-subject native format
is developed.
