import json
import os
import re
import time
import unittest
from concurrent.futures import ThreadPoolExecutor
from urllib.request import Request, urlopen

from playwright.sync_api import expect, sync_playwright


CONSOLE_URL = os.getenv("HTTPV_E2E_CONSOLE_URL", "http://127.0.0.1:15173")
CONTROL_URL = os.getenv("HTTPV_E2E_CONTROL_URL", "http://127.0.0.1:18090")


def api(method, path, payload=None):
    body = None if payload is None else json.dumps(payload).encode()
    request = Request(f"{CONTROL_URL}{path}", data=body, method=method)
    if body is not None:
        request.add_header("Content-Type", "application/json")
    with urlopen(request, timeout=5) as response:
        return response.status, response.read()


def subject(gate_enabled=False, timeout_ms=30_000, timeout_action="block"):
    return {
        "id": "demo-api",
        "name": "Demo API",
        "enabled": True,
        "display": True,
        "match": {"host": "", "path_prefix": "/", "method": ""},
        "gate": {
            "enabled": gate_enabled,
            "timeout_ms": timeout_ms,
            "timeout_action": timeout_action,
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
        api("PUT", "/api/subjects/demo-api", subject())
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

    def test_timeout_block_completes_without_aggregation(self):
        api("PUT", "/api/subjects/demo-api", subject(True, 100, "block"))
        self.page.reload(wait_until="networkidle")
        self.page.get_by_role("button", name=re.compile("发送当前请求|Send current request")).click()
        expect(self.page.locator(".flow-status")).to_contain_text(re.compile("已完成：403|Completed: 403"), timeout=5_000)
        self.assertEqual(self.page.locator(".overload-strip").count(), 0)

    def test_manual_block_decision_completes(self):
        api("PUT", "/api/subjects/demo-api", subject(True, 5_000, "block"))
        self.page.reload(wait_until="networkidle")
        api("POST", "/api/demo/burst", {"count": 1, "delay_ms": 0})
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

    def test_high_traffic_shows_aggregation(self):
        def burst(_):
            return api("POST", "/api/demo/burst", {"count": 100, "delay_ms": 0})

        with ThreadPoolExecutor(max_workers=4) as executor:
            list(executor.map(burst, range(4)))
        expect(self.page.locator(".overload-strip")).to_be_visible(timeout=8_000)


if __name__ == "__main__":
    unittest.main(verbosity=2)
