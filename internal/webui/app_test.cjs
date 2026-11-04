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
