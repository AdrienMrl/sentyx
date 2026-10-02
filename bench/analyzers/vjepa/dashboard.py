#!/usr/bin/env python3
"""Serve the V-JEPA training dashboard on the Mac and poll the GPU PC over SSH."""

import argparse
import http.server
import json
import os
import subprocess
import time

HERE = os.path.dirname(os.path.abspath(__file__))


class Handler(http.server.SimpleHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/api/status":
            try:
                out = subprocess.check_output(
                    ["ssh", self.server.target, "python", self.server.snapshot, self.server.features],
                    stderr=subprocess.STDOUT, timeout=8)
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Cache-Control", "no-store")
                self.end_headers()
                self.wfile.write(out)
            except Exception as exc:
                self.send_response(502)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps({"error": str(exc), "time_unix": time.time()}).encode())
            return
        if self.path in ("/", "/index.html"):
            self.path = "/dashboard.html"
        return super().do_GET()

    def log_message(self, fmt, *args):
        if args and str(args[0]).startswith("GET /api/status"):
            return
        super().log_message(fmt, *args)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--target", default="adri@192.168.1.58")
    p.add_argument("--remote-repo", default=r"C:\Users\adrie\code\tesyx")
    p.add_argument("--port", type=int, default=8765)
    a = p.parse_args()
    os.chdir(HERE)
    server = http.server.ThreadingHTTPServer(("127.0.0.1", a.port), Handler)
    server.target = a.target
    server.snapshot = a.remote_repo + r"\bench\analyzers\vjepa\dashboard_snapshot.py"
    server.features = a.remote_repo + r"\bench\features"
    print(f"V-JEPA dashboard: http://127.0.0.1:{a.port}")
    server.serve_forever()


if __name__ == "__main__":
    main()
