#!/usr/bin/env python3
"""Push: receive toli's notifications on a webhook.

Set it up once (the secret is printed once):

    toli agent notify my-agent --webhook http://127.0.0.1:8787/toli
    TOLI_WEBHOOK_SECRET=<secret> python3 webhook.py

toli POSTs {"agent", "text"} with "Authorization: Bearer <secret>" whenever
my-agent has no session. Answer 2xx within 30 seconds.

Python 3.9+, standard library only. Protocol: docs/custom-agents.md.
"""

import hmac
import json
import os
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

SECRET = os.environ.get("TOLI_WEBHOOK_SECRET", "")
PORT = int(os.environ.get("PORT", "8787"))


def on_notification(agent, text):
    """Send text to your agent as a user message, or start your agent with
    it. Replace with your agent's API."""
    print(f"[{agent}] {text}", flush=True)


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        auth = self.headers.get("Authorization", "")
        if not hmac.compare_digest(auth.encode(), f"Bearer {SECRET}".encode()):
            self.send_response(401)
            self.end_headers()
            return
        length = int(self.headers.get("Content-Length", 0))
        if length > 64 * 1024:
            self.send_response(413)
            self.end_headers()
            return
        try:
            note = json.loads(self.rfile.read(length))
            agent, text = note["agent"], note["text"]
        except (ValueError, KeyError):
            self.send_response(400)
            self.end_headers()
            return
        # Answer first: toli waits for the answer, not for your agent.
        self.send_response(204)
        self.end_headers()
        self.wfile.flush()
        on_notification(agent, text)

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    if not SECRET:
        sys.exit("set TOLI_WEBHOOK_SECRET to the secret toli agent notify printed")
    print(f"listening on 127.0.0.1:{PORT}", flush=True)
    HTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
