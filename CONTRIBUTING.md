# Contributing to HTTPV

HTTPV is an inline traffic-control system. Changes to the request path require
correctness, failure-mode, performance, and security evidence.

## Development checks

Before opening a pull request:

```sh
(cd control && go test ./...)
(cd edge-agent && cargo fmt --check && cargo test && cargo clippy --all-targets -- -D warnings)
(cd web && npm ci && npm run build)
HTTPV_ADMIN_TOKEN=validation docker compose -f compose.production.json config --quiet
docker compose -f compose.e2e.json up --build -d
python -m pytest -q e2e/test_console.py
docker compose -f compose.e2e.json down -v
```

Data-path changes should include direct-versus-gateway benchmark results using
`bench/loadgen`, plus OpenResty and edge-agent drop counters. Never commit
tokens, captured headers, bodies, cookies, certificates, or production traffic.

## Pull requests

Keep changes focused. Describe user-visible behavior, compatibility, failure
mode, tests, and performance impact. Update configuration, deployment, security,
and protocol documentation when relevant.

