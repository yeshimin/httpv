# HTTPV dynamic Nginx module

`ngx_http_httpv_module.so` is the future high-frequency telemetry module. It is built as a dynamic `.so`; it does not modify or replace the host OpenResty/Nginx binary.

The initial module registers an Nginx log-phase handler and emits a nonblocking binary `response_finished` event. Its primary transport is a fixed-size, single-producer ring file per Nginx worker; the Rust edge agent maps and drains those files outside the request path. If a ring cannot be initialized, the module falls back to its local Unix datagram socket.

The local Docker prototype stores ring files under the dedicated shared runtime volume at `/run/httpv/telemetry.ring.<worker-pid>`. The agent prepares that directory for the unprivileged OpenResty worker. A production installer should instead create this directory with an explicit shared group and restrictive group permissions.

When enabled in the HTTPV gateway configuration, the module only emits for a
request that Lua has marked as managed and displayable. It reuses the
`$httpv_request_id` and `$httpv_subject_id` variables, so the native completion
event remains on the same visual trajectory as the request-start event.

The source counters are exposed through the module's `httpv_native_metrics`
location handler, including ring enqueue and ring-full drops. See [the native-module baseline](../bench/results/local-2026-10-02-native-module.md) for measured behavior and current limitations.

## Native policy shadow mode

The Go control service also publishes a compact `HTVC` policy snapshot into the
shared runtime directory. Each C worker reloads it on a 250ms timer and records
native scope and simple block-rule matches. This shadow step verifies C/Lua
match parity before C handles traffic.

The local prototype now enables C enforcement for the snapshot's simple
`block` rules: up to 32 method/path/client-IP rules, with no manual gate. A
native hit returns `403` before Lua runs and emits a complete ring-backed
lifecycle. All other requests continue into Lua. Policy snapshots are refreshed
within 250ms, so this is an eventually consistent acceleration layer; Lua
remains the compatibility authority during transitions.

## Native normal fast-path experiment

The first native normal-path experiment matches a managed request in C, emits a
request lifecycle through the ring, and lets Lua skip duplicate orchestration.
It remains **disabled by default**: a later local verification exposed an
unreliable C header-filter handoff that could stall a request. The stable
configuration keeps Lua responsible for ordinary request start/response start,
while C block enforcement and ring-backed completion telemetry remain active.

The module is only loadable when its Nginx compatibility signature matches the host runtime. HTTPV must publish artifacts by OpenResty/Nginx version, operating system, architecture, libc, and build signature.
