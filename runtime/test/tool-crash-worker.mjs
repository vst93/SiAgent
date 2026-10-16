// Separate process so the test can kill a tool after its effect but before its result commit.
import { appendFile } from 'node:fs/promises';
import { join } from 'node:path';
import { BACKGROUND_CONTEXT as ctx } from '@earendil-works/chord/context';
import { Type } from '@earendil-works/pi-ai';
import { createModels, createProvider } from '@earendil-works/pi-ai/models';
import { stream, streamSimple } from '@earendil-works/pi-ai/api/openai-completions';
import { Harness, createRegistry, defineExtension, defineTool } from '@earendil-works/pi-durable';
import { openNodeSqliteStorage } from '@earendil-works/pi-durable/storage/sqlite/node';
const directory = process.env.PROBE_DIRECTORY;
const models = createModels();
const baseUrl = process.env.SIAGENT_MODEL_BASE_URL;
models.setProvider(createProvider({
  id:'fixture', baseUrl, auth:{apiKey:{name:'test',resolve:async()=>({auth:{apiKey:'test-only'}})}},
  models:[{id:'fixture',name:'fixture',provider:'fixture',api:'openai-completions',baseUrl,reasoning:false,input:['text'],contextWindow:32768,maxTokens:4096,cost:{input:0,output:0,cacheRead:0,cacheWrite:0}}],
  api:{stream,streamSimple},
}));
const registry = createRegistry();
registry.install(defineExtension({name:'probe',tools:[defineTool({
  name:'add', description:'Test effect with unknown outcome.',parameters:Type.Object({a:Type.Number(),b:Type.Number()}),
  execute:async()=>{
    await appendFile(join(directory,'effects.txt'),'effect\n');
    console.log('effect-started');
    await new Promise(()=>{});
    return {};
  },
})]}));
const harness = await Harness.open(await openNodeSqliteStorage(join(directory,'tool.sqlite')),{models,registry,settings:{retry:{maxRetries:0}}},ctx);
const root = await harness.root(ctx);
if (process.argv[2] === 'recover') {
  await root.abort(ctx,{background:true});
  const view = await root.viewState(ctx);
  console.log(JSON.stringify(view.value.entries));
  view.dispose();
  await harness.close(ctx);
} else {
  await root.configure({model:{provider:'fixture',modelId:'fixture'}},ctx);
  await (await root.submit({type:'input',content:'tool'},ctx)).wait(ctx);
}
