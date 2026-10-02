# HTTPV browser regression tests

Run the tests against an isolated stack so they never alter a developer's live
control state:

```sh
docker compose -f compose.e2e.json up --build -d
python -m pip install -r e2e/requirements.txt
python e2e/test_console.py
docker compose -f compose.e2e.json down -v
```

The suite checks canvas layout, timeout-block completion, and overload
aggregation. The isolated console uses `http://127.0.0.1:15173`.
