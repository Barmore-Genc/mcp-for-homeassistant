"""Walks the sign-in flows in a real Chromium, because the page's CSP
form-action rules are enforced by browsers and not by any Go test."""

import base64
import hashlib
import json
import os
import secrets
import subprocess
import sys
import threading
import time
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer

from playwright.sync_api import sync_playwright

BIN = "/work/.bin/mcp-for-homeassistant"
CALLBACK = "http://127.0.0.1:33418/callback"
IDP = "http://127.0.0.1:18940"
IDP_AUTHORIZE = "http://127.0.0.1:18941/authorize"

LANDED = []


class Landing(BaseHTTPRequestHandler):
    def do_GET(self):
        LANDED.append(f"http://{self.headers['Host']}{self.path}")
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.end_headers()
        self.wfile.write(b"landed")

    def log_message(self, *args):
        pass


def serve(port, handler):
    srv = HTTPServer(("127.0.0.1", port), handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


class DiscoveryOnly(BaseHTTPRequestHandler):
    def do_GET(self):
        doc = {
            "issuer": IDP,
            "authorization_endpoint": IDP_AUTHORIZE,
            "token_endpoint": IDP + "/token",
            "jwks_uri": IDP + "/jwks",
            "response_types_supported": ["code"],
            "subject_types_supported": ["public"],
            "id_token_signing_alg_values_supported": ["RS256"],
        }
        body = json.dumps(doc).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


def start_server(port, extra_env):
    origin = f"http://127.0.0.1:{port}"
    env = {
        "PATH": os.environ["PATH"],
        "HA_URL": os.environ["HA_URL"],
        "HA_TOKEN": os.environ["HA_TOKEN"],
        "MCP_ORIGIN": origin,
        "MCP_ADDR": f"127.0.0.1:{port}",
        "MCP_SIGNING_KEY": base64.b64encode(secrets.token_bytes(32)).decode(),
        **extra_env,
    }
    proc = subprocess.Popen([BIN], env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    for _ in range(100):
        try:
            urllib.request.urlopen(origin + "/healthz", timeout=1)
            return origin, proc
        except Exception:
            time.sleep(0.1)
    proc.kill()
    sys.exit(f"server on {origin} did not start:\n{proc.stdout.read().decode()}")


def post(url, data, json_body=False):
    if json_body:
        req = urllib.request.Request(url, json.dumps(data).encode(), {"Content-Type": "application/json"})
    else:
        req = urllib.request.Request(url, urllib.parse.urlencode(data).encode())
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.load(r)


def authorize_url(origin, verifier):
    client = post(origin + "/register", {"redirect_uris": [CALLBACK], "client_name": "Playwright"}, json_body=True)
    challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b"=").decode()
    q = {
        "client_id": client["client_id"],
        "redirect_uri": CALLBACK,
        "state": "st1",
        "code_challenge": challenge,
        "code_challenge_method": "S256",
        "response_type": "code",
        "scope": "homeassistant",
    }
    return client["client_id"], origin + "/authorize?" + urllib.parse.urlencode(q)


def new_page(browser):
    page = browser.new_context().new_page()
    console = []
    page.on("console", lambda m: console.append(m.text) if m.type == "error" else None)
    return page, console


def check_csp(console, name):
    violations = [c for c in console if "Content Security Policy" in c]
    if violations:
        sys.exit(f"FAIL {name}: CSP violations {violations}")


def password_flow(browser):
    origin, proc = start_server(18932, {"MCP_USERNAME": "operator", "MCP_PASSWORD": "correct-horse-battery"})
    try:
        verifier = secrets.token_urlsafe(48)
        client_id, url = authorize_url(origin, verifier)
        landed = LANDED
        page, console = new_page(browser)
        resp = page.goto(url)
        headers = resp.headers
        assert "frame-ancestors 'none'" in headers.get("content-security-policy", ""), headers
        assert headers.get("x-frame-options") == "DENY", headers
        assert "127.0.0.1:33418" in page.content(), "consent page does not show the redirect host"
        page.fill("#username", "operator")
        page.fill("#password", "correct-horse-battery")
        page.click("button.allow")
        page.wait_for_url(CALLBACK + "*", timeout=10000)
        check_csp(console, "password flow")
        q = urllib.parse.parse_qs(urllib.parse.urlparse(landed[-1]).query)
        assert q["state"] == ["st1"], q
        tok = post(origin + "/token", {
            "grant_type": "authorization_code",
            "code": q["code"][0],
            "client_id": client_id,
            "redirect_uri": CALLBACK,
            "code_verifier": verifier,
        })
        req = urllib.request.Request(origin + "/mcp", json.dumps({
            "jsonrpc": "2.0", "id": 1, "method": "initialize",
            "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "e2e", "version": "0"}},
        }).encode(), {
            "Authorization": "Bearer " + tok["access_token"],
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
        })
        with urllib.request.urlopen(req, timeout=10) as r:
            assert r.status == 200 and b"homeassistant" in r.read()
        print("ok password flow: sign-in, redirect, token, /mcp initialize")

        page.goto(url)
        page.fill("#username", "operator")
        page.fill("#password", "wrong-password-123")
        page.click("button.allow")
        page.wait_for_load_state()
        assert page.url.startswith(origin), page.url
        assert len(landed) == 1, landed
        print("ok password flow: wrong password stays on the sign-in page")

        page.goto(url)
        page.click("button.deny")
        page.wait_for_url(CALLBACK + "*", timeout=10000)
        check_csp(console, "cancel")
        assert "error=access_denied" in landed[-1], landed[-1]
        print("ok password flow: cancel redirects with access_denied")
    finally:
        proc.kill()


def oidc_flow(browser):
    idp = serve(18940, DiscoveryOnly)
    origin, proc = start_server(18933, {
        "MCP_OIDC_ISSUER": IDP,
        "MCP_OIDC_CLIENT_ID": "e2e",
        "MCP_OIDC_CLIENT_SECRET": "e2e-secret",
        "MCP_OIDC_ALLOWED_SUBJECTS": "someone",
    })
    try:
        _, url = authorize_url(origin, secrets.token_urlsafe(48))
        landed = LANDED
        page, console = new_page(browser)
        page.goto(url)
        page.click("button.allow")
        page.wait_for_url(IDP_AUTHORIZE + "*", timeout=10000)
        check_csp(console, "oidc flow")
        q = urllib.parse.parse_qs(urllib.parse.urlparse(landed[-1]).query)
        assert q["client_id"] == ["e2e"] and "state" in q, q
        cookies = [c["name"] for c in page.context.cookies() if c["name"].startswith("mcp-oidc-")]
        assert cookies, "no browser-binding cookie was set"
        print("ok oidc flow: continue redirects to the provider with a binding cookie")
    finally:
        proc.kill()
        idp.shutdown()


serve(33418, Landing)
serve(18941, Landing)
with sync_playwright() as p:
    browser = p.chromium.launch()
    password_flow(browser)
    oidc_flow(browser)
    browser.close()
print("all sign-in checks passed")
