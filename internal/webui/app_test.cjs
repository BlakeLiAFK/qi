// Headless DOM contract test. This is not a browser rendering test.
const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
function setup(){
 class Node {constructor(tag){this.tag=tag;this.children=[];this.scrollHeight=0;this.scrollTop=0;this.clientHeight=0;}append(...nodes){this.children.push(...nodes);}replaceChildren(...nodes){this.children=nodes;}focus(){}}
 const nodes=new Map(),requests=[];
 const fixture={id:'one',title:'Test',status:'idle',messages:[],cards:[{id:'card-one',revision:4,title:'Choose',summary:'Review',state:'pending',actions:[{id:'approve',label:'Approve'}]}]};
 const document={getElementById(id){if(!nodes.has(id))nodes.set(id,new Node('div'));return nodes.get(id);},createElement(tag){return new Node(tag);},hidden:false};
 const context=vm.createContext({document,setInterval(){},fetch:async(path,opts)=>{requests.push({path,opts});return {ok:true,json:async()=>path==='/api/tasks'||path==='/api/skills'||(path.endsWith('/approvals')&&opts.method==='GET')?[]:fixture};}});
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
