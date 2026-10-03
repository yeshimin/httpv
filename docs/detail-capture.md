# Request and response detail capture

HTTPV keeps lifecycle telemetry and request details on separate paths. Normal
events contain only method, path, sizes, status, duration, and control outcome.
Headers and bodies are captured only when a subject explicitly enables them.

```json
{
  "capture": {
    "enabled": true,
    "request_headers": true,
    "response_headers": true,
    "request_body": false,
    "response_body": false,
    "max_body_bytes": 16384,
    "ttl_seconds": 60,
    "sample_rate": 1,
    "content_types": [
      "application/json",
      "application/x-www-form-urlencoded",
      "application/xml",
      "text/"
    ],
    "redact_headers": [
      "authorization",
      "cookie",
      "set-cookie",
      "proxy-authorization",
      "x-api-key"
    ]
  }
}
```

## Data path

- Request headers and eligible request body previews are captured in the access
  phase.
- Response headers are captured in the header filter.
- Response body chunks are copied only until the configured byte limit; the
  original response stream is not modified or delayed.
- Details stay in an OpenResty shared-memory dictionary and expire by TTL.
- The browser requests details only after an administrator selects a request.
- Details never enter the HTVP lifecycle stream or the persisted Control state.

## Safety rules

Authorization, Cookie, Set-Cookie, Proxy-Authorization, and X-API-Key are always
redacted even if they are omitted from `redact_headers`. Additional header names
can be appended. Body capture is restricted to configured Content-Type prefixes.
Bodies buffered by OpenResty to a temporary file are not read back by the
capture path.

The configured body limit is per direction and must be between 1 byte and 1
MiB. TTL must be between 1 second and 1 hour. Enabling capture forces the
request onto the Lua compatibility path so native blocking cannot bypass the
capture policy.

