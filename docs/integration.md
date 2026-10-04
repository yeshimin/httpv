# Integrating an application

HTTPV is an inline reverse proxy. Clients must send traffic through HTTPV for
the gateway to visualize, hold, allow, or block it.

## Host application

For an application listening on the Docker host at `127.0.0.1:9998`, use:

```json
{
  "id": "local-app",
  "targets": [
    {"id": "local-app-1", "url": "http://host.docker.internal:9998", "host_header": "127.0.0.1:9998", "weight": 1}
  ],
  "max_failures": 1,
  "failure_timeout_ms": 10000
}
```

Then change clients from `http://127.0.0.1:9998` to
`http://127.0.0.1:8088`.

## Docker application

Attach the application and HTTPV to the same Docker network and use the service
name as the upstream origin, for example `http://orders:8080`. Set
`host_header` to the value expected by the application.

## Existing reverse proxy

For full interception, place HTTPV inline:

```text
Internet / load balancer -> HTTPV -> existing Nginx or application
```

Alternatively, keep the existing public Nginx as the TLS endpoint and proxy
its selected locations to HTTPV. HTTPV then forwards to the original upstream.
Avoid proxy loops by ensuring the HTTPV upstream points to the application's
original address, not back to the public listener.

## Routing example

```json
{
  "upstreams": [
    {"id": "orders", "name": "Orders", "targets": [{"id": "orders-1", "url": "http://orders:8080", "host_header": "orders", "weight": 1}], "max_failures": 1, "failure_timeout_ms": 10000},
    {"id": "admin", "name": "Admin", "targets": [{"id": "admin-1", "url": "http://admin:9000", "host_header": "admin", "weight": 1}], "max_failures": 1, "failure_timeout_ms": 10000}
  ],
  "routes": [
    {"id": "admin-route", "name": "Admin", "enabled": true, "match": {"host": "admin.example.test", "path_prefix": "/", "method": ""}, "upstream_id": "admin"},
    {"id": "orders-route", "name": "Orders", "enabled": true, "match": {"host": "", "path_prefix": "/api/orders", "method": ""}, "upstream_id": "orders"}
  ]
}
```

Route order matters. Put the catch-all route last.

## Compatibility checklist

The isolated regression suite verifies GET/POST/PUT/PATCH/DELETE, query strings,
headers, fixed and chunked request bodies, 2MiB uploads, redirects, 204/4xx
statuses, unbuffered chunk streams, SSE, WebSocket echo, weighted targets, and
passive failover. Validate application-specific timeout behavior and headers in
your environment. TLS termination, HTTP/2, and gRPC are not yet release claims.
