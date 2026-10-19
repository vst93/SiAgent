import {test} from 'node:test';
import assert from 'node:assert/strict';
import {withTurnTime} from '../dist/time-context.js';

test('turn time handles date boundaries and DST while keeping input separate',()=>{
 const shanghai=withTurnTime('今天是什么日期？',{received_at:'2026-10-17T16:00:00Z',timezone:'Asia/Shanghai'});
 assert.match(shanghai[0].text,/2026年10月18日/);
 assert.match(shanghai[0].text,/星期日/);
 assert.match(shanghai[0].text,/00:00:00/);
 assert.equal(shanghai[1].text,'今天是什么日期？');
 const ny=withTurnTime('hello',{received_at:'2026-03-08T07:30:00Z',timezone:'America/New_York'});
 assert.match(ny[0].text,/03:30:00/);
 assert.equal(withTurnTime('legacy probe'),'legacy probe');
 for(const time of [null,{}, {received_at:'now',timezone:'UTC'}, {received_at:'2026-10-17T16:00:00Z',timezone:'Mars/Olympus'}, {received_at:'2026-10-17T16:00:00Z',timezone:'Local'}]) {
  assert.throws(()=>withTurnTime('hello',time),/invalid_time_context/);
 }
});

test('turn attachments become image and extracted file content blocks',async()=>{
 const content=withTurnTime('看图',{received_at:'2026-10-17T16:00:00Z',timezone:'Asia/Shanghai'},[
  {kind:'file',name:'note.txt',mime:'text/plain',text:'附件内容'},
  {kind:'image',name:'screen.png',mime:'image/png',data:'aGVsbG8='},
 ]);
 assert(Array.isArray(content));
 const text=content.filter(part=>part.type==='text').map(part=>part.text).join('\n');
 assert.match(text,/附件内容/);
 const image=content.find(part=>part.type==='image');
 assert.equal(image.mimeType,'image/png');
 assert.equal(image.data,'aGVsbG8=');
});
