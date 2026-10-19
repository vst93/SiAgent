import {withTurnTime} from "../dist/time-context.js";
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {configuredModels} from '../dist/models.js';

for(const api of ['openai-completions','openai-responses','anthropic-messages']) {
 test(`model configuration reaches ${api} wire requests`,{timeout:15000},async t=>{
  const requests=[];
  const server=createServer(async(req,res)=>{
   let raw='';for await(const chunk of req)raw+=chunk;
   requests.push({path:req.url,headers:req.headers,body:JSON.parse(raw)});
   res.writeHead(200,{'content-type':'text/event-stream'});
   const event=(type,value)=>res.write(`event: ${type}\ndata: ${JSON.stringify({type,...value})}\n\n`);
   if(api==='openai-completions'){
    res.write(`data: ${JSON.stringify({id:'fixture',object:'chat.completion.chunk',created:1,model:'fixture',choices:[{index:0,delta:{role:'assistant',content:'OK'},finish_reason:null}]})}\n\n`);
    res.write(`data: ${JSON.stringify({id:'fixture',object:'chat.completion.chunk',created:1,model:'fixture',choices:[{index:0,delta:{},finish_reason:'stop'}]})}\n\ndata: [DONE]\n\n`);
   }else if(api==='openai-responses'){
    const item={id:'msg_fixture',type:'message',role:'assistant',status:'completed',content:[{type:'output_text',text:'OK',annotations:[]}]};
    event('response.created',{response:{id:'resp_fixture',status:'in_progress',output:[]}});
    event('response.output_item.added',{output_index:0,item:{...item,status:'in_progress',content:[]}});
    event('response.output_text.delta',{output_index:0,content_index:0,delta:'OK'});
    event('response.output_item.done',{output_index:0,item});
    event('response.completed',{response:{id:'resp_fixture',status:'completed',output:[item],usage:{input_tokens:10,output_tokens:2,total_tokens:12}}});
   }else{
    event('message_start',{message:{id:'msg_fixture',type:'message',role:'assistant',model:'fixture',content:[],stop_reason:null,stop_sequence:null,usage:{input_tokens:10,output_tokens:0}}});
    event('content_block_start',{index:0,content_block:{type:'text',text:''}});
    event('content_block_delta',{index:0,delta:{type:'text_delta',text:'OK'}});
    event('content_block_stop',{index:0});
    event('message_delta',{delta:{stop_reason:'end_turn',stop_sequence:null},usage:{output_tokens:2}});
    event('message_stop',{});
   }
   res.end();
  });await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
  t.after(async()=>{server.closeAllConnections();await new Promise(resolve=>server.close(resolve))});
  const options={api,context_window:65536,max_tokens:4096,reasoning:true,thinking_level:'low',temperature:api==='anthropic-messages'?null:0.2,top_p:api==='anthropic-messages'?null:0.8,max_tokens_field:api==='openai-completions'?'max_tokens':''};
  const models=configuredModels({name:'fixture',base_url:`http://127.0.0.1:${server.address().port}${api==='anthropic-messages'?'':'/v1'}`,api_key:'test-secret',options},()=>{});
  const model=models.getModel('siagent','fixture');
  assert.equal(model.contextWindow,65536);assert.equal(model.maxTokens,4096);
  const result=await models.completeSimple(model,{messages:[{role:'user',content:withTurnTime('hello',{received_at:'2026-10-17T16:00:00Z',timezone:'Asia/Shanghai'}),timestamp:Date.now()}]});
  assert.equal(result.stopReason,'stop',result.errorMessage);
  assert(result.content.some(part=>part.type==='text'&&part.text==='OK'));
  assert.equal(requests.length,1);
  const request=requests[0];
  assert.equal(request.body.model,'fixture');
  assert.match(JSON.stringify(request.body),/2026-10-17T16:00:00Z/);
  assert.match(JSON.stringify(request.body),/Asia\/Shanghai/);
  if(api==='openai-completions'){
   assert.equal(request.path,'/v1/chat/completions');assert.equal(request.body.max_tokens,4096);assert.equal(request.body.reasoning_effort,'low');
  }else if(api==='openai-responses'){
   assert.equal(request.path,'/v1/responses');assert.equal(request.body.max_output_tokens,4096);assert.equal(request.body.reasoning.effort,'low');
  }else{
   assert.equal(new URL(request.path,'http://fixture').pathname,'/v1/messages');assert.equal(request.headers['x-api-key'],'test-secret');assert.equal(request.body.thinking.type,'enabled');assert(request.body.max_tokens<=4096);assert(request.body.thinking.budget_tokens>=1024);
  }
  if(api!=='anthropic-messages'){
   assert.equal(request.headers.authorization,'Bearer test-secret');assert.equal(request.body.temperature,0.2);assert.equal(request.body.top_p,0.8);
  }
 });
}
test('model input budget reserves output before dispatch',async()=>{
 const models=configuredModels({name:'tiny',base_url:'http://localhost:1/v1',api_key:'secret',options:{context_window:256,max_tokens:128}},()=>{});
 const result=await models.completeSimple(models.getModel('siagent','tiny'),{messages:[{role:'user',content:'x'.repeat(1024),timestamp:1}]});
 assert.equal(result.stopReason,'error');assert.match(result.errorMessage,/context_limit/);
});

test('internal envelope protection is separate from a model Token budget',async()=>{
 const models=configuredModels({name:'large',base_url:'http://localhost:1/v1',api_key:'secret',options:{context_window:10000000,max_tokens:4096}},()=>{});
 const result=await models.completeSimple(models.getModel('siagent','large'),{messages:[{role:'user',content:'x'.repeat(9*1024*1024),timestamp:1}]});
 assert.equal(result.stopReason,'error');assert.match(result.errorMessage,/request_size_limit/);
});

test('fallback model is used when the primary fails before content',{timeout:15000},async t=>{
 const primary=createServer(async(req,res)=>{for await(const _ of req){}res.writeHead(503,{'content-type':'application/json'});res.end('{"error":"down"}')});
 const fallbackHeaders=[]; const fallback=createServer(async(req,res)=>{fallbackHeaders.push(req.headers);for await(const _ of req){}res.writeHead(200,{'content-type':'text/event-stream'});res.write(`data: ${JSON.stringify({id:'fallback',object:'chat.completion.chunk',created:1,model:'backup',choices:[{index:0,delta:{role:'assistant',content:'fallback-ok'},finish_reason:null}]})}\n\ndata: ${JSON.stringify({id:'fallback',object:'chat.completion.chunk',created:1,model:'backup',choices:[{index:0,delta:{},finish_reason:'stop'}]})}\n\ndata: [DONE]\n\n`);res.end()});
 await new Promise(resolve=>primary.listen(0,'127.0.0.1',resolve));
 await new Promise(resolve=>fallback.listen(0,'127.0.0.1',resolve));
 t.after(async()=>{for(const server of [primary,fallback]){server.closeAllConnections();await new Promise(resolve=>server.close(resolve))}});
 const models=configuredModels({
  name:'primary',base_url:`http://127.0.0.1:${primary.address().port}/v1`,api_key:'primary-key',
  options:{input:['text']},
  fallbacks:[{name:'backup',base_url:`http://127.0.0.1:${fallback.address().port}/v1`,api_key:'fallback-key',options:{input:['text','image']}}],
 },()=>{});
 const model=models.getModel('siagent','primary');
 const result=await models.completeSimple(model,{messages:[{role:'user',content:'hello',timestamp:Date.now()}]});
 assert.equal(result.stopReason,'stop',result.errorMessage);
 assert(result.content.some(part=>part.type==='text'&&part.text==='fallback-ok'));
 assert.equal(model.input.includes('image'),false);
 assert.equal(fallbackHeaders[0].authorization,'Bearer fallback-key');
});
