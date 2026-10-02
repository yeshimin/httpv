# HTTPV dynamic Nginx module

`ngx_http_httpv_module.so` is the future high-frequency telemetry module. It is built as a dynamic `.so`; it does not modify or replace the host OpenResty/Nginx binary.

The initial module registers an Nginx log-phase handler and can emit a nonblocking binary `response_finished` event to a local Unix datagram socket. HTTPV's edge agent owns the destination socket and exposes the effective kernel receive-buffer size as a metric. This protects short bursts without allowing the request path to block. It is disabled by default.

When enabled in the HTTPV gateway configuration, the module only emits for a
request that Lua has marked as managed and displayable. It reuses the
`$httpv_request_id` and `$httpv_subject_id` variables, so the native completion
event remains on the same visual trajectory as the request-start event.

The source counters are exposed through the module's `httpv_native_metrics`
location handler. See [the native-module baseline](../bench/results/local-2026-10-02-native-module.md) for measured behavior and current limitations.

The module is only loadable when its Nginx compatibility signature matches the host runtime. HTTPV must publish artifacts by OpenResty/Nginx version, operating system, architecture, libc, and build signature.
