# Single-node deployment

HTTPV currently supports one edge node. The gateway request path remains local:
OpenResty makes routing and policy decisions without calling the Control API.

## Development stack

Start the local stack:

```sh
docker compose -f compose.json up --build -d
```

The default endpoints are:

- Gateway: `http://127.0.0.1:8088`
- Console: `http://127.0.0.1:5173`
- Control API: `http://127.0.0.1:8090`
- Edge metrics: `http://127.0.0.1:9102`

The edge agent re-resolves the control-plane address every second by default
(`HTTPV_AGENT_CONTROL_DNS_REFRESH_MS`). Telemetry therefore resumes automatically
after a control container or service is replaced and receives a new address.

## Hardened local deployment

The production-oriented Compose file removes the bundled demo dependency,
serves the console as static files with Nginx, adds health checks and restart
policies, keeps the Control API private, and binds the console to localhost by
default:

```sh
export HTTPV_ADMIN_TOKEN='replace-with-a-long-random-value'
HTTPV_UPSTREAM_URL=http://host.docker.internal:9998 \
HTTPV_UPSTREAM_HOST=127.0.0.1:9998 \
docker compose -f compose.production.json up --build -d
```

Optional variables:

- `HTTPV_CONFIG_FILE`: host path to the gateway JSON document.
- `HTTPV_GATEWAY_BIND` and `HTTPV_GATEWAY_PORT`: public gateway binding.
- `HTTPV_CONSOLE_BIND` and `HTTPV_CONSOLE_PORT`: console binding.
- `HTTPV_EDGE_METRICS_PORT`: localhost metrics port.
- `HTTPV_VERSION`: local image tag.
- `HTTPV_ADMIN_TOKEN`: required production administrator token.
- `HTTPV_OPERATOR_TOKEN` and `HTTPV_VIEWER_TOKEN`: optional lower-privilege tokens.
- `HTTPV_PROXY_CONNECT_TIMEOUT`: upstream connection timeout, default `5s`.
- `HTTPV_PROXY_READ_TIMEOUT` and `HTTPV_PROXY_SEND_TIMEOUT`: default `60s`.
- `HTTPV_MAX_BODY_SIZE`: accepted request-body ceiling, default `64m`.
- `HTTPV_ACCESS_LOG`: Nginx access-log destination; defaults to `off` because
  per-request container logging is expensive under high traffic. Set it to
  `/dev/stdout` when raw access logs are explicitly required.
- `HTTPV_VISUAL_DETAIL_STARTS_PER_SEC`: per-browser ceiling for new request
  animations before ordinary events are summarized; default `360`. Pending and
  operator-decision events remain individually visible.
- `HTTPV_DEMO_MAX_CONCURRENCY`: maximum number of simultaneous requests created
  by the built-in demo traffic generator. Production defaults to `128`; the
  local development Compose uses `4096` for high-concurrency acceptance tests.

Request and response buffering are disabled so uploads, chunk streams, SSE, and
WebSocket Upgrade can pass through without waiting for a complete body. Choose
timeouts and body limits appropriate for the exposed application.

## Published images

Tagged releases attach `compose.release.json` and a default `httpv.json`. The
release Compose file pulls signed multi-architecture images from GHCR instead
of compiling locally. Pin `HTTPV_VERSION` to the desired release. See
[releasing](releasing.md) for image names and verification commands.

## State and backup

The `httpv_runtime` volume contains the active configuration, native policy
snapshot, worker acknowledgements, and local runtime sockets. Traffic events,
request bodies, and response bodies are not persisted there.

Back up the active configuration through `GET /api/config` or copy the source
configuration file. Runtime sockets and ring files must not be restored from a
backup.

## Security boundary

The current single-node release includes local token login, three roles, CSRF
protection, and an audit log. It does not yet include OIDC, TLS termination, or
control-plane mTLS. Keep the console and internal metrics bound to localhost or
a trusted administrative network, and put TLS in front of the console before
sharing it with other operators.
