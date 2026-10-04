import json
import http.client
import os
import re
import time
import unittest
from concurrent.futures import ThreadPoolExecutor
from http.cookiejar import CookieJar
from urllib.error import HTTPError
from urllib.parse import urlparse
from urllib.request import HTTPRedirectHandler, HTTPCookieProcessor, Request, build_opener, urlopen

from playwright.sync_api import expect, sync_playwright


CONSOLE_URL = os.getenv("HTTPV_E2E_CONSOLE_URL", "http://127.0.0.1:15173")
CONTROL_URL = os.getenv("HTTPV_E2E_CONTROL_URL", "http://127.0.0.1:18090")
GATEWAY_URL = os.getenv("HTTPV_E2E_GATEWAY_URL", "http://127.0.0.1:18088")
ADMIN_TOKEN = os.getenv("HTTPV_E2E_ADMIN_TOKEN", "httpv-e2e-admin")
COOKIE_JAR = CookieJar()
OPENER = build_opener(HTTPCookieProcessor(COOKIE_JAR))
CSRF_TOKEN = ""


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def api(method, path, payload=None):
    body = None if payload is None else json.dumps(payload).encode()
    request = Request(f"{CONTROL_URL}{path}", data=body, method=method)
    if body is not None:
        request.add_header("Content-Type", "application/json")
    if CSRF_TOKEN and method in {"POST", "PUT", "PATCH", "DELETE"} and path != "/api/auth/login":
        request.add_header("X-HTTPV-CSRF", CSRF_TOKEN)
    if path == "/api/pending/block-all":
        request.add_header("X-HTTPV-Confirm", "block-all-pending")
    if path.startswith("/api/config/rollback/"):
        request.add_header("X-HTTPV-Confirm", f"rollback:{path.rsplit('/', 1)[-1]}")
    with OPENER.open(request, timeout=5) as response:
        return response.status, response.read()


def authenticate_api():
    global CSRF_TOKEN
    _, raw = api("POST", "/api/auth/login", {"token": ADMIN_TOKEN})
    CSRF_TOKEN = json.loads(raw)["csrf_token"]


def wait_for_demo_idle(timeout=10):
    deadline = time.time() + timeout
    while time.time() < deadline:
        _, raw = api("GET", "/api/demo/status")
        status = json.loads(raw)
        if status["queued"] == 0 and status["active"] == 0:
            return
        time.sleep(0.05)
    raise AssertionError(f"demo traffic did not become idle: {status}")


def prometheus_metrics(url):
    with urlopen(url, timeout=5) as response:
        lines = response.read().decode().splitlines()
    return {
        parts[0]: float(parts[1])
        for line in lines
        if line and not line.startswith("#") and len(parts := line.split()) == 2
    }


def wait_for_telemetry_idle(timeout=10):
    deadline = time.time() + timeout
    stable_since = None
    while time.time() < deadline:
        gateway = prometheus_metrics("http://127.0.0.1:18091/internal/metrics")
        edge = prometheus_metrics("http://127.0.0.1:19102/metrics")
        gateway_idle = gateway["httpv_openresty_telemetry_events_enqueued_total"] == (
            gateway["httpv_openresty_telemetry_events_sent_total"]
            + gateway["httpv_openresty_telemetry_events_dropped_total"]
        )
        edge_idle = edge["httpv_edge_events_received"] == (
            edge["httpv_edge_events_forwarded"] + edge["httpv_edge_events_dropped"]
        )
        if gateway_idle and edge_idle:
            stable_since = stable_since or time.time()
            if time.time() - stable_since >= 0.25:
                return
        else:
            stable_since = None
        time.sleep(0.05)
    raise AssertionError("telemetry pipeline did not become idle")


def role_request(token, method, path, payload=None):
    opener = build_opener(HTTPCookieProcessor(CookieJar()))
    login = Request(f"{CONTROL_URL}/api/auth/login", data=json.dumps({"token": token}).encode(), method="POST", headers={"Content-Type": "application/json"})
    with opener.open(login, timeout=5) as response:
        csrf = json.loads(response.read())["csrf_token"]
    body = None if payload is None else json.dumps(payload).encode()
    request = Request(f"{CONTROL_URL}{path}", data=body, method=method)
    if body is not None:
        request.add_header("Content-Type", "application/json")
    if method in {"POST", "PUT", "PATCH", "DELETE"}:
        request.add_header("X-HTTPV-CSRF", csrf)
    return opener.open(request, timeout=5)


def subject(gate_enabled=False, timeout_ms=30_000, timeout_action="block", max_pending=2_000, overflow_action="block", failure_action="allow"):
    return {
        "id": "default",
        "name": "Default traffic",
        "enabled": True,
        "display": True,
        "match": {"host": "", "path_prefix": "/", "method": ""},
        "gate": {
            "enabled": gate_enabled,
            "timeout_ms": timeout_ms,
            "timeout_action": timeout_action,
            "max_pending": max_pending,
            "overflow_action": overflow_action,
            "failure_action": failure_action,
        },
        "capture": {
            "enabled": False,
            "request_headers": True,
            "response_headers": True,
            "request_body": False,
            "response_body": False,
            "max_body_bytes": 16384,
            "ttl_seconds": 60,
            "sample_rate": 1,
            "content_types": ["application/json", "application/x-www-form-urlencoded", "application/xml", "text/"],
            "redact_headers": ["authorization", "cookie", "set-cookie", "proxy-authorization", "x-api-key"],
        },
    }


class ConsoleE2E(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        deadline = time.time() + 30
        while True:
            try:
                api("GET", "/healthz")
                break
            except Exception:
                if time.time() >= deadline:
                    raise
                time.sleep(0.25)
        authenticate_api()
        cls.playwright = sync_playwright().start()
        cls.browser = cls.playwright.chromium.launch(headless=True)

    @classmethod
    def tearDownClass(cls):
        cls.browser.close()
        cls.playwright.stop()

    def setUp(self):
        api("PUT", "/api/subjects/default", subject())
        self.page = self.browser.new_page(viewport={"width": 1440, "height": 900})
        self.page.goto(CONSOLE_URL, wait_until="networkidle")
        if self.page.locator(".login-panel").count() > 0:
            self.page.locator('.login-panel input[type="password"]').fill(ADMIN_TOKEN)
            self.page.get_by_role("button", name=re.compile("登录|Sign in")).click()
            self.page.locator(".traffic-canvas").wait_for(state="visible", timeout=10_000)
        expect(self.page.locator(".status.live")).to_be_visible(timeout=10_000)

    def tearDown(self):
        self.page.close()

    def test_canvas_has_visible_lane_skeleton(self):
        canvas = self.page.locator(".traffic-canvas")
        expect(canvas).to_be_visible()
        self.assertGreater(canvas.evaluate("node => node.clientHeight"), 300)
        self.assertEqual(self.page.locator(".lane-label").all_text_contents(), ["Client", "OpenResty", "Upstream"])
        expect(self.page.locator(".role-state")).to_contain_text(re.compile("管理员|admin"))

    def test_audit_records_authenticated_changes(self):
        _, raw = api("GET", "/api/audit?limit=50")
        entries = json.loads(raw)["entries"]
        actions = {entry["action"] for entry in entries}
        self.assertIn("auth.login", actions)
        self.assertIn("subject.update", actions)
        self.assertNotIn(ADMIN_TOKEN, raw.decode())

    def test_rbac_enforces_viewer_and_operator_roles(self):
        with role_request("httpv-e2e-viewer", "GET", "/api/config") as response:
            self.assertEqual(response.status, 200)
        with self.assertRaises(HTTPError) as viewer_error:
            role_request("httpv-e2e-viewer", "POST", "/api/demo/burst", {"count": 1})
        self.assertEqual(viewer_error.exception.code, 403)

        with role_request("httpv-e2e-operator", "POST", "/api/demo/burst", {"count": 1}) as response:
            self.assertEqual(response.status, 202)
        _, raw = api("GET", "/api/config")
        with self.assertRaises(HTTPError) as operator_error:
            role_request("httpv-e2e-operator", "PUT", "/api/config", json.loads(raw))
        self.assertEqual(operator_error.exception.code, 403)

    def test_native_policy_has_worker_ack(self):
        deadline = time.time() + 5
        while True:
            _, raw = api("GET", "/api/policy-status")
            status = json.loads(raw)
            if status["state"] == "active":
                break
            if time.time() >= deadline:
                self.fail(f"policy did not activate: {status}")
            time.sleep(0.1)
        self.assertGreaterEqual(status["version"], 1)
        self.assertGreaterEqual(len(status["acks"]), 1)

    def test_proxy_preserves_path_and_query(self):
        with urlopen(f"{GATEWAY_URL}/orders/42?ms=0", timeout=5) as response:
            payload = json.loads(response.read())
            self.assertEqual(response.headers.get("X-HTTPV-Demo"), "upstream")
        self.assertEqual(payload["path"], "/orders/42")
        self.assertEqual(payload["delay_ms"], 0)

    def test_proxy_method_body_header_and_large_upload_compatibility(self):
        for method in ["GET", "POST", "PUT", "PATCH", "DELETE"]:
            body = None if method == "GET" else f"body-{method}".encode()
            request = Request(
                f"{GATEWAY_URL}/compat/{method.lower()}?ms=0&value=one",
                data=body,
                method=method,
                headers={"Content-Type": "text/plain", "X-HTTPV-Echo": method},
            )
            with urlopen(request, timeout=5) as response:
                payload = json.loads(response.read())
                self.assertEqual(response.headers.get("X-HTTPV-Echo"), method)
            self.assertEqual(payload["method"], method)
            self.assertEqual(payload["path"], f"/compat/{method.lower()}")
            self.assertIn("value=one", payload["query"])
            self.assertEqual(payload["request_bytes"], len(body or b""))

        large_body = b"x" * (2 << 20)
        request = Request(f"{GATEWAY_URL}/large?ms=0", data=large_body, method="POST", headers={"Content-Type": "application/octet-stream"})
        with urlopen(request, timeout=10) as response:
            payload = json.loads(response.read())
        self.assertEqual(payload["request_bytes"], len(large_body))

        parsed = urlparse(GATEWAY_URL)
        connection = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=5)
        connection.request(
            "POST",
            "/chunked?ms=0",
            body=iter([b"chunk-one", b"chunk-two"]),
            headers={"Content-Type": "text/plain"},
            encode_chunked=True,
        )
        response = connection.getresponse()
        payload = json.loads(response.read())
        connection.close()
        self.assertEqual(response.status, 200)
        self.assertEqual(payload["request_bytes"], len(b"chunk-onechunk-two"))

    def test_proxy_status_redirect_stream_sse_and_websocket(self):
        with urlopen(f"{GATEWAY_URL}/status/204", timeout=5) as response:
            self.assertEqual(response.status, 204)
        with self.assertRaises(HTTPError) as status_error:
            urlopen(f"{GATEWAY_URL}/status/418", timeout=5)
        self.assertEqual(status_error.exception.code, 418)

        no_redirect = build_opener(NoRedirect())
        with self.assertRaises(HTTPError) as redirect_error:
            no_redirect.open(f"{GATEWAY_URL}/redirect", timeout=5)
        self.assertEqual(redirect_error.exception.code, 302)
        self.assertEqual(redirect_error.exception.headers.get("Location"), "/redirect-target?from=demo")

        started = time.perf_counter()
        with urlopen(f"{GATEWAY_URL}/stream", timeout=5) as response:
            first = response.read(len(b"chunk-0\n"))
            first_latency = time.perf_counter() - started
            remaining = response.read()
        self.assertEqual(first, b"chunk-0\n")
        self.assertLess(first_latency, 0.5)
        self.assertIn(b"chunk-2", remaining)

        with urlopen(f"{GATEWAY_URL}/events", timeout=5) as response:
            self.assertTrue(response.headers.get("Content-Type", "").startswith("text/event-stream"))
            payload = response.read().decode()
        self.assertIn("event: probe", payload)
        self.assertIn("data: 2", payload)

        websocket_url = GATEWAY_URL.replace("http://", "ws://") + "/ws"
        echoed = self.page.evaluate(
            """url => new Promise((resolve, reject) => {
              const socket = new WebSocket(url);
              const timer = setTimeout(() => reject(new Error('websocket timeout')), 5000);
              socket.onopen = () => socket.send('httpv-probe');
              socket.onmessage = event => { clearTimeout(timer); socket.close(); resolve(event.data); };
              socket.onerror = () => { clearTimeout(timer); reject(new Error('websocket error')); };
            })""",
            websocket_url,
        )
        self.assertEqual(echoed, "httpv-probe")

    def test_opt_in_detail_capture_redacts_sensitive_headers(self):
        captured_subject = subject()
        captured_subject["capture"].update({"enabled": True, "request_body": True, "response_body": True, "max_body_bytes": 4096})
        api("PUT", "/api/subjects/default", captured_subject)
        started_at = int(time.time() * 1000)
        request = Request(
            f"{GATEWAY_URL}/capture-test?ms=0",
            data=b'{"hello":"world"}',
            method="POST",
            headers={"Content-Type": "application/json", "Authorization": "Bearer synthetic-secret", "X-HTTPV-Test": "visible"},
        )
        with urlopen(request, timeout=5) as response:
            self.assertEqual(response.status, 200)

        request_id = None
        deadline = time.time() + 5
        while time.time() < deadline:
            _, raw = api("GET", "/api/events")
            for event in reversed(json.loads(raw)):
                if event.get("phase") == "request_started" and event.get("method") == "POST" and event.get("path") == "/capture-test" and event.get("timestamp_ms", 0) >= started_at:
                    request_id = event["request_id"]
                    break
            if request_id:
                break
            time.sleep(0.05)
        self.assertIsNotNone(request_id)

        _, raw = api("GET", f"/api/requests/{request_id}/details")
        details = json.loads(raw)
        request_headers = {name.lower(): value for name, value in details["request"]["headers"].items()}
        response_headers = {name.lower(): value for name, value in details["response"]["headers"].items()}
        self.assertEqual(request_headers["authorization"], "[REDACTED]")
        self.assertEqual(request_headers["x-httpv-test"], "visible")
        self.assertEqual(details["request"]["body"], '{"hello":"world"}')
        self.assertEqual(response_headers["x-httpv-demo"], "upstream")
        self.assertIn('"path":"/capture-test"', details["response"]["body"].replace(" ", ""))
        self.assertNotIn("synthetic-secret", raw.decode())


    def test_detail_capture_renders_in_console(self):
        captured_subject = subject()
        captured_subject["capture"].update({"enabled": True, "response_body": True})
        api("PUT", "/api/subjects/default", captured_subject)
        self.page.reload(wait_until="networkidle")
        expect(self.page.locator(".status.live")).to_be_visible(timeout=10_000)
        trail = self.page.locator('.slider-row input[type="range"]').nth(1)
        trail.evaluate("node => { node.value = '10000'; node.dispatchEvent(new Event('input', { bubbles: true })) }")
        self.page.get_by_role("button", name=re.compile("发送当前请求|Send current request")).click()
        self.page.wait_for_timeout(900)
        canvas = self.page.locator(".traffic-canvas")
        box = canvas.bounding_box()
        selected = False
        if box:
            for offset in range(35, int(box["height"]) - 15, 8):
                self.page.mouse.click(box["x"] + box["width"] * 0.05, box["y"] + offset)
                if self.page.locator(".detail-tabs").count() > 0:
                    selected = True
                    break
        self.assertTrue(selected, "captured request could not be selected from the canvas")
        self.page.get_by_role("button", name=re.compile("Header|Headers")).click()
        expect(self.page.locator(".capture-grid")).to_contain_text(re.compile("x-httpv-demo|X-HTTPV-Demo"), timeout=5_000)
        self.page.get_by_role("button", name="Body", exact=True).click()
        expect(self.page.locator(".capture-grid")).to_contain_text('"ok":true', timeout=5_000)

    def test_config_validates_multiple_routes(self):
        _, raw = api("GET", "/api/config")
        config = json.loads(raw)
        config["upstreams"].append({"id": "secondary", "name": "Secondary", "url": "http://demo:9000", "host_header": "demo:9000"})
        config["routes"].insert(0, {"id": "secondary-route", "name": "Secondary", "enabled": True, "match": {"host": "secondary.local", "path_prefix": "/v2", "method": "GET"}, "upstream_id": "secondary"})
        status, validated = api("POST", "/api/config/validate", config)
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(validated)["status"], "valid")

    def test_admin_configuration_workbench_validates_current_config(self):
        self.page.get_by_role("button", name=re.compile("网关配置|Gateway configuration")).click()
        editor = self.page.locator(".config-editor textarea")
        expect(editor).to_be_visible()
        payload = json.loads(editor.input_value())
        self.assertEqual(payload["schema_version"], 1)
        self.assertGreaterEqual(len(payload["upstreams"][0]["targets"]), 1)
        self.page.get_by_role("button", name=re.compile("校验|Validate"), exact=True).click()
        expect(self.page.locator(".config-message")).to_contain_text(re.compile("配置有效|Configuration is valid"), timeout=5_000)
        expect(self.page.locator(".revision-list")).to_be_visible()

    def test_config_hot_applies_route_selection(self):
        _, raw = api("GET", "/api/config")
        original = json.loads(raw)
        changed = json.loads(raw)
        changed["upstreams"].append({"id": "unreachable", "name": "Unreachable", "url": "http://127.0.0.1:1", "host_header": "127.0.0.1:1"})
        changed["routes"].insert(0, {"id": "unreachable-route", "name": "Unreachable", "enabled": True, "match": {"host": "unreachable.local", "path_prefix": "/", "method": ""}, "upstream_id": "unreachable"})
        try:
            status, _ = api("PUT", "/api/config", changed)
            self.assertEqual(status, 200)
            request = Request(f"{GATEWAY_URL}/", headers={"Host": "unreachable.local"})
            with self.assertRaises(HTTPError) as error:
                urlopen(request, timeout=5)
            self.assertEqual(error.exception.code, 502)
            with urlopen(f"{GATEWAY_URL}/", timeout=5) as response:
                self.assertEqual(response.status, 200)
        finally:
            api("PUT", "/api/config", original)

    def test_weighted_targets_and_passive_failover(self):
        _, raw = api("GET", "/api/config")
        original = json.loads(raw)
        changed = json.loads(raw)
        changed["upstreams"].extend([
            {
                "id": "failover-pool", "name": "Failover pool", "url": "http://127.0.0.1:1", "host_header": "127.0.0.1:1",
                "targets": [
                    {"id": "broken", "url": "http://127.0.0.1:1", "host_header": "127.0.0.1:1", "weight": 1},
                    {"id": "healthy", "url": "http://demo:9000", "host_header": "demo:9000", "weight": 1},
                ],
                "max_failures": 1, "failure_timeout_ms": 30000,
            },
            {
                "id": "weighted-pool", "name": "Weighted pool", "url": "http://demo:9000", "host_header": "demo:9000",
                "targets": [
                    {"id": "primary", "url": "http://demo:9000", "host_header": "demo:9000", "weight": 1},
                    {"id": "secondary", "url": "http://demo2:9000", "host_header": "demo2:9000", "weight": 3},
                ],
                "max_failures": 1, "failure_timeout_ms": 30000,
            },
        ])
        changed["routes"][:0] = [
            {"id": "failover-route", "name": "Failover", "enabled": True, "match": {"host": "failover.local", "path_prefix": "/", "method": ""}, "upstream_id": "failover-pool"},
            {"id": "weighted-route", "name": "Weighted", "enabled": True, "match": {"host": "weighted.local", "path_prefix": "/", "method": ""}, "upstream_id": "weighted-pool"},
        ]
        try:
            api("PUT", "/api/config", changed)
            request = Request(f"{GATEWAY_URL}/?ms=0", headers={"Host": "failover.local"})
            with self.assertRaises(HTTPError) as first_error:
                urlopen(request, timeout=5)
            self.assertEqual(first_error.exception.code, 502)
            with urlopen(request, timeout=5) as response:
                self.assertEqual(response.status, 200)
                self.assertEqual(response.headers.get("X-HTTPV-Demo-Instance"), "primary")

            counts = {"primary": 0, "secondary": 0}
            for _ in range(8):
                request = Request(f"{GATEWAY_URL}/?ms=0", headers={"Host": "weighted.local"})
                with urlopen(request, timeout=5) as response:
                    counts[response.headers.get("X-HTTPV-Demo-Instance")] += 1
            self.assertEqual(counts, {"primary": 2, "secondary": 6})

            with urlopen("http://127.0.0.1:18091/internal/metrics", timeout=5) as response:
                metrics = response.read().decode()
            self.assertRegex(metrics, r"httpv_openresty_upstream_failures_total [1-9]")
            self.assertRegex(metrics, r"httpv_openresty_upstream_failover_skips_total [1-9]")
        finally:
            api("PUT", "/api/config", original)

    def test_config_revision_rollback(self):
        _, raw = api("GET", "/api/config")
        original = json.loads(raw)
        _, revision_raw = api("GET", "/api/config/revisions")
        original_revision = json.loads(revision_raw)["current"]
        changed = json.loads(raw)
        changed["subjects"][0]["name"] = "Changed by E2E"
        api("PUT", "/api/config", changed)
        _, changed_raw = api("GET", "/api/config/revisions")
        changed_revision = json.loads(changed_raw)["current"]
        self.assertGreater(changed_revision, original_revision)
        _, rollback_raw = api("POST", f"/api/config/rollback/{original_revision}")
        rollback = json.loads(rollback_raw)
        self.assertGreater(rollback["revision"], changed_revision)
        self.assertEqual(rollback["config"]["subjects"][0]["name"], original["subjects"][0]["name"])

    def test_timeout_block_completes_without_aggregation(self):
        api("PUT", "/api/subjects/default", subject(True, 100, "block"))
        self.page.reload(wait_until="networkidle")
        self.page.get_by_role("button", name=re.compile("发送当前请求|Send current request")).click()
        expect(self.page.locator(".flow-status")).to_contain_text(re.compile("已完成：403|Completed: 403"), timeout=5_000)
        self.assertEqual(self.page.locator(".overload-strip").count(), 0)

    def test_manual_block_decision_completes(self):
        api("PUT", "/api/subjects/default", subject(True, 5_000, "block"))
        self.page.reload(wait_until="networkidle")
        expect(self.page.locator(".status.live")).to_be_visible(timeout=10_000)
        started_at = int(time.time() * 1000)
        api("POST", "/api/demo/burst", {"count": 1})
        deadline = time.time() + 5
        request_id = None
        while time.time() < deadline:
            _, raw = api("GET", "/api/events")
            for event in reversed(json.loads(raw)):
                if event.get("phase") == "request_pending" and event.get("timestamp_ms", 0) >= started_at:
                    request_id = event["request_id"]
                    break
            if request_id:
                break
            time.sleep(0.1)
        self.assertIsNotNone(request_id, "request did not enter the manual gate")
        api("POST", f"/api/pending/{request_id}", {"action": "block"})
        expect(self.page.locator(".flow-status")).to_contain_text(re.compile("已完成：403|Completed: 403"), timeout=5_000)

    def test_pending_limit_rejects_overflow(self):
        api("PUT", "/api/subjects/default", subject(True, 5_000, "block", 1, "reject"))

        def gateway_request():
            try:
                with urlopen(f"{GATEWAY_URL}/", timeout=10) as response:
                    return response.status
            except HTTPError as error:
                return error.code

        with ThreadPoolExecutor(max_workers=1) as executor:
            first = executor.submit(gateway_request)
            deadline = time.time() + 5
            while time.time() < deadline:
                _, raw = api("GET", "/api/events")
                if any(event.get("phase") == "request_pending" for event in json.loads(raw)):
                    break
                time.sleep(0.05)
            else:
                self.fail("first request did not reserve the pending slot")

            self.assertEqual(gateway_request(), 503)
            api("POST", "/api/pending/block-all")
            self.assertEqual(first.result(timeout=5), 403)

        _, raw = api("GET", "/api/events")
        self.assertTrue(any(event.get("phase") == "request_overflow" and event.get("action") == "reject" for event in json.loads(raw)))

    def test_high_traffic_shows_aggregation(self):
        for _ in range(10):
            api("POST", "/api/demo/burst", {"count": 100, "path": "/?ms=0"})
        expect(self.page.locator(".overload-strip")).to_be_visible(timeout=8_000)
        wait_for_demo_idle()
        wait_for_telemetry_idle()


if __name__ == "__main__":
    unittest.main(verbosity=2)
