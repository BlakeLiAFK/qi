// Headless DOM contract tests, not a browser layout test.
const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const tick=()=>new Promise(setImmediate);
async function setup(){
 class Node{
  constructor(tag){this.tag=tag;this.children=[];this.scrollHeight=0;this.scrollTop=0;this.clientHeight=0;this.value='';}
  append(...nodes){this.children.push(...nodes);}
  replaceChildren(...nodes){this.children=nodes;}
  focus(){}
 }
 const nodes=new Map(),requests=[];
 const task={id:'A',title:'<img src=x onerror=alert(1)>',status:'completed',unread_result:true,unread_run_id:'r1',terminal_run_id:'r1',runs:[{id:'r1',status:'completed'}],messages:[{role:'assistant',content:'Finished answer'}]};
 const state={task,requests,nodes,failPath:null,hook:null,ackFailures:0};
 const document={hidden:false,visibilityState:'visible',getElementById(id){if(!nodes.has(id))nodes.set(id,new Node('div'));return nodes.get(id);},createElement(tag){return new Node(tag);}};
 const fetch=async(path,opts)=>{
  requests.push({path,opts});
  if(state.hook)await state.hook(path);
  if(state.failPath===path)throw Error('Fetch failed');
  if(path.endsWith('/read-result')){
   if(state.ackFailures>0){state.ackFailures--;throw Error('ACK failed');}
   assert.equal(nodes.get('conversation').children[0].children[1].textContent,'Finished answer','complete answer must be painted before POST');
   return {ok:true,json:async()=>({ok:true})};
  }
  const value=path==='/api/tasks'||path==='/api/tasks?archived=true'?[structuredClone(task)]:path==='/api/auth'?{}:path==='/api/skills'||path.endsWith('/approvals')||path.endsWith('/artifacts')?[]:structuredClone(task);
  return {ok:true,json:async()=>value};
 };
 const context=vm.createContext({crypto:require('node:crypto').webcrypto,TextEncoder,document,setInterval(){},fetch});
 vm.runInContext(fs.readFileSync(__dirname+'/assets/app.js','utf8'),context);
 await tick();
 return Object.assign(state,{context,document,acks:()=>requests.filter(r=>r.path.endsWith('/read-result')),open:async()=>{context.selectTask(task);await context.refresh();}});
}
test('list only shows safe lightweight badge alongside unchanged status; list fetch never acknowledges',async()=>{
 const s=await setup();assert.equal(s.acks().length,0);
 const button=s.nodes.get('tasks').children[0];
 assert.equal(button.textContent,s.task.title);assert.equal(button.children[0].textContent,'completed');
 assert.equal(button.children[1].textContent,'New result');assert.equal(button.children[1].children.length,0);
 s.task.unread_result=false;await s.context.refresh();assert.equal(s.nodes.get('tasks').children[0].children.length,1);
});
test('visible selected full detail acknowledges exact terminal run once; newer result is separately acknowledged',async()=>{
 const s=await setup();await s.open();
 assert.equal(s.acks().length,1);assert.deepEqual(JSON.parse(s.acks()[0].opts.body),{run_id:'r1'});
 assert.equal(s.acks()[0].opts.method,'POST');assert.equal(s.acks()[0].opts.headers['X-Qi-Request'],'1');
 await s.context.refresh();await s.context.refresh();assert.equal(s.acks().length,1);
 s.context.selectTask(null);await s.open();assert.equal(s.acks().length,1);
 s.task.terminal_run_id=s.task.unread_run_id='r2';s.task.runs.push({id:'r2',status:'failed'});s.task.status='failed';
 await s.context.refresh();assert.equal(s.acks().length,2);assert.deepEqual(JSON.parse(s.acks()[1].opts.body),{run_id:'r2'});
});
test('hidden document does not acknowledge; later visible successful paint does',async()=>{
 for(const property of ['hidden','visibilityState']){
  const s=await setup();s.document[property]=property==='hidden'?true:'hidden';await s.open();assert.equal(s.acks().length,0);
  s.document.hidden=false;s.document.visibilityState='visible';await s.context.refresh();assert.equal(s.acks().length,1);
 }
});
test('failed detail, approvals and artifacts fetches never acknowledge',async()=>{
 for(const path of ['/api/tasks/A','/api/tasks/A/approvals','/api/tasks/A/artifacts']){
  const s=await setup();s.failPath=path;await s.open();assert.equal(s.acks().length,0,path);
  s.failPath=null;await s.context.refresh();assert.equal(s.acks().length,1,path);
 }
});
test('failed paint is not cached as successful; same snapshot must fully repaint before acknowledgment',async()=>{
 const s=await setup();const original=s.context.paintArtifacts;let paints=0;
 s.context.paintArtifacts=()=>{paints++;throw Error('Paint failed');};
 await s.open();await s.context.refresh();assert.equal(paints,2);assert.equal(s.acks().length,0);
 s.context.paintArtifacts=original;await s.context.refresh();assert.equal(s.acks().length,1);
});
test('stale navigation including A to B to A invalidates fetched snapshot',async()=>{
 for(const returnToA of [false,true]){
  const s=await setup();s.context.selectTask(s.task);let release;
  s.hook=path=>path==='/api/tasks/A/artifacts'?new Promise(resolve=>{release=resolve;}):undefined;
  const pending=s.context.refresh();await tick();
  s.context.selectTask({id:'B',title:'B',status:'idle'});if(returnToA)s.context.selectTask(s.task);
  release();await pending;assert.equal(s.acks().length,0);assert.equal(s.nodes.get('conversation').children.length,0);
  s.hook=null;if(returnToA){await s.context.refresh();assert.equal(s.acks().length,1);}
 }
});
test('visibility changing during supporting fetch prevents acknowledgment',async()=>{
 const s=await setup();s.hook=path=>{if(path.endsWith('/artifacts'))s.document.hidden=true;};await s.open();assert.equal(s.acks().length,0);
});
test('only exact matching terminal runs qualify, never queued/running or missing snapshots',async()=>{
 for(const mutate of [
  t=>{t.runs=[];},t=>{t.runs[0].id='other';},t=>{t.terminal_run_id='other';},
  t=>{delete t.terminal_run_id;},t=>{t.unread_run_id='later';},t=>{t.unread_result=false;},
  t=>{t.runs[0].status='running';},t=>{t.runs[0].status='queued';},
  t=>{t.runs[0].status='waiting_for_input';},t=>{t.status='running';},t=>{t.status='queued';}
 ]){
  const s=await setup();mutate(s.task);await s.open();assert.equal(s.acks().length,0);
 }
 for(const status of ['completed','failed','cancelled','interrupted']){
  const s=await setup();s.task.status=status;s.task.runs[0].status=status;await s.open();assert.equal(s.acks().length,1,status);
 }
});
test('failed acknowledgment retries later, successful retry stops duplicate requests',async()=>{
 const s=await setup();s.ackFailures=1;await s.open();assert.equal(s.acks().length,1);
 await s.context.refresh();assert.equal(s.acks().length,2);await s.context.refresh();assert.equal(s.acks().length,2);
});
test('result finishing after fetched snapshot never changes the exact acknowledged run',async()=>{
 const s=await setup();s.hook=path=>{if(path.endsWith('/artifacts')){s.task.unread_run_id=s.task.terminal_run_id='r2';s.task.runs.push({id:'r2',status:'completed'});}};
 await s.open();assert.deepEqual(JSON.parse(s.acks()[0].opts.body),{run_id:'r1'});
 s.hook=null;await s.context.refresh();assert.deepEqual(JSON.parse(s.acks()[1].opts.body),{run_id:'r2'});
});
test('waiting for input acknowledges displayed completed question result but never an outstanding run',async()=>{
 const s=await setup();s.task.status='waiting_for_input';
 s.task.cards=[{id:'q',kind:'question',title:'Choose',summary:'What next?',state:'pending',input_type:'choice',choices:[{id:'yes',label:'Yes'}],expires_at:'2099-01-01T00:00:00Z'}];
 await s.open();assert.equal(s.acks().length,1);
 assert.ok(s.nodes.get('conversation').children.some(node=>node.className==='card question'));
 for(const runStatus of ['queued','running']){
  const blocked=await setup();blocked.task.status='waiting_for_input';blocked.task.runs.push({id:'next',status:runStatus});
  await blocked.open();assert.equal(blocked.acks().length,0,runStatus);
 }
 const approval=await setup();approval.task.status='waiting_for_approval';await approval.open();assert.equal(approval.acks().length,0);
});
test('detail response for a different task is neither painted nor acknowledged',async()=>{
 const s=await setup();const original=s.context.fetch;
 s.context.fetch=async(path,opts)=>path==='/api/tasks/A'?{ok:true,json:async()=>({...s.task,id:'B',title:'Other private task'})}:original(path,opts);
 await s.open();assert.equal(s.acks().length,0);assert.equal(s.nodes.get('conversation').children.length,0);
 assert.equal(s.nodes.get('task-title').textContent,s.task.title);
});
