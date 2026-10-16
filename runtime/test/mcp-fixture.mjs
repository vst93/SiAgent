import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import { CallToolRequestSchema, ListToolsRequestSchema } from '@modelcontextprotocol/sdk/types.js';
import { appendFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { setTimeout as wait } from 'node:timers/promises';

export function fixtureServer(workspace=process.cwd()) {
  const server = new Server({ name: 'fixture-mcp', version: '1.0.0' }, { capabilities: { tools: {} } });
  server.setRequestHandler(ListToolsRequestSchema, async () => ({ tools: [
    { name: 'inspect', description: 'Inspect explicit environment and workspace. '+(process.env.TOKEN??''), inputSchema: { type: 'object', properties: {} }, annotations: { readOnlyHint: true } },
    { name: 'mutate', description: 'Append one local fixture effect.', inputSchema: { type: 'object', properties: {} } },
    { name: 'fail', description: 'Known tool failure.', inputSchema: { type: 'object', properties: {} }, annotations: { readOnlyHint: true } },
    { name: 'hang', description: 'Record an effect then wait.', inputSchema: { type: 'object', properties: {} } },
    { name: 'disconnect', description: 'Record an effect then lose the connection.', inputSchema: { type: 'object', properties: {} } },
  ] }));
  server.setRequestHandler(CallToolRequestSchema, async (request, extra) => {
    const name = request.params.name;
    if (name === 'inspect') return { content: [{ type: 'text', text: JSON.stringify({ cwd: process.cwd(), env: process.env, pid: process.pid }) }] };
    if (name === 'fail') return { isError: true, content: [{ type: 'text', text: 'known failure' }] };
    await appendFile(join(workspace,'effects.txt'), name+'\n');
    await writeFile(join(workspace,'active-pid.txt'),String(process.pid));
    if (name === 'hang') await wait(60000, undefined, { signal: extra.signal });
    if (name === 'disconnect') process.exit(1);
    return { content: [{ type: 'text', text: 'effect recorded' }] };
  });
  return server;
}
if (process.argv[1]?.endsWith('mcp-fixture.mjs')) {
  await fixtureServer().connect(new StdioServerTransport());
}
