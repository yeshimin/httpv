# Edge Event Protocol

The OpenResty-to-edge-agent transport uses a persistent Unix Stream Socket at `/run/httpv/edge-agent.sock`. This path is local to an edge node and is never exposed on TCP or UDP network ports. Every binary frame is preceded by a 4-byte unsigned big-endian frame length.

## Frame layout

All integer fields are unsigned big-endian values.

| Bytes | Field |
|---:|---|
| 4 | Magic: `HTVP` |
| 1 | Protocol version: `1` |
| 1 | Event phase code |
| 2 | Reserved |
| 8 | Event timestamp in milliseconds |
| 8 | Request bytes |
| 8 | Response bytes |
| 2 | HTTP status |
| 4 | Duration in milliseconds |
| variable | Six UTF-8 strings, each prefixed with a 2-byte length: request ID, subject ID, method, path, client IP, action |

## Event phases

| Code | Phase |
|---:|---|
| 1 | `request_started` |
| 2 | `request_pending` |
| 3 | `request_decided` |
| 4 | `request_timeout` |
| 5 | `request_blocked` |
| 6 | `response_started` |
| 7 | `response_finished` |

The Rust edge agent validates the magic, protocol version, length-prefixed fields, and phase code before forwarding. Invalid frames are dropped and counted. The agent currently translates valid frames to JSON only at the compatibility boundary to the existing Go control service; this JSON hop will be replaced in the next migration stage.
