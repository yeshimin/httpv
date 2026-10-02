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

The module is only loadable when its Nginx compatibility signature matches the host runtime. HTTPV must publish artifacts by OpenResty/Nginx version, operating system, architecture, libc, and build signature.
