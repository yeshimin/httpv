# Local shared-ring transport check — 2026-10-02

## Scope

Validate the first HTTPV native shared-memory transport without replacing the
stock OpenResty executable. The C module writes fixed-size records to one SPSC
ring file per Nginx worker. The Rust edge agent maps the file, advances the
consumer sequence, and forwards each recovered HTVP frame through the existing
bounded control-plane transport.

At this checkpoint, only `response_finished` uses the ring. Request start and
response start remain on the Lua compatibility path.

## Result

With the corrected 1,000-RPS input for 10 seconds:

| Metric | Value |
|---|---:|
| Completed requests | 7,731 |
| HTTP errors | 0 |
| C ring enqueued | 7,732 (includes one warm-up request) |
| C ring drops | 0 |
| Agent ring events received | 7,732 |
| Agent queue drops / invalid frames | 0 / 0 |

The completed RPS is not an overall capacity claim: the current Lua managed
scope path remains in the request flow. The check proves that ring transport
removes the prior C-to-agent Unix datagram backpressure for completion events.
