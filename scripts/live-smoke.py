#!/usr/bin/env python3
"""Exercise the real compiled server in a disposable directory (stdlib only)."""
import html
import http.cookiejar
import os
from pathlib import Path
import re
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


class Browser:
    def __init__(self, base):
        self.base = base
        self.jar = http.cookiejar.CookieJar()
        self.client = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(self.jar)
        )
        self.csrf = ""

    def get(self, path):
        with self.client.open(self.base + path, timeout=10) as response:
            body = response.read().decode()
            match = re.search(r'name="csrf"\s+value="([^"]+)"', body)
            if match:
                self.csrf = html.unescape(match.group(1))
            return body

    def post(self, path, fields, expected=200):
        # urllib follows the server's 303 to the resulting GET page.
        pairs = [("csrf", self.csrf)] + fields
        request = urllib.request.Request(
            self.base + path,
            data=urllib.parse.urlencode(pairs).encode(),
            headers={"Content-Type": "application/x-www-form-urlencoded"},
        )
        try:
            with self.client.open(request, timeout=10) as response:
                status, body = response.status, response.read().decode()
        except urllib.error.HTTPError as error:
            status, body = error.code, error.read().decode()
        assert status == expected, (path, status, expected, body[:300])
        match = re.search(r'name="csrf"\s+value="([^"]+)"', body)
        if match:
            self.csrf = html.unescape(match.group(1))
        return body


def main():
    root = Path(__file__).resolve().parent.parent
    with tempfile.TemporaryDirectory(prefix="discuss-hub-smoke-") as directory:
        binary = Path(directory) / "server"
        subprocess.run(
            ["go", "build", "-o", str(binary), "./cmd/server"], cwd=root, check=True
        )
        with socket.socket() as reserve:
            reserve.bind(("127.0.0.1", 0))
            port = reserve.getsockname()[1]
        base = f"http://127.0.0.1:{port}"
        environment = dict(os.environ, ADDR=f"127.0.0.1:{port}",
                           DB_PATH=str(Path(directory) / "forum.db"),
                           COOKIE_SECURE="false")
        process = None
        log = open(Path(directory) / "server.log", "w+")

        def start():
            process = subprocess.Popen([str(binary)], env=environment,
                                       stdout=log, stderr=log)
            for _ in range(100):
                if process.poll() is not None:
                    raise RuntimeError("server exited before listening")
                try:
                    with urllib.request.urlopen(base + "/healthz", timeout=1):
                        return process
                except (urllib.error.URLError, TimeoutError):
                    time.sleep(0.05)
            process.terminate()
            process.wait(timeout=15)
            raise RuntimeError("server did not become ready")

        def stop(process):
            process.send_signal(signal.SIGTERM)
            process.wait(timeout=15)
            assert process.returncode == 0, "shutdown was not graceful"

        try:
            process = start()
            alice, guest = Browser(base), Browser(base)
            assert "Community discussions" in guest.get("/")
            alice.get("/register")
            alice.post("/register", [("email", "alice@example.invalid"),
                                     ("username", "audit_alice"),
                                     ("password", "smoke-test-only-password")])
            alice.post("/login", [("email", "alice@example.invalid"),
                                  ("password", "smoke-test-only-password")])
            alice.get("/posts/new")
            body = alice.post("/posts", [("title", "Live audit discussion"),
                                         ("content", "great love bad hate"),
                                         ("categories", "1"), ("categories", "2")])
            assert "Live audit discussion" in body
            alice.post("/posts/1/comments", [("content", "A helpful reply")])
            alice.post("/reactions", [("kind", "post"), ("id", "1"), ("value", "1")])
            assert "A helpful reply" in guest.get("/posts/1")
            assert "Neutral" in guest.get("/insights")
            assert '"topics"' in guest.get("/insights/trending?format=json")
            assert "Live audit discussion" in alice.get("/?scope=liked&category=2")
            alice.post("/posts", [("title", " "), ("content", " ")], expected=400)
            stop(process)
            process = start()
            # Database and valid session survive restart. GET renews CSRF.
            assert "audit_alice" in alice.get("/posts/new")
            assert "Live audit discussion" in guest.get("/posts/1")
            alice.post("/posts/1/comments", [("content", "After restart")])
            assert "After restart" in guest.get("/posts/1")
            alice.post("/logout", [])
            stop(process)
            process = None
            print("PASS: real HTTP registration, login, multi-category post, reply, reaction,")
            print("guest reading, filters, insights, invalid input, SIGTERM and session/data persistence.")
        except BaseException:
            log.flush()
            log.seek(0)
            print(log.read(), file=sys.stderr)
            raise
        finally:
            if process is not None and process.poll() is None:
                process.terminate()
                process.wait(timeout=15)
            log.close()


if __name__ == "__main__":
    main()
