import json
import os
import socket
import subprocess
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Lock
from urllib.parse import parse_qs, urlsplit
from urllib.request import urlopen


state = {"delay_ms": 0, "fail_every": 0, "requests": 0}
lock = Lock()


def dependencies():
    checks = {}
    try:
        result = subprocess.run(
            ["psql", "-X", "-qAt", "-c", "SELECT 1"],
            capture_output=True,
            text=True,
            timeout=5,
            check=True,
        )
        checks["postgres"] = result.stdout.strip() == "1"
    except (OSError, subprocess.SubprocessError):
        checks["postgres"] = False
    try:
        with socket.create_connection(("redis", 6379), timeout=5) as peer:
            peer.sendall(b"*1\r\n$4\r\nPING\r\n")
            checks["redis"] = peer.recv(64).startswith(b"+PONG")
    except OSError:
        checks["redis"] = False
    for name, url in (
        ("object-store", "http://object-store:9000/minio/health/live"),
        ("registry", "http://registry:5000/v2/"),
    ):
        try:
            with urlopen(url, timeout=5) as response:
                checks[name] = response.status == 200
        except OSError:
            checks[name] = False
    return checks


class Handler(BaseHTTPRequestHandler):
    def send_json(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        path = urlsplit(self.path).path
        if path == "/health":
            self.send_json(200, {"ok": True})
            return
        if path == "/dependencies":
            checks = dependencies()
            self.send_json(200 if all(checks.values()) else 503, {"checks": checks})
            return
        if path != "/work":
            self.send_json(404, {"error": "not found"})
            return
        with lock:
            state["requests"] += 1
            count = state["requests"]
            delay_ms = state["delay_ms"]
            fail_every = state["fail_every"]
        if delay_ms:
            time.sleep(delay_ms / 1000)
        failed = fail_every > 0 and count % fail_every == 0
        self.send_json(503 if failed else 200, {
            "ok": not failed,
            "request": count,
            "node": os.uname().nodename,
            "delay_ms": delay_ms,
        })

    def do_POST(self):
        parsed = urlsplit(self.path)
        if parsed.path != "/control":
            self.send_json(404, {"error": "not found"})
            return
        params = parse_qs(parsed.query)
        try:
            delay_ms = int(params.get("delay_ms", ["0"])[0])
            fail_every = int(params.get("fail_every", ["0"])[0])
        except ValueError:
            self.send_json(400, {"error": "values must be integers"})
            return
        if not 0 <= delay_ms <= 10000 or not 0 <= fail_every <= 1000:
            self.send_json(400, {"error": "values out of range"})
            return
        with lock:
            state["delay_ms"] = delay_ms
            state["fail_every"] = fail_every
            state["requests"] = 0
            current = dict(state)
        self.send_json(200, current)


ThreadingHTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
