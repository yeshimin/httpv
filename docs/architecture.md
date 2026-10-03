# Architecture

## Data-plane rule

Only OpenResty decides whether a request is allowed, challenged, rate-limited, held, or blocked. It owns a local rule snapshot and cannot depend on the network, database, control plane, or browser to make that decision.

## Event path

```text
OpenResty -> local edge-agent -> control plane -> browser
```

The edge agent is a bounded buffer between the request path and observability. It records explicit drops and can preferentially retain operator-relevant events.

## Current migration stages

1. **Prototype** — Lua emits JSON UDP telemetry; Vue/Pixi verifies the interaction model.
2. **Edge-agent extraction** — Rust sidecar owns buffering, forwarding, health, and telemetry-loss metrics.
3. **Binary event protocol** — Lua sends length-prefixed HTVP frames over a
   local Unix stream, the native module writes the same frames to a per-worker
   shared-memory ring, and the edge agent forwards request-stable shards to the
   Control service over local UDP.
4. **Renderer split** — Vue remains the control shell; WebGL2 typed-array renderer owns flow and marker layers.
5. **Native Nginx module** — response completion, compatible single-subject
   policy lookup, simple block enforcement, ring transport, and worker policy
   acknowledgements are native. Gates, routing, and multi-subject policies use
   the Lua compatibility path.
6. **Distributed operation** — NATS command fan-out, ClickHouse telemetry storage, PostgreSQL configuration and audit records.

## Why the layers are separate

- OpenResty protects traffic even if UI, database, broker, or edge-agent are unavailable.
- Rust edge-agent protects OpenResty workers from slow consumers and measures telemetry loss.
- The control plane handles identities, audit, configuration, and operator commands, none of which belongs in a request hot path.
- The browser treats all request data as a rendering stream; it never participates in enforcement.
