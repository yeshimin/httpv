import json
import os
import re
import time
import unittest
from concurrent.futures import ThreadPoolExecutor
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from playwright.sync_api import expect, sync_playwright


CONSOLE_URL = os.getenv("HTTPV_E2E_CONSOLE_URL", "http://127.0.0.1:15173")
CONTROL_URL = os.getenv("HTTPV_E2E_CONTROL_URL", "http://127.0.0.1:18090")
GATEWAY_URL = os.getenv("HTTPV_E2E_GATEWAY_URL", "http://127.0.0.1:18088")


def api(method, path, payload=None):
    body = None if payload is None else json.dumps(payload).encode()
    request = Request(f"{CONTROL_URL}{path}", data=body, method=method)
    if body is not None:
        request.add_header("Content-Type", "application/json")
    with urlopen(request, timeout=5) as response:
        return response.status, response.read()


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

    def tearDown(self):
        self.page.close()

    def test_canvas_has_visible_lane_skeleton(self):
        canvas = self.page.locator(".traffic-canvas")
        expect(canvas).to_be_visible()
        self.assertGreater(canvas.evaluate("node => node.clientHeight"), 300)
        self.assertEqual(self.page.locator(".lane-label").all_text_contents(), ["Client", "OpenResty", "Upstream"])

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

    def test_config_validates_multiple_routes(self):
        _, raw = api("GET", "/api/config")
        config = json.loads(raw)
        config["upstreams"].append({"id": "secondary", "name": "Secondary", "url": "http://demo:9000", "host_header": "demo:9000"})
        config["routes"].insert(0, {"id": "secondary-route", "name": "Secondary", "enabled": True, "match": {"host": "secondary.local", "path_prefix": "/v2", "method": "GET"}, "upstream_id": "secondary"})
        status, validated = api("POST", "/api/config/validate", config)
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(validated)["status"], "valid")

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

    def test_timeout_block_completes_without_aggregation(self):
        api("PUT", "/api/subjects/default", subject(True, 100, "block"))
        self.page.reload(wait_until="networkidle")
        self.page.get_by_role("button", name=re.compile("发送当前请求|Send current request")).click()
        expect(self.page.locator(".flow-status")).to_contain_text(re.compile("已完成：403|Completed: 403"), timeout=5_000)
        self.assertEqual(self.page.locator(".overload-strip").count(), 0)

    def test_manual_block_decision_completes(self):
        api("PUT", "/api/subjects/default", subject(True, 5_000, "block"))
        self.page.reload(wait_until="networkidle")
        api("POST", "/api/demo/burst", {"count": 1})
        deadline = time.time() + 5
        request_id = None
        while time.time() < deadline:
            _, raw = api("GET", "/api/events")
            for event in reversed(json.loads(raw)):
                if event.get("phase") == "request_pending":
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
        def burst(_):
            return api("POST", "/api/demo/burst", {"count": 100})

        with ThreadPoolExecutor(max_workers=10) as executor:
            list(executor.map(burst, range(10)))
        expect(self.page.locator(".overload-strip")).to_be_visible(timeout=8_000)


if __name__ == "__main__":
    unittest.main(verbosity=2)
