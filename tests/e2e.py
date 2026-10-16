"""Full Go + Node + SQLite acceptance against a local model protocol fixture.
Run after make build: python3 tests/e2e.py. No real model credentials required.
"""
import base64
import hashlib
import http.cookiejar
import http.server
import json
import os
import pathlib
import signal
import socket
import subprocess
import tarfile
import tempfile
import threading
import time
import urllib.error
import urllib.request


class Model(http.server.BaseHTTPRequestHandler):
    requests = []
    gates = {name: threading.Event() for name in ("gate", "gate2", "gate3", "hang")}

    def log_message(self, *args):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        self.requests.append(body)
        if self.headers.get('Authorization') != 'Bearer e2e-key':
            self.send_response(401)
            self.end_headers()
            self.wfile.write(b'{"error":"rejected"}')
            return
        self.send_response(200)
        if not body.get('stream'):
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(b'{"choices":[{"message":{"content":"OK"}}]}')
            return
        self.send_header('Content-Type', 'text/event-stream')
        self.end_headers()
        question = body['messages'][-1]['content']
        if question == 'e2e-knowledge':
            for delta, reason in [({'role': 'assistant', 'content': ''}, None), ({'tool_calls': [{'index': 0, 'id': 'knowledge-call-' + str(len(self.requests)), 'type': 'function', 'function': {'name': 'knowledge_search', 'arguments': json.dumps({'query': '发票开具流程'})}}]}, None), ({}, 'tool_calls')]:
                event = {'id': 'e2e', 'object': 'chat.completion.chunk', 'created': 1, 'model': body['model'], 'choices': [{'index': 0, 'delta': delta, 'finish_reason': reason}]}
                self.wfile.write(('data: ' + json.dumps(event) + '\n\n').encode())
            self.wfile.write(b'data: [DONE]\n\n')
            return
        if question.startswith('e2e-mcp:'):
            name = question.split(':', 1)[1]
            alias = 'mcp_' + self.mcp_id + '_' + hashlib.sha256(name.encode()).hexdigest()[:16]
            for delta, reason in [({'role': 'assistant', 'content': ''}, None), ({'tool_calls': [{'index': 0, 'id': 'e2e-tool-' + str(len(self.requests)), 'type': 'function', 'function': {'name': alias, 'arguments': '{}'}}]}, None), ({}, 'tool_calls')]:
                event = {'id': 'e2e', 'object': 'chat.completion.chunk', 'created': 1, 'model': body['model'], 'choices': [{'index': 0, 'delta': delta, 'finish_reason': reason}]}
                self.wfile.write(('data: ' + json.dumps(event) + '\n\n').encode())
            self.wfile.write(b'data: [DONE]\n\n')
            return
        if question in self.gates:
            partial = {'id': 'e2e', 'object': 'chat.completion.chunk', 'created': 1, 'model': body['model'], 'choices': [{'index': 0, 'delta': {'role': 'assistant', 'content': 'partial '}, 'finish_reason': None}]}
            self.wfile.write(('data: ' + json.dumps(partial) + '\n\n').encode())
            self.wfile.flush()
            self.gates[question].wait(15)
        answer = 'echo: ' + question
        for delta, reason in [({'role': 'assistant', 'content': ''}, None), ({'content': answer}, None), ({}, 'stop')]:
            event = {'id': 'e2e', 'object': 'chat.completion.chunk', 'created': 1, 'model': body['model'], 'choices': [{'index': 0, 'delta': delta, 'finish_reason': reason}]}
            try:
                self.wfile.write(('data: ' + json.dumps(event) + '\n\n').encode())
                self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError):
                return
        try:
            self.wfile.write(b'data: [DONE]\n\n')
        except (BrokenPipeError, ConnectionResetError):
            pass


def free_port():
    with socket.socket() as connection:
        connection.bind(('127.0.0.1', 0))
        return connection.getsockname()[1]


def main():
    model = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Model)
    threading.Thread(target=model.serve_forever, daemon=True).start()
    port = free_port()
    base = f'http://127.0.0.1:{port}'
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    csrf = ''

    def request(path, method='GET', body=None):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(base + path, data=data, method=method, headers={'Content-Type': 'application/json', 'X-CSRF-Token': csrf})
        try:
            with opener.open(req, timeout=10) as response:
                return response.status, json.load(response)
        except urllib.error.HTTPError as error:
            return error.code, json.load(error)

    with tempfile.TemporaryDirectory(prefix='siagent-e2e-') as directory:
        def start():
            env = {key: value for key, value in os.environ.items() if not key.startswith('SIAGENT_')}
            process = subprocess.Popen(['./bin/siagent', '-data-dir', directory, '-listen', f'127.0.0.1:{port}'], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            for _ in range(100):
                try:
                    with urllib.request.urlopen(base + '/health/ready', timeout=1) as response:
                        if response.status == 200:
                            token = process.stderr.readline().decode().strip().rsplit(': ', 1)[-1]
                            return process, token
                except Exception:
                    if process.poll() is not None:
                        raise RuntimeError('server failed to start')
                    time.sleep(.05)
            process.terminate()
            process.communicate(timeout=8)
            raise RuntimeError('readiness timed out')

        process, setup_token = start()
        try:
            assert request('/api/conversations')[0] == 401
            status, auth = request('/api/auth/status')
            assert status == 200 and not auth['initialized']
            status, result = request('/api/auth/setup', 'POST', {'password': 'e2e-long-test-password', 'token': setup_token})
            assert status == 201, result
            csrf = result['csrf']
            for key, expected in [('wrong-key', 502), ('e2e-key', 200)]:
                assert request('/api/model', 'PUT', {'base_url': f'http://127.0.0.1:{model.server_port}/v1', 'name': 'e2e-model', 'api_key': key})[0] == 200
                status, result = request('/api/model/test', 'POST')
                assert status == expected, result
            status, config = request('/api/model')
            assert status == 200 and config['has_key'] and 'api_key' not in config
            assert request('/api/agent')[1]['id'] == 'default'
            status, conversation = request('/api/conversations', 'POST')
            assert status == 201
            path = '/api/conversations/' + conversation['id']
            assert request(path + '/runs', 'POST', {'input': 'first question'})[0] == 202
            for _ in range(100):
                status, snapshot = request(path)
                if snapshot['runs'][0]['status'] == 'completed':
                    break
                time.sleep(.05)
            else:
                raise AssertionError(snapshot)
            assert snapshot['messages'][1]['content'] == 'echo: first question', snapshot
            with opener.open(base + path + '/events', timeout=5) as stream:
                assert stream.headers['Content-Type'] == 'text/event-stream'
                assert stream.readline().strip() == b'event: snapshot'
            process.send_signal(signal.SIGTERM)
            process.communicate(timeout=8)
            assert process.returncode == 0
            process, setup_token = start()
            assert request('/api/auth/status')[1]['authenticated']
            status, recovered = request(path)
            assert recovered == snapshot
            assert request('/api/model/test', 'POST')[0] == 200
            assert request(path + '/runs', 'POST', {'input': 'second question'})[0] == 202
            for _ in range(100):
                _, final = request(path)
                if len(final['runs']) == 2 and final['runs'][-1]['status'] == 'completed':
                    break
                time.sleep(.05)
            else:
                raise AssertionError(final)
            assert any(message.get('content') == 'first question' for message in Model.requests[-1]['messages'])
            assert final['messages'][-1]['content'] == 'echo: second question'
            def wait_snapshot(route, predicate):
                for _ in range(160):
                    code, state = request(route)
                    if code == 200 and predicate(state):
                        return state
                    time.sleep(.05)
                raise AssertionError(state)

            # Same-conversation FIFO and an independent concurrent conversation.
            queue = request('/api/conversations', 'POST')[1]
            queue_path = '/api/conversations/' + queue['id']
            run1 = request(queue_path + '/runs', 'POST', {'input': 'gate'})[1]
            run2 = request(queue_path + '/runs', 'POST', {'input': 'queued two'})[1]
            run3 = request(queue_path + '/runs', 'POST', {'input': 'queued three'})[1]
            assert run2['status'] == run3['status'] == 'queued'
            other = request('/api/conversations', 'POST')[1]
            other_path = '/api/conversations/' + other['id']
            assert request(other_path + '/runs', 'POST', {'input': 'independent'})[0] == 202
            wait_snapshot(other_path, lambda state: state['runs'][0]['status'] == 'completed')
            assert not any(body['messages'][-1]['content'] == 'queued two' for body in Model.requests)
            Model.gates['gate'].set()
            queued = wait_snapshot(queue_path, lambda state: len(state['runs']) == 3 and all(run['status'] == 'completed' for run in state['runs']))
            assert [message['content'] for message in queued['messages'] if message['role'] == 'user'] == ['gate', 'queued two', 'queued three']
            assert any(message.get('content') == 'queued two' for message in Model.requests[-1]['messages'])

            # Queue overflow is explicit; cancellation never sends a queued input.
            limits = request('/api/limits')[1]
            assert request('/api/limits', 'PUT', {**limits, 'concurrent': 1, 'conversation_queue': 1, 'global_queue': 1})[0] == 200
            assert request(queue_path + '/runs', 'POST', {'input': 'gate2'})[0] == 202
            cancelled = request(queue_path + '/runs', 'POST', {'input': 'never execute'})[1]
            assert request(queue_path + '/runs', 'POST', {'input': 'overflow'})[0] == 429
            assert request('/api/runs/' + cancelled['id'] + '/cancel', 'POST')[0] == 200
            Model.gates['gate2'].set()
            wait_snapshot(queue_path, lambda state: state['runs'][-2]['status'] == 'completed')
            assert not any(body['messages'][-1]['content'] in ('never execute', 'overflow') for body in Model.requests)

            # A hung provider is cancelled at the saved run deadline.
            assert request('/api/limits', 'PUT', {**limits, 'run_timeout_seconds': 1})[0] == 200
            timed = request(other_path + '/runs', 'POST', {'input': 'hang'})[1]
            stopped = wait_snapshot(other_path, lambda state: state['runs'][-1]['id'] == timed['id'] and state['runs'][-1]['status'] == 'failed')
            assert '时间' in stopped['runs'][-1]['error']
            assert 'partial' in stopped['messages'][-1]['content']
            assert request('/api/limits', 'PUT', limits)[0] == 200

            # Kill the real private Runtime, then verify no replay or silent queue resume.
            crashed = request(queue_path + '/runs', 'POST', {'input': 'gate3'})[1]
            abandoned = request(queue_path + '/runs', 'POST', {'input': 'abandoned queued'})[1]
            wait_snapshot(queue_path, lambda state: state['messages'][-3]['content'].startswith('partial'))
            children = subprocess.check_output(['ps', '-A', '-o', 'pid=,ppid='], text=True)
            child_pids = [int(row.split()[0]) for row in children.splitlines() if len(row.split()) == 2 and int(row.split()[1]) == process.pid]
            assert len(child_pids) == 1, child_pids
            os.kill(child_pids[0], signal.SIGKILL)
            interrupted = wait_snapshot(queue_path, lambda state: all(run['status'] == 'interrupted' for run in state['runs'] if run['id'] in (crashed['id'], abandoned['id'])))
            assert interrupted['messages'][-3]['content'].startswith('partial')
            for _ in range(100):
                try:
                    with urllib.request.urlopen(base + '/health/ready', timeout=1) as response:
                        if response.status == 200:
                            break
                except Exception:
                    pass
                time.sleep(.05)
            assert request(queue_path + '/runs', 'POST', {'input': 'explicit restart'})[0] == 202
            wait_snapshot(queue_path, lambda state: state['runs'][-1]['status'] == 'completed')
            assert sum(body['messages'][-1]['content'] == 'gate3' for body in Model.requests) == 1
            assert not any(body['messages'][-1]['content'] == 'abandoned queued' for body in Model.requests)
            detail = request('/api/runs/' + crashed['id'])[1]
            assert any(event['kind'] == 'interrupted' for event in detail['events'])

            # Imported Skills and real SDK MCP processes through the public Go API.
            skill_text = '---\nname: e2e-skill\ndescription: Uploaded integration instructions.\ncompatibility: No dependencies.\n---\nUse only explicitly selected tools.\n'
            skill_boundary = 'siagent-skill-upload'
            skill_body = (f'--{skill_boundary}\r\nContent-Disposition: form-data; name="trusted"\r\n\r\ntrue\r\n'
                f'--{skill_boundary}\r\nContent-Disposition: form-data; name="file"; filename="SKILL.md"\r\nContent-Type: text/markdown\r\n\r\n'
                + skill_text + f'\r\n--{skill_boundary}--\r\n').encode()
            skill_request = urllib.request.Request(base + '/api/skills', data=skill_body, method='POST', headers={'Content-Type': 'multipart/form-data; boundary=' + skill_boundary, 'X-CSRF-Token': csrf})
            with opener.open(skill_request, timeout=10) as response:
                code, skill = response.status, json.load(response)
            assert code == 201, skill
            executable = subprocess.check_output(['node', '-p', 'process.execPath'], text=True).strip()
            code, mcp = request('/api/mcps', 'POST', {'name': 'E2E MCP', 'transport': 'stdio', 'command': executable, 'args': [str(pathlib.Path('runtime/test/mcp-fixture.mjs').resolve())], 'env': {'TOKEN': 'e2e-mcp-private'}, 'trusted': True})
            assert code == 201 and 'e2e-mcp-private' not in json.dumps(mcp), mcp
            Model.mcp_id = mcp['id']
            code, tested = request('/api/mcps/' + mcp['id'] + '/test', 'POST')
            assert code == 200 and len(tested['tools']) == 5, tested
            assert 'e2e-mcp-private' not in json.dumps(tested)
            binding = {'skills': [skill['id']], 'mcps': [{'id': mcp['id'], 'tools': ['inspect', 'fail', 'hang', 'disconnect']}], 'confirm': True}
            assert request('/api/agent/capabilities', 'PUT', binding)[0] == 200
            tools_chat = request('/api/conversations', 'POST')[1]
            tools_path = '/api/conversations/' + tools_chat['id']
            inspected = request(tools_path + '/runs', 'POST', {'input': 'e2e-mcp:inspect'})[1]
            wait_snapshot(tools_path, lambda state: state['runs'][-1]['status'] == 'completed')
            inspected_detail = request('/api/runs/' + inspected['id'])[1]
            call = inspected_detail['tools'][0]
            assert call['status'] == 'completed' and call['display_name'] == 'E2E MCP / inspect', inspected_detail
            call_data = request('/api/runs/' + inspected['id'] + '/tools/' + call['id'])[1]
            assert '[redacted]' in call_data['result'] and 'e2e-mcp-private' not in json.dumps(call_data), call_data
            assert 'e2e-mcp-private' not in json.dumps(Model.requests)
            failed_tool = request(tools_path + '/runs', 'POST', {'input': 'e2e-mcp:fail'})[1]
            wait_snapshot(tools_path, lambda state: state['runs'][-1]['status'] == 'completed')
            assert request('/api/runs/' + failed_tool['id'])[1]['tools'][0]['status'] == 'failed'

            # A real MCP deadline preserves unknown outcome rather than replaying.
            assert request('/api/limits', 'PUT', {**limits, 'tool_timeout_seconds': 1})[0] == 200
            timeout_tool = request(tools_path + '/runs', 'POST', {'input': 'e2e-mcp:hang'})[1]
            wait_snapshot(tools_path, lambda state: state['runs'][-1]['status'] in ('failed', 'interrupted'))
            assert request('/api/runs/' + timeout_tool['id'])[1]['tools'][0]['status'] == 'interrupted'
            wait_snapshot('/api/runs/' + timeout_tool['id'], lambda state: state['released'])
            assert request('/api/limits', 'PUT', limits)[0] == 200

            # Killing Runtime during a local MCP effect also kills the child server.
            pid_file = pathlib.Path(directory) / 'workspaces/default/active-pid.txt'
            old_pid = int(pid_file.read_text())
            crashed_tool = request(tools_path + '/runs', 'POST', {'input': 'e2e-mcp:hang'})[1]
            for _ in range(100):
                mcp_pid = int(pid_file.read_text())
                if mcp_pid != old_pid:
                    break
                time.sleep(.02)
            else:
                raise AssertionError('MCP tool did not start')
            children = subprocess.check_output(['ps', '-A', '-o', 'pid=,ppid='], text=True)
            runtime_pid = next(int(row.split()[0]) for row in children.splitlines() if len(row.split()) == 2 and int(row.split()[1]) == process.pid)
            os.kill(runtime_pid, signal.SIGKILL)
            wait_snapshot(tools_path, lambda state: state['runs'][-1]['status'] == 'interrupted')
            assert request('/api/runs/' + crashed_tool['id'])[1]['tools'][0]['status'] == 'interrupted'
            for _ in range(100):
                try:
                    os.kill(mcp_pid, 0)
                except ProcessLookupError:
                    break
                time.sleep(.02)
            else:
                raise AssertionError('MCP process survived its Runtime')
            effects = pathlib.Path(directory) / 'workspaces/default/effects.txt'
            assert effects.read_text() == 'hang\nhang\n'
            assert sum(body['messages'][-1]['content'] == 'e2e-mcp:hang' for body in Model.requests) == 2

            # Native knowledge is independent of MCP, survives a full service restart,
            # and resolves real versioned references through the Go authority.
            wait_snapshot('/api/diagnostics', lambda state: state['runtime_ready'])
            code, knowledge = request('/api/knowledge/bases', 'POST', {'name': 'Finance', 'description': 'Native local retrieval'})
            assert code == 201, knowledge
            boundary = 'siagent-knowledge-boundary'
            content = '# 发票开具流程\n\n发票开具流程：提交订单编号，七个工作日内完成。\n'
            multipart = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="manual.md"\r\nContent-Type: text/markdown\r\n\r\n' + content + f'\r\n--{boundary}--\r\n').encode()
            upload_request = urllib.request.Request(base + '/api/knowledge/bases/' + knowledge['id'] + '/documents', data=multipart, method='POST', headers={'Content-Type': 'multipart/form-data; boundary=' + boundary, 'X-CSRF-Token': csrf})
            with opener.open(upload_request, timeout=10) as uploaded:
                assert uploaded.status == 202
                document = json.load(uploaded)
            wait_snapshot('/api/knowledge/bases/' + knowledge['id'] + '/documents', lambda state: state[0]['state'] == 'ready')
            assert request('/api/agent/knowledge', 'PUT', {'knowledge_ids': [knowledge['id']], 'confirm': True})[0] == 200
            native_chat = request('/api/conversations', 'POST')[1]
            native_path = '/api/conversations/' + native_chat['id']
            native_run = request(native_path + '/runs', 'POST', {'input': 'e2e-knowledge'})[1]
            native = wait_snapshot(native_path, lambda state: state['runs'][-1]['status'] == 'completed')
            assert native['sources'] and '七个工作日' in native['messages'][-1]['content'], native
            source = native['sources'][0]
            assert source['knowledge_id'] == knowledge['id'] and source['run_id'] == native_run['id'] and source['text'] == ''
            source_api = '/api/knowledge/sources/' + source['source_id']
            assert '七个工作日' in request(source_api)[1]['document_text']
            process.send_signal(signal.SIGTERM)
            process.communicate(timeout=8)
            assert process.returncode == 0
            process, setup_token = start()
            assert request(native_path)[1] == native
            assert '七个工作日' in request(source_api)[1]['document_text']
            assert request('/api/knowledge/documents/' + document['id'], 'DELETE')[0] == 200
            assert request(source_api)[1]['deleted']
            code, empty = request('/api/knowledge/search', 'POST', {'query': '发票开具流程', 'knowledge_ids': [knowledge['id']]})
            assert code == 200 and not empty['hits'], empty
            assert request('/api/knowledge/bases/' + knowledge['id'], 'DELETE')[0] == 200
            assert request('/api/agent/knowledge')[1]['knowledge_ids'] == []

            code, connection = request('/api/models', 'POST', {'base_url': f'http://127.0.0.1:{model.server_port}/v1', 'name': 'separate-model', 'api_key': 'e2e-key'})
            assert code == 201 and 'api_key' not in connection, connection
            code, other = request('/api/agents', 'POST', {'name': 'Separate role', 'instructions': 'Use only your own conversation.', 'enabled': True, 'model_id': connection['id']})
            assert code == 201, other
            code, other_chat = request('/api/conversations', 'POST', {'agent_id': other['id']})
            assert code == 201 and other_chat['agent_id'] == other['id'], other_chat
            other_path = '/api/conversations/' + other_chat['id']
            assert request(other_path + '/runs', 'POST', {'input': 'isolated-role-e2e'})[0] == 202
            other_history = wait_snapshot(other_path, lambda state: state['runs'][-1]['status'] == 'completed')
            assert other_history['messages'][-1]['content'] == 'echo: isolated-role-e2e', other_history
            assert Model.requests[-1]['model'] == 'separate-model' and not Model.requests[-1].get('tools')
            assert 'e2e-knowledge' not in json.dumps(Model.requests[-1]['messages'])
            assert request('/api/agents/' + other['id'], 'DELETE')[0] == 409
            assert request('/api/models/' + str(connection['id']), 'DELETE')[0] == 409
            assert request('/api/agents/' + other['id'], 'PUT', {'name': other['name'], 'instructions': other['instructions'], 'enabled': False, 'model_id': connection['id']})[0] == 200
            assert request('/api/agents/' + other['id'], 'DELETE')[0] == 200
            assert request('/api/models/' + str(connection['id']), 'DELETE')[0] == 200
            process.send_signal(signal.SIGTERM)
            process.communicate(timeout=8)
            assert process.returncode == 0
            process, setup_token = start()
            assert request(other_path)[1] == other_history
            assert request('/api/agents/' + other['id'])[1]['archived']
            assert any(item['id'] == connection['id'] and item['archived'] for item in request('/api/models')[1])
            assert request(other_path + '/runs', 'POST', {'input': 'must not run'})[0] == 409

            # A complete offline backup restores into a different directory with
            # the same binary, including private Runtime context and local files.
            code, restore_base = request('/api/knowledge/bases', 'POST', {'name': 'Restore proof'})
            assert code == 201, restore_base
            upload_request = urllib.request.Request(base + '/api/knowledge/bases/' + restore_base['id'] + '/documents', data=multipart, method='POST', headers={'Content-Type': 'multipart/form-data; boundary=' + boundary, 'X-CSRF-Token': csrf})
            with opener.open(upload_request, timeout=10) as uploaded:
                restore_document = json.load(uploaded)
            wait_snapshot('/api/knowledge/bases/' + restore_base['id'] + '/documents', lambda state: state[0]['state'] == 'ready')
            restore_binding = {'knowledge_ids': [restore_base['id']], 'confirm': True}
            assert request('/api/agent/knowledge', 'PUT', restore_binding)[0] == 200
            callback_key = base64.b64encode(bytes([7]) * 32).decode().rstrip('=')
            code, channel = request('/api/wecom/channels', 'POST', {'corp_id': 'wx-restore', 'app_id': 7, 'agent_id': 'default', 'enabled': False, 'secret': 'restore-channel-secret', 'token': 'restore-token', 'aes_key': callback_key})
            assert code == 201 and 'restore-channel-secret' not in json.dumps(channel), channel
            preserved = {route: request(route)[1] for route in [path, other_path, '/api/models', '/api/agents', '/api/agent/capabilities', '/api/agent/knowledge', '/api/wecom/channels']}
            before_restore_calls = len(Model.requests)
            process.send_signal(signal.SIGTERM)
            process.communicate(timeout=8)
            assert process.returncode == 0
            with tempfile.TemporaryDirectory(prefix='siagent-restored-') as restore_root:
                archive_path = pathlib.Path(restore_root) / 'full-backup.tar.gz'
                source_directory = pathlib.Path(directory)
                manifest = {str(file.relative_to(source_directory)): hashlib.sha256(file.read_bytes()).hexdigest() for file in source_directory.rglob('*') if file.is_file()}
                assert 'master.key' in manifest and any(name.startswith('runtime/conversations/') for name in manifest)
                assert any(name.startswith('knowledge/') and name.endswith('/original') for name in manifest)
                with tarfile.open(archive_path, 'w:gz') as archive:
                    archive.add(directory, arcname='.')
                os.chmod(archive_path, 0o600)
                restored_directory = pathlib.Path(restore_root) / 'data'
                restored_directory.mkdir(mode=0o700)
                with tarfile.open(archive_path) as archive:
                    archive.extractall(restored_directory, filter='data')
                restored_manifest = {str(file.relative_to(restored_directory)): hashlib.sha256(file.read_bytes()).hexdigest() for file in restored_directory.rglob('*') if file.is_file()}
                assert manifest == restored_manifest, 'backup changed or omitted files'
                directory = str(restored_directory)
                # Fresh login rather than relying on the copied browser session.
                opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
                process, setup_token = start()
                assert request('/api/auth/status')[1]['initialized']
                assert request(path)[0] == 401
                code, login = request('/api/auth/login', 'POST', {'password': 'e2e-long-test-password'})
                assert code == 200, login
                csrf = login['csrf']
                for route, expected in preserved.items():
                    assert request(route)[1] == expected, route
                assert len(Model.requests) == before_restore_calls, 'restore replayed old model work'
                assert request('/api/model/test', 'POST')[0] == 200
                assert request('/api/skills/' + skill['id'] + '/files/SKILL.md')[1]['content'].startswith('---')
                assert (restored_directory / 'workspaces/default/effects.txt').read_text() == 'hang\nhang\n'
                assert request('/api/runs/' + crashed_tool['id'])[1]['run']['status'] == 'interrupted'
                code, restored_search = request('/api/knowledge/search', 'POST', {'query': '发票开具流程', 'knowledge_ids': [restore_base['id']]})
                assert code == 200 and restored_search['hits'], restored_search
                restored_source = request('/api/knowledge/sources/' + restored_search['hits'][0]['source_id'])[1]
                assert '七个工作日' in restored_source['document_text']
                assert (restored_directory / 'knowledge' / restore_document['id'] / '1' / 'original').read_text() == content
                assert request(source_api)[1]['deleted']
                assert request('/api/mcps/' + mcp['id'] + '/test', 'POST')[0] == 200
                # Verify restored encrypted callback keys without sending a message.
                crypto_script = """import{createCipheriv,createHash}from'node:crypto';const k=Buffer.alloc(32,7),e=Buffer.from('restored-echo'),n=Buffer.alloc(4);n.writeUInt32BE(e.length);let b=Buffer.concat([Buffer.alloc(16,1),n,e,Buffer.from('wx-restore')]);const p=32-b.length%32;b=Buffer.concat([b,Buffer.alloc(p,p)]);const c=createCipheriv('aes-256-cbc',k,k.subarray(0,16));c.setAutoPadding(false);const x=Buffer.concat([c.update(b),c.final()]).toString('base64'),t=String(Math.floor(Date.now()/1000)),nonce='restore-nonce',sig=createHash('sha1').update(['restore-token',t,nonce,x].sort().join('')).digest('hex');process.stdout.write(new URLSearchParams({timestamp:t,nonce,msg_signature:sig,echostr:x}).toString());"""
                query = subprocess.check_output(['node', '--input-type=module', '-e', crypto_script], text=True)
                with urllib.request.urlopen(base + channel['callback_path'] + '?' + query, timeout=5) as response:
                    assert response.read() == b'restored-echo'
                assert request(path + '/runs', 'POST', {'input': 'restored continuation'})[0] == 202
                restored_chat = wait_snapshot(path, lambda state: state['runs'][-1]['status'] == 'completed')
                assert restored_chat['messages'][-1]['content'] == 'echo: restored continuation'
                assert any(message.get('content') == 'first question' for message in Model.requests[-1]['messages'])
                for file in restored_directory.glob('platform.sqlite*'):
                    assert b'restore-channel-secret' not in file.read_bytes()
                wait_snapshot('/api/runs/' + restored_chat['runs'][-1]['id'], lambda state: state['released'])
                assert request(path, 'DELETE', {'confirm': False})[0] == 400
                assert request(path, 'DELETE', {'confirm': True})[0] == 202
                assert request(path)[0] == 404
                wait_snapshot('/api/storage', lambda state: state['cleanup_pending'] == 0)
                for suffix in ['', '-wal', '-shm']:
                    assert not (restored_directory / 'runtime/conversations' / (conversation['id'] + '.sqlite' + suffix)).exists()
                assert request(other_path)[1] == other_history
                assert request('/api/knowledge/search', 'POST', {'query': '发票开具流程', 'knowledge_ids': [restore_base['id']]})[1]['hits']
                assert request('/api/auth/logout', 'POST')[0] == 200
                assert request(path)[0] == 401
                process.send_signal(signal.SIGTERM)
                process.communicate(timeout=8)
                assert process.returncode == 0
            directory = str(source_directory)
            for file in pathlib.Path(directory).glob('platform.sqlite*'):
                assert b'e2e-key' not in file.read_bytes()
                assert b'e2e-mcp-private' not in file.read_bytes()
            print('E2E passed: setup, cookie/CSRF, encrypted model config, actionable auth error, default Agent, real Runtime chat, SSE, restart history and context, logout, FIFO/concurrency, queue overflow/cancel, real timeout, Runtime SIGKILL without replay, Skills import, real MCP lifecycle/tool details, redacted MCP credentials, MCP deadline and Runtime crash cleanup, native knowledge upload/index/Agent retrieval/sources/restart/deletion, multi-model/Agent isolation and archive/history persistence, complete offline archive/byte verification/restore into a new directory, fresh authentication, model/MCP/WeCom key recovery, native files/index/sources and real Runtime context continuation, authenticated explicit conversation cleanup across Go and Runtime without affecting other history or knowledge.')
        finally:
            if process.poll() is None:
                process.terminate()
                process.communicate(timeout=8)
            for gate in Model.gates.values():
                gate.set()
            model.shutdown()
            model.server_close()


if __name__ == '__main__':
    main()
