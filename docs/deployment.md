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

## Hardened local deployment

The production-oriented Compose file removes the bundled demo dependency,
serves the console as static files with Nginx, adds health checks and restart
policies, keeps the Control API private, and binds the console to localhost by
default:

```sh
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

## State and backup

The `httpv_runtime` volume contains the active configuration, native policy
snapshot, worker acknowledgements, and local runtime sockets. Traffic events,
request bodies, and response bodies are not persisted there.

Back up the active configuration through `GET /api/config` or copy the source
configuration file. Runtime sockets and ring files must not be restored from a
backup.

## Security boundary

The current single-node release does not yet include login, RBAC, TLS
termination, or control-plane mTLS. Keep the console and internal metrics bound
to localhost or a trusted administrative network. Put an authenticated reverse
proxy in front of the console before sharing it with other operators.

