'use strict';
const $=id=>document.getElementById(id);
const node=(tag,text)=>{const n=document.createElement(tag);n.textContent=text;return n;};
let generation=0, ended=false;
function clearCatalog(){++generation;$('plugins-protected').hidden=true;$('plugins-builtins').replaceChildren();$('plugins-mcp').replaceChildren();$('plugins-status').textContent='';}
function endSession(){ended=true;clearCatalog();$('plugins-reload').disabled=true;$('plugins-error').textContent='Session ended. Return to tasks to sign in again.';$('plugins-error').hidden=false;}
function renderEntry(entry){
 const card=node('section','');card.className='card';
 card.append(node('h3',entry.name),node('p',(entry.enabled?'Enabled':'Disabled')+' · '+(entry.kind==='mcp'?'MCP plugin':'Built-in capability')),node('p','Source: '+entry.source),node('p',entry.description),node('p',entry.warning));
 if(entry.command)card.append(node('p','Executable: '+entry.command));
 if(entry.directory)card.append(node('p','Directory: '+entry.directory));
 const list=node('ul','');for(const tool of entry.tools||[])list.append(node('li',tool.name+': '+tool.description));card.append(list);
 return card;
}
async function reload(){
 if(ended)return;clearCatalog();const serial=generation;const controller=new AbortController();let timer;
 $('plugins-error').hidden=true;
 try{
  const timeout=new Promise((_,reject)=>{timer=setTimeout(()=>{controller.abort();reject(new Error('Catalog request timed out. Retry to inspect the current configuration.'));},5000);});
  const load=(async()=>{const response=await fetch('/api/plugins',{credentials:'same-origin',cache:'no-store',signal:controller.signal});if(response.status===401){endSession();throw new Error('Session ended. Return to tasks to sign in again.');}if(!response.ok)throw new Error('Catalog unavailable. Retry or return to tasks.');return response.json();})();
  const catalog=await Promise.race([load,timeout]);if(ended||serial!==generation)return;
  $('plugins-builtins').append(node('h2','Built-in capabilities'));$('plugins-mcp').append(node('h2','MCP plugins'));
  for(const entry of catalog.entries||[])$(entry.kind==='mcp'?'plugins-mcp':'plugins-builtins').append(renderEntry(entry));
  $('plugins-status').textContent=catalog.mcp_enabled?'MCP registrations loaded from the operator’s startup allowlist.':'MCP disabled: no servers registered in this daemon.';
  $('plugins-protected').hidden=false;
 }catch(e){if(!ended&&serial===generation){clearCatalog();$('plugins-error').textContent=e.message;$('plugins-error').hidden=false;}}finally{clearTimeout(timer);}
}
$('plugins-reload').onclick=reload;
try{if(typeof BroadcastChannel!=='undefined'){const channel=new BroadcastChannel('qi-auth');channel.onmessage=event=>{if(event.data==='logout')endSession();};}}catch(_){}
if(typeof addEventListener==='function'){for(const event of ['focus','pageshow'])addEventListener(event,reload);addEventListener('pagehide',clearCatalog);addEventListener('storage',event=>{if(event.key==='qi-logout')endSession();});}
if(typeof document.addEventListener==='function')document.addEventListener('visibilitychange',()=>{if(document.hidden)clearCatalog();else reload();});
if(typeof setInterval==='function')setInterval(reload,30000);
reload();
