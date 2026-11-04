'use strict';
const $=id=>document.getElementById(id);
let skillControls=[];
let selected=null, busy=false, version='', refreshRunning=false;
function error(e){$('error').textContent=e.message||String(e);$('error').hidden=false;}
async function api(path,body){const response=await fetch(path,{method:body===undefined?'GET':'POST',credentials:'same-origin',headers:body===undefined?{}:{'Content-Type':'application/json','X-Qi-Request':'1'},body:body===undefined?undefined:JSON.stringify(body)});if(!response.ok)throw new Error((await response.text()).trim()||'Request failed');return response.json();}
function element(tag,text,cls){const node=document.createElement(tag);if(text!==undefined)node.textContent=text;if(cls)node.className=cls;return node;}
function active(status){return ['running','queued','waiting_for_approval','waiting_for_input'].includes(status);}
function paint(task){const signature=JSON.stringify(task)+JSON.stringify((task.approvals||[]).map(a=>Date.parse(a.expires_at)<=Date.now()));$('send').disabled=busy||active(task.status);if(version===signature)return;version=signature;$('task-title').textContent=task.title;$('status').textContent=task.status;$('cancel').hidden=!active(task.status);$('send').disabled=busy||active(task.status);const panel=$('conversation');const nearBottom=panel.scrollHeight-panel.scrollTop-panel.clientHeight<100;panel.replaceChildren();paintContextNotice(task,panel);for(const msg of task.messages||[]){if(!['user','assistant','tool'].includes(msg.role))continue;const block=element('div',undefined,'message '+msg.role);block.append(element('small',msg.role==='assistant'?'QI':msg.role.toUpperCase()),element('div',msg.content||''));panel.append(block);}for(const run of task.runs||[]){if(run.error){const notice=element('section',undefined,'card');notice.append(element('h3','Run '+run.status),element('p',run.error));panel.append(notice);}}for(const card of task.cards||[]){const block=element('section',undefined,'card');block.append(element('h3',card.title),element('p',card.summary),element('small',card.state));for(const action of card.actions||[]){const button=element('button',action.label);button.disabled=card.state!=='pending';button.onclick=async()=>{button.disabled=true;try{await api('/api/tasks/'+encodeURIComponent(task.id)+'/actions',{card_id:card.id,action:action.id,revision:card.revision});await refresh();}catch(e){error(e);await refresh();}};block.append(button);}panel.append(block);}paintApprovals(task,panel);if(nearBottom)panel.scrollTop=panel.scrollHeight;}
async function refresh(){if(refreshRunning)return;refreshRunning=true;try{const tasks=await api('/api/tasks');const nav=$('tasks');nav.replaceChildren();for(const task of tasks){const button=element('button',task.title,task.id===selected?'active':'');button.append(element('small',task.status));button.onclick=()=>{if(busy)return;clearSkills();selected=task.id;version='';refresh();};nav.append(button);}if(selected){const id=selected;const task=await api('/api/tasks/'+encodeURIComponent(id));task.approvals=await api('/api/tasks/'+encodeURIComponent(id)+'/approvals');if(selected===id)paint(task);}}catch(e){error(e);}finally{refreshRunning=false;}}
$('new-task').onclick=()=>{if(busy)return;clearSkills();selected=null;version='';$('task-title').textContent='A new thread.';$('status').textContent='Ready';$('conversation').replaceChildren();$('cancel').hidden=true;$('send').disabled=busy;$('prompt').focus();refresh();};
$('composer').onsubmit=async event=>{event.preventDefault();if(busy)return;const prompt=$('prompt').value.trim();if(!prompt)return;const names=skillControls.filter(input=>input.checked).map(input=>input.value);busy=true;$('skills').disabled=true;$('send').disabled=true;$('error').hidden=true;try{if(!selected){const task=await api('/api/tasks',{title:Array.from(prompt).slice(0,35).join('')});selected=task.id;}const id=selected;await api('/api/tasks/'+encodeURIComponent(id)+'/messages',{prompt,skills:names});$('prompt').value='';clearSkills();version='';}catch(e){error(e);}finally{busy=false;$('skills').disabled=false;await refresh();}};
$('cancel').onclick=async()=>{const id=selected;if(!id)return;$('cancel').disabled=true;try{await api('/api/tasks/'+encodeURIComponent(id)+'/cancel',{});await refresh();}catch(e){error(e);}finally{$('cancel').disabled=false;}};
loadSkills();refresh();setInterval(()=>{if(!document.hidden)refresh();},1500);

// These durable exact-action decisions are NOT informational task cards.
function paintApprovals(task,panel){
 for(const approval of task.approvals||[]){
  const block=element('section',undefined,'card approval');
  block.append(element('h3','Permission to run '+approval.action.kind),
   element('p','This can change files or access the network. Review the complete action before allowing it.'),
   element('p','Target: '+approval.action.target),
   element('pre',JSON.stringify(approval.action.payload,null,2)),
   element('p','State: '+approval.status+' · Revision: '+approval.revision+' · Expires: '+approval.expires_at),
   element('p','Action fingerprint: '+approval.digest));
  const buttons=[];
  for(const [label,allow] of [['Allow this action once',true],['Deny',false]]){
   const button=element('button',label);buttons.push(button);
   button.disabled=approval.status!=='pending'||!Number.isFinite(Date.parse(approval.expires_at))||Date.parse(approval.expires_at)<=Date.now();
   button.onclick=async()=>{
    for(const item of buttons)item.disabled=true;
    try{await api('/api/tasks/'+encodeURIComponent(task.id)+'/approvals',{id:approval.id,revision:approval.revision,digest:approval.digest,allow});}
    catch(e){error(e);}finally{version='';await refresh();}
   };block.append(button);
  }
  panel.append(block);
 }
}

function clearSkills(){for(const input of skillControls)input.checked=false;}
function paintSkills(catalog){
 skillControls=[];const fieldset=$('skills');fieldset.replaceChildren(element('legend','Skills for this message only'));
 for(const skill of catalog){
  const row=element('label',undefined,'skill-option');const input=element('input');input.type='checkbox';input.value=skill.name;input.checked=false;
  const text=element('span');text.append(element('strong',skill.name),element('span',skill.description),element('small',skill.source+' · '+skill.path));
  row.append(input,text);fieldset.append(row);skillControls.push(input);
 }
 $('skill-picker').hidden=catalog.length===0;
}
async function loadSkills(){try{paintSkills(await api('/api/skills'));}catch(e){error(e);}}

function paintContextNotice(task,panel){
 const notice=task.context_notice;if(!notice)return;
 const block=element('section',undefined,'card context-notice');
 block.append(element('h3','Latest saved context reduction'),
  element('p',notice.dropped_messages+' earlier messages were omitted from a model request, including any user instructions in those messages. Full saved history is unchanged; system and developer instructions were retained.'),
  element('small','Input size: '+notice.input_bytes+' bytes · Run: '+notice.run_id),
  element('p','This records the latest reduction, not a guarantee of what a later request includes.'));
 panel.append(block);
}
