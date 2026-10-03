# Security baseline

HTTPV supports local token authentication, role-based authorization, CSRF
protection, dangerous-action confirmation, and an append-only audit log.

## Roles

- `viewer`: read configuration, events, policy state, and the live WebSocket.
- `operator`: viewer access plus request decisions, emergency blocking, test
  traffic, and dynamic rules.
- `admin`: operator access plus subjects, full configuration, revisions,
  rollback, and audit records.

Configure one or more tokens through environment variables:

```text
HTTPV_VIEWER_TOKEN
HTTPV_OPERATOR_TOKEN
HTTPV_ADMIN_TOKEN
```

When no token is configured, authentication is disabled for local development
and requests run with the admin role. `compose.production.json` requires an
admin token. Do not commit tokens to the repository or place them in the
gateway JSON document.

Successful login creates an eight-hour, in-memory session using an HttpOnly,
SameSite=Strict cookie. Every authenticated mutation also requires the
session-specific `X-HTTPV-CSRF` token returned by the login or status endpoint.

## Dangerous actions

Emergency blocking and rollback require an additional exact confirmation:

```text
X-HTTPV-Confirm: block-all-pending
X-HTTPV-Confirm: rollback:<revision>
```

This is enforced by the Control API, not only by the browser.

## Audit log

Set `HTTPV_AUDIT_LOG_PATH` to persist JSON Lines audit records. The supplied
Compose stacks use `/run/httpv/audit.jsonl`. Records contain timestamp, role,
action, target, and outcome; authentication tokens and CSRF tokens are never
logged. Admins can read the most recent records from `GET /api/audit`.

## Current boundary

This baseline is intended for a trusted single-node administrative network.
OIDC, user identities, external session storage, TLS termination, control-plane
mTLS, and multi-node audit aggregation remain future work. Put the console
behind TLS before exposing it outside localhost.

