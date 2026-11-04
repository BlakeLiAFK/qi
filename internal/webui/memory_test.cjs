const test=require('node:test'),assert=require('node:assert/strict'),vm=require('node:vm'),fs=require('node:fs');
function setup(){
 class Node{constructor(tag){this.tag=tag;this.children=[];this.value='';this.checked=false;this.selectedOptions=[{textContent:'Task one'}];}append(...n){this.children.push(...n);}replaceChildren(...n){this.children=n;}}
 const nodes=new Map(),calls=[],listeners={};let handler=async()=>({ok:true,status:200,json:async()=>[]});
 const context=vm.createContext({AbortController,setTimeout,clearTimeout,addEventListener:(name,fn)=>{listeners[name]=fn;},document:{getElementById(id){if(!nodes.has(id))nodes.set(id,new Node('div'));return nodes.get(id);},createElement(tag){return new Node(tag);}},fetch:async(path,opts)=>{calls.push({path,opts});return handler(path,opts);},confirm:()=>true});
 vm.runInContext(fs.readFileSync(__dirname+'/assets/memory.js','utf8'),context);
 return {nodes,context,calls,listeners,setHandler:h=>handler=h};
}
const record={id:'a'.repeat(32),revision:1,content:'Use examples',source_ref:'Owner entry',kind:'preference',confidence:1,confidentiality:'private',approval_ref:'web-owner:fixture'};
test('scope starts empty, selection clears edited content and unchecked approval',async()=>{
 const s=setup();await new Promise(setImmediate);assert.equal(s.calls.filter(c=>c.path.startsWith('/api/memory')).length,0);
 s.nodes.get('memory-scope').value='task:one';s.setHandler(async()=>({ok:true,json:async()=>[record]}));await s.context.reload();
 const card=s.nodes.get('memory-list').children[0];card.children.find(n=>n.textContent==='Edit').onclick();assert.equal(s.nodes.get('memory-content').value,record.content);
 s.nodes.get('memory-confirm').checked=true;s.nodes.get('memory-source').oninput();assert.equal(s.nodes.get('memory-confirm').checked,false);
 s.nodes.get('memory-scope').value='global';await s.context.reload();assert.equal(s.nodes.get('memory-content').value,'');assert.equal(s.nodes.get('memory-confirm').checked,false);
});
test('cancel clears edit, repeated save blocked, request bound to selected scope',async()=>{
 const s=setup();await new Promise(setImmediate);s.nodes.get('memory-scope').value='task:one';await s.context.reload();
 s.nodes.get('memory-content').value='Keep';s.nodes.get('memory-source').value='Owner';s.nodes.get('memory-confirm').checked=true;
 let resolve;s.setHandler(async(path,opts)=>opts.method==='POST'?new Promise(r=>resolve=r):({ok:true,json:async()=>[]}));
 const send=s.nodes.get('memory-form').onsubmit({preventDefault(){}});await s.nodes.get('memory-form').onsubmit({preventDefault(){}});
 const posts=s.calls.filter(c=>c.opts.method==='POST');assert.equal(posts.length,1);assert.equal(posts[0].path,'/api/memory?task_id=one');const body=JSON.parse(posts[0].opts.body);assert.equal(body.confirm,true);assert.equal(body.non_sensitive,true);assert.equal(body.fact.content,'Keep');assert.equal(body.fact.approval_ref,undefined);
 resolve({ok:true,json:async()=>record});await send;assert.equal(s.nodes.get('memory-confirm').checked,false);assert.equal(s.nodes.get('memory-content').value,'');
 s.nodes.get('memory-content').value='Cancel me';s.nodes.get('memory-cancel').onclick();assert.equal(s.nodes.get('memory-content').value,'');
});
test('late previous scope response is discarded and authentication expiry clears facts',async()=>{
 const s=setup();await new Promise(setImmediate);s.nodes.get('memory-scope').value='task:one';let resolve;
 s.setHandler(async path=>path.includes('task_id=one')?new Promise(r=>resolve=r):({ok:true,json:async()=>[]}));const pending=s.context.reload();
 s.nodes.get('memory-scope').value='global';await s.context.reload();resolve({ok:true,json:async()=>[record]});await pending;assert.equal(s.nodes.get('memory-list').children.length,0);
 s.setHandler(async()=>({ok:true,json:async()=>[record]}));await s.context.reload();assert.equal(s.nodes.get('memory-list').children.length,1);
 s.setHandler(async()=>({ok:false,status:401,text:async()=>'authentication required'}));await s.context.reload();assert.equal(s.nodes.get('memory-list').children.length,0);assert.equal(s.nodes.get('memory-form').hidden,true);assert.equal(s.nodes.get('memory-scope').disabled,true);
});
test('delete cancel sends nothing; confirmation sends exact revision and scope once',async()=>{
 const s=setup();await new Promise(setImmediate);s.nodes.get('memory-scope').value='task:one';s.setHandler(async()=>({ok:true,json:async()=>[record]}));await s.context.reload();
 const del=s.nodes.get('memory-list').children[0].children.find(n=>n.textContent==='Delete all revisions');s.context.confirm=()=>false;await del.onclick();assert.equal(s.calls.filter(c=>c.opts.method==='POST').length,0);
 let resolve;s.context.confirm=()=>true;s.setHandler(async(path,opts)=>opts.method==='POST'?new Promise(r=>resolve=r):({ok:true,json:async()=>[]}));const first=del.onclick();await del.onclick();const posts=s.calls.filter(c=>c.opts.method==='POST');assert.equal(posts.length,1);assert.deepEqual(JSON.parse(posts[0].opts.body),{action:'delete',id:record.id,revision:1,confirm:true});assert.equal(posts[0].path,'/api/memory?task_id=one');resolve({ok:true,json:async()=>({deleted:true})});await first;
});

test('local sign out and cross-tab storage event clear protected content',async()=>{
 for(const external of [true,false]){
  const s=setup();await new Promise(setImmediate);s.nodes.get('memory-scope').value='task:one';s.setHandler(async()=>({ok:true,json:async()=>[record]}));await s.context.reload();assert.equal(s.nodes.get('memory-list').children.length,1);
  if(external)s.listeners.storage({key:'qi-logout'});
  else{vm.runInContext('sessionAuth=true',s.context);s.setHandler(async()=>({ok:true,json:async()=>({authenticated:false})}));await s.nodes.get('memory-logout').onclick();const req=s.calls.find(c=>c.path==='/api/logout');assert.deepEqual(JSON.parse(req.opts.body),{all:false});assert.equal(req.opts.headers['X-Qi-Request'],'1');}
  assert.equal(s.nodes.get('memory-list').children.length,0);assert.equal(s.nodes.get('memory-scope').children.length,0);assert.equal(s.nodes.get('memory-form').hidden,true);assert.equal(s.nodes.get('memory-target').textContent,'');
 }
});
test('focus and restored-page auth checks fail closed and clear records',async()=>{
 for(const event of ['focus','pageshow']){const s=setup();await new Promise(setImmediate);s.nodes.get('memory-scope').value='task:one';s.setHandler(async()=>({ok:true,json:async()=>[record]}));await s.context.reload();s.setHandler(async()=>({ok:false,status:401}));await s.listeners[event]();assert.equal(s.nodes.get('memory-list').children.length,0);assert.equal(s.nodes.get('memory-form').hidden,true);}
});

test('revalidation hides facts immediately until successful bounded response',async()=>{
 const s=setup();await new Promise(setImmediate);s.nodes.get('memory-scope').value='task:one';s.setHandler(async()=>({ok:true,json:async()=>[record]}));await s.context.reload();
 let resolve;s.setHandler(async()=>new Promise(r=>resolve=r));const checking=s.listeners.focus();assert.equal(s.nodes.get('memory-protected').hidden,true);
 const edit=s.nodes.get('memory-list').children[0].children.find(n=>n.textContent==='Edit');edit.onclick();assert.equal(s.nodes.get('memory-content').value,'');
 resolve({ok:true,json:async()=>({session_auth:true})});await checking;assert.equal(s.nodes.get('memory-protected').hidden,false);assert.equal(s.nodes.get('memory-list').children.length,1);
});
test('hung fetch or JSON times out, aborts and cannot repopulate after late success',async()=>{
 for(const hungBody of [false,true]){const s=setup();await new Promise(setImmediate);s.nodes.get('memory-scope').value='task:one';s.setHandler(async()=>({ok:true,json:async()=>[record]}));await s.context.reload();
  let fire,resolve;s.context.setTimeout=(fn,ms)=>{assert.equal(ms,5000);fire=fn;return 1;};s.context.clearTimeout=()=>{};
  const pending=new Promise(r=>resolve=r);s.setHandler(async()=>hungBody?({ok:true,json:()=>pending}):pending);const check=s.listeners.pageshow();assert.equal(s.nodes.get('memory-protected').hidden,true);fire();await check;
  assert.equal(s.nodes.get('memory-list').children.length,0);assert.equal(s.nodes.get('memory-scope').children.length,0);assert.equal(s.nodes.get('memory-protected').hidden,true);
  assert.equal(s.calls.filter(c=>c.path==='/api/auth').at(-1).opts.signal.aborted,true);resolve(hungBody?{session_auth:true}:{ok:true,json:async()=>({session_auth:true})});await new Promise(setImmediate);assert.equal(s.nodes.get('memory-protected').hidden,true);assert.equal(s.nodes.get('memory-list').children.length,0);
 }
});

const recordTwo={...record,id:'b'.repeat(32),content:'Prefer 中文 answers',source_ref:'Meeting with Ada'};
function filter(s,text){s.nodes.get('memory-filter').value=text;s.nodes.get('memory-filter').oninput();}
const cardText=s=>s.nodes.get('memory-list').children.map(card=>card.children[0].textContent);
async function loadRecords(s,records=[record,recordTwo]){
 await new Promise(setImmediate);s.nodes.get('memory-scope').value='task:one';
 s.setHandler(async()=>({ok:true,json:async()=>records}));await s.context.reload();
}
test('filter is local content/source substring matching, not metadata, revisions, or selection',async()=>{
 const s=setup();await loadRecords(s);
 const calls=s.calls.length;
 filter(s,'  EXAMPLES  ');assert.deepEqual(cardText(s),[record.content]);
 filter(s,'ADA');assert.deepEqual(cardText(s),[recordTwo.content]);
 filter(s,'中文');assert.deepEqual(cardText(s),[recordTwo.content]);
 filter(s,'preference');assert.deepEqual(cardText(s),[]);
 assert.match(s.nodes.get('memory-filter-status').textContent,/0 of 2.*No matches/);
 filter(s,'');assert.deepEqual(cardText(s),[record.content,recordTwo.content]);
 assert.equal(s.calls.length,calls);assert.equal(s.nodes.get('memory-scope').value,'task:one');
 assert.equal(s.nodes.get('memory-content').value,'');assert.equal(s.nodes.get('memory-confirm').checked,false);
});
test('scope switches clear keyword, old cached results and late responses; empty scope is empty',async()=>{
 const s=setup();await loadRecords(s);filter(s,'examples');
 let resolve;s.setHandler(async()=>new Promise(r=>resolve=r));
 const pending=s.context.reload();assert.deepEqual(cardText(s),[]);assert.equal(s.nodes.get('memory-filter').disabled,true);
 s.nodes.get('memory-scope').value='global';s.setHandler(async()=>({ok:true,json:async()=>[recordTwo]}));
 await s.nodes.get('memory-scope').onchange();assert.equal(s.nodes.get('memory-filter').value,'');
 resolve({ok:true,json:async()=>[record]});await pending;
 filter(s,'examples');assert.deepEqual(cardText(s),[]);filter(s,'');assert.deepEqual(cardText(s),[recordTwo.content]);
 s.nodes.get('memory-scope').value='';await s.nodes.get('memory-scope').onchange();
 assert.deepEqual(cardText(s),[]);assert.equal(s.nodes.get('memory-filter').disabled,true);
 assert.equal(s.nodes.get('memory-filter-status').textContent,'');assert.equal(s.nodes.get('memory-target').textContent,'');
});
test('filter invalidates detached actions and pending revision responses, including errors',async()=>{
 for(const fails of [false,true]){
  const s=setup();await loadRecords(s);
  const old=s.nodes.get('memory-list').children[0],button=label=>old.children.find(n=>n.textContent===label);
  let resolve;s.setHandler(async()=>new Promise(r=>resolve=r));
  const pending=button('Show revisions').onclick();
  filter(s,'ADA');const calls=s.calls.length;
  button('Edit').onclick();await button('Delete all revisions').onclick();
  assert.equal(s.nodes.get('memory-content').value,'');assert.equal(s.calls.length,calls);
  resolve(fails?{ok:false,status:500,text:async()=>'old scope error'}:{ok:true,json:async()=>[{content:'old secret revision'}]});
  await pending;assert.deepEqual(cardText(s),[recordTwo.content]);
  assert.equal(old.children.filter(n=>n.tag==='pre').length,0);assert.notEqual(s.nodes.get('memory-error')?.hidden,false);
 }
});
test('edit and delete refresh filtered results without stale cached records, including reload failure',async()=>{
 for(const action of ['edit','delete']){
  for(const fails of [false,true]){
   const s=setup();await loadRecords(s);filter(s,'examples');
   const card=s.nodes.get('memory-list').children[0];
   let resolvePost,resolveGet;
   s.setHandler(async(path,opts)=>new Promise(r=>{if(opts.method==='POST')resolvePost=r;else resolveGet=r;}));
   let pending;
   if(action==='edit'){
    card.children.find(n=>n.textContent==='Edit').onclick();
    s.nodes.get('memory-content').value='Updated without old keyword';s.nodes.get('memory-confirm').checked=true;
    pending=s.nodes.get('memory-form').onsubmit({preventDefault(){}});
   }else pending=card.children.find(n=>n.textContent==='Delete all revisions').onclick();
   assert.equal(s.nodes.get('memory-filter').disabled,true);
   resolvePost({ok:true,json:async()=>({})});await new Promise(setImmediate);
   assert.deepEqual(cardText(s),[]);assert.equal(s.nodes.get('memory-filter').disabled,true);
   resolveGet(fails?{ok:false,status:500,text:async()=>'refresh failed'}:{ok:true,json:async()=>action==='edit'?[{...record,content:'Updated without old keyword'}]:[]});
   await pending;assert.deepEqual(cardText(s),[]);assert.equal(s.nodes.get('memory-filter').value,'examples');
   filter(s,'');assert.deepEqual(cardText(s),!fails&&action==='edit'?['Updated without old keyword']:[]);
  }
 }
});
test('sign out clears filter cache and cannot reveal facts through filter input or late history',async()=>{
 const s=setup();await loadRecords(s);filter(s,'examples');
 let resolve;s.setHandler(async()=>new Promise(r=>resolve=r));
 const card=s.nodes.get('memory-list').children[0],pending=card.children.find(n=>n.textContent==='Show revisions').onclick();
 s.listeners.storage({key:'qi-logout'});assert.equal(s.nodes.get('memory-filter').value,'');
 filter(s,'');assert.deepEqual(cardText(s),[]);assert.equal(s.nodes.get('memory-filter').disabled,true);
 resolve({ok:true,json:async()=>[{content:'old revision'}]});await pending;
 assert.deepEqual(cardText(s),[]);assert.equal(s.nodes.get('memory-filter-status').textContent,'');
});
test('literal markup is text and filtering does not alter active edit or approval',async()=>{
 const s=setup(),literal={...record,content:'<img src=x onerror=alert(1)>',source_ref:'[.*] provenance'};await loadRecords(s,[literal,recordTwo]);
 const card=s.nodes.get('memory-list').children[0];card.children.find(n=>n.textContent==='Edit').onclick();
 s.nodes.get('memory-confirm').checked=true;filter(s,'[.*]');assert.deepEqual(cardText(s),[literal.content]);
 filter(s,'Ada');assert.deepEqual(cardText(s),[recordTwo.content]);assert.equal(s.nodes.get('memory-content').value,literal.content);
 assert.equal(s.nodes.get('memory-confirm').checked,true);assert.equal(s.nodes.get('memory-heading').textContent,'Edit revision 1');
});
test('write failure remains visible after refresh and stale cache clears during auth revalidation',async()=>{
 const s=setup();await loadRecords(s);filter(s,'examples');
 const del=s.nodes.get('memory-list').children[0].children.find(n=>n.textContent==='Delete all revisions');
 s.setHandler(async(path,opts)=>opts.method==='POST'?{ok:false,status:500,text:async()=>'write uncertain'}:{ok:true,json:async()=>[]});
 await del.onclick();assert.equal(s.nodes.get('memory-error').hidden,false);assert.match(s.nodes.get('memory-error').textContent,/write uncertain.*uncertain/);
 await loadRecords(s);filter(s,'examples');
 let post,auth;s.setHandler(async(path,opts)=>new Promise(r=>{if(path==='/api/auth')auth=r;else post=r;}));
 const pending=s.nodes.get('memory-list').children[0].children.find(n=>n.textContent==='Delete all revisions').onclick();
 const checking=s.listeners.focus();post({ok:true,json:async()=>({})});await pending;
 assert.deepEqual(cardText(s),[]);assert.equal(s.nodes.get('memory-filter').disabled,true);
 auth({ok:true,json:async()=>({session_auth:true})});await checking;filter(s,'');
 assert.deepEqual(cardText(s),[]);
});
test('mutation invalidates an older list GET even when auth revalidation prevents replacement reload',async()=>{
 const s=setup();await loadRecords(s);
 let listResponse,writeResponse,authResponse;
 s.setHandler(async(path,opts)=>new Promise(resolve=>{
  if(path==='/api/auth')authResponse=resolve;
  else if(opts.method==='POST')writeResponse=resolve;
  else listResponse=resolve;
 }));
 const oldList=s.context.reload();
 // The create form is available while a list refresh is pending. This write
 // must invalidate that earlier snapshot, even without a replacement GET.
 s.nodes.get('memory-content').value='New fact';
 s.nodes.get('memory-source').value='Owner';
 s.nodes.get('memory-confirm').checked=true;
 const write=s.nodes.get('memory-form').onsubmit({preventDefault(){}});
 const auth=s.listeners.focus();
 writeResponse({ok:true,json:async()=>({...record,content:'New fact'})});await write;
 assert.equal(s.nodes.get('memory-filter').disabled,true);
 listResponse({ok:true,json:async()=>[record]});await oldList;
 assert.deepEqual(cardText(s),[]);assert.equal(s.nodes.get('memory-filter-status').textContent,'');
 authResponse({ok:true,json:async()=>({session_auth:true})});await auth;
 filter(s,'');assert.deepEqual(cardText(s),[]);assert.equal(s.nodes.get('memory-filter').disabled,true);
 const memoryGets=s.calls.filter(c=>c.path.startsWith('/api/memory?')&&c.opts.method==='GET');
 assert.equal(memoryGets.length,2); // initial load and obsolete reload only
});
