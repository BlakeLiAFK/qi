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
 const context=vm.createContext({document,setInterval(){},fetch:async(path,opts)=>{requests.push({path,opts});return {ok:true,json:async()=>path==='/api/tasks'?[]:fixture};}});
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
