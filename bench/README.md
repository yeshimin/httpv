# Benchmarks

`loadgen` is an intentionally small, dependency-free HTTP load generator for repeatable local baselines. It measures end-to-end HTTP latency from the generator's perspective; it is not a replacement for distributed-load testing.

## Local baseline

Start HTTPV first, then run:

```sh
cd bench/loadgen
go run . -duration 15s -concurrency 64 -rate 1000 -json
```

Inspect local telemetry alongside the report:

```sh
curl http://127.0.0.1:8091/internal/metrics
curl http://127.0.0.1:9102
```

Run the same command before and after each data-path change. Record the command, machine, browser/device when applicable, Docker resource limits, and output in a pull request or benchmark report.
