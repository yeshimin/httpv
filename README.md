# HTTPV

HTTPV is a local prototype for real-time, per-request HTTP traffic visualization and control.

The prototype keeps enforcement inside OpenResty. The Go control service and browser UI never sit on the request forwarding path.

## License

HTTPV is licensed under [Apache-2.0](LICENSE).

## Performance-oriented architecture

The repository now includes a Rust edge agent between OpenResty and the control service. It owns a bounded telemetry queue and publishes received, forwarded, dropped, and forwarding-error counters at `:9102` inside the container network. This is the first migration step away from an OpenResty worker directly serving observability consumers.

Read [the architecture](docs/architecture.md) and [the performance contract](docs/performance-contract.md) before making data-path changes.

## Adaptive visualization

HTTPV treats policy enforcement and browser rendering as separate concerns. A
managed request is still evaluated by OpenResty even when the browser is under
load. Each connected console receives up to 360 ordinary request starts per
second as detailed, clickable request lifecycles. Above that budget, ordinary
traffic is summarized into 500ms windows by method and path. Waiting-for-manual
decision and blocked traffic remain individual, actionable records.

The console displays the aggregated rate and its busiest paths, and an operator
can create a future block rule directly from a displayed aggregate path. This
prevents a slow browser from adding backpressure to the request path while
preserving per-request analysis during normal traffic.

## Local control-state recovery

The local Compose stack atomically persists the managed subject, manual-gate
settings, and dynamic block rules in its dedicated HTTPV runtime volume. A
Control restart restores these policy settings before it synchronizes OpenResty
again. Traffic events, request metadata, headers, and bodies are never written
to this state file.

## What this prototype demonstrates

- A configurable managed-traffic subject (all paths by default).
- Per-request `request_started`, `request_pending`, `response_started`, and `response_finished` events.
- A Vue + PixiJS traffic canvas with adjustable render delay and trail duration.
- A local OpenResty dynamic block rule that takes effect for subsequent requests.
- An optional manual gate that holds matching requests until an operator allows or blocks them.

## Run locally

```sh
docker compose -f compose.json up --build
```

Open the control console at `http://localhost:5173`.

The test traffic gateway is at `http://localhost:8088`. The console's **Send test request** button sends requests to `/demo/slow` through that gateway.

## Safety notes

- This is a localhost-only prototype. Its internal OpenResty control port is not published to the host.
- The event protocol intentionally contains only request metadata: no authorization headers, cookies, query string, request body, or response body.
- A manual gate can hold connections. Its timeout and timeout action are configurable for experiments. An indefinite timeout is therefore only appropriate for controlled local testing.

## Deliberately deferred

- Authentication, RBAC, TLS and multi-node control-plane security.
- Durable event retention and historical replay.
- Multi-node rule distribution.
- Full request/response body capture.
