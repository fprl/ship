"""Fake Caddy reverse proxy used by tests/fake-vps/fake-podman.

Stands in for `caddy:2-alpine` inside the fake-vps container. Listens
on argv[1] (the --publish host port) and on every request:

  1. Reads /etc/caddy/conf.d/*.caddy fragments.
  2. Looks for a site block whose label matches the request's Host
     header.
  3. Pulls `handle`, `handle_path`, `reverse_proxy`, `root`, and `redir`
     directives out of that block.
  4. Resolves <container_name> to a localhost port via
     <state_dir>/containers/<container_name>.port (written by
     fake-podman when it started the app container).
  5. Proxies the request to that local port.

`redir` directives are honored as 301s. Anything unmatched returns
404. The proxy re-reads conf.d/ on every request, so a deploy that
updates fragments takes effect without any explicit reload signal —
which is why fake-podman can leave `podman exec caddy caddy reload`
as a no-op.
"""

import http.client
import base64
import crypt
import os
import re
import sys
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


SITE_RE = re.compile(r'^\s*"(?P<host>[^"]+)"\s*\{\s*$')
PROXY_RE = re.compile(r'^\s*reverse_proxy\s+http://(?P<upstream>[A-Za-z0-9_.-]+):(?P<port>\d+)\s*$')
REDIR_RE = re.compile(r'^\s*redir\s+"(?P<to>[^"]+)"\s+permanent\s*$')
ROOT_RE = re.compile(r'^\s*root\s+\*\s+"(?P<root>[^"]+)"\s*$')
BYPASS_RE = re.compile(r'^\s*not\s+header\s+x-ship-bypass\s+"(?P<token>[^"]+)"\s*$')
TEAM_HASH_RE = re.compile(r'^\s*team\s+"(?P<hash>\$2[^"]+)"\s*$')
SHARE_QUERY_RE = re.compile(r'^\s*@ship_share\s+query\s+"ship_share=(?P<token>[^"]+)"\s*$')

CONF_DIR = "/etc/caddy/conf.d"


HANDLE_RE = re.compile(r'^\s*handle(?:\s+(?P<path>/[^\s{]+))?\s*\{\s*$')
HANDLE_PATH_RE = re.compile(r'^\s*handle_path\s+(?P<path>/[^\s{]+)\s*\{\s*$')
REWRITE_ROOT_RE = re.compile(r'^\s*rewrite\s+\*\s+/\s*$')


def load_routes():
    """Return {host: [route, ...]} preserving generated Caddy order."""
    routes = {}
    if not os.path.isdir(CONF_DIR):
        return routes
    for name in sorted(os.listdir(CONF_DIR)):
        if not name.endswith(".caddy"):
            continue
        path = os.path.join(CONF_DIR, name)
        try:
            with open(path) as f:
                lines = f.readlines()
        except OSError:
            continue

        current_host = None
        current_route = None
        direct_route = None
        skip_depth = 0
        for raw in lines:
            line = raw.rstrip("\n")
            stripped = line.strip()
            if stripped.startswith("#") or stripped == "":
                continue
            if current_host is None:
                m = SITE_RE.match(line)
                if m:
                    current_host = m.group("host")
                    routes.setdefault(current_host, [])
                    direct_route = None
                continue
            if skip_depth > 0:
                if stripped.endswith("{"):
                    skip_depth += 1
                elif stripped == "}":
                    skip_depth -= 1
                continue
            if current_route is not None and stripped == "}":
                routes[current_host].append(current_route)
                current_route = None
                continue
            if stripped == "}":
                current_host = None
                direct_route = None
                continue
            m = HANDLE_PATH_RE.match(line)
            if m:
                current_route = {"match": "prefix-strip", "path": m.group("path").removesuffix("/*")}
                continue
            m = HANDLE_RE.match(line)
            if m:
                path = m.group("path") or ""
                match = "fallback"
                if path.endswith("/*"):
                    path = path.removesuffix("/*")
                    match = "prefix"
                elif path:
                    match = "exact"
                current_route = {"match": match, "path": path}
                continue
            if current_route is None and stripped.endswith("{"):
                # Site-level block that is not a handle (the @ship_auth
                # matcher or basic_auth stanza) — routes never live inside.
                skip_depth = 1
                continue
            target_route = current_route
            if target_route is None:
                if direct_route is None:
                    direct_route = {"match": "fallback", "path": ""}
                    routes[current_host].append(direct_route)
                target_route = direct_route
            m = PROXY_RE.match(line)
            if m:
                target_route["proxy"] = (m.group("upstream"), int(m.group("port")))
                continue
            m = REDIR_RE.match(line)
            if m:
                target_route["redir"] = m.group("to")
                continue
            m = ROOT_RE.match(line)
            if m:
                target_route["static"] = m.group("root")
                continue
            if REWRITE_ROOT_RE.match(line):
                target_route["rewrite_root"] = True
                continue
    return routes


def load_protections():
    """Read generated preview basic_auth, bypass, and share stanzas."""
    protections = {}
    if not os.path.isdir(CONF_DIR):
        return protections
    for name in sorted(os.listdir(CONF_DIR)):
        if not name.endswith(".caddy"):
            continue
        try:
            with open(os.path.join(CONF_DIR, name)) as f:
                lines = f.readlines()
        except OSError:
            continue
        host = None
        token = None
        hash_value = None
        share_token = None
        for raw in lines:
            line = raw.rstrip("\n")
            m = SITE_RE.match(line)
            if m:
                host = m.group("host")
                token = None
                hash_value = None
                share_token = None
                continue
            if host is None:
                continue
            m = BYPASS_RE.match(line)
            if m:
                token = m.group("token")
                continue
            m = TEAM_HASH_RE.match(line)
            if m:
                hash_value = m.group("hash")
                continue
            m = SHARE_QUERY_RE.match(line)
            if m:
                share_token = m.group("token")
                continue
            if line.strip() == "}" and token and hash_value:
                protections[host] = (token, hash_value, share_token)
                host = None
    return protections


def container_port(state_dir, name):
    path = os.path.join(state_dir, "containers", name + ".port")
    try:
        with open(path) as f:
            return int(f.read().strip())
    except (OSError, ValueError):
        return None


class Handler(BaseHTTPRequestHandler):
    state_dir = ""  # set in main()

    def log_message(self, *args):
        return

    def host_header(self):
        raw = self.headers.get("Host", "")
        return raw.split(":", 1)[0].lower().rstrip(".")

    def not_found(self, msg=b"not found"):
        self.send_response(404)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(msg)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(msg)

    def proxy(self, upstream_name, _upstream_port):
        # The Caddy fragment names an in-container port. In the fake we
        # don't actually run on that port; the app container's
        # fake-podman.app.py listener bound an OS-assigned local port
        # and recorded it. Look that up and proxy.
        local_port = container_port(self.state_dir, upstream_name)
        if local_port is None:
            self.not_found(b"unknown upstream " + upstream_name.encode())
            return

        body_length = int(self.headers.get("Content-Length", "0") or 0)
        body = self.rfile.read(body_length) if body_length > 0 else b""

        forward = http.client.HTTPConnection("127.0.0.1", local_port, timeout=5)
        forward_headers = {}
        for name, value in self.headers.items():
            if name.lower() in ("host", "content-length", "connection"):
                continue
            forward_headers[name] = value
        try:
            forward.request(self.command, self.path, body=body, headers=forward_headers)
            resp = forward.getresponse()
            resp_body = resp.read()
        except Exception as exc:
            self.send_response(502)
            self.send_header("Content-Type", "text/plain")
            msg = f"upstream {upstream_name} unreachable: {exc}".encode()
            self.send_header("Content-Length", str(len(msg)))
            self.end_headers()
            if self.command != "HEAD":
                self.wfile.write(msg)
            return
        finally:
            forward.close()

        self.send_response(resp.status)
        skip = {"connection", "transfer-encoding"}
        for name, value in resp.getheaders():
            if name.lower() in skip:
                continue
            self.send_header(name, value)
        self.send_header("Content-Length", str(len(resp_body)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(resp_body)

    def redir(self, to):
        self.send_response(301)
        self.send_header("Location", to)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def static(self, root, rel=None):
        if rel is None:
            rel = self.path.split("?", 1)[0].split("#", 1)[0]
        if rel in ("", "/"):
            rel = "/index.html"
        rel = os.path.normpath(rel.lstrip("/"))
        if rel.startswith("..") or os.path.isabs(rel):
            self.not_found()
            return
        target = os.path.abspath(os.path.join(root, rel))
        root_abs = os.path.abspath(root)
        if target != root_abs and not target.startswith(root_abs + os.sep):
            self.not_found()
            return
        if os.path.isdir(target):
            target = os.path.join(target, "index.html")
        try:
            with open(target, "rb") as f:
                body = f.read()
        except OSError:
            self.not_found()
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)

    def serve(self):
        routes = load_routes()
        host = self.host_header()
        protection = load_protections().get(host)
        if protection and self.share_redirect(protection[2]):
            return
        if protection and not self.authorized(*protection):
            self.send_response(401)
            self.send_header("WWW-Authenticate", 'Basic realm="ship preview"')
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        route = self.match_route(routes.get(host, []))
        if route is None:
            self.not_found()
            return
        if "proxy" in route:
            self.proxy(*route["proxy"])
            return
        if "redir" in route:
            self.redir(route["redir"])
            return
        if "static" in route:
            self.static(route["static"], route.get("request_path"))
            return
        self.not_found()

    def share_redirect(self, share_token):
        if not share_token:
            return False
        query = urllib.parse.parse_qs(urllib.parse.urlsplit(self.path).query)
        if query.get("ship_share") != [share_token]:
            return False
        self.send_response(302)
        self.send_header("Set-Cookie", "ship_share=" + share_token + "; Path=/; HttpOnly; Secure")
        self.send_header("Location", self.path.split("?", 1)[0])
        self.send_header("Content-Length", "0")
        self.end_headers()
        return True

    def authorized(self, bypass_token, password_hash, share_token):
        if self.headers.get("x-ship-bypass", "") == bypass_token:
            return True
        if share_token and any(cookie.strip() == "ship_share=" + share_token for cookie in self.headers.get("Cookie", "").split(";")):
            return True
        raw = self.headers.get("Authorization", "")
        if not raw.startswith("Basic "):
            return False
        try:
            user_password = base64.b64decode(raw[6:]).decode("utf-8")
            user, password = user_password.split(":", 1)
        except Exception:
            return False
        return user == "team" and crypt.crypt(password, password_hash) == password_hash

    def match_route(self, routes):
        path = self.path.split("?", 1)[0].split("#", 1)[0]
        fallback = None
        for route in routes:
            match = route.get("match")
            route_path = route.get("path", "")
            if match == "fallback":
                fallback = route
                continue
            if match == "exact" and path == route_path:
                matched = dict(route)
                if matched.get("rewrite_root"):
                    matched["request_path"] = "/"
                return matched
            if match == "prefix" and (path == route_path or path.startswith(route_path + "/")):
                return route
            if match == "prefix-strip" and path.startswith(route_path + "/"):
                matched = dict(route)
                stripped = path[len(route_path):]
                matched["request_path"] = stripped or "/"
                return matched
        return fallback

    def do_GET(self):
        self.serve()

    def do_HEAD(self):
        self.serve()

    def do_POST(self):
        self.serve()


class Server(ThreadingHTTPServer):
    allow_reuse_address = True


def main():
    if len(sys.argv) != 3:
        print("usage: fake-podman.caddy.py <port> <state_dir>", file=sys.stderr)
        sys.exit(2)
    port = int(sys.argv[1])
    Handler.state_dir = sys.argv[2]
    Server(("0.0.0.0", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
