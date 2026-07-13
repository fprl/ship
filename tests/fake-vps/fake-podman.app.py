"""Fake app container listener used by tests/fake-vps/fake-podman.

Binds to an OS-assigned localhost port and writes that port number to
the file path given as argv[1] so fake-podman state can record where
this container is reachable from the host. Answers 200 "ok" to every
GET/HEAD/POST so the helper's deploy-time health check and any
Caddy-proxied request both succeed.
"""

import os
import sqlite3
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        print(f"{self.command} {self.path}", flush=True)

    def _ok(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", "2")
        self.end_headers()
        self.wfile.write(b"ok")

    def _ship_env(self):
        keys = ["SHIP_URL", "SHIP_BRANCH", "SHIP_ENV", "SHIP_RELEASE"]
        body = "".join(f"{key}={os.environ.get(key, '')}\n" for key in keys).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _data_count(self):
        data_dir = os.environ.get("SHIP_FAKE_DATA_DIR", "")
        db = os.path.join(data_dir, "app.db")
        count = "missing"
        if os.path.exists(db):
            with sqlite3.connect(db) as conn:
                count = str(conn.execute("select count(*) from items").fetchone()[0])
        body = count.encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _upload_file(self):
        data_dir = os.environ.get("SHIP_FAKE_DATA_DIR", "")
        path = os.path.join(data_dir, "uploads", "file.txt")
        try:
            with open(path, "rb") as f:
                body = f.read()
        except OSError:
            body = b"missing"
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _request_headers(self):
        body = (
            f"authorization={self.headers.get('Authorization', '')}\n"
            f"x-ship-capability={self.headers.get('x-ship-capability', '')}\n"
            f"cookie={self.headers.get('Cookie', '')}\n"
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/ship-env":
            self._ship_env()
            return
        if self.path == "/data-count":
            self._data_count()
            return
        if self.path == "/upload-file":
            self._upload_file()
            return
        if self.path == "/request-headers":
            self._request_headers()
            return
        self._ok()

    def do_HEAD(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.end_headers()

    def do_POST(self):
        self._ok()


def main():
    if len(sys.argv) != 2:
        print("usage: fake-podman.app.py <port_file>", file=sys.stderr)
        sys.exit(2)
    port_file = sys.argv[1]
    server = HTTPServer(("127.0.0.1", 0), Handler)
    port = server.server_address[1]
    with open(port_file, "w") as f:
        f.write(str(port) + "\n")
    if os.environ.get("LEAK_ON_PROBE_LOG"):
        print(f"probe log marker {os.environ['LEAK_ON_PROBE_LOG']}", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
