#!/usr/bin/env python3
"""Real installed-app update through a local TLS GitHub proxy; no public release is modified."""
import hashlib
import http.cookiejar
import http.server
import json
import os
from pathlib import Path
import re
import signal
import socket
import ssl
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parent.parent


def wait(check, timeout=90):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            value = check()
            if value:
                return value
        except (OSError, urllib.error.URLError, ValueError):
            pass
        time.sleep(.2)
    raise AssertionError('update fixture timed out')


def main():
    archive = Path(sys.argv[1]).resolve()
    with tempfile.TemporaryDirectory(prefix='real-update-', dir=ROOT / 'artifacts') as temporary:
        root = Path(temporary).resolve()
        release_root = root / 'assets'
        release_root.mkdir()
        with tarfile.open(archive) as bundle:
            bundle.extractall(root / 'bundle', filter='data')
        bundle = next((root / 'bundle').iterdir())
        for version in ['v0.0.1', 'v0.0.2']:
            subprocess.run(['go', 'build', '-trimpath', '-ldflags', '-s -w -X main.buildVersion=' + version,
                            '-o', str(bundle / 'siagent'), './cmd/siagent'], cwd=ROOT, check=True)
            (bundle / 'VERSION').write_text(version + '\n')
            target = release_root / version
            target.mkdir()
            with tarfile.open(target / archive.name, 'w:xz', preset=0) as output:
                output.add(bundle, arcname=bundle.name)
            checksum = hashlib.sha256((target / archive.name).read_bytes()).hexdigest()
            (target / 'SHA256SUMS').write_text(checksum + '  ' + archive.name + '\n')

        certificate = root / 'certificate.pem'
        key = root / 'key.pem'
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                        '-subj', '/CN=github.com', '-addext', 'subjectAltName=DNS:github.com',
                        '-keyout', str(key), '-out', str(certificate)], check=True,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain(certificate, key)
        accessed = []

        class Releases(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_CONNECT(self):
                assert self.path == 'github.com:443', self.path
                self.send_response(200)
                self.end_headers()
                self.connection = tls.wrap_socket(self.connection, server_side=True)
                self.rfile = self.connection.makefile('rb')
                self.wfile = self.connection.makefile('wb')
                self.handle_one_request()
                self.close_connection = True

            def do_GET(self):
                accessed.append(self.path)
                if self.path == '/vst93/SiAgent/releases/latest/download/VERSION':
                    content = b'v0.0.2\n'
                else:
                    match = re.fullmatch(r'/vst93/SiAgent/releases/download/(v0\.0\.[12])/([^/]+)', self.path)
                    if not match or not (release_root / match[1] / match[2]).is_file():
                        self.send_error(404)
                        return
                    content = (release_root / match[1] / match[2]).read_bytes()
                self.send_response(200)
                self.send_header('Content-Length', str(len(content)))
                self.send_header('Connection', 'close')
                self.end_headers()
                self.wfile.write(content)
                self.wfile.flush()

        proxy = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Releases)
        threading.Thread(target=proxy.serve_forever, daemon=True).start()
        env = {key: value for key, value in os.environ.items()
               if not key.startswith('SIAGENT_') and key.lower() not in ('http_proxy', 'https_proxy', 'all_proxy', 'no_proxy')}
        env.update(HOME=str(root), HTTPS_PROXY=f'http://127.0.0.1:{proxy.server_port}',
                   SSL_CERT_FILE=str(certificate), CURL_CA_BUNDLE=str(certificate),
                   SIAGENT_GITHUB_MIRRORS='')
        prefix = root / 'installation'
        subprocess.run(['sh', str(ROOT / 'scripts/install.sh'), '--version', 'v0.0.1', '--prefix', str(prefix),
                        '--bin-dir', str(root / 'bin'), '--no-modify-path'], env=env, check=True,
                       stdout=subprocess.DEVNULL)
        with socket.socket() as reserved:
            reserved.bind(('127.0.0.1', 0))
            port = reserved.getsockname()[1]
        base = f'http://127.0.0.1:{port}'
        client = urllib.request.build_opener(urllib.request.ProxyHandler({}),
                                            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        csrf = ''

        def request(path, body=None, method=None):
            raw = json.dumps(body).encode() if body is not None else None
            req = urllib.request.Request(base + path, data=raw, method=method,
                                         headers={'Content-Type': 'application/json', 'X-CSRF-Token': csrf})
            with client.open(req, timeout=5) as response:
                return json.load(response)

        def processes():
            output = subprocess.check_output(['ps', '-axo', 'pid=,args='], text=True)
            return [int(line.split(None, 1)[0]) for line in output.splitlines()
                    if str(prefix / 'versions') + '/' in line and '/siagent -listen ' in line]

        process = None
        try:
            with (root / 'application.log').open('w') as log:
                process = subprocess.Popen([str(prefix / 'siagent'), '-listen', f'127.0.0.1:{port}'],
                                           cwd=root, env=env, stdout=log, stderr=log)
            assert wait(lambda: request('/health/ready'))['version'] == 'v0.0.1'
            token = re.search(r'首次启动令牌.*: (\S+)', (root / 'application.log').read_text())[1]
            csrf = request('/api/auth/setup', {'password': 'real-update-password', 'token': token})['csrf']
            model = request('/api/model', {'base_url': 'http://127.0.0.1:1/v1', 'name': 'retained model',
                                           'api_key': 'retained-private-key'}, 'PUT')
            conversation = request('/api/conversations', {})
            marker = prefix / 'data' / 'update-retained.txt'
            marker.write_text('retained complete data')
            master = (prefix / 'data' / 'master.key').read_bytes()
            assert request('/api/updates')['supported'] is True
            request('/api/updates/check', {})
            latest = wait(lambda: (value if (value := request('/api/updates'))['phase'] != 'checking' else None))
            assert latest['available'] is True and latest['latest'] == 'v0.0.2', latest
            request('/api/updates/prepare', {})
            prepared = wait(lambda: (value if (value := request('/api/updates'))['phase'] != 'downloading' else None))
            assert prepared['phase'] == 'ready', prepared
            assert request('/health/ready')['version'] == 'v0.0.1', 'download replaced live server'
            request('/api/updates/apply', {'confirm': True})
            assert process.wait(timeout=30) == 0, 'old service did not shut down cleanly'
            status = wait(lambda: (value if (value := request('/api/updates'))['phase'] in ('completed', 'failed', 'rolled_back') else None))
            assert status['phase'] == 'completed', status
            assert request('/health/ready')['version'] == 'v0.0.2'
            assert marker.read_text() == 'retained complete data'
            assert (prefix / 'data' / 'master.key').read_bytes() == master
            assert request('/api/model')['name'] == model['name']
            assert request('/api/model')['has_key'] is True
            assert any(item['id'] == conversation['id'] for item in request('/api/conversations'))
            backup = Path(status['backup'])
            assert (backup / 'update-retained.txt').read_text() == marker.read_text()
            assert (backup / 'master.key').read_bytes() == master
            assert 'v0.0.2-' in str((prefix / 'current').resolve())
            assert any(path.endswith('/v0.0.2/SHA256SUMS') for path in accessed)
            print('Application update passed: local TLS release fixture with production GitHub URLs, official-first source selection, SHA-256, bundled runtime, staged live download, graceful old-process exit, offline backup, new version readiness, persisted login/model key/conversation/data and automatic restart.')
        except BaseException:
            for path in [root / 'application.log', prefix / 'update.log']:
                if path.exists():
                    print(path.read_text(), file=sys.stderr)
            raise
        finally:
            if process is not None and process.poll() is None:
                process.terminate()
                process.wait(timeout=20)
            for pid in processes():
                try:
                    os.kill(pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
            wait(lambda: not processes(), timeout=20)
            proxy.shutdown()
            proxy.server_close()


if __name__ == '__main__':
    main()
