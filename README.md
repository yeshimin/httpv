# HTTPV

[![CI](https://github.com/yeshimin/httpv/actions/workflows/ci.yml/badge.svg)](https://github.com/yeshimin/httpv/actions/workflows/ci.yml)

HTTPV is a single-node preview for real-time, per-request HTTP traffic visualization and control.

The prototype keeps enforcement inside OpenResty. The Go control service and browser UI never sit on the request forwarding path.

## License

HTTPV is licensed under [Apache-2.0](LICENSE).

## Performance-oriented architecture

The repository now includes a Rust edge agent between OpenResty and the control service. It owns a bounded telemetry queue and publishes received, forwarded, dropped, and forwarding-error counters at `:9102` inside the container network. This is the first migration step away from an OpenResty worker directly serving observability consumers.

Read [the architecture](docs/architecture.md), [configuration](docs/configuration.md),
[deployment guide](docs/deployment.md), [integration guide](docs/integration.md),
[security baseline](docs/security.md), [detail capture](docs/detail-capture.md),
[release process](docs/releasing.md), and [performance contract](docs/performance-contract.md)
before making data-path changes.

## Adaptive visualization

HTTPV treats policy enforcement and browser rendering as separate concerns. A
managed request is still evaluated by OpenResty even when the browser is under
load. By default, every real request lifecycle remains a raw, per-request event;
the console never replaces those events with aggregate traffic summaries. The
browser controls cost by assigning full motion to a bounded number of newly
arriving requests while overflow requests remain static, clickable, and
actionable. An admitted animation keeps its slot until that request finishes.

The telemetry and WebSocket queues remain bounded so a slow or disconnected
console can never add backpressure to the request path. Optional legacy summary
mode can be enabled explicitly for unusually constrained deployments.

Each browser also has a local concurrent individual-animation limit, configured
from **Traffic controls → Individual animation limit**. The default is `360`;
`0` removes the browser-side limit. This setting is stored only in that browser
and does not change gateway enforcement or raw-event transport.

## Local control-state recovery

The local Compose stack atomically persists the managed subject, manual-gate
settings, and dynamic block rules in its dedicated HTTPV runtime volume. A
Control restart restores these policy settings before it synchronizes OpenResty
again. Traffic events, request metadata, headers, and bodies are never written
to this state file.

## What this prototype demonstrates

- A configurable managed-traffic subject (all paths by default).
- Per-request `request_started`, `request_pending`, `request_aborted`, `response_started`, and `response_finished` events.
- A Vue + PixiJS traffic canvas with adjustable render delay and trail duration.
- A local OpenResty dynamic block rule that takes effect for subsequent requests.
- An optional manual gate that holds matching requests until an operator allows or blocks them.

## Run locally

```sh
docker compose -f compose.json up --build
```

Open the control console at `http://localhost:5173`.

The test traffic gateway is at `http://localhost:8088`. The local stack forwards
requests to the real test upstream at `http://127.0.0.1:9998` through Docker's
host gateway. The console's **Send current request** and burst controls send
`GET /` requests through the gateway without adding an artificial upstream
delay. Change `HTTPV_UPSTREAM_URL` and `HTTPV_UPSTREAM_HOST` in `compose.json`
to target a different upstream. The isolated E2E stack continues to use the
bundled demo service.

Use `compose.production.json` for the production-oriented single-node layout.
It serves a prebuilt static console, keeps the Control API private, requires an
administrator token, and adds health checks and restart policies. TLS
termination, OIDC, and multi-node command routing are still deliberately deferred.

Tagged releases also publish signed AMD64/ARM64 images and a no-build
`compose.release.json` deployment. See [the release process](docs/releasing.md).

## Safety notes

- This is a localhost-only prototype. Its internal OpenResty control port is not published to the host.
- The event protocol intentionally contains only request metadata: no authorization headers, cookies, query string, request body, or response body.
- A manual gate can hold connections. Its timeout and timeout action are configurable for experiments. An indefinite timeout is therefore only appropriate for controlled local testing.

## Deliberately deferred

- Authentication, RBAC, TLS and multi-node control-plane security.
- Durable event retention and historical replay.
- Multi-node rule distribution.
- Full request/response body capture.
