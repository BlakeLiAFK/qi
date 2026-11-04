// Headless DOM contract test. This is not a browser rendering test.
const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
function setup(taskList=[]){
 class Node {constructor(tag){this.tag=tag;this.children=[];this.scrollHeight=0;this.scrollTop=0;this.clientHeight=0;}append(...nodes){this.children.push(...nodes);}replaceChildren(...nodes){this.children=nodes;}focus(){}}
 const nodes=new Map(),requests=[];
 const fixture={id:'one',title:'Test',status:'idle',messages:[],cards:[{id:'card-one',revision:4,title:'Choose',summary:'Review',state:'pending',actions:[{id:'approve',label:'Approve'}]}]};
 const document={getElementById(id){if(!nodes.has(id))nodes.set(id,new Node('div'));return nodes.get(id);},createElement(tag){return new Node(tag);},hidden:false};
 const context=vm.createContext({crypto:require("node:crypto").webcrypto,TextEncoder,document,setInterval(){},fetch:async(path,opts)=>{requests.push({path,opts});return {ok:true,json:async()=>path==='/api/tasks'?taskList:path==='/api/skills'||((path.endsWith('/approvals')||path.endsWith('/artifacts'))&&opts.method==='GET')?[]:{...fixture,...(taskList.find(task=>path==='/api/tasks/'+task.id)||{})}};}});
 vm.runInContext(fs.readFileSync(__dirname+'/assets/app.js','utf8'),context);
 return {nodes,context,fixture,requests};
}
test('engine pending cards enable actions; all settled and unknown states disable them',()=>{
 const {nodes,context,fixture}=setup();
 for(const state of ['pending','approved','denied','cancelled','expired','open','unknown']){
  fixture.cards[0].state=state;context.paint(fixture);
  const card=nodes.get('conversation').children[0];const button=card.children.at(-1);
  assert.equal(button.disabled,state!=='pending',state);
 }
});
test('card click submits authoritative identifiers and revision, then disables locally',async()=>{
 const {nodes,context,fixture,requests}=setup();context.paint(fixture);
 const button=nodes.get('conversation').children[0].children.at(-1);
 await button.onclick();assert.equal(button.disabled,true);
 const action=requests.find(r=>r.path.endsWith('/actions'));
 assert.ok(action);assert.equal(action.opts.method,'POST');
 assert.deepEqual(JSON.parse(action.opts.body),{card_id:'card-one',action:'approve',revision:4});
});

test('durable coding approval shows full action and submits its digest-bound decision',async()=>{
 const {nodes,context,fixture,requests}=setup();
 const payload={agent:'codex',prompt:'Change only the requested file',workspace:'/safe/workspace',executable:'/tools/codex',arguments:['exec','--sandbox','workspace-write'],timeout_seconds:60,access:'May change files and access network'};
 fixture.approvals=[{id:'approval-one',revision:3,digest:'a'.repeat(64),status:'pending',expires_at:new Date(Date.now()+60000).toISOString(),action:{kind:'coding.run',target:'/safe/workspace',payload}}];
 context.paint(fixture);const block=nodes.get('conversation').children.at(-1);
 assert.deepEqual(JSON.parse(block.children.find(x=>x.tag==='pre').textContent),payload);
 assert.ok(block.children.some(x=>String(x.textContent).includes('a'.repeat(64))));
 const allow=block.children.find(x=>x.tag==='button');assert.equal(allow.disabled,false);
 await allow.onclick();const req=requests.find(r=>r.path.endsWith('/approvals'));
 assert.deepEqual(JSON.parse(req.opts.body),{id:'approval-one',revision:3,digest:'a'.repeat(64),allow:true});
 assert.ok(block.children.filter(x=>x.tag==='button').every(x=>x.disabled));
});
test('expired or settled durable approvals cannot be clicked',()=>{
 const {nodes,context,fixture}=setup();
 for(const [status,expires] of [['pending',-1000],['approved',60000],['executing',60000],['unknown',60000]]){
  fixture.approvals=[{id:'a',revision:1,digest:'b'.repeat(64),status,expires_at:new Date(Date.now()+expires).toISOString(),action:{kind:'coding.run',target:'workspace',payload:{}}}];
  context.paint(fixture);assert.ok(nodes.get('conversation').children.at(-1).children.filter(x=>x.tag==='button').every(x=>x.disabled));
 }
});

test('skills start unchecked, explicit choices apply to only one sent message',async()=>{
 const {nodes,context,requests}=setup();await new Promise(setImmediate);
 context.paintSkills([{name:'review',description:'Untrusted guidance',source:'explicit',path:'/skills/review/SKILL.md'}]);
 const input=nodes.get('skills').children[1].children[0];assert.equal(input.checked,false);
 vm.runInContext("selected='one'",context);context.document.getElementById('prompt').value='First request';
 await nodes.get('composer').onsubmit({preventDefault(){}});
 let sent=requests.filter(r=>r.path.endsWith('/messages'));assert.deepEqual(JSON.parse(sent.at(-1).opts.body).skills,[]);
 input.checked=true;context.document.getElementById('prompt').value='Use review now';await nodes.get('composer').onsubmit({preventDefault(){}});
 sent=requests.filter(r=>r.path.endsWith('/messages'));assert.deepEqual(JSON.parse(sent.at(-1).opts.body).skills,['review']);assert.equal(input.checked,false);
 input.checked=true;nodes.get('new-task').onclick();assert.equal(input.checked,false);
});

test('latest context reduction notice explains omitted user instructions without claiming current context',()=>{
 const {nodes,context,fixture}=setup();fixture.context_notice={dropped_messages:12,input_bytes:8192,run_id:'run-one'};context.paint(fixture);
 const notice=nodes.get('conversation').children[0];const text=notice.children.map(n=>n.textContent).join(' ');
 assert.match(text,/12 earlier messages/);assert.match(text,/user instructions/);assert.match(text,/Full saved history is unchanged/);assert.match(text,/system and developer/);assert.match(text,/not a guarantee/);assert.match(text,/8192/);
});

test('task navigation isolates drafts and never sends task A text to task B',async()=>{
 const tasks=[{id:'A',title:'Task A',status:'idle'},{id:'B',title:'Task B',status:'idle'}];
 const {nodes,context,requests}=setup(tasks);await new Promise(setImmediate);
 const prompt=context.document.getElementById('prompt');
 nodes.get('tasks').children[0].onclick();await new Promise(setImmediate);
 prompt.value='Private draft belonging to task A';
 nodes.get('tasks').children[1].onclick();await new Promise(setImmediate);
 assert.equal(prompt.value,'');assert.equal(nodes.get('task-title').textContent,'Task B');
 await nodes.get('composer').onsubmit({preventDefault(){}});
 assert.equal(requests.filter(r=>r.opts.method==='POST').length,0,'blank B must not transmit A draft');
 prompt.value='Only task B message';
 await nodes.get('composer').onsubmit({preventDefault(){}});
 let sent=requests.filter(r=>r.path.endsWith('/messages'));
 assert.equal(sent.length,1);assert.equal(sent[0].path,'/api/tasks/B/messages');assert.equal(JSON.parse(sent[0].opts.body).prompt,'Only task B message');
 nodes.get('tasks').children[0].onclick();await new Promise(setImmediate);
 assert.equal(prompt.value,'Private draft belonging to task A');
 await nodes.get('composer').onsubmit({preventDefault(){}});
 sent=requests.filter(r=>r.path.endsWith('/messages'));assert.equal(sent[1].path,'/api/tasks/A/messages');assert.equal(JSON.parse(sent[1].opts.body).prompt,'Private draft belonging to task A');
 nodes.get('tasks').children[1].onclick();await new Promise(setImmediate);assert.equal(prompt.value,'','sent B draft must not return');
});

test('New task starts blank, preserves existing task draft, and cannot transmit it',async()=>{
 const {nodes,context,requests}=setup([{id:'A',title:'Task A',status:'idle'}]);await new Promise(setImmediate);
 const prompt=context.document.getElementById('prompt');nodes.get('tasks').children[0].onclick();await new Promise(setImmediate);
 prompt.value='Belongs only to A';nodes.get('new-task').onclick();await new Promise(setImmediate);
 assert.equal(prompt.value,'');await nodes.get('composer').onsubmit({preventDefault(){}});assert.equal(requests.filter(r=>r.opts.method==='POST').length,0);
 prompt.value='Unsaved new draft';nodes.get('new-task').onclick();assert.equal(prompt.value,'','New task resets unsent new-task text');
 await new Promise(setImmediate);nodes.get('tasks').children[0].onclick();await new Promise(setImmediate);assert.equal(prompt.value,'Belongs only to A');
});

test('failed send retains its own draft and pending send blocks navigation',async()=>{
 const {nodes,context,requests}=setup([{id:'A',title:'Task A',status:'idle'},{id:'B',title:'Task B',status:'idle'}]);await new Promise(setImmediate);
 const prompt=context.document.getElementById('prompt');nodes.get('tasks').children[0].onclick();await new Promise(setImmediate);prompt.value='Retry only in A';
 const originalFetch=context.fetch;let release;
 context.fetch=async(path,opts)=>{if(path.endsWith('/messages')){requests.push({path,opts});await new Promise(resolve=>{release=resolve;});return {ok:false,text:async()=> 'Temporary failure'};}return originalFetch(path,opts);};
 const pending=nodes.get('composer').onsubmit({preventDefault(){}});assert.equal(prompt.readOnly,true);
 nodes.get('tasks').children[1].onclick();nodes.get('new-task').onclick();assert.equal(vm.runInContext('selected',context),'A');
 release();await pending;assert.equal(prompt.readOnly,false);assert.equal(prompt.value,'Retry only in A');
 nodes.get('tasks').children[1].onclick();await new Promise(setImmediate);assert.equal(prompt.value,'');
 nodes.get('tasks').children[0].onclick();await new Promise(setImmediate);assert.equal(prompt.value,'Retry only in A');
});
test('question choices submit bound task/card/revision and cannot act after expiry',async()=>{
 const {nodes,context,fixture,requests}=setup();fixture.cards=[{id:'q',task_id:'one',kind:'question',revision:3,title:'Choose',summary:'Question',state:'pending',input_type:'choice',expires_at:new Date(Date.now()+60000).toISOString(),choices:[{id:'yes',label:'Yes'}]}];context.paint(fixture);
 let button=nodes.get('conversation').children[0].children.at(-1);assert.equal(button.disabled,false);await button.onclick();
 assert.deepEqual(JSON.parse(requests.find(r=>r.path.endsWith('/actions')).opts.body),{task_id:'one',card_id:'q',revision:3,action:'answer',choice_id:'yes'});
 fixture.cards[0].expires_at='2000-01-01T00:00:00Z';context.paint(fixture);button=nodes.get('conversation').children[0].children.find(c=>c.tag==='button');assert.equal(button.disabled,true);
});
test('deliverable links are task scoped, escaped and never execute filename markup',()=>{
 const {nodes,context,fixture}=setup();fixture.cards=[];fixture.artifacts=[{id:'a/b',name:'<script>bad</script>',media_type:'text/html',size:123,sha256:'abc'}];context.paint(fixture);
 const card=nodes.get('conversation').children[0];assert.equal(card.children[1].textContent,'<script>bad</script>');const link=card.children.at(-1);assert.equal(link.href,'/api/tasks/one/artifacts/a%2Fb');assert.equal(link.download,'<script>bad</script>');
});

test('question text rejects oversized multibyte answers before sending',async()=>{
 const {nodes,context,fixture,requests}=setup();fixture.cards=[{id:'q',task_id:'one',kind:'question',revision:3,title:'Input',state:'pending',input_type:'text',expires_at:new Date(Date.now()+60000).toISOString()}];context.paint(fixture);
 const card=nodes.get('conversation').children[0];const input=card.children.find(c=>c.tag==='label').children[0];input.value='你'.repeat(3000);
 await card.children.find(c=>c.tag==='button').onclick();assert.equal(requests.filter(r=>r.path.endsWith('/actions')).length,0);assert.match(nodes.get('error').textContent,/8,000 UTF-8 bytes/);
});

test('progress streams close on navigation and ignore stale or wrong-task events',async()=>{
 const {context,requests}=setup([{id:'A',title:'A',status:'running'},{id:'B',title:'B',status:'idle'}]);await new Promise(setImmediate);
 const streams=[];
 context.EventSource=class {constructor(url){this.url=url;this.listeners={};streams.push(this);}addEventListener(name,fn){this.listeners[name]=fn;}close(){this.closed=true;}};
 context.selectTask({id:'A',title:'A',status:'running'});
 assert.equal(streams[0].url,'/api/tasks/A/events');
 streams[0].listeners.progress({data:JSON.stringify({task_id:'A',event_id:1})});await new Promise(setImmediate);
 assert.ok(requests.some(r=>r.path==='/api/tasks/A'));
 context.selectTask({id:'B',title:'B',status:'idle'});assert.equal(streams[0].closed,true);
 const before=requests.length;
 streams[0].listeners.progress({data:JSON.stringify({task_id:'A',event_id:2})});
 streams[1].listeners.progress({data:JSON.stringify({task_id:'A',event_id:3})});
 streams[1].listeners.progress({data:'incomplete JSON'});await new Promise(setImmediate);
 assert.equal(requests.length,before,'stale, malformed or cross-task events must not refresh');
 streams[1].listeners.progress({data:JSON.stringify({task_id:'B',event_id:4})});await new Promise(setImmediate);
 assert.ok(requests.slice(before).some(r=>r.path==='/api/tasks/B'));
 context.selectTask(null);assert.equal(streams[1].closed,true);
});

test('answered question has a single-use explicit continuation action',async()=>{
 const {nodes,context,fixture,requests}=setup();fixture.status='completed';fixture.cards=[{id:'q',task_id:'one',run_id:'r',kind:'question',revision:2,title:'Input',state:'answered',input_type:'text',answer:{text:'saved'}}];fixture.runs=[{id:'r',status:'completed'}];context.paint(fixture);
 const button=nodes.get('conversation').children[0].children.find(c=>c.tag==='button');assert.equal(button.textContent,'Continue task');assert.equal(button.disabled,false);
 const first=button.onclick();await button.onclick();await first;
 const actions=requests.filter(r=>r.path.endsWith('/actions'));assert.equal(actions.length,1);assert.deepEqual(JSON.parse(actions[0].opts.body),{task_id:'one',card_id:'q',revision:2,action:'continue'});
 fixture.cards[0].state='continued';context.paint(fixture);assert.equal(nodes.get('conversation').children[0].children.some(c=>c.tag==='button'),false);
});

test('cancelled and historical question continuations are disabled',()=>{
 const {nodes,context,fixture}=setup();fixture.status='cancelled';fixture.cards=[{id:'q',task_id:'one',run_id:'old',kind:'question',revision:2,title:'Input',state:'answered',answer:{text:'saved'}}];context.paint(fixture);
 assert.equal(nodes.get('conversation').children[0].children.find(c=>c.tag==='button').disabled,true);
 fixture.status='completed';fixture.runs=[{id:'new'}];context.paint(fixture);assert.equal(nodes.get('conversation').children[0].children.find(c=>c.tag==='button').disabled,true);
});

test('uncertain submissions reuse keys while changed requests and known acceptance get fresh keys',()=>{
 const {context}=setup();
 const a=context.submissionKey('one','hello',[]);
 assert.equal(context.submissionKey('one','hello',[]),a);
 assert.notEqual(context.submissionKey('two','hello',[]),a);
 const b=context.submissionKey('one','hello',['review']);assert.notEqual(a,b);
 context.clearSubmission('one');assert.notEqual(context.submissionKey('one','hello',['review']),b);
});
test('submission identity survives page reload when session storage is available',()=>{
 const storage=new Map(),sessionStorage={getItem:k=>storage.get(k)||null,setItem:(k,v)=>storage.set(k,v),removeItem:k=>storage.delete(k)};
 const first=setup();first.context.sessionStorage=sessionStorage;
 const key=first.context.submissionKey('one','hello',[]);
 const second=setup();second.context.sessionStorage=sessionStorage;
 assert.equal(second.context.submissionKey('one','hello',[]),key);
 second.context.clearSubmission('one');assert.equal(storage.size,0);
});

test('live model snapshot replaces on replay and final message removes provisional text',()=>{
 const {nodes,context,fixture}=setup();fixture.cards=[];fixture.status='running';fixture.runs=[{id:'run-one',status:'running'}];
 fixture.live_text={task_id:'one',run_id:'run-one',request_id:10,revision:11,text:'<img src=x> Hello',truncated:false};
 context.paint(fixture);context.paint(fixture);
 let live=nodes.get('conversation').children.filter(n=>n.className==='message live-text');assert.equal(live.length,1);assert.equal(live[0].children[1].textContent,'<img src=x> Hello');assert.equal(live[0].children[1].children.length,0);
 fixture.live_text={...fixture.live_text,revision:12,text:'<img src=x> Hello world'};context.paint(fixture);live=nodes.get('conversation').children.filter(n=>n.className==='message live-text');assert.equal(live.length,1);assert.equal(live[0].children[1].textContent,'<img src=x> Hello world');
 fixture.status='completed';fixture.runs[0].status='completed';fixture.messages=[{role:'assistant',content:'Final answer'}];delete fixture.live_text;context.paint(fixture);assert.equal(nodes.get('conversation').children.filter(n=>n.className==='message live-text').length,0);
});
test('live snapshot rejects cross-task/run data and bounds displayed content',()=>{
 const {nodes,context,fixture}=setup();fixture.cards=[];fixture.status='running';fixture.runs=[{id:'run-one',status:'running'}];
 fixture.live_text={task_id:'other-task',run_id:'run-one',request_id:10,revision:11,text:'Private other task',truncated:false};context.paint(fixture);assert.equal(nodes.get('conversation').children.length,0);
 fixture.live_text.task_id='one';fixture.live_text.run_id='other-run';context.paint(fixture);assert.equal(nodes.get('conversation').children.length,0);
 fixture.live_text.run_id='run-one';fixture.live_text.text='x'.repeat(100000);context.paint(fixture);const node=nodes.get('conversation').children[0];assert.equal(node.children[1].textContent.length,65536);assert.match(node.children.at(-1).textContent,/limit reached/);
});

test('session logout controls are opt-in and send exact CSRF-protected scope',async()=>{
 for(const all of [false,true]){
  const {nodes,context,requests}=setup();await new Promise(setImmediate);const redirects=[];context.location={replace:path=>redirects.push(path)};
  assert.equal(nodes.get('auth-controls').hidden,true);await nodes.get('logout').onclick();assert.equal(requests.filter(r=>r.path==='/api/logout').length,0);
  const original=context.fetch;context.fetch=async(path,opts)=>{if(path==='/api/auth')return {ok:true,json:async()=>({session_auth:true})};if(path==='/api/logout'){requests.push({path,opts});return {ok:true,json:async()=>({authenticated:false})};}return original(path,opts);};
  await context.loadAuth();assert.equal(nodes.get('auth-controls').hidden,false);
  context.document.getElementById('prompt').value='Private unsent text';vm.runInContext("taskDrafts.set('A','private draft')",context);
  await nodes.get(all?'logout-all':'logout').onclick();const sent=requests.filter(r=>r.path==='/api/logout');assert.equal(sent.length,1);assert.deepEqual(JSON.parse(sent[0].opts.body),{all});assert.equal(sent[0].opts.credentials,'same-origin');assert.equal(sent[0].opts.headers['X-Qi-Request'],'1');assert.equal(sent[0].opts.headers['Content-Type'],'application/json');
  assert.deepEqual(redirects,['/login']);assert.equal(context.document.getElementById('prompt').value,'');assert.equal(vm.runInContext('taskDrafts.size',context),0);assert.equal(nodes.get('conversation').children.length,0);
 }
});
test('logout failure is not retried and does not pretend successful sign-out',async()=>{
 const {nodes,context,requests}=setup();await new Promise(setImmediate);const redirects=[];context.location={replace:path=>redirects.push(path)};vm.runInContext('sessionAuth=true',context);
 context.document.getElementById('prompt').value='Keep my draft';context.fetch=async(path,opts)=>{requests.push({path,opts});return {ok:false,status:503,text:async()=> 'Try again later'};};
 await nodes.get('logout-all').onclick();assert.equal(requests.filter(r=>r.path==='/api/logout').length,1);assert.deepEqual(redirects,[]);assert.equal(context.document.getElementById('prompt').value,'Keep my draft');assert.equal(nodes.get('logout').disabled,false);assert.equal(nodes.get('error').hidden,false);
});
test('expired session redirects only known session auth, never fabricates Basic logout',async()=>{
 for(const sessionMode of [false,true]){const {context}=setup();await new Promise(setImmediate);const redirects=[];context.location={replace:path=>redirects.push(path)};vm.runInContext('sessionAuth='+sessionMode,context);context.fetch=async()=>({ok:false,status:401,text:async()=> 'authentication required'});await assert.rejects(context.api('/api/tasks'));assert.deepEqual(redirects,sessionMode?['/login']:[]);}
});
test('late response cannot repopulate protected content after logout',async()=>{
 const {context,nodes}=setup();await new Promise(setImmediate);let resolve;context.fetch=async()=>({ok:true,json:()=>new Promise(r=>{resolve=r;})});const pending=context.api('/api/tasks');await new Promise(setImmediate);context.finishSignOut();resolve([{id:'private',title:'Private task'}]);await assert.rejects(pending,/Signed out/);assert.equal(nodes.get('tasks').children.length,0);
});

test('logout clears question/composer/submission drafts and cached task snapshot',async()=>{
 const {context,nodes,fixture}=setup();await new Promise(setImmediate);
 const question={id:'q-secret',revision:2,kind:'question',input_type:'text',title:'Private question',summary:'Sensitive answer',state:'pending',expires_at:new Date(Date.now()+60000).toISOString()};
 const panel=context.document.getElementById('conversation');context.paintQuestion(fixture,question,panel);
 const block=panel.children.at(-1);const label=block.children.find(n=>n.tag==='label');const input=label.children[0];input.value='Private draft answer';input.oninput();
 vm.runInContext("taskDrafts.set('one','private composer');pendingSubmissions.set('one',{signature:'private request',key:'id'});version='private snapshot'",context);
 assert.equal(vm.runInContext('questionDrafts.size',context),1);context.finishSignOut();
 for(const name of ['questionDrafts','taskDrafts','pendingSubmissions'])assert.equal(vm.runInContext(name+'.size',context),0,name);
 assert.equal(vm.runInContext('version',context),'');assert.equal(nodes.get('conversation').children.length,0);assert.equal(nodes.get('skills').children.length,0);
 input.value='Late detached input';input.oninput();assert.equal(vm.runInContext('questionDrafts.size',context),0,'late input cannot restore secret draft');
});

function browserStorage(){const data=new Map();return {get length(){return data.size;},key:i=>Array.from(data.keys())[i]||null,getItem:k=>data.get(k)||null,setItem:(k,v)=>data.set(k,String(v)),removeItem:k=>data.delete(k)};}
function creationServer({loseCreate=0,loseMessage=0}={}){
 const creations=[],messages=[],tasks=new Map(),runs=new Set();
 async function fetch(path,opts){
  if(path==='/api/tasks'&&opts.method==='POST'){
   const body=JSON.parse(opts.body);creations.push(body);assert.ok(body.idempotency_key,'task creation needs stable key');
   if(!tasks.has(body.idempotency_key))tasks.set(body.idempotency_key,{id:'created-'+tasks.size,title:body.title,status:'idle',messages:[],cards:[],runs:[]});
   if(loseCreate-->0)throw new Error('Creation reply lost');
   return {ok:true,json:async()=>tasks.get(body.idempotency_key)};
  }
  if(path.endsWith('/messages')&&opts.method==='POST'){
   const body=JSON.parse(opts.body);messages.push({path,body});runs.add(path+'|'+body.idempotency_key);
   if(loseMessage-->0)throw new Error('Message reply lost');return {ok:true,json:async()=>({accepted:true})};
  }
  return {ok:true,json:async()=>path==='/api/auth'?{session_auth:false}:path==='/api/tasks'?Array.from(tasks.values()):path==='/api/skills'||path.endsWith('/approvals')||path.endsWith('/artifacts')?[]:Array.from(tasks.values()).find(t=>path==='/api/tasks/'+t.id)};
 }
 return {fetch,creations,messages,tasks,runs};
}
test('lost task-creation response waits for explicit retry and reuses one stable key',async()=>{
 const server=creationServer({loseCreate:1});const {context,nodes}=setup();await new Promise(setImmediate);context.fetch=server.fetch;context.sessionStorage=browserStorage();
 const prompt=context.document.getElementById('prompt');prompt.value='Create exactly one task';
 await nodes.get('composer').onsubmit({preventDefault(){}});assert.equal(server.creations.length,1);assert.equal(server.messages.length,0);assert.equal(server.tasks.size,1);
 await nodes.get('composer').onsubmit({preventDefault(){}});assert.equal(server.creations.length,2);assert.equal(server.creations[0].idempotency_key,server.creations[1].idempotency_key);assert.equal(server.tasks.size,1);assert.equal(server.runs.size,1);
 nodes.get('new-task').onclick();await new Promise(setImmediate);prompt.value='Create exactly one task';await nodes.get('composer').onsubmit({preventDefault(){}});assert.equal(server.tasks.size,2,'accepted completion must permit intentional new task');
});
test('creation identity and first-message identity survive reload after uncertain message acceptance',async()=>{
 const server=creationServer({loseMessage:1}),storage=browserStorage();const first=setup();await new Promise(setImmediate);first.context.fetch=server.fetch;first.context.sessionStorage=storage;first.context.document.getElementById('prompt').value='Same explicit request';
 await first.nodes.get('composer').onsubmit({preventDefault(){}});assert.equal(server.creations.length,1);assert.equal(server.messages.length,1);assert.equal(server.runs.size,1);
 const second=setup();await new Promise(setImmediate);second.context.fetch=server.fetch;second.context.sessionStorage=storage;second.context.document.getElementById('prompt').value='Same explicit request';
 await second.nodes.get('composer').onsubmit({preventDefault(){}});assert.equal(server.tasks.size,1);assert.equal(server.creations[0].idempotency_key,server.creations[1].idempotency_key);assert.equal(server.messages[0].body.idempotency_key,server.messages[1].body.idempotency_key);assert.equal(server.runs.size,1,'lost acceptance must not execute a second run');
});
test('changed new-task content does not reuse an uncertain key merely because title matches',async()=>{
 const server=creationServer({loseCreate:1});const {context,nodes}=setup();await new Promise(setImmediate);context.fetch=server.fetch;context.sessionStorage=browserStorage();const prefix='x'.repeat(40);context.document.getElementById('prompt').value=prefix+' first';
 await nodes.get('composer').onsubmit({preventDefault(){}});context.document.getElementById('prompt').value=prefix+' different';await nodes.get('composer').onsubmit({preventDefault(){}});
 assert.equal(server.creations[0].title,server.creations[1].title);assert.notEqual(server.creations[0].idempotency_key,server.creations[1].idempotency_key);
});

test('creation record is task-bound, full-request-bound and cleared on logout',()=>{
 const {context}=setup();const storage=browserStorage();context.sessionStorage=storage;
 const key=context.submissionKey('__create__','same prompt',['review']);context.bindCreation('A');context.clearCreationFor('B');assert.equal(context.creationRecord().key,key);
 const changed=context.submissionKey('__create__','same prompt',[]);assert.notEqual(changed,key);context.bindCreation('A');context.clearCreationFor('A');assert.equal(context.creationRecord(),null);
 context.submissionKey('__create__','private draft',[]);context.bindCreation('C');context.finishSignOut();assert.equal(storage.getItem('qi-submission:__create__'),null);assert.equal(vm.runInContext('pendingSubmissions.size',context),0);
});

function archiveServer(task){const calls=[];return {calls,fetch:async(path,opts)=>{
 calls.push({path,opts});if(path.endsWith('/archive')&&opts.method==='POST'){if(['running','queued'].includes(task.status))return {ok:false,status:409,text:async()=> 'active task'};task.archived=JSON.parse(opts.body).archived;return {ok:true,json:async()=>({archived:task.archived})};}
 return {ok:true,json:async()=>path==='/api/tasks'?task.archived?[]:[task]:path==='/api/tasks?archived=true'?task.archived?[task]:[]:path.endsWith('/approvals')||path.endsWith('/artifacts')||path==='/api/skills'?[]:task};
}};}
test('archive and restore use explicit task scope, preserve draft, and disable archived sending',async()=>{
 const task={id:'A',title:'Task A',status:'completed',archived:false,messages:[{role:'assistant',content:'Saved answer'}],cards:[],runs:[]};
 const {context,nodes}=setup();await new Promise(setImmediate);const server=archiveServer(task);context.fetch=server.fetch;await context.refresh();nodes.get('tasks').children[0].onclick();await new Promise(setImmediate);
 const prompt=context.document.getElementById('prompt');prompt.value='Draft belonging to A';await nodes.get('archive-task').onclick();
 const action=server.calls.find(c=>c.path==='/api/tasks/A/archive');assert.deepEqual(JSON.parse(action.opts.body),{archived:true});assert.equal(nodes.get('archive-task').textContent,'Restore task');assert.equal(prompt.value,'Draft belonging to A');assert.equal(prompt.readOnly,true);assert.equal(nodes.get('send').disabled,true);assert.equal(nodes.get('show-archived').checked,true);
 await nodes.get('composer').onsubmit({preventDefault(){}});assert.equal(server.calls.some(c=>c.path.endsWith('/messages')),false);
 assert.ok(server.calls.some(c=>c.path==='/api/tasks?archived=true'));assert.ok(nodes.get('conversation').children.some(n=>n.className==='message assistant'));
 await nodes.get('archive-task').onclick();assert.equal(task.archived,false);assert.equal(prompt.value,'Draft belonging to A');assert.equal(prompt.readOnly,false);assert.equal(nodes.get('show-archived').checked,false);
 const posts=server.calls.filter(c=>c.opts.method==='POST');assert.equal(posts.length,2);assert.deepEqual(JSON.parse(posts[1].opts.body),{archived:false});
});
test('archive filter clears composer and preserves drafts without crossing tasks; active tasks cannot archive',async()=>{
 const task={id:'A',title:'Task A',status:'running',archived:false,messages:[],cards:[],runs:[]};const {context,nodes}=setup();await new Promise(setImmediate);const server=archiveServer(task);context.fetch=server.fetch;await context.refresh();nodes.get('tasks').children[0].onclick();await new Promise(setImmediate);
 const prompt=context.document.getElementById('prompt');prompt.value='A only';assert.equal(nodes.get('archive-task').disabled,true);await nodes.get('archive-task').onclick();assert.equal(server.calls.some(c=>c.path.endsWith('/archive')),false);
 nodes.get('show-archived').checked=true;nodes.get('show-archived').onchange();await new Promise(setImmediate);assert.equal(prompt.value,'');assert.equal(nodes.get('tasks').children.length,0);
 nodes.get('show-archived').checked=false;nodes.get('show-archived').onchange();await new Promise(setImmediate);nodes.get('tasks').children[0].onclick();await new Promise(setImmediate);assert.equal(prompt.value,'A only');
});

async function memoryPickerFixture(){
 const state=setup();await new Promise(setImmediate);
 const {context,requests}=state,original=context.fetch;
 state.records=[{id:'fact-one',revision:3,content:'<script>fact</script>',source_ref:'user supplied note'},{id:'fact-two',revision:1,content:'Second fact',source_ref:'manual'}];
 context.fetch=async(path,opts)=>{if(path==='/api/memory-destination'){requests.push({path,opts});return {ok:true,json:async()=>({id:'dest-one',provider:'Example provider',model:'example-model',endpoint:'https://provider.example/v1'})};}if(path.startsWith('/api/memory?')){requests.push({path,opts});return {ok:true,json:async()=>state.records};}return original(path,opts);};
 state.open=async(scope='task')=>{context.document.getElementById('memory-picker').open=true;await context.document.getElementById('memory-picker').ontoggle();context.document.getElementById('turn-memory-scope').value=scope;await context.loadTurnMemory();};
 state.inputs=()=>state.nodes.get('turn-memory-list').children.filter(n=>n.tag==='label').map(n=>n.children[0]);
 return state;
}
test('memory requires explicit opening and scope; safe content, source and revision shown unchecked',async()=>{
 const s=await memoryPickerFixture(),{context,nodes,requests}=s;
 context.selectTask({id:'one',title:'One',status:'idle'});await context.refresh();
 assert.equal(requests.some(r=>r.path.startsWith('/api/memory?')),false);
 nodes.get('memory-picker').open=true;nodes.get('memory-picker').ontoggle();
 assert.equal(requests.some(r=>r.path.startsWith('/api/memory?')),false,'opening does not choose a scope');
 await s.open();assert.equal(requests.find(r=>r.path.startsWith('/api/memory?')).path,'/api/memory?task_id=one');
 assert.ok(s.inputs().every(i=>i.checked===false));
 const text=nodes.get('turn-memory-list').children[0].children[1].children;
 assert.equal(text[1].textContent,'<script>fact</script>');assert.equal(text[1].children.length,0);
 assert.match(text[2].textContent,/user supplied note.*Revision: 3.*fact-one/);
 await s.open('global');assert.equal(requests.filter(r=>r.path.startsWith('/api/memory?')).at(-1).path,'/api/memory?global=true');
 const html=fs.readFileSync(__dirname+'/assets/index.html','utf8');assert.match(html,/configured model provider for this turn only/);assert.match(html,/not instructions or permission to run tools/);
});
test('memory disclosure is explicit, selection changes invalidate consent, and acceptance resets all choices',async()=>{
 const s=await memoryPickerFixture(),{context,nodes,requests}=s;context.selectTask({id:'one',title:'One',status:'idle'});await s.open();
 context.document.getElementById('prompt').value='Use selected facts';s.inputs()[0].checked=true;s.inputs()[0].onchange();
 await nodes.get('composer').onsubmit({preventDefault(){}});assert.equal(requests.some(r=>r.path.endsWith('/messages')),false);assert.match(nodes.get('error').textContent,/Confirm disclosure/);
 nodes.get('turn-memory-consent').checked=true;s.inputs()[1].checked=true;s.inputs()[1].onchange();assert.equal(nodes.get('turn-memory-consent').checked,false);
 s.inputs()[1].checked=false;s.inputs()[1].onchange();nodes.get('turn-memory-consent').checked=true;
 await nodes.get('composer').onsubmit({preventDefault(){}});
 const sent=JSON.parse(requests.find(r=>r.path.endsWith('/messages')).opts.body);
 assert.deepEqual(sent.memory,[{id:'fact-one',revision:3,global:false}]);assert.equal(sent.memory_disclosed,true);
 assert.equal(nodes.get('turn-memory-consent').checked,false);assert.equal(nodes.get('turn-memory-list').children.length,0);assert.equal(nodes.get('memory-picker').open,false);
 context.document.getElementById('prompt').value='Next message';await nodes.get('composer').onsubmit({preventDefault(){}});
 const next=JSON.parse(requests.filter(r=>r.path.endsWith('/messages')).at(-1).opts.body);assert.equal(next.memory,undefined);assert.equal(next.memory_disclosed,undefined);
});
test('memory lost acceptance retries identical payload/key and changed revision gets a different identity',async()=>{
 const s=await memoryPickerFixture(),{context,nodes,requests}=s;context.selectTask({id:'one',title:'One',status:'idle'});await s.open('global');
 s.inputs()[0].checked=true;nodes.get('turn-memory-consent').checked=true;context.document.getElementById('prompt').value='Use this fact';
 const original=context.fetch;let failures=1;context.fetch=async(path,opts)=>{if(path.endsWith('/messages')&&failures-->0){requests.push({path,opts});throw new Error('Lost response');}return original(path,opts);};
 await nodes.get('composer').onsubmit({preventDefault(){}});assert.equal(s.inputs()[0].checked,true);assert.equal(nodes.get('turn-memory-consent').checked,true);
 await nodes.get('composer').onsubmit({preventDefault(){}});const sent=requests.filter(r=>r.path.endsWith('/messages'));assert.equal(sent[0].opts.body,sent[1].opts.body);
 assert.equal(JSON.parse(sent[0].opts.body).memory[0].global,true);
 const a=context.submissionKey('one','same',[],[{id:'f',revision:1,global:false}]);
 assert.notEqual(a,context.submissionKey('one','same',[],[{id:'f',revision:2,global:false}]));
 assert.notEqual(a,context.submissionKey('one','same',[],[{id:'f',revision:1,global:true}]));
});
test('scope changes, close, navigation and logout clear memory consent and ignore late lists',async()=>{
 const s=await memoryPickerFixture(),{context,nodes}=s;context.selectTask({id:'one',title:'One',status:'idle'});await s.open();s.inputs()[0].checked=true;nodes.get('turn-memory-consent').checked=true;
 await s.open('global');assert.equal(nodes.get('turn-memory-consent').checked,false);assert.ok(s.inputs().every(i=>!i.checked));
 nodes.get('memory-picker').open=false;nodes.get('memory-picker').ontoggle();assert.equal(nodes.get('turn-memory-list').children.length,0);
 await s.open();let resolve;context.fetch=async()=>({ok:true,json:()=>new Promise(r=>{resolve=r;})});
 const pending=context.loadTurnMemory();await new Promise(setImmediate);context.selectTask({id:'two',title:'Two',status:'idle'});resolve(s.records);await pending;
 assert.equal(nodes.get('turn-memory-list').children.length,0);assert.equal(nodes.get('turn-memory-scope').value,'');assert.equal(nodes.get('turn-memory-consent').checked,false);
 // A stale response after sign-out must not reveal records either.
 nodes.get('memory-picker').open=true;vm.runInContext("turnMemoryDestination={id:'dest'}",context);nodes.get('turn-memory-scope').value='global';const late=context.loadTurnMemory();await new Promise(setImmediate);context.finishSignOut();resolve(s.records);await late;assert.equal(nodes.get('turn-memory-list').children.length,0);
});
test('new tasks only list global records, expired records are disabled and oversized selections blocked',async()=>{
 const s=await memoryPickerFixture(),{context,nodes,requests}=s;await s.open();assert.equal(requests.some(r=>r.path.startsWith('/api/memory?')),false);assert.equal(nodes.get('turn-memory-task').disabled,true);
 s.records=[{id:'old',revision:1,content:'Expired',expires_at:'2000-01-01T00:00:00Z'}];await s.open('global');assert.equal(s.inputs()[0].disabled,true);
 s.records=Array.from({length:33},(_,i)=>({id:'f'+i,revision:1,content:'Fact'}));await s.open('global');for(const input of s.inputs())input.checked=true;nodes.get('turn-memory-consent').checked=true;context.document.getElementById('prompt').value='Too many facts';await nodes.get('composer').onsubmit({preventDefault(){}});assert.equal(requests.some(r=>r.opts.method==='POST'),false);assert.match(nodes.get('error').textContent,/at most 32/);
});
test('past memory snapshot is collapsed, safe, revision-scoped and never selected for new runs',()=>{
 const {context,nodes,fixture,requests}=setup();fixture.cards=[];fixture.memory_selections={'run-old':[{id:'fact',revision:2,content:'<img src=x>',source_ref:'saved note',scope:{global:true}}]};
 context.paint(fixture);const audit=nodes.get('conversation').children[0];assert.equal(audit.tag,'details');assert.notEqual(audit.open,true);assert.match(audit.children[0].textContent,/run-old/);assert.match(audit.children[1].textContent,/not confirmation of provider delivery/);assert.equal(audit.children[3].textContent,'<img src=x>');assert.equal(audit.children[3].children.length,0);assert.match(audit.children[4].textContent,/Global.*saved note.*Revision: 2/);assert.equal(vm.runInContext('turnMemoryControls.length',context),0);assert.equal(requests.some(r=>r.path.startsWith('/api/memory?')),false);
 const html=fs.readFileSync(__dirname+'/assets/index.html','utf8');assert.match(html,/audit snapshot, even after a saved record is deleted/);assert.match(html,/Model replies may quote facts into ordinary conversation history/);
});
test('memory destination is reviewed explicitly, payload-bound and refreshed only on picker opening',async()=>{
 const s=await memoryPickerFixture(),{context,nodes,requests}=s;context.selectTask({id:'one',title:'One',status:'idle'});
 assert.equal(requests.some(r=>r.path==='/api/memory-destination'),false);await s.open('global');
 assert.match(nodes.get('turn-memory-destination').textContent,/Example provider.*example-model.*https:\/\/provider.example\/v1/);
 s.inputs()[0].checked=true;nodes.get('turn-memory-consent').checked=true;context.document.getElementById('prompt').value='Selected';await context.refresh();
 assert.equal(requests.filter(r=>r.path==='/api/memory-destination').length,1);
 await nodes.get('composer').onsubmit({preventDefault(){}});const sent=JSON.parse(requests.find(r=>r.path.endsWith('/messages')).opts.body);assert.equal(sent.memory_destination,'dest-one');
 const selections=[{id:'f',revision:1,global:true}];assert.notEqual(context.submissionKey('one','test',[],selections,'old'),context.submissionKey('one','test',[],selections,'new'));
 await s.open('global');s.inputs()[0].checked=true;nodes.get('turn-memory-consent').checked=true;
 const original=context.fetch;context.fetch=async(path,opts)=>path==='/api/memory-destination'?{ok:true,json:async()=>({id:'dest-two',provider:'Changed provider',model:'changed-model',endpoint:'https://changed.example/v1'})}:original(path,opts);
 nodes.get('memory-picker').open=false;nodes.get('memory-picker').ontoggle();await s.open('global');assert.match(nodes.get('turn-memory-destination').textContent,/Changed provider.*changed-model.*changed.example/);assert.equal(nodes.get('turn-memory-consent').checked,false);assert.ok(s.inputs().every(i=>!i.checked));
});
test('missing or late destination fails closed and cannot repopulate another task',async()=>{
 const s=await memoryPickerFixture(),{context,nodes,requests}=s;context.selectTask({id:'one',title:'One',status:'idle'});const original=context.fetch;
 context.fetch=async(path,opts)=>path==='/api/memory-destination'?{ok:true,json:async()=>({id:'incomplete',provider:'Unknown'})}:original(path,opts);
 await s.open('global');assert.equal(nodes.get('turn-memory').disabled,true);assert.equal(s.inputs().length,0);assert.equal(requests.some(r=>r.path.startsWith('/api/memory?')),false);assert.match(nodes.get('turn-memory-destination').textContent,/Cannot verify/);
 let resolve;context.fetch=async()=>({ok:true,json:()=>new Promise(r=>{resolve=r;})});nodes.get('memory-picker').open=true;const pending=nodes.get('memory-picker').ontoggle();await new Promise(setImmediate);context.selectTask({id:'two',title:'Two',status:'idle'});resolve({id:'late',provider:'Late',model:'late',endpoint:'https://late.example'});await pending;assert.equal(nodes.get('turn-memory-destination').textContent,'');assert.equal(nodes.get('turn-memory').disabled,true);assert.equal(vm.runInContext('turnMemoryDestination',context),null);
});
test('audit shows original provider destination and deletion text explains accepted snapshots',()=>{
 const {context,nodes,fixture}=setup();fixture.cards=[];fixture.memory_selections={r:[{id:'f',revision:1,content:'Fact',source_ref:'note',scope:{global:true}}]};fixture.memory_destinations={r:{id:'old-dest',provider:'Original provider',model:'Original model',endpoint:'https://original.example'}};context.paint(fixture);
 assert.match(nodes.get('conversation').children[0].children[2].textContent,/Original provider.*Original model.*https:\/\/original.example/);
 for(const name of ['index.html','memory.html']){const html=fs.readFileSync(__dirname+'/assets/'+name,'utf8');assert.match(html,/Accepted queued or running snapshots survive source edits and deletion/);assert.match(html,/[Cc]ancel the task to stop pending disclosure/);}
 assert.match(fs.readFileSync(__dirname+'/assets/memory.html','utf8'),/Saving alone never sends/);
});
