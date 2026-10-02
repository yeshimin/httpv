# Local transport comparison — 2026-10-02

## Purpose

Measure whether the OpenResty-to-edge-agent telemetry experiment can stay on the request path at 1,000 target RPS.

## Command

```sh
cd bench/loadgen
go run . -duration 10s -concurrency 64 -rate 1000 -json
```

The load generator drains each HTTP response body before closing it so client connection reuse is part of the measurement.

> Historical note: these measurements were taken before the load generator's
> 1ms ticker was corrected. They remain useful for comparing the old transport
> experiments to each other, but are not absolute 1,000-RPS capacity claims.
> See [the corrected scheduler baseline](local-2026-10-02-rate-scheduler-correction.md).

## Results

| Mode | RPS | p50 | p95 | p99 | Event loss |
|---|---:|---:|---:|---:|---:|
| Managed traffic, display disabled | 925.9 | 20.86ms | 89.23ms | 205.51ms | n/a |
| Binary Unix Stream telemetry, 5ms Lua micro-batch | 556.5 | 82.45ms | 297.76ms | 426.63ms | 0 / 16,695 |

## Decision

The binary Unix Stream protocol is correct and loss-free, but the Lua scheduling and socket work still causes unacceptable request-tail latency. Do not continue optimizing this Lua publisher as the production telemetry data path.

The next experiment is a native Nginx C module that emits bounded binary telemetry from Nginx phases without Lua timer or cosocket scheduling. Lua remains appropriate for policy orchestration and low-volume interactive gate handling.
