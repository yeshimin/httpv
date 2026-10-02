# Local native fast-path check — 2026-10-02

## Scope

Validate the common managed request path after C takes over scope matching and
three lifecycle events. The request is eligible only when the native policy
snapshot matches, the manual gate is off, and no simple native block rule
matches. Eligible requests bypass Lua policy orchestration and write their
three events through the shared worker ring.

## Result

With the corrected 1,000-RPS input for 10 seconds:

| Metric | Value |
|---|---:|
| Completed requests | 9,999 |
| HTTP errors | 0 |
| p50 / p95 / p99 | 0.50ms / 35.95ms / 61.49ms |
| Native fast-path requests | 10,000 (includes one warm-up request) |
| C ring enqueued / dropped | 30,000 / 0 |
| Agent ring received | 30,000 |
| Agent queue drops / invalid frames | 0 / 0 |

## Compatibility check

Turning on the manual gate prevented the C fast-path counter from increasing.
The request instead used Lua's gate/timeout flow, while its completion event
continued to use the ring. This confirms that interactive control traffic is
not accidentally bypassed by the common fast path.
