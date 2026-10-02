# Local rate-scheduler correction — 2026-10-02

## Why this exists

The original load generator used a one-millisecond `time.Ticker` at a target
rate of 1,000 RPS. On this local Docker host, that ticker silently skipped
ticks, so a reported 600 RPS could mean the generator only sent 600 jobs, not
that the target could handle only 600 RPS.

`bench/loadgen` now uses an elapsed-time scheduler with bounded catch-up
bursts. A direct measurement of the demo upstream reaches 9,999 successful
requests in 10 seconds, confirming that the corrected generator delivers the
intended rate.

## Corrected comparison

All commands used `-duration 10s -concurrency 64 -rate 1000 -json`.

| Route | Completed RPS | p50 | p95 | p99 | HTTP errors |
|---|---:|---:|---:|---:|---:|
| Direct demo upstream | 999.9 | 0.40ms | 1.81ms | 16.64ms | 0 |
| OpenResty, request outside HTTPV scope | 999.9 | 12.10ms | 103.08ms | 165.62ms | 0 |
| OpenResty, HTTPV scope with display enabled | 870.5 | 22.76ms | 241.87ms | 355.31ms | 0 |

For the visible HTTPV run, the C source reported 8,128 completion events sent
and 577 dropped during the 8,705 completed requests. The edge agent reported
zero queue drops and zero invalid frames; the lost events therefore occurred
before the agent, at the bounded nonblocking C-to-agent socket boundary.

## Interpretation

The prior 459-RPS result was a rejected experiment that synchronously encoded
all three lifecycle events in Nginx C request phases. It is not an HTTPV or
OpenResty capacity limit.

The corrected data does show that the current Lua policy path and source-side
telemetry still add material tail latency. The production migration must move
the common managed-scope match and simple fast policy path into the dynamic C
module, while emitting fixed-size records to a per-worker ring drained outside
the request path. Lua remains appropriate for manual gates and complex,
low-volume policies.
