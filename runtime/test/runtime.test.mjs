import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtemp, readFile, rm, realpath, mkdir, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createInterface } from 'node:readline';
import { once } from 'node:events';
import { randomUUID } from 'node:crypto';
import { resolve } from 'node:path';
import { alias, connectMCP } from '../dist/capabilities.js';
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js';
import { fixtureServer } from './mcp-fixture.mjs';

async function fixture(t) {
  const requests = [];
  const server = createServer(async (req, res) => {
    let raw = '';
    for await (const chunk of req) raw += chunk;
    const body = JSON.parse(raw);
    requests.push({ ...body, authorization: req.headers.authorization });
    if (req.headers.authorization === 'Bearer rejected' || (body.model === 'summary-fails' && body.messages.some(message => message.role === 'system' && JSON.stringify(message.content).includes('context summarization assistant')))) {
      res.writeHead(401, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ error: { message: 'Incorrect API key', type: 'invalid_api_key' } }));
      return;
    }
    res.writeHead(200, { 'content-type': 'text/event-stream' });
    const send = (delta, finish_reason = null) => res.write(`data: ${JSON.stringify({ id: 'fixture', object: 'chat.completion.chunk', created: 1, model: 'fixture', choices: [{ index: 0, delta, finish_reason }] })}\n\n`);
    send({ role: 'assistant', content: '' });
    if (body.messages.some(message => message.role === 'system' && JSON.stringify(message.content).includes('context summarization assistant'))) {
      send({content:'Checkpoint summary: previous facts are retained.'}); send({},'stop'); res.end('data: [DONE]\n\n'); return;
    }
    const latest = body.messages.at(-1);
    const last = { ...latest, content: typeof latest.content === 'string' ? latest.content : latest.content.filter(part => part.type === 'text').at(-1)?.text ?? '' };
    if(last?.content === 'think') send({reasoning_content:'A provider reasoning summary.'});
    if (last?.content === 'hang') { send({ content: 'partial' }); return; }
    if (last?.content === 'fail') { res.end('data: invalid-json\n\n'); return; }
    if (last?.content?.startsWith('mcp:')) {
      const tool = last.content.slice(4);
      send({ tool_calls: [{ index: 0, id: 'mcp-call-'+requests.length, type: 'function', function: { name: alias('server', tool === 'unselected' ? 'mutate' : tool), arguments: '{}' } }] });
      send({}, 'tool_calls'); res.end('data: [DONE]\n\n'); return;
    }
    if (last?.content?.startsWith('knowledge:')) {
      const mode=last.content.slice(10);
      const name=mode==='read'?'knowledge_read':'knowledge_search';
      const args=mode==='read'?{source_id:'document.1.0'}:{query:'invoice',...(mode==='forbidden'?{knowledge_ids:['private']}:{})};
      send({tool_calls:[{index:0,id:'knowledge-call-'+requests.length,type:'function',function:{name,arguments:JSON.stringify(args)}}]});
      send({},'tool_calls');res.end('data: [DONE]\n\n');return;
    }
    if (last?.content?.startsWith('skill:')) {
      const mode=last.content.slice(6);
      send({ tool_calls: [{ index: 0, id: 'skill-call-'+requests.length, type: 'function', function: { name:'skill_resource', arguments: JSON.stringify({skill_id:mode==='unselected'?'another-skill':'skill',path:mode==='escape'?'../master.key':'reference.md'}) } }] });
      send({},'tool_calls');res.end('data: [DONE]\n\n');return;
    }
    if (last?.content === 'bash-tool') {
      send({ tool_calls: [{ index: 0, id: 'bash-call-'+requests.length, type: 'function', function: { name: 'bash', arguments: JSON.stringify({ command: 'printf advanced-ok' }) } }] });
      send({}, 'tool_calls'); res.end('data: [DONE]\n\n'); return;
    }
    const loop = body.messages.some(message => message.role === 'user' && message.content === 'loop');
    if ((last?.content === 'tool' || last?.content === 'wait' || loop) && body.tools?.length) {
      send({ tool_calls: [{ index: 0, id: 'tool-'+requests.length, type: 'function', function: { name: last?.content === 'wait' ? 'wait' : 'add', arguments: last?.content === 'wait' ? '{"milliseconds":2000}' : '{"a":2,"b":3}' } }] });
      send({}, 'tool_calls');
    } else {
      send({ content: last.role === 'tool' ? `sum=${last.content}` : 'hello' });
      send({}, 'stop');
    }
    res.end('data: [DONE]\n\n');
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const directory = await mkdtemp(join(tmpdir(), 'siagent-test-'));
  const children = [];
  t.after(async () => {
    for (const child of children) if (child.exitCode === null && child.signalCode === null) {
      const closed = once(child, 'close'); child.kill('SIGKILL'); await closed;
    }
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
    await rm(directory, { recursive: true, force: true });
  });
  function start() {
    const child = spawn(process.execPath, ['dist/index.js'], {
      env: { ...process.env, SIAGENT_RUNTIME_CANARY: 'must-not-be-inherited', SIAGENT_DATA_DIR: directory, SIAGENT_MODEL: 'fixture', SIAGENT_MODEL_API_KEY: 'test-only', SIAGENT_MODEL_BASE_URL: `http://127.0.0.1:${server.address().port}/v1` },
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    children.push(child);
    let stderr = '';
    child.stderr.on('data', chunk => stderr += chunk);
    const events = [];
    const pending = new Set();
    const lines = createInterface({ input: child.stdout });
    lines.on('line', line => {
      const event = JSON.parse(line);
      assert.equal(event.v, 1);
      events.push(event);
      for (const notify of pending) notify();
    });
    function wait(predicate) {
      return new Promise((resolve, reject) => {
        const timer = setTimeout(() => { pending.delete(check); reject(new Error(`event timeout: ${JSON.stringify(events)}; stderr: ${stderr}`)); }, 10000);
        function check() {
          const event = events.find(predicate);
          if (event) { clearTimeout(timer); pending.delete(check); resolve(event); }
        }
        pending.add(check); check();
      });
    }
    function send(type, fields = {}) {
      const request_id = `request-${Math.random().toString(36).slice(2)}`;
      child.stdin.write(JSON.stringify({ v: 1, type, request_id, ...fields }) + '\n');
      return request_id;
    }
    return { child, events, wait, send };
  }
  return { requests, start, directory, baseUrl: `http://127.0.0.1:${server.address().port}/v1`, children };
}

const terminal = (run) => e => e.run_id === run && ['run_completed', 'run_cancelled', 'run_failed', 'run_interrupted'].includes(e.type);

test('real Durable adapter: question, tool, history, idempotent rejection and ordered events', { timeout: 30000 }, async t => {
  const f = await fixture(t);
  const r = f.start();
  await r.wait(e => e.type === 'ready');
  r.send('start', { run_id: 'one', conversation_id: 'chat', input: 'hello' });
  const first = await r.wait(terminal('one'));
  assert.equal(first.type, 'run_completed');
  const lateCancel = r.send('cancel', { run_id: 'one' });
  assert.equal((await r.wait(e => e.request_id === lateCancel)).code, 'run_not_cancellable');
  assert.match(JSON.stringify(first.data.entries), /hello/);
  assert.equal(f.requests[0].tools?.length ?? 0, 0);
  r.send('start', { run_id: 'two', conversation_id: 'chat', input: 'tool', probe_tool: true });
  const result = await r.wait(terminal('two'));
  assert.equal(result.type, 'run_completed');
  assert.match(JSON.stringify(result.data.entries), /sum=5/);
  assert(r.events.some(e => e.run_id === 'two' && e.type === 'tool_start'));
  assert(r.events.some(e => e.run_id === 'two' && e.type === 'tool_end'));
  const seq = r.events.filter(e => e.run_id === 'two').map(e => e.seq);
  assert.deepEqual(seq, seq.map((_, i) => i + 1));
  assert(f.requests[1].messages.some(m => m.role === 'assistant' && m.content === 'hello'));
  const duplicate = r.send('start', { run_id: 'one', conversation_id: 'chat', input: 'hello' });
  assert.equal((await r.wait(e => e.request_id === duplicate)).code, 'duplicate_run');
  const closed = once(r.child, 'close'); r.child.kill('SIGTERM'); await closed;
  const reopened = f.start();
  await reopened.wait(e => e.type === 'ready');
  const statusID = reopened.send('status', { run_id: 'two' });
  assert.equal((await reopened.wait(e => e.request_id === statusID)).data.status, 'completed');
  reopened.send('start', { run_id: 'three', conversation_id: 'chat', input: 'hello' });
  assert.equal((await reopened.wait(terminal('three'))).type, 'run_completed');
  assert(f.requests.at(-1).messages.some(m => m.role === 'tool' && m.content === '5'));
});

test('cancel aborts the model request and preserves partial output', { timeout: 20000 }, async t => {
  const f = await fixture(t); const r = f.start();
  await r.wait(e => e.type === 'ready');
  r.send('start', { run_id: 'cancel', conversation_id: 'chat', input: 'hang' });
  await r.wait(e => e.type === 'message_delta');
  const busyID = r.send('start', { run_id: 'busy', conversation_id: 'chat', input: 'hello' });
  assert.equal((await r.wait(e => e.request_id === busyID)).code, 'busy');
  r.send('cancel', { run_id: 'cancel' });
  const result = await r.wait(terminal('cancel'));
  assert.equal(result.type, 'run_cancelled');
  assert.match(JSON.stringify(result.data.entries), /partial/);
  assert.equal(f.requests.length, 1);
});

test('SIGKILL recovery marks interrupted without issuing another model request', { timeout: 30000 }, async t => {
  const f = await fixture(t); const r = f.start();
  await r.wait(e => e.type === 'ready');
  r.send('start', { run_id: 'crash', conversation_id: 'chat', input: 'hang' });
  await r.wait(e => e.type === 'message_delta');
  const closed = once(r.child, 'close'); r.child.kill('SIGKILL'); await closed;
  const reopened = f.start();
  await reopened.wait(e => e.type === 'ready');
  const statusID = reopened.send('status', { run_id: 'crash' });
  assert.equal((await reopened.wait(e => e.request_id === statusID)).data.status, 'interrupted');
  assert.equal(f.requests.length, 1);
  reopened.send('start', { run_id: 'next', conversation_id: 'chat', input: 'hello' });
  const result = await reopened.wait(terminal('next'));
  assert.equal(result.type, 'run_completed');
  assert(f.requests.at(-1).messages.some(message => message.role === "user" && message.content === "hang"));
  assert.equal(f.requests.length, 2);
});

test('invalid protocol input does not kill the adapter', { timeout: 15000 }, async t => {
  const f = await fixture(t); const r = f.start();
  await r.wait(e => e.type === 'ready');
  r.child.stdin.write('not-json\nnull\n');
  await r.wait(e => e.code === 'invalid_json');
  await r.wait(e => e.code === 'invalid_request');
  const id = r.send('start', { run_id: '../escape', conversation_id: 'chat', input: 'hello' });
  assert.equal((await r.wait(e => e.request_id === id)).code, 'invalid_run');
  assert.equal(f.requests.length, 0);
});


test('workspace coding tools are offered only when the agent opts in', { timeout: 20000 }, async t => {
  const f = await fixture(t);
  const r = f.start();
  await r.wait(e => e.type === 'ready');
  const capabilities = { skills: [{ id: 'skill', name: 'example', description: 'Skill', content: 'Run the script when asked.', files: [{ path: 'scripts/query.js', size: 16 }] }], mcps: [] };
  r.send('start', { run_id: 'plain', conversation_id: 'plain', input: 'hello', capabilities });
  assert.equal((await r.wait(terminal('plain'))).type, 'run_completed');
  assert.deepEqual((f.requests.at(-1).tools ?? []).map(tool => tool.function.name), ['skill_resource']);
  assert.doesNotMatch(JSON.stringify(f.requests.at(-1).messages), /skills\/skill/);

  const workspace = join(f.directory, 'workspaces', 'default');
  r.send('start', { run_id: 'advanced', conversation_id: 'advanced', input: 'bash-tool', capabilities, advanced_tools: true, workspace });
  const result = await r.wait(terminal('advanced'));
  assert.equal(result.type, 'run_completed');
  assert.deepEqual((f.requests.at(-1).tools ?? []).map(tool => tool.function.name).sort(), ['bash', 'edit', 'read', 'skill_resource', 'write']);
  assert(r.events.some(e => e.run_id === 'advanced' && e.type === 'tool_end'));
  assert.match(JSON.stringify(result.data.entries), /advanced-ok/);
  assert.match(JSON.stringify(f.requests.at(-1).messages), /skills\/skill/);
});

test('tool crash after an external effect: abort on reopen does not repeat that effect', { timeout: 20000 }, async t => {
  const f = await fixture(t);
  const worker = mode => {
    const child = spawn(process.execPath, ['test/tool-crash-worker.mjs', mode], {
      env: { ...process.env, PROBE_DIRECTORY: f.directory, SIAGENT_MODEL_BASE_URL: f.baseUrl },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    f.children.push(child);
    return child;
  };
  const first = worker('start');
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('tool never started')), 10000);
    first.stdout.on('data', chunk => { if (String(chunk).includes('effect-started')) { clearTimeout(timer); resolve(); } });
  });
  const killed = once(first, 'close'); first.kill('SIGKILL'); await killed;
  const recovered = worker('recover');
  let output = '', stderr = '';
  recovered.stdout.on('data', chunk => output += chunk);
  recovered.stderr.on('data', chunk => stderr += chunk);
  const [code] = await once(recovered, 'close');
  assert.equal(code, 0, stderr);
  assert.match(output, /tool/);
  assert.equal(await readFile(join(f.directory, 'effects.txt'), 'utf8'), 'effect\n');
  assert.equal(f.requests.length, 1);
});


test('cancellation during conversation setup prevents a model request', { timeout: 15000 }, async t => {
  const f = await fixture(t); const r = f.start();
  await r.wait(e => e.type === 'ready');
  r.send('start', { run_id: 'early', conversation_id: 'chat', input: 'hello' });
  r.send('cancel', { run_id: 'early' });
  assert.equal((await r.wait(terminal('early'))).type, 'run_cancelled');
  assert.equal(f.requests.length, 0);
});


test('private per-run model configuration overrides environment and classifies auth failure', { timeout: 15000 }, async t => {
  const f = await fixture(t); const r = f.start();
  await r.wait(e => e.type === 'ready');
  r.send('start', { run_id: 'private', conversation_id: 'private', input: 'hello', instructions: 'custom instruction', model: { base_url: f.baseUrl, name: 'private-model', api_key: 'private-key' } });
  assert.equal((await r.wait(terminal('private'))).type, 'run_completed');
  assert.equal(f.requests[0].model, 'private-model');
  assert.equal(f.requests[0].authorization, 'Bearer private-key');
  assert.match(JSON.stringify(f.requests[0].messages), /custom instruction/);
  r.send('start', { run_id: 'reject', conversation_id: 'reject', input: 'hello', model: { base_url: f.baseUrl, name: 'private-model', api_key: 'rejected' } });
  const failed = await r.wait(terminal('reject'));
  assert.equal(failed.type, 'run_failed');
  assert.equal(failed.data.error_code, 'auth');
});


test('run timeout confirms cancellation and releases the conversation before reuse', { timeout: 15000 }, async t => {
 const f=await fixture(t);const r=f.start();await r.wait(e=>e.type==='ready');
 r.send('start',{run_id:'timed',conversation_id:'chat',input:'hang',limits:{run_timeout_ms:200}});
 const result=await r.wait(terminal('timed'));assert.equal(result.type,'run_failed');assert.equal(result.data.error_code,'timeout');
 await r.wait(e=>e.type==='run_released'&&e.run_id==='timed');
 r.send('start',{run_id:'after-timeout',conversation_id:'chat',input:'hello'});
 assert.equal((await r.wait(terminal('after-timeout'))).type,'run_completed');
});

test('tool deadline stops later model calls and round limit prevents more tools', { timeout: 15000 }, async t => {
 const f=await fixture(t);const r=f.start();await r.wait(e=>e.type==='ready');
 r.send('start',{run_id:'tool-timeout',conversation_id:'tool-chat',input:'wait',probe_tool:true,limits:{tool_timeout_ms:40}});
 const timeout=await r.wait(terminal('tool-timeout'));assert.equal(timeout.type,'run_failed');assert.equal(timeout.data.error_code,'tool_timeout');assert.equal(f.requests.length,1);
 await r.wait(e=>e.type==='run_released'&&e.run_id==='tool-timeout');
 r.send('start',{run_id:'rounds',conversation_id:'loop-chat',input:'loop',probe_tool:true,limits:{tool_rounds:2}});
 const result=await r.wait(terminal('rounds'));assert.equal(result.type,'run_failed');assert.equal(result.data.error_code,'tool_rounds');
 assert.equal(f.requests.length,4);
 assert.equal(result.data.entries.filter(entry=>entry.kind==='pi.tool-result'&&entry.model?.some(message=>message.role==='toolResult'&&!message.isError)).length,2);
});

test('context guard rejects before contacting the provider and retains the input', {timeout:15000},async t=>{
 const f=await fixture(t);const r=f.start();await r.wait(e=>e.type==='ready');
 r.send('start',{run_id:'context',conversation_id:'context',input:'x'.repeat(6000),model:{base_url:f.baseUrl,name:'fixture',api_key:'test-only',options:{context_window:1024,max_tokens:512}},limits:{context_bytes:1024}});
 const result=await r.wait(terminal('context'));assert.equal(result.type,'run_failed');assert.equal(result.data.error_code,'context_limit');assert.equal(f.requests.length,0);assert.match(JSON.stringify(result.data.entries),/xxxx/);
});

async function configuredMCP(r, selected) {
 const config={id:'server',name:'Fixture MCP',transport:'stdio',command:process.execPath,args:[resolve('test/mcp-fixture.mjs')],url:'',tools:[]};
 const secret={env:{TOKEN:'mcp-private-token'},headers:{}};
 const request=r.send('mcp_test',{mcp:config,mcp_secret:secret});
 const response=await r.wait(event=>event.request_id===request);
 assert.equal(response.type,'mcp_test_result',JSON.stringify(response));
 config.tools=response.data.tools.filter(tool=>selected.includes(tool.name));
 assert.equal(config.tools.length,selected.length);
 return {capabilities:{skills:[],mcps:[config]},mcp_secrets:{server:secret},agent_id:'default'};
}

test('MCP stdio: exact environment, selected tools, known failure, redacted output and workspace', {timeout:20000},async t=>{
 const f=await fixture(t);const r=f.start();await r.wait(e=>e.type==='ready');
 const capabilities=await configuredMCP(r,['inspect','fail']);
 r.send('start',{run_id:'inspect',conversation_id:'inspect',input:'mcp:inspect',...capabilities});
 const result=await r.wait(terminal('inspect'));assert.equal(result.type,'run_completed');
 const end=r.events.find(e=>e.type==='tool_end'&&e.run_id==='inspect');
 const detail=JSON.parse(end.data.result);assert.deepEqual(Object.fromEntries(Object.entries(detail.env).filter(([key])=>process.platform !== 'darwin' || key !== '__CF_USER_TEXT_ENCODING')),{TOKEN:'[redacted]'});assert.equal(detail.cwd,await realpath(join(f.directory,'workspaces','default')));
 assert(!JSON.stringify(r.events).includes('mcp-private-token'));assert(!JSON.stringify(f.requests).includes('mcp-private-token'));
 assert.deepEqual(f.requests[0].tools.map(tool=>tool.function.name).sort(),[alias('server','inspect'),alias('server','fail')].sort());
 r.send('start',{run_id:'unselected',conversation_id:'unselected',input:'mcp:unselected',...capabilities});
 assert.equal((await r.wait(terminal('unselected'))).type,'run_completed');
 await assert.rejects(readFile(join(f.directory,'workspaces','default','effects.txt')),{code:'ENOENT'});
 r.send('start',{run_id:'known-failure',conversation_id:'known-failure',input:'mcp:fail',...capabilities});
 assert.equal((await r.wait(terminal('known-failure'))).type,'run_completed');
 const failed=r.events.find(e=>e.type==='tool_end'&&e.run_id==='known-failure');assert.equal(failed.data.entry.model[0].isError,true);assert.equal(failed.data.result,'known failure');
});

test('MCP timeout, cancellation and lost connection retain unknown effects and do not retry', {timeout:20000},async t=>{
 const f=await fixture(t);const r=f.start();await r.wait(e=>e.type==='ready');
 const capabilities=await configuredMCP(r,['hang','disconnect']);
 r.send('start',{run_id:'deadline',conversation_id:'deadline',input:'mcp:hang',limits:{tool_timeout_ms:150},...capabilities});
 const timed=await r.wait(terminal('deadline'));assert(['run_failed','run_interrupted'].includes(timed.type));
 await r.wait(e=>e.type==='run_released'&&e.run_id==='deadline');
 assert.equal(f.requests.length,1);
 r.send('start',{run_id:'cancel-mcp',conversation_id:'cancel-mcp',input:'mcp:hang',...capabilities});
 await r.wait(e=>e.type==='tool_start'&&e.run_id==='cancel-mcp');
 r.send('cancel',{run_id:'cancel-mcp'});
 assert.equal((await r.wait(terminal('cancel-mcp'))).type,'run_cancelled');
 await r.wait(e=>e.type==='run_released'&&e.run_id==='cancel-mcp');
 assert.equal(f.requests.length,2);
 r.send('start',{run_id:'disconnect',conversation_id:'disconnect',input:'mcp:disconnect',...capabilities});
 assert.equal((await r.wait(terminal('disconnect'))).type,'run_interrupted');
 await r.wait(e=>e.type==='run_released'&&e.run_id==='disconnect');
 assert.equal(f.requests.length,3);
 const effects=await readFile(join(f.directory,'workspaces','default','effects.txt'),'utf8');assert.equal(effects.trim().split('\n').filter(line=>line==='disconnect').length,1);
 assert(r.events.some(e=>e.type==='tool_end'&&e.run_id==='disconnect'&&e.data.outcome_unknown));
});

test('MCP Streamable HTTP authenticates, calls tools, cancels a request and terminates its session', {timeout:15000},async t=>{
 const directory=await mkdtemp(join(tmpdir(),'mcp-http-test-'));
 const requests=[];
 const transport=new StreamableHTTPServerTransport({sessionIdGenerator:randomUUID,enableJsonResponse:true});
 const mcp=fixtureServer(directory);await mcp.connect(transport);
 const http=createServer(async(req,res)=>{
   requests.push({method:req.method,authorization:req.headers.authorization});
   if(req.headers.authorization!=='Bearer remote-secret'){res.writeHead(401);res.end();return;}
   await transport.handleRequest(req,res);
 });
 http.listen(0,'127.0.0.1');await once(http,'listening');
 t.after(async()=>{await transport.close();await mcp.close();http.closeAllConnections();await new Promise(resolve=>http.close(resolve));await rm(directory,{recursive:true,force:true});});
 const config={id:'http',name:'HTTP fixture',transport:'http',command:'',args:[],url:`http://127.0.0.1:${http.address().port}/mcp`,tools:[]};
 await assert.rejects(connectMCP(config,{env:{},headers:{}},directory,AbortSignal.timeout(2000)),/mcp_connection_failed/);
 const connection=await connectMCP(config,{env:{},headers:{Authorization:'Bearer remote-secret'}},directory,AbortSignal.timeout(5000));
 assert.equal(connection.tools.length,5);
 const result=await connection.client.callTool({name:'fail',arguments:{}});assert.equal(result.isError,true);
 const aborted = connection.client.callTool({name:'hang',arguments:{}}, undefined, {signal:AbortSignal.timeout(150)});
 await assert.rejects(aborted);
 await connection.close();assert(requests.some(request=>request.authorization==='Bearer remote-secret'));
 assert(requests.some(request=>request.method==='DELETE'));

});

test('Runtime SIGKILL closes the guardian lifeline and kills an executing MCP process without replay', {timeout:20000},async t=>{
 const f=await fixture(t);const r=f.start();await r.wait(e=>e.type==='ready');
 const capabilities=await configuredMCP(r,['hang']);
 r.send('start',{run_id:'crashed-mcp',conversation_id:'crashed-mcp',input:'mcp:hang',...capabilities});
 let pid;
 for(let attempt=0;attempt<100;attempt++){
   try {pid=Number(await readFile(join(f.directory,'workspaces','default','active-pid.txt'),'utf8'));if(pid)break;} catch {}
   await new Promise(resolve=>setTimeout(resolve,20));
 }
 assert(pid,'MCP server did not record the running call');
 const closed=once(r.child,'close');r.child.kill('SIGKILL');await closed;
 let alive=true;
 for(let attempt=0;attempt<100;attempt++){
   try {process.kill(pid,0);}catch{alive=false;break;}
   await new Promise(resolve=>setTimeout(resolve,20));
 }
 assert.equal(alive,false,'MCP process survived Runtime SIGKILL');
 const reopened=f.start();await reopened.wait(e=>e.type==='ready');
 const request=reopened.send('status',{run_id:'crashed-mcp'});
 assert.equal((await reopened.wait(e=>e.request_id===request)).data.status,'interrupted');
 assert.equal(await readFile(join(f.directory,'workspaces','default','effects.txt'),'utf8'),'hang\n');
 assert.equal(f.requests.length,1);
});

test('Skills use the Durable tool registry with selected instructions and bounded resource access', {timeout:15000},async t=>{
 const f=await fixture(t);const r=f.start();await r.wait(e=>e.type==='ready');
 const root=join(f.directory,'skills','skill');await mkdir(root,{recursive:true});await writeFile(join(root,'reference.md'),'Trusted local reference.');
 const capabilities={skills:[{id:'skill',name:'example',description:'Reference instructions',content:'Read the selected reference when needed.',files:[{path:'reference.md',size:24}]}],mcps:[]};
 r.send('start',{run_id:'skill-read',conversation_id:'skill-read',input:'skill:read',capabilities});
 assert.equal((await r.wait(terminal('skill-read'))).type,'run_completed');
 assert.match(f.requests[0].messages.find(message=>message.role==='system').content,/Read the selected reference/);
 assert.deepEqual(f.requests[0].tools.map(tool=>tool.function.name),['skill_resource']);
 assert.equal(r.events.find(event=>event.run_id==='skill-read'&&event.type==='tool_end').data.result,'Trusted local reference.');
 for(const mode of ['unselected','escape']) {
  r.send('start',{run_id:'skill-'+mode,conversation_id:'skill-'+mode,input:'skill:'+mode,capabilities});
  assert.equal((await r.wait(terminal('skill-'+mode))).type,'run_completed');
  assert.equal(r.events.find(event=>event.run_id==='skill-'+mode&&event.type==='tool_end').data.entry.model[0].isError,true);
 }
});

test('a changed MCP input schema fails closed before any model or tool request', {timeout:15000},async t=>{
 const f=await fixture(t);const r=f.start();await r.wait(e=>e.type==='ready');
 const capabilities=await configuredMCP(r,['mutate']);
 capabilities.capabilities.mcps[0].tools[0].input_schema={type:'object',properties:{changed:{type:'string'}}};
 r.send('start',{run_id:'changed-schema',conversation_id:'changed-schema',input:'mcp:mutate',...capabilities});
 const result=await r.wait(terminal('changed-schema'));assert.equal(result.type,'run_failed');assert.equal(result.data.error_code,'tool_setup');
 assert.equal(f.requests.length,0);
 await assert.rejects(readFile(join(f.directory,'workspaces','default','effects.txt')),{code:'ENOENT'});
});

test('native knowledge tools use private Go RPC and remain absent without a binding', {timeout:15000},async t=>{
 const f=await fixture(t);const r=f.start();await r.wait(e=>e.type==='ready');
 r.send('start',{run_id:'knowledge',conversation_id:'knowledge',input:'knowledge:search',knowledge_ids:['allowed']});
 const request=await r.wait(e=>e.type==='knowledge_request'&&e.run_id==='knowledge');
 assert.equal(request.data.operation,'search');assert.equal(request.data.input.query,'invoice');
 r.send('knowledge_result',{request_id:request.request_id,data:{ok:true,result:{hits:[{source_id:'document.1.0',source_url:'/?source=document.1.0',text:'Invoice policy.'}],match_mode:'all_terms'}}});
 assert.equal((await r.wait(terminal('knowledge'))).type,'run_completed');
 assert.deepEqual(f.requests[0].tools.map(tool=>tool.function.name).sort(),['knowledge_read','knowledge_search']);
 assert.match(f.requests[1].messages.at(-1).content,/Invoice policy/);
 r.send('start',{run_id:'unbound-knowledge',conversation_id:'unbound-knowledge',input:'knowledge:search'});
 assert.equal((await r.wait(terminal('unbound-knowledge'))).type,'run_completed');
 assert(!r.events.some(e=>e.type==='knowledge_request'&&e.run_id==='unbound-knowledge'));
 assert.equal(r.events.find(e=>e.type==='tool_end'&&e.run_id==='unbound-knowledge').data.entry.model[0].isError,true);
});

test('native knowledge cancellation aborts pending RPC and ignores a late response', {timeout:15000},async t=>{
 const f=await fixture(t);const r=f.start();await r.wait(e=>e.type==='ready');
 r.send('start',{run_id:'cancel-knowledge',conversation_id:'cancel-knowledge',input:'knowledge:search',knowledge_ids:['allowed']});
 const request=await r.wait(e=>e.type==='knowledge_request'&&e.run_id==='cancel-knowledge');
 r.send('cancel',{run_id:'cancel-knowledge'});
 assert.equal((await r.wait(terminal('cancel-knowledge'))).type,'run_cancelled');
 await r.wait(e=>e.type==='knowledge_cancel'&&e.request_id===request.request_id);
 await r.wait(e=>e.type==='run_released'&&e.run_id==='cancel-knowledge');
 r.send('knowledge_result',{request_id:request.request_id,data:{ok:true,result:{hits:[]}}});
 const status=r.send('status',{run_id:'cancel-knowledge'});await r.wait(e=>e.request_id===status);
 assert.equal(f.requests.length,1);assert(!r.events.some(e=>e.type==='error'&&e.request_id===request.request_id));
});

test('conversation deletion erases closed context and journal, is idempotent and rejects active/path traversal requests', {timeout:15000}, async t=>{
 const f=await fixture(t),r=f.start();await r.wait(e=>e.type==='ready');
 r.send('start',{run_id:'erase-run',conversation_id:'erase-chat',input:'erase-only-input'});await r.wait(e=>e.run_id==='erase-run'&&e.type==='run_released');
 r.send('start',{run_id:'keep-run',conversation_id:'keep-chat',input:'keep-only-input'});await r.wait(e=>e.run_id==='keep-run'&&e.type==='run_released');
 const deletion=r.send('conversation_delete',{conversation_id:'erase-chat'});assert.equal((await r.wait(e=>e.request_id===deletion)).type,'conversation_deleted');
 for(const suffix of ['','-wal','-shm'])await assert.rejects(readFile(join(f.directory,'runtime','conversations','erase-chat.sqlite'+suffix)),{code:'ENOENT'});
 assert((await readFile(join(f.directory,'runtime','conversations','keep-chat.sqlite'))).length>0);
 const status=r.send('status',{run_id:'erase-run'});assert.equal((await r.wait(e=>e.request_id===status)).data,null);
 const repeated=r.send('conversation_delete',{conversation_id:'erase-chat'});assert.equal((await r.wait(e=>e.request_id===repeated)).type,'conversation_deleted');
 r.send('start',{run_id:'fresh-run',conversation_id:'erase-chat',input:'fresh-only-input'});await r.wait(e=>e.run_id==='fresh-run'&&e.type==='run_released');assert(!JSON.stringify(f.requests.at(-1).messages).includes('erase-only-input'));
 r.send('start',{run_id:'busy-run',conversation_id:'erase-chat',input:'hang'});await r.wait(e=>e.run_id==='busy-run'&&e.type==='message_delta');
 const busy=r.send('conversation_delete',{conversation_id:'erase-chat'});assert.equal((await r.wait(e=>e.request_id===busy)).code,'conversation_busy');
 const invalid=r.send('conversation_delete',{conversation_id:'../keep-chat'});assert.equal((await r.wait(e=>e.request_id===invalid)).type,'error');
 r.send('cancel',{run_id:'busy-run'});await r.wait(e=>e.run_id==='busy-run'&&e.type==='run_released');
});

test('model Token window allows inputs beyond the deprecated byte limit', {timeout:15000},async t=>{
 const f=await fixture(t);const r=f.start();await r.wait(e=>e.type==='ready');
 r.send('start',{run_id:'long-context',conversation_id:'long-context',input:'x'.repeat(30000),model:{base_url:f.baseUrl,name:'fixture',api_key:'test-only',options:{context_window:131072,max_tokens:4096}},limits:{context_bytes:1024}});
 const result=await r.wait(terminal('long-context'));assert.equal(result.type,'run_completed');assert.equal(f.requests.length,1);
});


test('configured recent context trims model requests while durable history remains and tools stay paired', { timeout: 30000 }, async t => {
 const f=await fixture(t);const r=f.start();await r.wait(e=>e.type==='ready');
 for(const [run,input,probe] of [['old','old fact',false],['tool-turn','tool',true],['current','new question',false]]){
  r.send('start',{run_id:run,conversation_id:'bounded',input,probe_tool:probe,context_policy:{strategy:'recent',max_turns:2}});
  assert.equal((await r.wait(terminal(run))).type,'run_completed');await r.wait(e=>e.run_id===run&&e.type==='run_released');
 }
 const messages=f.requests.at(-1).messages;
 assert(!messages.some(m=>m.role==='user'&&m.content==='old fact'));
 assert(messages.some(m=>m.role==='assistant'&&m.tool_calls?.length));
 assert(messages.some(m=>m.role==='tool'));
 r.send('start',{run_id:'restore-full',conversation_id:'bounded',input:'show history',context_policy:{strategy:'full'}});
 assert.equal((await r.wait(terminal('restore-full'))).type,'run_completed');
 assert(f.requests.at(-1).messages.some(m=>m.role==='user'&&m.content==='old fact'));
});

test('reasoning output is opt-in and provider summaries never mix into the final answer', { timeout: 30000 }, async t => {
 const f=await fixture(t);const r=f.start();await r.wait(e=>e.type==='ready');
 const model={name:'fixture',base_url:f.baseUrl,api_key:'test-only',options:{reasoning:true,thinking_level:'low'}};
 for (const thinking of [false,true]) {
  const run='thinking-'+thinking;
  r.send('start',{run_id:run,conversation_id:run,input:'think',model,output_policy:{thinking,tools:'summary',sources:true,progress:true}});
  const result=await r.wait(terminal(run));assert.equal(result.type,'run_completed');assert.equal(result.data.answer,'hello');
  await r.wait(e=>e.run_id===run&&e.type==='run_released');
  const thoughts=r.events.filter(e=>e.run_id===run&&e.type==='thinking_delta').map(e=>e.data.text).join('');
  assert.equal(thoughts,thinking?'A provider reasoning summary.':'');
 }
});

test('automatic summary compacts older context and preserves recent verbatim text across restart', { timeout: 60000 }, async t => {
 const f=await fixture(t);const r=f.start();await r.wait(e=>e.type==='ready');
 const model={name:'fixture',base_url:f.baseUrl,api_key:'test-only',options:{context_window:4096,max_tokens:256}};
 for(let index=0;index<9;index++){
  const run='summary-'+index;
  r.send('start',{run_id:run,conversation_id:'summarized',input:`fact-${index}: `+'historical details '.repeat(70),model,context_policy:{strategy:'summary',summary_threshold:50,keep_recent_tokens:256}});
  const result=await r.wait(terminal(run));assert.equal(result.type,'run_completed',JSON.stringify(result.data));
  await r.wait(e=>e.run_id===run&&e.type==='run_released');
 }
 assert(r.events.some(e=>e.type==='compaction_start'));
 assert(r.events.some(e=>e.type==='compaction_end'));
 assert(f.requests.some(request=>request.messages.some(m=>JSON.stringify(m.content).includes('Checkpoint summary: previous facts'))));
 const closed=once(r.child,'close');r.child.kill('SIGTERM');await closed;
 const reopened=f.start();await reopened.wait(e=>e.type==='ready');
 reopened.send('start',{run_id:'after-summary-restart',conversation_id:'summarized',input:'continue',model,context_policy:{strategy:'summary',summary_threshold:50,keep_recent_tokens:256}});
 assert.equal((await reopened.wait(terminal('after-summary-restart'))).type,'run_completed');
 assert(f.requests.at(-1).messages.some(m=>JSON.stringify(m.content).includes('Checkpoint summary: previous facts')));
});


test('failed summarization stops without silently dropping history or retrying the request', { timeout: 30000 }, async t => {
 const f=await fixture(t);const r=f.start();await r.wait(e=>e.type==='ready');
 const model={name:'summary-fails',base_url:f.baseUrl,api_key:'test-only',options:{context_window:4096,max_tokens:256}};
 let failed=false;
 for(let index=0;index<9;index++) {
  const run='summary-failure-'+index;
  r.send('start',{run_id:run,conversation_id:'summary-failure',input:`fact-${index}: `+'historical details '.repeat(70),model,context_policy:{strategy:'summary',summary_threshold:50,keep_recent_tokens:256}});
  const result=await r.wait(terminal(run));await r.wait(e=>e.run_id===run&&e.type==='run_released');
  if(result.type==='run_failed'){assert.equal(result.data.error_code,'compaction_failed');failed=true;break;}
  assert.equal(result.type,'run_completed');
 }
 assert(failed,'summary rejection did not fail the run');
 assert.equal(f.requests.filter(request=>request.messages.some(m=>m.role==='system'&&JSON.stringify(m.content).includes('context summarization assistant'))).length,1);
 assert(r.events.some(e=>e.type==='compaction_end'&&e.data.failed));
});

test('recent strategy continues after archived history exceeds the model window', { timeout: 30000 }, async t => {
 const f=await fixture(t);const r=f.start();await r.wait(e=>e.type==='ready');
 const model={name:'fixture',base_url:f.baseUrl,api_key:'test-only',options:{context_window:2048,max_tokens:256}};
 for(let index=0;index<12;index++) {
  const run='long-archive-'+index;
  r.send('start',{run_id:run,conversation_id:'long-archive',input:`turn-${index}: `+'historical details '.repeat(50),model,context_policy:{strategy:'recent',max_turns:2}});
  const result=await r.wait(terminal(run));assert.equal(result.type,'run_completed',JSON.stringify(result.data));
  await r.wait(e=>e.run_id===run&&e.type==='run_released');
 }
 const users=f.requests.at(-1).messages.filter(message=>message.role==='user');
 assert.equal(users.length,2);assert(users[0].content.startsWith('turn-10:'));assert(users[1].content.startsWith('turn-11:'));
 assert(!JSON.stringify(f.requests.at(-1)).includes('turn-0:'));
});


test('turn time survives tool rounds, later turns and restart without rewriting the cached prefix', {timeout:30000}, async t => {
  const f = await fixture(t);
  let r = f.start();
  await r.wait(e => e.type === 'ready');
  const instructions = 'Keep this role stable.';
  const firstTime = {received_at:'2026-03-08T06:30:00Z',timezone:'America/New_York'};
  r.send('start',{run_id:'clock-one',conversation_id:'clock',input:'hello',instructions,probe_tool:true,time_context:firstTime});
  assert.equal((await r.wait(terminal('clock-one'))).type,'run_completed');
  await r.wait(e=>e.run_id==='clock-one'&&e.type==='run_released');
  const first = f.requests[0];
  const originalUser = first.messages.find(m=>m.role==='user');
  assert.equal(originalUser.content.at(-1).text,'hello');
  assert.match(originalUser.content[0].text,/01:30:00/);
  assert(!JSON.stringify(first.messages.filter(m=>m.role==='system')).includes(firstTime.received_at));
  const secondTime = {received_at:'2026-03-08T07:30:00Z',timezone:'America/New_York'};
  r.send('start',{run_id:'clock-two',conversation_id:'clock',input:'tool',instructions,time_context:secondTime,probe_tool:true});
  assert.equal((await r.wait(terminal('clock-two'))).type,'run_completed');
  await r.wait(e=>e.run_id==='clock-two'&&e.type==='run_released');
  const second = f.requests[1], afterTools = f.requests[2];
  const currentUser = second.messages.filter(m=>m.role==='user').at(-1);
  assert.match(currentUser.content[0].text,/03:30:00/);
  assert.deepEqual(second.messages.find(m=>m.role==='user'),originalUser);
  assert.deepEqual(afterTools.messages.filter(m=>m.role==='user').at(-1),currentUser);
  assert.deepEqual(second.messages.slice(0,first.messages.length),first.messages);
  const closed=once(r.child,'close');r.child.kill('SIGTERM');await closed;
  r=f.start();await r.wait(e=>e.type==='ready');
  r.send('start',{run_id:'clock-three',conversation_id:'clock',input:'hello',instructions,probe_tool:true,time_context:{received_at:'2026-03-09T00:01:00Z',timezone:'UTC'}});
  assert.equal((await r.wait(terminal('clock-three'))).type,'run_completed');
  const reopened=f.requests.at(-1);
  assert.deepEqual(reopened.messages.find(m=>m.role==='user'),originalUser);
  assert.deepEqual(reopened.messages.filter(m=>m.role==='user')[1],currentUser);
  assert.match(reopened.messages.filter(m=>m.role==='user').at(-1).content[0].text,/2026-03-09T00:01:00Z/);
});
