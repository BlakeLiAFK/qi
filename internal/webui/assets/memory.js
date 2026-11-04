'use strict';
const $=id=>document.getElementById(id);
let generation=0, editing=null, saving=false, ended=false, sessionAuth=false, checkingAuth=false, signingOut=false;
const node=(tag,text)=>{const n=document.createElement(tag);n.textContent=text;return n;};
function scopeQuery(){const value=$('memory-scope').value;return value==='global'?'global=true':value.startsWith('task:')?'task_id='+encodeURIComponent(value.slice(5)):'';}
function endSession(){ended=true;$('memory-protected').hidden=true;$('memory-logout').hidden=true;++generation;clearForm();$('memory-list').replaceChildren();$('memory-form').hidden=true;$('memory-scope').value='';$('memory-scope').replaceChildren();$('memory-target').textContent='';setBusy(false);}
function failure(e){$('memory-error').textContent=e.message||String(e);$('memory-error').hidden=false;}
async function api(query,body){const response=await fetch('/api/memory?'+query,{method:body===undefined?'GET':'POST',credentials:'same-origin',headers:body===undefined?{}:{'Content-Type':'application/json','X-Qi-Request':'1'},body:body===undefined?undefined:JSON.stringify(body)});if(response.status===401)endSession();if(!response.ok)throw new Error((await response.text()).trim()||'Request failed');const result=await response.json();if(ended)throw new Error('Session ended. Reload to sign in again.');return result;}
function clearForm(){editing=null;$('memory-heading').textContent='Create a record';$('memory-content').value='';$('memory-source').value='';$('memory-kind').value='preference';$('memory-confidence').value='1';$('memory-private').value='private';$('memory-expiry').value='';$('memory-confirm').checked=false;}
function setBusy(value){saving=value;for(const id of ['memory-scope','memory-reload','memory-save','memory-cancel'])$(id).disabled=value||ended;}
async function reload(){if(saving||ended||signingOut||checkingAuth)return;const serial=++generation,query=scopeQuery();clearForm();$('memory-list').replaceChildren();$('memory-form').hidden=!query;if(!query)return;$('memory-target').textContent='Save scope: '+$('memory-scope').selectedOptions[0].textContent;try{const items=await api(query);if(serial!==generation)return;for(const item of items)render(item,query,serial);}catch(e){if(serial===generation)failure(e);}}
function render(item,query,serial){
 const card=node('section','');card.className='card';
 card.append(node('p',item.content),node('p','Source (user asserted): '+item.source_ref),node('p','Revision '+item.revision+' · '+item.kind+' · Confidence '+item.confidence+' · '+item.confidentiality),node('p','Updated '+item.updated_at+' · Expiry '+(item.expires_at||'none')),node('small','Approval audit reference: '+item.approval_ref));
 const edit=node('button','Edit'),history=node('button','Show revisions'),del=node('button','Delete all revisions');
 edit.onclick=()=>{if(saving||checkingAuth||serial!==generation)return;editing=item;$('memory-heading').textContent='Edit revision '+item.revision;$('memory-content').value=item.content;$('memory-source').value=item.source_ref;$('memory-kind').value=item.kind;$('memory-confidence').value=item.confidence;$('memory-private').value=item.confidentiality;$('memory-expiry').value=item.expires_at||'';$('memory-confirm').checked=false;};
 history.onclick=async()=>{if(saving||checkingAuth||serial!==generation)return;history.disabled=true;try{const versions=await api(query+'&id='+encodeURIComponent(item.id));if(serial!==generation)return;for(const v of versions)card.append(node('pre',JSON.stringify(v,null,2)));}catch(e){failure(e);history.disabled=false;}};
 del.onclick=async()=>{if(saving||checkingAuth||serial!==generation)return;if(!confirm('Delete this record and ALL its revisions from '+$('memory-scope').selectedOptions[0].textContent+'?\n\n'+item.content+'\n\nBackups, SQLite remnants and existing conversations are not erased.'))return;setBusy(true);try{await api(query,{action:'delete',id:item.id,revision:item.revision,confirm:true});}catch(e){failure(new Error(e.message+' Reload and inspect before retrying; the outcome may be uncertain.'));}finally{setBusy(false);await reload();}};
 card.append(edit,history,del);$('memory-list').append(card);
}
$('memory-form').onsubmit=async event=>{event.preventDefault();if(saving||ended||signingOut||checkingAuth||!scopeQuery()||!$('memory-confirm').checked)return;const query=scopeQuery();const fact={content:$('memory-content').value,source_ref:$('memory-source').value,kind:$('memory-kind').value,confidence:Number($('memory-confidence').value),confidentiality:$('memory-private').value};const expiry=$('memory-expiry').value.trim();if(expiry)fact.expires_at=expiry;const body={action:editing?'edit':'create',confirm:true,non_sensitive:true,fact};if(editing){body.id=editing.id;body.revision=editing.revision;}setBusy(true);$('memory-error').hidden=true;try{await api(query,body);}catch(e){failure(new Error(e.message+' Reload and inspect records before retrying; the outcome may be uncertain.'));}finally{setBusy(false);await reload();}};
$('memory-scope').onchange=reload;$('memory-reload').onclick=reload;$('memory-cancel').onclick=clearForm;
// Any change invalidates the checkbox. A save always requires a fresh deliberate decision.
for(const id of ['memory-content','memory-source','memory-kind','memory-confidence','memory-private','memory-expiry'])$(id).oninput=()=>{$('memory-confirm').checked=false;};
(async()=>{try{for(const archived of [false,true]){const response=await fetch('/api/tasks'+(archived?'?archived=true':''),{credentials:'same-origin'});if(response.status===401)endSession();if(!response.ok)throw new Error('Cannot load task scopes. Sign in again if your session ended.');const tasks=await response.json();if(ended)return;for(const task of tasks){const option=node('option',(archived?'Archived task: ':'Task: ')+task.title+' ('+task.id+')');option.value='task:'+task.id;$('memory-scope').append(option);}}}catch(e){failure(e);}})();

let logoutChannel=null;
try{if(typeof BroadcastChannel!=='undefined'){logoutChannel=new BroadcastChannel('qi-auth');logoutChannel.onmessage=event=>{if(event.data==='logout'){endSession();failure(new Error('Signed out in another tab. Reload to sign in.'));}};}}catch(_){}
function broadcastSignOut(){try{if(logoutChannel)logoutChannel.postMessage('logout');}catch(_){}try{localStorage.setItem('qi-logout',String(Date.now()));}catch(_){}}
const authTimeoutMS=5000;
async function verifyAuth(){
 if(ended||checkingAuth||signingOut)return;checkingAuth=true;
 // Hide all protected text and controls synchronously, before any network wait.
 // Do not reveal them on a hung response or while its JSON body is still loading.
 $('memory-protected').hidden=true;
 const controller=new AbortController();let timer;
 try{
  const timeout=new Promise((_,reject)=>{timer=setTimeout(()=>{controller.abort();reject(new Error('Session check timed out. Reload to sign in again.'));},authTimeoutMS);});
  const check=(async()=>{const response=await fetch('/api/auth',{credentials:'same-origin',cache:'no-store',signal:controller.signal});if(!response.ok)throw new Error('Session unavailable. Reload to sign in again.');return response.json();})();
  const auth=await Promise.race([check,timeout]);if(ended)return;
  sessionAuth=auth.session_auth===true;$('memory-logout').hidden=!sessionAuth;$('memory-protected').hidden=false;
 }catch(e){endSession();failure(e);}finally{clearTimeout(timer);checkingAuth=false;}
}
$('memory-logout').onclick=async()=>{
 if(!sessionAuth||signingOut||ended)return;signingOut=true;$('memory-logout').disabled=true;
 try{const response=await fetch('/api/logout',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-Qi-Request':'1'},body:JSON.stringify({all:false})});if(response.status===401){endSession();return;}if(!response.ok)throw new Error('Sign out not confirmed. Try again.');const result=await response.json();if(result.authenticated!==false)throw new Error('Sign out not confirmed.');broadcastSignOut();endSession();failure(new Error('Signed out. Reload to sign in again.'));}
 catch(e){failure(e);}finally{signingOut=false;$('memory-logout').disabled=false;}
};
if(typeof addEventListener==='function'){for(const event of ['focus','pageshow'])addEventListener(event,verifyAuth);addEventListener('storage',event=>{if(event.key==='qi-logout'){endSession();failure(new Error('Signed out in another tab. Reload to sign in.'));}});}
if(typeof document.addEventListener==='function')document.addEventListener('visibilitychange',()=>{if(!document.hidden)verifyAuth();});
if(typeof setInterval==='function')setInterval(verifyAuth,30000);
verifyAuth();
