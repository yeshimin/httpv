# Benchmarks

`loadgen` is an intentionally small, dependency-free HTTP load generator for repeatable local baselines. It measures end-to-end HTTP latency from the generator's perspective; it is not a replacement for distributed-load testing.

## Local baseline

Start HTTPV first, then run:

```sh
cd bench/loadgen
go run . -url http://127.0.0.1:8088/ -duration 15s -concurrency 64 -rate 1000 -json
```

Inspect local telemetry alongside the report:

```sh
curl http://127.0.0.1:8091/internal/metrics
curl http://127.0.0.1:9102
```

Run the same command before and after each data-path change. Record the command, machine, browser/device when applicable, Docker resource limits, and output in a pull request or benchmark report.

## Direct-versus-gateway comparison

Use the same rate, concurrency, duration, application endpoint, and machine for
both runs. Disable the manual gate when measuring ordinary proxy overhead and
record the active Subject, capture, and rule configuration with the result:

```sh
go run . -url http://127.0.0.1:9998/ -duration 30s -concurrency 128 -rate 1000 -json
go run . -url http://127.0.0.1:8088/ -duration 30s -concurrency 128 -rate 1000 -json
```

Repeat the gateway run with detail capture disabled, headers-only, and bounded
request/response body capture. The report uses actual elapsed time, including
the tail needed to drain submitted requests, and publishes both total and
successful RPS.

The comparison script records both JSON reports and minimal environment metadata:

```sh
LABEL=capture-off RATE=1000 DURATION=30s ./bench/run-comparison.sh
```

Run it again with labels such as `headers-only` and `body-16k` after applying
the corresponding subject capture policy. Generated JSON result directories are
ignored by Git; promote reviewed results to a Markdown report when they support
a release claim.
