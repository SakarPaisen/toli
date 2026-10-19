#!/usr/bin/env python3
"""Pull: get toli's notifications while your agent runs.

In your agent's program, start listening in the background and pass each
notification to your agent the way a user's message reaches it:

    from pull import listen

    listener = listen(mcp_url, on_notification=my_agent.send_user_message)
    my_agent.add_context(listener.context)   # pending work, if your agent can take it
    ...
    listener.stop()                          # when your agent exits

Or run this file on its own to print notifications:

    TOLI_MCP_URL=http://127.0.0.1:7420/mcp/<token> python3 pull.py

Your agent does the work itself with toli's tools: point its MCP client at
the same MCP URL. Python 3.9+, standard library only. Protocol:
docs/custom-agents.md.
"""

import json
import os
import signal
import sys
import threading
import urllib.error
import urllib.parse
import urllib.request
import uuid

HARNESS = "my-agent"  # a name for your agent software


def listen(mcp_url, on_notification, harness=HARNESS):
    """Starts a session with toli and returns a Listener that collects its
    notifications in a background thread, calling on_notification(text) for
    each batch. Raises RuntimeError if toli refuses the session, OSError if
    it can't reach toli."""
    listener = Listener(mcp_url, on_notification, harness)
    listener.start()
    return listener


class Listener(threading.Thread):
    def __init__(self, mcp_url, on_notification, harness):
        super().__init__(daemon=True, name="toli")
        u = urllib.parse.urlsplit(mcp_url)
        prefix = "/mcp/"
        if not u.path.startswith(prefix) or len(u.path) == len(prefix):
            raise ValueError("the MCP URL should look like http://127.0.0.1:7420/mcp/<token>")
        self._token = u.path[len(prefix):].strip("/")
        self._hook = f"{u.scheme}://{u.netloc}/hook"  # the token goes in a header
        self._on_notification = on_notification
        self._session = str(uuid.uuid4())
        self._stopped = threading.Event()

        status, resp = self._call("POST", "/session-start", {"harness": harness, "session_id": self._session})
        if status != 200:
            raise RuntimeError(f"toli session-start: HTTP {status}")
        # A summary for your agent: work that arrived while it wasn't running.
        self.context = resp["context"]

    def stop(self):
        """Ends the session. Returns at once; the thread exits after its
        current wait."""
        self._stopped.set()
        try:
            self._call("POST", "/session-end", {"session_id": self._session})
        except OSError:
            pass

    def run(self):
        while not self._stopped.is_set():
            try:
                # Waits up to ~30s for notifications.
                status, resp = self._call("GET", f"/notifications?session={self._session}", timeout=60)
            except OSError as e:  # daemon down or restarting
                print(f"toli: {e}; retrying", file=sys.stderr)
                self._stopped.wait(5)
                continue
            if self._stopped.is_set():
                return
            if status == 410:  # a newer session took over; it gets them now
                print("toli: replaced by a newer session", file=sys.stderr)
                return
            if status != 200:
                print(f"toli: HTTP {status}; retrying", file=sys.stderr)
                self._stopped.wait(5)
                continue
            if resp["notifications"]:
                try:
                    self._on_notification("\n".join(resp["notifications"]))
                except Exception as e:  # keep listening
                    print(f"toli: on_notification failed: {e!r}", file=sys.stderr)

    def _call(self, method, path, body=None, timeout=10):
        """Returns (status, JSON body or None)."""
        req = urllib.request.Request(
            self._hook + path,
            method=method,
            data=None if body is None else json.dumps(body).encode(),
            headers={"Authorization": f"Bearer {self._token}", "Content-Type": "application/json"},
        )
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                raw = resp.read()
                return resp.status, json.loads(raw) if raw else None
        except urllib.error.HTTPError as e:
            return e.code, None


def main():
    def on_notification(text):
        print(f"[notification]\n{text}\n", flush=True)

    try:
        listener = listen(os.environ.get("TOLI_MCP_URL", ""), on_notification)
    except (ValueError, RuntimeError, OSError) as e:
        sys.exit(f"toli: {e}")
    print(f"[context]\n{listener.context}\n", flush=True)

    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
    try:
        while listener.is_alive():
            listener.join(1)
    except (KeyboardInterrupt, SystemExit):
        pass
    finally:
        listener.stop()


if __name__ == "__main__":
    main()
