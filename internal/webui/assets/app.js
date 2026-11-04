'use strict';
const $=id=>document.getElementById(id);
let skillControls=[];
let sessionAuth=false, signingOut=false, loggedOut=false;
// Unsent drafts stay in memory and are keyed by the task they belong to.
const taskDrafts=new Map();
let selected=null, busy=false, version='', refreshRunning=false, refreshAgain=false;
let taskStream=null, streamTask=null;
function watchTask(id){
 if(streamTask===id)return;
 if(taskStream)taskStream.close();taskStream=null;streamTask=id;
 if(!id||typeof EventSource==='undefined')return;
 const stream=new EventSource('/api/tasks/'+encodeURIComponent(id)+'/events');taskStream=stream;
 const changed=()=>{if(taskStream===stream&&selected===id)refresh();};
 stream.addEventListener('ready',changed);
 stream.addEventListener('progress',event=>{try{const data=JSON.parse(event.data);if(data.task_id===id)changed();}catch(_){}});
 // EventSource reconnects using Last-Event-ID. Polling remains a fallback.
}
function error(e){if(loggedOut)return;$('error').textContent=e.message||String(e);$('error').hidden=false;}
async function api(path,body){
 if(loggedOut)throw new Error('Signed out');
 const response=await fetch(path,{method:body===undefined?'GET':'POST',credentials:'same-origin',headers:body===undefined?{}:{'Content-Type':'application/json','X-Qi-Request':'1'},body:body===undefined?undefined:JSON.stringify(body)});
 if(loggedOut)throw new Error('Signed out');
 if(response.status===401&&sessionAuth){finishSignOut();throw new Error('Session ended. Sign in again.');}
 if(!response.ok)throw new Error((await response.text()).trim()||'Request failed');
 const result=await response.json();
 if(loggedOut)throw new Error('Signed out');
 return result;
}
function element(tag,text,cls){const node=document.createElement(tag);if(text!==undefined)node.textContent=text;if(cls)node.className=cls;return node;}
function active(status){return ['running','queued','waiting_for_approval','waiting_for_input'].includes(status);}
function paint(task){if(loggedOut)return;const signature=JSON.stringify(task)+JSON.stringify((task.approvals||[]).map(a=>Date.parse(a.expires_at)<=Date.now()))+JSON.stringify((task.cards||[]).map(c=>Date.parse(c.expires_at)<=Date.now()));$('send').disabled=busy||active(task.status);if(version===signature)return;version=signature;$('task-title').textContent=task.title;$('status').textContent=task.status;$('cancel').hidden=!active(task.status);$('send').disabled=busy||active(task.status);const panel=$('conversation');const nearBottom=panel.scrollHeight-panel.scrollTop-panel.clientHeight<100;panel.replaceChildren();paintContextNotice(task,panel);for(const msg of task.messages||[]){if(!['user','assistant','tool'].includes(msg.role))continue;const block=element('div',undefined,'message '+msg.role);block.append(element('small',msg.role==='assistant'?'QI':msg.role.toUpperCase()),element('div',msg.content||''));panel.append(block);}paintLiveText(task,panel);for(const run of task.runs||[]){if(run.error){const notice=element('section',undefined,'card');notice.append(element('h3','Run '+run.status),element('p',run.error));panel.append(notice);}}for(const card of task.cards||[]){if(card.kind==='question'){paintQuestion(task,card,panel);continue;}const block=element('section',undefined,'card');block.append(element('h3',card.title),element('p',card.summary),element('small',card.state));for(const action of card.actions||[]){const button=element('button',action.label);button.disabled=card.state!=='pending';button.onclick=async()=>{button.disabled=true;try{await api('/api/tasks/'+encodeURIComponent(task.id)+'/actions',{card_id:card.id,action:action.id,revision:card.revision});await refresh();}catch(e){error(e);await refresh();}};block.append(button);}panel.append(block);}paintApprovals(task,panel);paintArtifacts(task,panel);if(nearBottom)panel.scrollTop=panel.scrollHeight;}
async function refresh(){if(loggedOut||signingOut)return;if(refreshRunning){refreshAgain=true;return;}refreshRunning=true;watchTask(selected);try{const tasks=await api('/api/tasks');const nav=$('tasks');nav.replaceChildren();for(const task of tasks){const button=element('button',task.title,task.id===selected?'active':'');button.append(element('small',task.status));button.onclick=()=>{if(busy)return;selectTask(task);refresh();};nav.append(button);}if(selected){const id=selected;const task=await api('/api/tasks/'+encodeURIComponent(id));task.approvals=await api('/api/tasks/'+encodeURIComponent(id)+'/approvals');task.artifacts=await api('/api/tasks/'+encodeURIComponent(id)+'/artifacts');if(selected===id)paint(task);}}catch(e){error(e);}finally{refreshRunning=false;if(refreshAgain){refreshAgain=false;refresh();}}}
function selectTask(task){
 const nextID=task?task.id:null;
 if(task&&nextID===selected)return;
 if(selected!==null)taskDrafts.set(selected,$('prompt').value);
 selected=nextID;watchTask(selected);
 $('prompt').value=task?taskDrafts.get(nextID)||'':'';
 clearSkills();version='';$('error').hidden=true;
 $('task-title').textContent=task?task.title:'A new thread.';
 $('status').textContent=task?task.status:'Ready';
 $('conversation').replaceChildren();
 $('cancel').hidden=!task||!active(task.status);
 $('send').disabled=busy||Boolean(task&&active(task.status));
}
$('new-task').onclick=()=>{
 if(busy)return;
 selectTask(null);$('prompt').focus();refresh();
};
// Keep a key until acceptance is observed. An uncertain network failure must
// not turn a retry into a new run. Session storage also survives page reloads.
const pendingSubmissions=new Map();
function submissionKey(id,prompt,names){
 const signature=JSON.stringify({prompt,skills:names}),storageKey='qi-submission:'+id;
 let prior=pendingSubmissions.get(id);
 try{if(!prior)prior=JSON.parse(sessionStorage.getItem(storageKey));}catch(_){}
 if(!prior||prior.signature!==signature){prior={signature,key:crypto.randomUUID()};}
 pendingSubmissions.set(id,prior);
 try{sessionStorage.setItem(storageKey,JSON.stringify(prior));}catch(_){}
 return prior.key;
}
function clearSubmission(id){pendingSubmissions.delete(id);try{sessionStorage.removeItem('qi-submission:'+id);}catch(_){}}
$('composer').onsubmit=async event=>{
 event.preventDefault();if(busy)return;
 const draft=$('prompt').value,prompt=draft.trim();if(!prompt)return;
 const names=skillControls.filter(input=>input.checked).map(input=>input.value);
 busy=true;$('prompt').readOnly=true;$('skills').disabled=true;$('send').disabled=true;$('error').hidden=true;
 try{
  if(selected===null){
   const task=await api('/api/tasks',{title:Array.from(prompt).slice(0,35).join('')});
   selected=task.id;
  }
  const id=selected;
  taskDrafts.set(id,draft);
  await api('/api/tasks/'+encodeURIComponent(id)+'/messages',{prompt,skills:names,idempotency_key:submissionKey(id,prompt,names)});
  clearSubmission(id);
  taskDrafts.delete(id);$('prompt').value='';clearSkills();version='';
 }catch(e){error(e);}
 finally{busy=false;$('prompt').readOnly=false;$('skills').disabled=false;await refresh();}
};
$('cancel').onclick=async()=>{const id=selected;if(!id)return;$('cancel').disabled=true;try{await api('/api/tasks/'+encodeURIComponent(id)+'/cancel',{});await refresh();}catch(e){error(e);}finally{$('cancel').disabled=false;}};
loadAuth().finally(()=>{if(!loggedOut){loadSkills();refresh();}});setInterval(()=>{if(!document.hidden)refresh();},1500);

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

// Question replies only record task input. They do not grant tool permissions.
const questionDrafts=new Map();
function paintQuestion(task,card,panel){
 const block=element('section',undefined,'card question');
 block.append(element('h3',card.title),element('p',card.summary),element('small',card.state));
 const pending=card.state==='pending'&&Number.isFinite(Date.parse(card.expires_at))&&Date.parse(card.expires_at)>Date.now();
 if(card.answer){
  block.append(element('p','Answer: '+(card.answer.text||(card.choices||[]).find(c=>c.id===card.answer.choice_id)?.label||card.answer.choice_id)));
  if(card.state==='answered'){
   block.append(element('small','Answer saved. Continue starts a new turn; it does not approve external actions.'));
   const button=element('button','Continue task');const latestRun=(task.runs||[]).at(-1);button.disabled=task.status!=='completed'||(latestRun&&latestRun.id!==card.run_id);
   button.onclick=async()=>{if(button.disabled)return;button.disabled=true;try{await api('/api/tasks/'+encodeURIComponent(task.id)+'/actions',{task_id:task.id,card_id:card.id,revision:card.revision,action:'continue'});}catch(e){error(e);}finally{version='';await refresh();}};
   block.append(button);
   if(button.disabled)block.append(element('small','Wait for the current turn to finish. For older, cancelled or interrupted work, send a new message.'));
  }else block.append(element('small','Continuation requested. This card cannot start another turn; interrupted work is never automatically replayed.'));
  panel.append(block);return;
 }
 const controls=[];const key=task.id+'/'+card.id+'/'+card.revision;
 async function submit(answer){
  for(const c of controls)c.disabled=true;
  try{await api('/api/tasks/'+encodeURIComponent(task.id)+'/actions',{task_id:task.id,card_id:card.id,revision:card.revision,action:'answer',...answer});questionDrafts.delete(key);}
  catch(e){error(e);}finally{version='';await refresh();}
 }
 if(card.input_type==='choice'){
  for(const choice of card.choices||[]){const button=element('button',choice.label);button.disabled=!pending;button.onclick=()=>submit({choice_id:choice.id});controls.push(button);block.append(button);}
 }else if(card.input_type==='text'){
  block.append(element('p','不要输入密码或 API Key，回答会保存并可能发给模型'));const label=element('label','Your answer');const input=element('textarea');input.maxLength=8000;input.value=questionDrafts.get(key)||'';input.disabled=!pending;input.oninput=()=>{if(!loggedOut)questionDrafts.set(key,input.value);};label.append(input);
  const button=element('button','Save answer');button.disabled=!pending;button.onclick=()=>{const text=input.value.trim();if(new TextEncoder().encode(text).length>8000){error(new Error('Answer is too long. Please use at most 8,000 UTF-8 bytes.'));return;}if(text)return submit({text});};controls.push(input,button);block.append(label,button);
 }
 if(!pending)block.append(element('small','This question is closed or expired.'));
 panel.append(block);
}
function paintArtifacts(task,panel){
 for(const artifact of task.artifacts||[]){
  const block=element('section',undefined,'card deliverable');
  block.append(element('small','DELIVERABLE'),element('h3',artifact.name),element('p',artifact.media_type+' · '+artifact.size+' bytes'),element('small','SHA-256: '+artifact.sha256));
  const link=element('a','Download file');link.href='/api/tasks/'+encodeURIComponent(task.id)+'/artifacts/'+encodeURIComponent(artifact.id);link.download=artifact.name;block.append(link);panel.append(block);
 }
}

function paintLiveText(task,panel){
 const live=task.live_text;
 if(!live||task.status!=='running'||live.task_id!==task.id||typeof live.text!=='string'||!Number.isSafeInteger(live.revision)||!Number.isSafeInteger(live.request_id))return;
 if(!(task.runs||[]).some(run=>run.id===live.run_id&&run.status==='running'))return;
 const block=element('section',undefined,'message live-text');
 block.append(element('small','QI · PROVISIONAL TEXT'),element('div',live.text.slice(0,65536)),element('small','Still generating. This is not a completed answer or a tool result. Live previews may pause at size or event limits.'));
 if(live.truncated||live.text.length>65536)block.append(element('small','Live preview limit reached. The completed message will replace this preview.'));
 panel.append(block);
}

async function loadAuth(){
 try{const auth=await api('/api/auth');sessionAuth=auth.session_auth===true;$('auth-controls').hidden=!sessionAuth;}
 catch(e){$('auth-controls').hidden=true;error(e);}
}
function finishSignOut(){
 if(loggedOut)return;loggedOut=true;watchTask(null);selected=null;taskDrafts.clear();questionDrafts.clear();pendingSubmissions.clear();version='';
 try{const keys=[];for(let i=0;i<sessionStorage.length;i++){const key=sessionStorage.key(i);if(key&&key.startsWith('qi-submission:'))keys.push(key);}for(const key of keys)sessionStorage.removeItem(key);}catch(_){}
 clearSkills();skillControls=[];$('skills').replaceChildren();$('skill-picker').hidden=true;$('prompt').value='';$('prompt').readOnly=true;$('send').disabled=true;$('cancel').hidden=true;$('conversation').replaceChildren();$('tasks').replaceChildren();$('task-title').textContent='Signed out';$('status').textContent='';$('error').hidden=true;$('auth-controls').hidden=true;
 if(typeof location!=='undefined')location.replace('/login');
}
async function signOut(all){
 if(!sessionAuth||signingOut||loggedOut)return;signingOut=true;$('logout').disabled=true;$('logout-all').disabled=true;
 try{const result=await api('/api/logout',{all});if(result.authenticated!==false)throw new Error('Sign out was not confirmed');finishSignOut();}
 catch(e){error(e);}
 finally{signingOut=false;if(!loggedOut){$('logout').disabled=false;$('logout-all').disabled=false;}}
}
$('logout').onclick=()=>signOut(false);
$('logout-all').onclick=()=>signOut(true);
