"""Serve one signed application release through an SSH loopback tunnel.

No production backend, device registration, or signing key is served. Bind only
to loopback; use an SSH remote forward for access from the intended Pi. The Pi
still verifies the release signature/hash and its normal update safety gate.
"""
import argparse
import http.server
import json
import pathlib
import signal

p = argparse.ArgumentParser()
p.add_argument('--release', required=True, type=pathlib.Path)
p.add_argument('--artifact', required=True, type=pathlib.Path)
p.add_argument('--device', required=True)
p.add_argument('--port', required=True, type=int)
p.add_argument('--seconds', required=True, type=int)
a = p.parse_args()
if a.seconds <= 0 or '/' in a.device:
    p.error('positive lifetime and single device ID required')
release = json.loads(a.release.read_text())
release['artifactUrl'] = '/artifact'
plan = json.dumps({'release': release, 'campaignId': 'local-retention-recovery'}).encode()

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == f'/v1/devices/{a.device}/updates/plan':
            body = plan
        elif self.path == '/artifact':
            body = a.artifact.read_bytes()
        else:
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        if self.path != f'/v1/devices/{a.device}/updates/status':
            self.send_error(404)
            return
        length = int(self.headers.get('Content-Length', '0'))
        if length > 65536:
            self.send_error(413)
            return
        status = json.loads(self.rfile.read(length))
        print(json.dumps(status), flush=True)
        self.send_response(204)
        self.end_headers()

    def log_message(self, *_):
        pass  # Never log Authorization headers or other request details.

signal.alarm(a.seconds)
print(f'Loopback OTA server on port {a.port}, lifetime {a.seconds}s', flush=True)
http.server.HTTPServer(('127.0.0.1', a.port), Handler).serve_forever()
