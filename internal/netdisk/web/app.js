'use strict';
const $ = (id) => document.getElementById(id);
const state = {user: null, files: [], folders: [], allFolders: [], folder: '', crumbs: [], mode: 'login', busy: false, action: null, limit: null};
const messages = {'invalid credentials':'用户名或密码不正确，请重试。','username unavailable':'这个用户名已被使用，请换一个。','invalid file name':'名称不能包含路径分隔符、控制字符或点目录。','request verification failed':'请求未通过验证，请刷新页面后重试。','upload too large':'文件超过上传大小限制，请选择较小的文件。','file not found':'文件不存在或已被移除，请刷新列表。'};
Object.assign(messages,{'folder not found':'目录不存在或不可访问，请返回根目录。','folder name conflict':'同级已有同名文件夹，请换一个名称。','folder not empty':'文件夹非空，请先移走其中的文件和子文件夹。','folder cycle':'不能移动到自身或自己的子目录。'});
function notice(text, error=false) { $('notice').textContent=text; $('notice').classList.toggle('error',error); $('notice').hidden=!text; $('dialog-error').textContent=error?text:''; $('dialog-error').hidden=!error; }
function setMode(mode) {
  state.mode=mode; const register=mode==='register'; $('auth-title').textContent=register?'创建你的空间':'登录你的空间'; $('auth-description').textContent=register?'创建账号，开始存放你的文件。':'继续管理你的文件。'; $('auth-submit').textContent=register?'创建账号':'登录';
  for (const [id,active] of [['login-tab',!register],['register-tab',register]]) {$(id).classList.toggle('active',active);$(id).setAttribute('aria-pressed',String(active));}
  $('password').autocomplete=register?'new-password':'current-password'; $('password').value='';
}
function showUser(user) {
  state.user=user; $('auth-view').hidden=!!user; $('app-view').hidden=!user; $('current-user').textContent=user?user.username:'';
  if(!user){state.files=[];state.folders=[];state.allFolders=[];state.folder='';state.crumbs=[];state.limit=null;$('file-rows').replaceChildren();$('upload-input').value='';if($('action-dialog').open)$('action-dialog').close();}
}
function controls() {document.querySelectorAll('button,input,select').forEach(el=>{el.disabled=state.busy;});$('upload-button').disabled=state.busy||!$('upload-input').files.length;$('up-button').disabled=state.busy||!state.folder;}
async function run(work) {if(state.busy)return;state.busy=true;controls();notice('');try{await work();}catch(err){notice(err.message||'操作未完成，请重试。',true);}finally{state.busy=false;controls();}}
async function api(path, options={}) {
  let response;try{response=await fetch(path,{credentials:'same-origin',...options,headers:{'X-NetDisk-Request':'1',...options.headers}});}catch{throw new Error('暂时无法连接，请检查连接后重试。');}
  if(!response.ok){let body={};try{body=await response.json();}catch{}
    if(response.status===401&&state.user){showUser(null);setMode('login');throw new Error('登录已过期，请重新登录。');}
    throw new Error(messages[body.error]||(response.status===401?'请先登录。':response.status===400?'输入不符合要求，请检查后重试。':response.status>=500?'服务暂时不可用，请稍后重试。':'操作未完成，请刷新后重试。'));
  }
  return response.status===204||options.method==='HEAD'?null:response.json();
}
function json(method,body){return {method,headers:{'Content-Type':'application/json'},body:JSON.stringify(body)};}
function size(bytes){if(bytes<1024)return `${bytes} B`;const units=['KiB','MiB','GiB'];let v=bytes/1024,i=0;while(v>=1024&&i<units.length-1){v/=1024;i++;}return `${v.toFixed(v<10?1:0)} ${units[i]}`;}
function textElement(tag,text,className){const e=document.createElement(tag);e.textContent=text;if(className)e.className=className;return e;}
function renderFiles(){
  const rows=$('file-rows');rows.replaceChildren();$('side-count').textContent=state.files.length;$('file-summary').textContent=`${state.folders.length} 个文件夹 · ${state.files.length} 个文件 · ${size(state.files.reduce((n,f)=>n+f.size,0))}`;
  $('empty-state').hidden=state.files.length+state.folders.length>0;$('file-table').hidden=state.files.length+state.folders.length===0;
  for(const folder of state.folders){const row=document.createElement('tr'),cell=document.createElement('td'),name=textElement('div','','file-name');const enter=textElement('button',folder.name,'folder-link');enter.type='button';enter.setAttribute('aria-label','进入 '+folder.name);enter.addEventListener('click',()=>navigate(folder.id));name.append(textElement('span','DIR','file-icon folder-icon'),enter);cell.append(name);row.append(cell,textElement('td','文件夹','muted'),textElement('td',new Date(folder.created_at*1000).toLocaleDateString('zh-CN'),'muted'));const actionCell=document.createElement('td'),actions=textElement('div','','file-actions');for(const [label,action] of [['重命名','rename'],['移动','move'],['删除','delete']]){const button=textElement('button',label,action==='delete'?'delete-action':'');button.type='button';button.setAttribute('aria-label',`${label}文件夹 ${folder.name}`);button.addEventListener('click',()=>openAction(action,folder,'folder'));actions.append(button);}actionCell.append(actions);row.append(actionCell);rows.append(row);}
  for(const file of state.files){
    const row=document.createElement('tr'),nameCell=document.createElement('td'),name=textElement('div','','file-name');
    const ext=file.name.includes('.')?file.name.split('.').pop().slice(0,4).toUpperCase():'FILE';name.append(textElement('span',ext||'FILE','file-icon'),textElement('span',file.name));nameCell.append(name);row.append(nameCell,textElement('td',size(file.size),'muted'),textElement('td',new Date(file.created_at*1000).toLocaleString('zh-CN',{year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false}),'muted'));
    const actionsCell=document.createElement('td'),actions=textElement('div','','file-actions');
    for(const [label,action] of [['下载','download'],['重命名','rename'],['移动','move'],['删除','delete']]){const button=textElement('button',label,action==='delete'?'delete-action':'');button.type='button';button.setAttribute('aria-label',`${label} ${file.name}`);button.addEventListener('click',()=>action==='download'?run(()=>download(file)):openAction(action,file));actions.append(button);}
    actionsCell.append(actions);row.append(actionsCell);rows.append(row);
  }
}
async function refresh(){const result=await api('/api/directory?folder_id='+encodeURIComponent(state.folder));const tree=await api('/api/folders');state.files=result.files;state.folders=result.folders;state.crumbs=result.breadcrumbs;state.allFolders=tree.folders;renderFiles();renderNavigation();}
function navigate(id){return run(async()=>{state.folder=id;await refresh();});}
function renderNavigation(){const holder=$('breadcrumbs');holder.replaceChildren();for(const f of [{id:'',name:'根目录'},...state.crumbs]){const button=textElement('button',f.name,'quiet');button.type='button';button.addEventListener('click',()=>navigate(f.id));holder.append(button);if(f.id===state.folder){button.setAttribute('aria-current','page');}else holder.append(textElement('span','/','muted'));}$('directory-title').textContent=state.crumbs.length?state.crumbs[state.crumbs.length-1].name:'我的文件';$('up-button').disabled=state.busy||!state.folder;}
function folderPath(folder){const parts=[folder.name],seen=new Set([folder.id]);let id=folder.parent_id;while(id){if(seen.has(id))break;seen.add(id);const parent=state.allFolders.find(f=>f.id===id);if(!parent)break;parts.unshift(parent.name);id=parent.parent_id;}return '根目录 / '+parts.join(' / ');}
async function loadSpace(){const config=await api('/api/config');state.limit=config.max_upload_bytes;$('upload-limit').textContent=`单文件不超过 ${size(state.limit)}，保留原始内容。`;await refresh();}
async function download(file){
  await api(file.download_url,{method:'HEAD'});
  const link=document.createElement('a');link.href=file.download_url;link.download=file.name;document.body.append(link);link.click();link.remove();notice('已发起下载，请查看浏览器下载列表。');
}
function openAction(action,file={name:'',id:''},kind='file'){
  if(state.busy)return;notice('');state.action={action,file,kind};const naming=action==='rename'||action==='create',moving=action==='move',label=kind==='folder'?'文件夹':'文件';
  $('dialog-title').textContent=action==='create'?'新建文件夹':action==='rename'?'重命名'+label:moving?'移动'+label:'确认删除'+label+'？';
  $('dialog-description').textContent=action==='create'?'在当前目录中新建文件夹。':moving?`将“${file.name}”移动到所选目录，内容不会改变。`:action==='rename'?`为“${file.name}”设置新名称。`:kind==='folder'?`删除“${file.name}”。仅支持空文件夹，非空时请先移走内容。`:`将永久删除“${file.name}”。此操作无法撤销。`;
  $('name-label').hidden=!naming;$('new-name').hidden=!naming;$('new-name').required=naming;$('new-name').value=action==='rename'?file.name:'';$('target-label').hidden=!moving;$('move-target').hidden=!moving;
  if(moving){$('move-target').replaceChildren();for(const f of [{id:'',name:'根目录'},...state.allFolders]){const option=textElement('option',f.id?folderPath(f):'根目录');option.value=f.id;$('move-target').append(option);}$('move-target').value=kind==='folder'?file.parent_id:file.folder_id||'';}
  $('dialog-submit').textContent=action==='create'?'创建文件夹':moving?'确认移动':naming?'保存名称':'确认删除';$('dialog-submit').className=action==='delete'?'primary danger':'primary';$('action-dialog').showModal();if(naming){$('new-name').focus();$('new-name').select();}else{$('dialog-cancel').focus();}
}
$('login-tab').addEventListener('click',()=>{setMode('login');notice('');});$('register-tab').addEventListener('click',()=>{setMode('register');notice('');});
$('auth-form').addEventListener('submit',event=>{event.preventDefault();run(async()=>{
  const body={username:$('username').value,password:$('password').value};
  try{if(state.mode==='register'){const n=new TextEncoder().encode(body.password).length;if(n<10||n>72)throw new Error('密码需为 10–72 字节，请调整长度。');await api('/api/register',json('POST',body));setMode('login');notice('注册成功，请使用新账号登录。');}
  else{const user=await api('/api/login',json('POST',body));showUser(user);await loadSpace();notice('登录成功，欢迎回来。');}}finally{$('password').value='';body.password='';}
});});
$('logout-button').addEventListener('click',()=>run(async()=>{await api('/api/logout',{method:'POST'});showUser(null);setMode('login');notice('已安全退出登录。');}));
$('refresh-button').addEventListener('click',()=>run(async()=>{await refresh();notice('列表已更新。');}));$('home-button').addEventListener('click',()=>navigate(''));$('up-button').addEventListener('click',()=>navigate(state.crumbs.length?state.crumbs[state.crumbs.length-1].parent_id:''));$('new-folder-button').addEventListener('click',()=>openAction('create',undefined,'folder'));
$('upload-input').addEventListener('change',controls);
$('upload-form').addEventListener('submit',event=>{event.preventDefault();run(async()=>{
  const file=$('upload-input').files[0];if(!file)return;if(state.limit&&file.size>state.limit)throw new Error(`文件超过 ${size(state.limit)} 的上传限制。`);
  $('upload-status').hidden=false;$('upload-status').textContent=`正在上传“${file.name}”，请稍候…`;
  try{await api('/api/files?name='+encodeURIComponent(file.name)+'&folder_id='+encodeURIComponent(state.folder),{method:'POST',headers:{'Content-Type':'application/octet-stream'},body:file});$('upload-input').value='';await refresh();notice('文件上传成功。');}finally{$('upload-status').hidden=true;}
});});
$('dialog-cancel').addEventListener('click',()=>$('action-dialog').close());
$('action-form').addEventListener('submit',event=>{event.preventDefault();run(async()=>{const {action,file,kind}=state.action;const endpoint=kind==='folder'?'/api/folders':'/api/files';if(action==='create')await api('/api/folders',json('POST',{name:$('new-name').value,parent_id:state.folder}));else if(action==='rename')await api(endpoint+'/'+file.id,json('PATCH',{name:$('new-name').value}));else if(action==='move')await api(endpoint+'/'+file.id+'/move',json('POST',kind==='folder'?{parent_id:$('move-target').value}:{folder_id:$('move-target').value}));else await api(endpoint+'/'+file.id,{method:'DELETE'});$('action-dialog').close();await refresh();notice(action==='create'?'文件夹已创建。':action==='rename'?'名称已更新。':action==='move'?'已移动到目标目录。':'已删除。');});});
$('action-dialog').addEventListener('cancel',event=>{if(state.busy)event.preventDefault();});
run(async()=>{try{const user=await api('/api/me');showUser(user);await loadSpace();}catch(err){if(err.message==='请先登录。')return;throw err;}});
