# Local native-module baseline — 2026-10-02

## Scope

This run verifies HTTPV's first dynamically loaded Nginx module without
replacing the stock OpenResty executable. The module emits a nonblocking
binary `response_finished` frame to a Unix datagram socket. The edge agent
forwards the binary frame unchanged to the Go control service, which decodes
it once. `request_started` and `response_started` remain on the compatibility
Lua path in this intermediate migration stage.

The source and agent counters are captured from:

- `http://127.0.0.1:8091/internal/native-metrics`
- `http://127.0.0.1:9102/metrics`

## Command

```sh
cd bench/loadgen
go run . -duration 10s -concurrency 64 -rate 1000 -json
```

The load generator drains every response body before closing it, so connection
reuse is included in the measurement.

## Results

| Target rate | Actual RPS | p50 | p95 | p99 | HTTP errors | Native source events | Agent drops |
|---|---:|---:|---:|---:|---:|---:|---:|
| 1,000 RPS | 967.4 | 4.07ms | 61.06ms | 156.97ms | 0 | 9,356 sent / 319 dropped | 0 |
| 2,000 RPS | 820.3 | 5.63ms | 573.52ms | 906.65ms | 0 | 8,203 sent / 0 dropped | 0 |

The 2,000-RPS target did not produce 2,000 completed RPS on this local,
single-worker Docker setup. Its tail latency therefore represents local
resource contention and queueing, not a claim of 2,000-RPS capacity.

## Decision

The dynamic module is proven loadable by the existing OpenResty runtime and
the native datagram → binary edge transport is correct under this baseline.
It deliberately never blocks an HTTP request when its bounded downstream path
is full; source drops are counted and observable.

This is not the final high-frequency architecture yet. The next native-module
increment must move `request_started` and `response_started` out of the Lua
publisher, then introduce adaptive visual sampling/aggregation for overload
instead of treating every raw request as a browser-renderable object.
