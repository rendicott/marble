#!/usr/bin/env python3
"""Serve the Marble repo (ADR docs + review HTML) on a fixed port.

  python3 adr/serve.py          # default :8791
  python3 adr/serve.py 8792

Maps /adr and /adr/ → /adr/index.html. Regenerated docs live next to the markdown:
  python3 adr/render_docs.py
"""

from __future__ import annotations

import sys
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
DEFAULT_PORT = 8791


class Handler(SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=str(REPO_ROOT), **kwargs)

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        qs = "?" + self.path.split("?", 1)[1] if "?" in self.path else ""
        # Redirect the bare /adr → /adr/ so the browser's base URL ends with a
        # trailing slash. Otherwise relative links (index.html → 0028-doc.html)
        # resolve against "/" instead of "/adr/" and 404.
        if path == "/adr":
            self.send_response(301)
            self.send_header("Location", "/adr/" + qs)
            self.end_headers()
            return
        if path == "/adr/":
            self.path = "/adr/index.html" + qs
        return super().do_GET()

    def log_message(self, fmt: str, *args) -> None:
        sys.stderr.write("%s - %s\n" % (self.address_string(), fmt % args))


def main() -> int:
    port = DEFAULT_PORT
    if len(sys.argv) > 1:
        port = int(sys.argv[1])
    httpd = ThreadingHTTPServer(("0.0.0.0", port), Handler)
    print(f"Marble ADR server: http://127.0.0.1:{port}/adr/", flush=True)
    print(f"  root={REPO_ROOT}", flush=True)
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\nbye", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
