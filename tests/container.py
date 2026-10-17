"""Real deployment acceptance. Build image first, then run this script.

Default: Docker image + private fixture network + two disposable named volumes.
--local: run the same API/backup/restore journey with real Go/Node processes.
The local mode does not validate Docker commands, image contents or permissions.
"""
import argparse
import hashlib
import http.cookiejar
import json
import os
import pathlib
import re
import shutil
import socket
import subprocess
import tarfile
import tempfile
import time
import urllib.error
import urllib.request
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]
PASSWORD = 'container-acceptance-password'
FIXTURE = ROOT / 'tests/container-model.mjs'
MANIFEST = """const fs=require('node:fs'),p=require('node:path'),c=require('node:crypto'),out={};function walk(dir){for(const e of fs.readdirSync(dir,{withFileTypes:true})){const f=p.join(dir,e.name),key=p.relative('/data',f);if(e.isDirectory())walk(f);else if(e.isSymbolicLink())out[key]='link:'+fs.readlinkSync(f);else out[key]=c.createHash('sha256').update(fs.readFileSync(f)).digest('hex')}}walk('/data');console.log(JSON.stringify(out))"""


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


class API:
    def __init__(self, base):
        self.base = base
        self.csrf = ''
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def request(self, path, method='GET', body=None, content_type='application/json'):
        data = json.dumps(body).encode() if body is not None and not isinstance(body, bytes) else body
        req = urllib.request.Request(self.base + path, data=data, method=method, headers={'Content-Type': content_type, 'X-CSRF-Token': self.csrf})
        with self.opener.open(req, timeout=12) as response:
            return json.load(response)

    def wait(self, path, predicate):
        deadline = time.monotonic() + 40
        while time.monotonic() < deadline:
            value = self.request(path)
            if predicate(value):
                return value
            time.sleep(.1)
        raise RuntimeError(f'timed out waiting for {path}')

    def login(self):
        self.csrf = self.request('/api/auth/login', 'POST', {'password': PASSWORD})['csrf']


class Deployment:
    def __init__(self, local, image, directory):
        self.local, self.image, self.root = local, image, pathlib.Path(directory)
        self.prefix = 'siagent-check-' + uuid.uuid4().hex[:12]
        self.network = self.prefix + '-net'
        self.fixture_name = self.prefix + '-model'
        self.processes, self.containers, self.volumes, self.logs = {}, [], [], {}
        self.network_created = False

    def docker(self, *args, **kwargs):
        return subprocess.run(['docker', *args], check=True, stderr=subprocess.PIPE, timeout=60, **kwargs)

    def helper(self, role, *command, **kwargs):
        return self.docker('run', '--rm', '--pull', 'never', '--network', 'none', '-i', '--mount', f'type=volume,source={self.prefix}-{role},target=/data', '--entrypoint', command[0], self.image, *command[1:], **kwargs)

    def prepare(self):
        if self.local:
            fixture_port = port()
            self.model_base = f'http://127.0.0.1:{fixture_port}'
            self.processes['model'] = subprocess.Popen(['node', str(FIXTURE)], env={**os.environ, 'HOST': '127.0.0.1', 'PORT': str(fixture_port)}, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        else:
            self.docker('image', 'inspect', self.image, stdout=subprocess.DEVNULL)
            # Docker 29 does not publish ports for internal-only networks. The fixture
            # uses its own bridge; only the application API is published on loopback.
            self.docker('network', 'create', self.network, stdout=subprocess.DEVNULL)
            self.network_created = True
            self.containers.append(self.fixture_name)
            self.docker('run', '-d', '--pull', 'never', '--init', '--name', self.fixture_name, '--network', self.network, '-e', 'HOST=0.0.0.0', '--entrypoint', 'node', self.image, '--input-type=module', '-e', FIXTURE.read_text(), stdout=subprocess.DEVNULL)
            self.model_base = f'http://{self.fixture_name}:8000'
            for role in ('source', 'restored'):
                volume = f'{self.prefix}-{role}'
                self.docker('volume', 'create', volume, stdout=subprocess.DEVNULL)
                self.volumes.append(volume)
        deadline = time.monotonic() + 30
        while True:
            try:
                self.model_requests()
                break
            except (OSError, subprocess.CalledProcessError):
                if time.monotonic() > deadline:
                    raise RuntimeError('local model fixture did not start') from None
                time.sleep(.1)

    def model_requests(self):
        if self.local:
            with urllib.request.urlopen(self.model_base + '/requests', timeout=2) as response:
                return json.load(response)
        result = self.docker('exec', self.fixture_name, 'node', '-e', "fetch('http://127.0.0.1:8000/requests',{signal:AbortSignal.timeout(2000)}).then(async r=>{if(!r.ok)process.exit(1);console.log(await r.text())}).catch(()=>process.exit(1))", stdout=subprocess.PIPE)
        return json.loads(result.stdout)

    def start(self, role):
        if self.local:
            address = f'127.0.0.1:{port()}'
            log = self.root / f'{role}.log'
            with log.open('wb') as output:
                env = {key: value for key, value in os.environ.items() if not key.startswith('SIAGENT_')}
                self.processes[role] = subprocess.Popen([str(ROOT / 'bin/siagent'), '-data-dir', str(self.root / role), '-listen', address], cwd=ROOT, env=env, stdout=output, stderr=output)
            self.logs[role] = log
        else:
            name = self.prefix + '-' + role
            self.containers.append(name)
            self.docker('run', '-d', '--pull', 'never', '--init', '--name', name, '--network', self.network, '-e', 'NO_PROXY=*', '-e', 'no_proxy=*', '-p', '127.0.0.1::8080', '--mount', f'type=volume,source={name},target=/data', self.image, stdout=subprocess.DEVNULL)
            address = self.docker('port', name, '8080/tcp', stdout=subprocess.PIPE).stdout.decode().strip()
        api = API('http://' + address)
        deadline = time.monotonic() + 40
        while True:
            try:
                api.request('/health/ready')
                return api
            except (OSError, ValueError):
                if time.monotonic() > deadline:
                    raise RuntimeError(f'{role} did not become ready') from None
                time.sleep(.2)

    def setup_token(self, role):
        if self.local:
            logs = self.logs[role].read_text()
        else:
            result = self.docker('logs', self.prefix + '-' + role, stdout=subprocess.PIPE)
            logs = (result.stdout + result.stderr).decode()
        match = re.search(r'首次启动令牌.*: (\S+)', logs)
        if not match:
            raise RuntimeError('initial setup token missing')
        return match[1]

    def stop(self, role):
        if self.local:
            process = self.processes[role]
            process.terminate()
            if process.wait(timeout=20) != 0:
                raise RuntimeError(f'{role} did not stop cleanly')
            del self.processes[role]
        else:
            name = self.prefix + '-' + role
            self.docker('stop', '--time', '20', name, stdout=subprocess.DEVNULL)
            state = json.loads(self.docker('inspect', '--format', '{{json .State}}', name, stdout=subprocess.PIPE).stdout)
            if state['ExitCode'] != 0 or state['OOMKilled']:
                raise RuntimeError(f'{role} did not stop cleanly')
            self.docker('rm', name, stdout=subprocess.DEVNULL)
            self.containers.remove(name)

    def manifest(self, role):
        if self.local:
            root = self.root / role
            return {str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest() for p in root.rglob('*') if p.is_file()}
        return json.loads(self.helper(role, 'node', '-e', MANIFEST, stdout=subprocess.PIPE).stdout)

    def backup(self, archive):
        if self.local:
            with tarfile.open(archive, 'w:gz') as output:
                output.add(self.root / 'source', arcname='.')
        else:
            with archive.open('wb') as output:
                self.helper('source', 'tar', '-C', '/data', '-czf', '-', '.', stdout=output)

    def restore(self, archive):
        if self.local:
            target = self.root / 'restored'
            target.mkdir(mode=0o700)
            with tarfile.open(archive) as source:
                source.extractall(target, filter='data')
        else:
            with archive.open('rb') as source:
                self.helper('restored', 'sh', '-eu', '-c', 'test -z "$(ls -A /data)"; tar -C /data -xzf -', stdin=source, stdout=subprocess.DEVNULL)

    def cleanup(self):
        for process in self.processes.values():
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=20)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
        if not self.local:
            failures = []
            for name in self.containers:
                if subprocess.run(['docker', 'rm', '-fv', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode:
                    failures.append(name)
            for volume in self.volumes:
                if subprocess.run(['docker', 'volume', 'rm', volume], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode:
                    failures.append(volume)
            if self.network_created and subprocess.run(['docker', 'network', 'rm', self.network], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode:
                failures.append(self.network)
            if failures:
                raise RuntimeError('could not remove test resources: ' + ', '.join(failures))


def journey(deployment):
    deployment.prepare()
    api = deployment.start('source')
    if not deployment.local:
        name = deployment.prefix + '-source'
        assert deployment.docker('exec', name, 'id', '-u', stdout=subprocess.PIPE).stdout.strip() == b'1000'
        deployment.docker('exec', name, 'node', '-e', "const fs=require('node:fs');if((fs.statSync('/data').mode&0o777)!==0o700)process.exit(1)", stdout=subprocess.DEVNULL)
        deployment.docker('exec', name, 'pdftotext', '-v', stdout=subprocess.DEVNULL)
        deployment.docker('exec', name, 'pdfinfo', '-v', stdout=subprocess.DEVNULL)
        deadline = time.monotonic() + 45
        while True:
            health = deployment.docker('inspect', '--format', '{{.State.Health.Status}}', name, stdout=subprocess.PIPE).stdout.strip()
            if health == b'healthy':
                break
            if health == b'unhealthy' or time.monotonic() > deadline:
                raise RuntimeError('image HEALTHCHECK did not become healthy')
            time.sleep(.5)
    assert not api.request('/api/auth/status')['initialized']
    api.csrf = api.request('/api/auth/setup', 'POST', {'password': PASSWORD, 'token': deployment.setup_token('source')})['csrf']
    updates = api.request('/api/updates')
    assert updates['supported'] is False
    if not deployment.local:
        assert updates['mode'] == 'docker', updates
        assert 'docker compose' in updates['message'], updates
        try:
            api.request('/api/updates/prepare', 'POST')
        except urllib.error.HTTPError as error:
            assert error.code == 409
        else:
            raise AssertionError('Docker accepted an in-container package update')
    api.request('/api/model', 'PUT', {'base_url': deployment.model_base + '/v1', 'name': 'container-fixture', 'api_key': 'container-fixture-key'})
    api.request('/api/model/test', 'POST')
    conversation = api.request('/api/conversations', 'POST')
    path = '/api/conversations/' + conversation['id']
    api.request(path + '/runs', 'POST', {'input': 'first container question'})
    history = api.wait(path, lambda value: value['runs'][-1]['status'] == 'completed')
    assert history['messages'][-1]['content'] == 'echo: first container question'
    knowledge = api.request('/api/knowledge/bases', 'POST', {'name': 'Container restore'})
    content = '# 容器恢复\n\n发票开具流程：提交订单编号，七个工作日内完成。\n'
    boundary = 'container-restore-boundary'
    upload = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="restore.md"\r\nContent-Type: text/markdown\r\n\r\n{content}\r\n--{boundary}--\r\n').encode()
    api.request('/api/knowledge/bases/' + knowledge['id'] + '/documents', 'POST', upload, 'multipart/form-data; boundary=' + boundary)
    api.wait('/api/knowledge/bases/' + knowledge['id'] + '/documents', lambda value: value[0]['state'] == 'ready')
    api.request('/api/agent/knowledge', 'PUT', {'knowledge_ids': [knowledge['id']], 'confirm': True})
    search_body = {'query': '发票开具流程', 'knowledge_ids': [knowledge['id']]}
    source_id = api.request('/api/knowledge/search', 'POST', search_body)['hits'][0]['source_id']
    before = len(deployment.model_requests())
    deployment.stop('source')
    # Recreate a container on the original volume, not merely its writable layer.
    api = deployment.start('source')
    api.login()
    assert api.request(path) == history
    assert len(deployment.model_requests()) == before
    deployment.stop('source')
    expected = deployment.manifest('source')
    assert 'master.key' in expected and any(key.startswith('runtime/conversations/') for key in expected)
    assert any(key.startswith('knowledge/') and key.endswith('/original') for key in expected)
    archive = deployment.root / 'full-backup.tar.gz'
    deployment.backup(archive)
    archive.chmod(0o600)
    deployment.restore(archive)
    assert deployment.manifest('restored') == expected, 'restored volume differs from offline source'
    api = deployment.start('restored')
    assert api.request('/api/auth/status')['initialized']
    try:
        api.request(path)
        raise AssertionError('restored history accessible without authentication')
    except urllib.error.HTTPError as error:
        assert error.code == 401
    api.login()
    assert api.request(path) == history
    assert len(deployment.model_requests()) == before, 'restore replayed model execution'
    assert api.request('/api/model')['has_key']
    api.request('/api/model/test', 'POST')
    assert api.request('/api/agent/knowledge')['knowledge_ids'] == [knowledge['id']]
    assert api.request('/api/knowledge/search', 'POST', search_body)['hits'][0]['source_id'] == source_id
    assert api.request('/api/knowledge/sources/' + source_id)['document_text'] == content
    api.request(path + '/runs', 'POST', {'input': 'restored container continuation'})
    final = api.wait(path, lambda value: value['runs'][-1]['status'] == 'completed')
    assert final['messages'][-1]['content'] == 'echo: restored container continuation'
    last = deployment.model_requests()[-1]
    assert any(message.get('content') == 'first container question' or (isinstance(message.get('content'), list) and message['content'][-1].get('text') == 'first container question') for message in last['messages'])
    assert api.request('/api/auth/logout', 'POST')
    deployment.stop('restored')
    return ('Local journey passed (Docker NOT validated)' if deployment.local else 'Docker acceptance passed') + ': setup, real Runtime/model protocol, authentication, instance recreation, offline full backup, exact file hashes in a new data root, model key, knowledge originals/index/binding and conversation context.'



def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', default='siagent:check')
    parser.add_argument('--local', action='store_true', help='validate the shared journey using local Go/Node; does not test Docker')
    args = parser.parse_args()
    if not args.local and not shutil.which('docker'):
        parser.exit(2, 'Docker is unavailable. Install/start Docker to run container acceptance; --local only validates the shared journey.\n')
    os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix='siagent-deployment-') as directory:
        deployment = Deployment(args.local, args.image, directory)
        try:
            message = journey(deployment)
        finally:
            deployment.cleanup()
        print(message + ' Test resources removed.')


if __name__ == '__main__':
    main()
