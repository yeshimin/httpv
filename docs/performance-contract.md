# Performance Contract

HTTPV is a traffic control system. Rendering overload or telemetry loss must never delay, allow, or block a request by accident.

## Design targets

These are engineering targets, not claims of current benchmark results.

| Concern | Target |
|---|---|
| Managed traffic | 10,000 requests/s per edge node |
| Manual-gate requests | 2,000 concurrent pending decisions per edge node |
| Control-rule lookup | Local-only; no remote call on the request path |
| Telemetry path | Bounded and lossy for low-priority events, lossless for control outcomes |
| Full-motion visual layer | 500 tablet / 1,000 desktop requests |
| Static selectable visual layer | 10,000 request markers |
| Operator command | One batch command, never one browser request per selected item |

## Non-negotiable invariants

1. The OpenResty data plane keeps enforcing its last valid local rule snapshot if every external component is unavailable.
2. Telemetry backpressure can drop normal completed-request events, but never changes a request decision.
3. Pending, blocked, allowed, and operator-selected events are higher priority than normal completed-request telemetry.
4. The browser receives batched events and never places one reactive component/object on the hot path for every request.
5. All performance work is measured with reproducible load tests and reported with hardware, browser, resolution, RPS, and percentile frame times.

## Benchmark gates

Before a release can claim a target, it must report:

- OpenResty request latency overhead: p50, p95, p99.
- Edge-agent received, forwarded, and dropped telemetry counts.
- Pending-decision success and timeout rates.
- Browser FPS/frame-time percentiles at 500, 1,000, 5,000, and 10,000 visual records.
- Memory use for the edge node, edge agent, and browser tab.
