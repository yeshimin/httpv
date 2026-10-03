# Integrating an application

HTTPV is an inline reverse proxy. Clients must send traffic through HTTPV for
the gateway to visualize, hold, allow, or block it.

## Host application

For an application listening on the Docker host at `127.0.0.1:9998`, use:

```json
{
  "id": "local-app",
  "url": "http://host.docker.internal:9998",
  "host_header": "127.0.0.1:9998"
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
    {"id": "orders", "name": "Orders", "url": "http://orders:8080", "host_header": "orders"},
    {"id": "admin", "name": "Admin", "url": "http://admin:9000", "host_header": "admin"}
  ],
  "routes": [
    {"id": "admin-route", "name": "Admin", "enabled": true, "match": {"host": "admin.example.test", "path_prefix": "/", "method": ""}, "upstream_id": "admin"},
    {"id": "orders-route", "name": "Orders", "enabled": true, "match": {"host": "", "path_prefix": "/api/orders", "method": ""}, "upstream_id": "orders"}
  ]
}
```

Route order matters. Put the catch-all route last.

## Compatibility checklist

Before production use, verify the application's methods, request bodies, query
strings, redirects, large responses, streaming endpoints, WebSockets, timeout
behavior, and expected `Host` and `X-Forwarded-*` headers through port 8088.
TLS termination, HTTP/2, gRPC, and WebSocket compatibility are not yet release
claims and remain part of the product hardening test matrix.

