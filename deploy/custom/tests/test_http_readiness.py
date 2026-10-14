import hashlib
import importlib.util
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


spec = importlib.util.spec_from_file_location("readiness_http", Path(__file__).parents[1] / "readiness.py")
readiness = importlib.util.module_from_spec(spec)
spec.loader.exec_module(readiness)


class HTTPReadinessTests(unittest.TestCase):
    def setUp(self):
        self.panel = b"<html>pinned panel</html>"
        self.calls = []
        self.mode = "normal"
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                owner.calls.append(self.path)
                if owner.mode == "redirect" and self.path == "/v1/models" and self.headers.get("Authorization"):
                    self.send_response(302)
                    self.send_header("Location", "/leaked")
                    self.end_headers()
                    return
                code = 401 if self.path == "/v1/models" and not self.headers.get("Authorization") else 200
                self.send_response(code)
                self.end_headers()
                if self.path == "/management.html":
                    self.wfile.write(owner.panel)

            def log_message(self, *_args):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.start()
        self.addCleanup(self.stop)
        self.base = "http://127.0.0.1:" + str(self.server.server_port)

    def stop(self):
        self.server.shutdown()
        self.thread.join()
        self.server.server_close()

    def test_repeated_gates_do_not_attempt_anonymous_management_login(self):
        digest = hashlib.sha256(self.panel).hexdigest()
        for _ in range(6):
            readiness.verify_http(self.base, ["b" * 64], digest)
        self.assertFalse(any(path.startswith("/v8/management") for path in self.calls))

    def test_served_panel_must_match_release_checksum(self):
        digest = hashlib.sha256(self.panel).hexdigest()
        self.panel = b"<html>wrong panel</html>"
        with self.assertRaises(ValueError):
            readiness.verify_http(self.base, ["b" * 64], digest)

    def test_redirects_are_rejected_without_forwarding_credentials(self):
        self.mode = "redirect"
        with self.assertRaises(ValueError):
            readiness.verify_http(self.base, ["b" * 64], hashlib.sha256(self.panel).hexdigest())
        self.assertNotIn("/leaked", self.calls)


if __name__ == "__main__":
    unittest.main()
