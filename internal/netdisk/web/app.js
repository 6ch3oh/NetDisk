'use strict';
const $ = (id) => document.getElementById(id);
const state = {user: null, files: [], folders: [], allFolders: [], folder: '', crumbs: [], mode: 'login', busy: false, action: null, limit: null, threshold: null, partSize: null, selected: new Set(), batch: null};
const uploadResumes=uploadResumeStore(browserUploadStorage());
const messages = {'invalid credentials':'用户名或密码不正确，请重试。','username unavailable':'这个用户名已被使用，请换一个。','invalid file name':'名称不能包含路径分隔符、控制字符或点目录。','request verification failed':'请求未通过验证，请刷新页面后重试。','upload too large':'文件超过上传大小限制，请选择较小的文件。','file not found':'文件不存在或已被移除，请刷新列表。','user quota exceeded':'空间配额不足，请先删除不需要的文件或取消上传会话。','invalid or expired code':'验证码错误、过期或已使用。','current password incorrect':'当前密码不正确。','email unavailable':'此邮箱已被使用。','wait before requesting another code':'请等待 30 秒后再获取验证码。','email adapter disabled':'尚未开启本地邮箱演示模式。'};
Object.assign(messages,{'folder not found':'目录不存在或不可访问，请返回根目录。','folder name conflict':'同级已有同名文件夹，请换一个名称。','folder not empty':'文件夹非空，请先移走其中的文件和子文件夹。','folder cycle':'不能移动到自身或自己的子目录。'});
function notice(text, error=false) { $('notice').textContent=text; $('notice').classList.toggle('error',error); $('notice').hidden=!text; $('dialog-error').textContent=error?text:''; $('dialog-error').hidden=!error;for(const id of ['profile-feedback','share-feedback','archives-feedback']){$(id).textContent=text;$(id).hidden=!text;$(id).classList.toggle('error',error);} }
function setMode(mode) {
  state.mode=mode; const register=mode==='register'; $('auth-title').textContent=register?'创建你的空间':'登录你的空间'; $('auth-description').textContent=register?'创建账号，开始存放你的文件。':'继续管理你的文件。'; $('auth-submit').textContent=register?'创建账号':'登录';
  for (const [id,active] of [['login-tab',!register],['register-tab',register]]) {$(id).classList.toggle('active',active);$(id).setAttribute('aria-pressed',String(active));}
  $('password').autocomplete=register?'new-password':'current-password'; $('password').value='';
}
function showUser(user) {
  state.user=user; $('auth-view').hidden=!!user; $('app-view').hidden=!user; $('current-user').textContent=user?(user.display_name||user.username):'';
  if(!user){state.files=[];state.folders=[];state.allFolders=[];state.folder='';state.crumbs=[];state.limit=null;state.threshold=null;state.partSize=null;uploadResumes.clear();$('file-rows').replaceChildren();$('upload-input').value='';$('upload-results').hidden=true;$('upload-list').replaceChildren();$('upload-status').textContent='';if($('action-dialog').open)$('action-dialog').close();$('profile-dialog').close();$('shares-dialog').close();for(const id of ['share-url','nfs-path','email-address','email-code','old-password','new-password'])$(id).value='';$('email-delivery').textContent='';$('nfs-list').replaceChildren();}
  if(!user){clearSelection();clearBatchFeedback();state.batch=null;$('batch-dialog').close();$('batch-rename-fields').replaceChildren();$('batch-items').replaceChildren();}
  if(!user){$('archives-dialog').close();$('archives-list').replaceChildren();}
}
function controls() {document.querySelectorAll('button,input,select').forEach(el=>{el.disabled=state.busy;});$('upload-button').disabled=state.busy||!$('upload-input').files.length;$('up-button').disabled=state.busy||!state.folder;renderSelection();}
async function run(work) {if(state.busy)return;state.busy=true;controls();notice('');try{await work();}catch(err){notice(err.message||'操作未完成，请重试。',true);}finally{state.busy=false;controls();}}
async function api(path, options={}) {
  let response;try{response=await fetch(path,{credentials:'same-origin',...options,headers:{'X-NetDisk-Request':'1',...options.headers}});}catch{const err=new Error('暂时无法连接，请检查连接后重试。');err.status=0;throw err;}
  if(!response.ok){let body={};try{body=await response.json();}catch{}
    if(response.status===401&&state.user){showUser(null);setMode('login');throw new Error('登录已过期，请重新登录。');}
    const err=new Error(messages[body.error]||(response.status===401?'请先登录。':response.status===400?'输入不符合要求，请检查后重试。':response.status>=500?'服务暂时不可用，请稍后重试。':'操作未完成，请刷新后重试。'));err.status=response.status;throw err;
  }
  return response.status===204||options.method==='HEAD'?null:response.json();
}
function json(method,body){return {method,headers:{'Content-Type':'application/json'},body:JSON.stringify(body)};}
async function loadArchives(){
  const result=await api('/api/archives');const holder=$('archives-list');holder.replaceChildren();
  if(!result.archives.length){holder.append(textElement('p','还没有归档记录。完成硬盘备份后，可在客户端选择文件进行归档。','muted'));return;}
  for(const item of result.archives){
    const card=document.createElement('section');card.className='archive-record';
    const path=[...item.file.folders.map(f=>f.name),item.file.name].join(' / ');
    card.append(textElement('h3',item.file.name),textElement('p',path,'muted'));
    card.append(textElement('p',`${size(item.file.size)} · 硬盘：${item.disk_label} · ${new Date(item.archived_at*1000).toLocaleString()}`));
    card.append(textElement('p',item.restored_file_id?'已恢复到在线空间；硬盘副本继续保留。':'已归档到硬盘，接入硬盘后可通过客户端恢复。'));
    const details=document.createElement('details');details.append(textElement('summary','查看恢复信息'));
    details.append(textElement('p','原文件 ID：'+item.file.id),textElement('p','硬盘内位置：'+item.local_path));card.append(details);holder.append(card);
  }
}
$('archives-button').addEventListener('click',()=>run(async()=>{await loadArchives();$('archives-dialog').showModal();}));
$('archives-refresh').addEventListener('click',()=>run(loadArchives));
$('archives-close').addEventListener('click',()=>$('archives-dialog').close());
function size(bytes){if(bytes<1024)return `${bytes} B`;const units=['KiB','MiB','GiB'];let v=bytes/1024,i=0;while(v>=1024&&i<units.length-1){v/=1024;i++;}return `${v.toFixed(v<10?1:0)} ${units[i]}`;}
function textElement(tag,text,className){const e=document.createElement(tag);e.textContent=text;if(className)e.className=className;return e;}
function renderFiles(){
  const rows=$('file-rows');rows.replaceChildren();$('side-count').textContent=state.files.length;
  $('file-summary').textContent=`${state.folders.length} 个文件夹 · ${state.files.length} 个文件 · ${size(state.files.reduce((n,f)=>n+f.size,0))}`;
  const resources=visibleResources();$('empty-state').hidden=resources.length>0;$('file-table').hidden=!resources.length;
  for(const {kind,value} of resources){
    const folder=kind==='folder',row=document.createElement('tr'),nameCell=document.createElement('td'),name=textElement('div','','file-name');
    row.append(selectionCell(row,kind,value));
    const ext=folder?'DIR':value.name.includes('.')?value.name.split('.').pop().slice(0,4).toUpperCase():'FILE';
    name.append(textElement('span',ext||'FILE','file-icon'+(folder?' folder-icon':'')));
    if(folder){const enter=textElement('button',value.name,'folder-link');enter.type='button';enter.setAttribute('aria-label','进入 '+value.name);enter.addEventListener('click',()=>navigate(value.id));name.append(enter);}
    else name.append(textElement('span',value.name));
    nameCell.append(name);
    const uploaded=new Date(value.created_at*1000);
    row.append(nameCell,textElement('td',folder?'文件夹':size(value.size),'muted'),textElement('td',folder?uploaded.toLocaleDateString('zh-CN'):uploaded.toLocaleString('zh-CN',{year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false}),'muted'));
    const actionsCell=document.createElement('td'),actions=textElement('div','','file-actions');
    for(const [label,action] of [[folder?'ZIP 下载':'下载','download'],['分享','share'],['重命名','rename'],['移动','move'],['删除','delete']]){
      const button=textElement('button',label,action==='delete'?'delete-action':'');button.type='button';button.setAttribute('aria-label',`${label}${folder?'文件夹':''} ${value.name}`);
      button.addEventListener('click',()=>action==='download'?run(()=>download(folder?{name:value.name+'.zip',download_url:'/api/folders/'+value.id+'/download'}:value)):action==='share'?run(()=>shareResource(kind,value.id)):openAction(action,value,kind));actions.append(button);
    }
    actionsCell.append(actions);row.append(actionsCell);rows.append(row);
  }
  renderSelection();
}
async function refresh(folder=state.folder, navigation=false){const result=await api('/api/directory?folder_id='+encodeURIComponent(folder));const tree=await api('/api/folders');const stats=await api('/api/me/stats');if(navigation){clearSelection();clearBatchFeedback();}state.folder=folder;state.files=result.files;state.folders=result.folders;state.crumbs=result.breadcrumbs;state.allFolders=tree.folders;renderFiles();renderNavigation();$('quota-summary').textContent=`空间 ${size(stats.used_bytes)} / ${size(stats.quota_bytes)} · 上传预留 ${size(stats.reserved_bytes)} · ${stats.files} 个文件`;$('email-mode').textContent=stats.dev_email?'本地演示模式：验证码只在当前页面显示，不发送真实邮件。':'邮箱演示适配器未开启。';}
function navigate(id){return run(()=>refresh(id,true));}
function renderNavigation(){const holder=$('breadcrumbs');holder.replaceChildren();for(const f of [{id:'',name:'根目录'},...state.crumbs]){const button=textElement('button',f.name,'quiet');button.type='button';button.addEventListener('click',()=>navigate(f.id));holder.append(button);if(f.id===state.folder){button.setAttribute('aria-current','page');}else holder.append(textElement('span','/','muted'));}$('directory-title').textContent=state.crumbs.length?state.crumbs[state.crumbs.length-1].name:'我的文件';$('up-button').disabled=state.busy||!state.folder;}
function folderPath(folder){const parts=[folder.name],seen=new Set([folder.id]);let id=folder.parent_id;while(id){if(seen.has(id))break;seen.add(id);const parent=state.allFolders.find(f=>f.id===id);if(!parent)break;parts.unshift(parent.name);id=parent.parent_id;}return '根目录 / '+parts.join(' / ');}
async function loadSpace(){const config=await api('/api/config');state.limit=config.max_upload_bytes;state.threshold=config.chunk_threshold_bytes;state.partSize=config.chunk_part_bytes;$('upload-limit').textContent=`可多选文件（按住 Ctrl 或 Shift），单文件不超过 ${size(state.limit)}。`;await refresh();}
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
initSelection();
function renderUploads({items,completed,total}) {
  const labels={waiting:'等待',uploading:'上传中',success:'成功',failed:'失败'};
  $('upload-results').hidden=false;
  $('upload-status').textContent=`已完成 ${completed}/总数 ${total}`;
  const rows=items.map(item=>{
    const row=textElement('li','','upload-item');row.dataset.status=item.status;
    const label=item.status==='uploading'&&item.progress>0?`${labels[item.status]} · ${Math.round(item.progress*100)}%`:labels[item.status];
    row.append(textElement('span',item.file.name,'upload-item-name'),textElement('span',label,'upload-item-state'));
    if(item.error)row.append(textElement('span',item.error,'upload-item-error'));
    return row;
  });
  $('upload-list').replaceChildren(...rows);
}
$('upload-form').addEventListener('submit',event=>{event.preventDefault();run(async()=>{
  const files=Array.from($('upload-input').files);if(!files.length)return;
  const folder=state.folder,owner=state.user.id;
  const result=await uploadBatch(files,{limit:state.limit,
    upload:(file,progress)=>uploadFile(file,{folder,owner,api,threshold:state.threshold,partSize:state.partSize,resumes:uploadResumes,progress}),
    changed:progress=>{if(state.user?.id===owner)renderUploads(progress);}
  });
  $('upload-input').value='';
  if(!state.user)return;
  await refresh();
  notice(`批量上传结束：成功 ${result.success}，失败 ${result.failed}${result.skipped?`（${result.skipped} 个超限已跳过）`:''}。`,result.failed>0);
});});
$('dialog-cancel').addEventListener('click',()=>$('action-dialog').close());
$('action-form').addEventListener('submit',event=>{event.preventDefault();run(async()=>{const {action,file,kind}=state.action;const endpoint=kind==='folder'?'/api/folders':'/api/files';if(action==='create')await api('/api/folders',json('POST',{name:$('new-name').value,parent_id:state.folder}));else if(action==='rename')await api(endpoint+'/'+file.id,json('PATCH',{name:$('new-name').value}));else if(action==='move')await api(endpoint+'/'+file.id+'/move',json('POST',kind==='folder'?{parent_id:$('move-target').value}:{folder_id:$('move-target').value}));else await api(endpoint+'/'+file.id,{method:'DELETE'});$('action-dialog').close();await refresh();notice(action==='create'?'文件夹已创建。':action==='rename'?'名称已更新。':action==='move'?'已移动到目标目录。':'已删除。');});});
$('action-dialog').addEventListener('cancel',event=>{if(state.busy)event.preventDefault();});
run(async()=>{try{const user=await api('/api/me');showUser(user);await loadSpace();}catch(err){if(err.message==='请先登录。')return;throw err;}});

async function loadShares(){
 const result=await api('/api/shares');const list=$('share-list');list.replaceChildren();
 if(!result.shares.length)list.append(textElement('p','还没有分享。','muted'));
 for(const share of result.shares){const row=textElement('p',`${share.type==='p2p'?'P2P':share.file_id?'文件':'文件夹'} ${share.file_id||share.folder_id} `);const revoke=textElement('button','撤销','delete-action');revoke.type='button';revoke.addEventListener('click',()=>run(async()=>{if(!confirm('撤销此分享？链接将立即失效。'))return;await api('/api/shares/'+share.id,{method:'DELETE'});$('share-url').value='';await loadShares();notice('分享已撤销。');}));const analytics=textElement('button','访问统计','secondary');analytics.type='button';analytics.addEventListener('click',()=>run(async()=>{const stats=await api('/api/shares/'+share.id+'/analytics');notice(`目录访问 ${stats.views} 次 · 下载请求 ${stats.download_requests} 次 · P2P 连接查询 ${stats.signals} 次`);}));row.append(analytics,revoke);list.append(row);}
}
async function shareResource(kind,id){const created=await api('/api/shares',json('POST',{resource_type:kind,resource_id:id}));$('share-url').value=new URL(created.url,location.origin).href;await loadShares();if(!$('shares-dialog').open)$('shares-dialog').showModal();}
$('shares-button').addEventListener('click',()=>run(async()=>{await loadShares();$('shares-dialog').showModal();}));
$('shares-close').addEventListener('click',()=>$('shares-dialog').close());
$('copy-share-button').addEventListener('click',async()=>{if(!$('share-url').value)return;try{await navigator.clipboard.writeText($('share-url').value);notice('链接已复制。');}catch{$('share-url').focus();$('share-url').select();notice('请复制已选中的链接。');}});
$('profile-button').addEventListener('click',()=>run(async()=>{$('display-name').value=state.user.display_name||'';$('email-address').value=state.user.email||'';await loadExports();const policy=await api('/api/storage/candidates');$('storage-candidates').textContent=policy.candidates.length?`${policy.candidates.length} 个文件符合迁移条件。`:'暂无符合条件的文件。';$('profile-dialog').showModal();}));
$('profile-close').addEventListener('click',()=>$('profile-dialog').close());
$('profile-form').addEventListener('submit',event=>{event.preventDefault();run(async()=>{showUser(await api('/api/me',json('PATCH',{display_name:$('display-name').value})));$('profile-dialog').close();notice('显示名称已更新。');});});
$('delete-account-button').addEventListener('click',()=>run(async()=>{if(!confirm('永久删除当前账号、文件、文件夹、上传会话和分享？此操作无法撤销。'))return;await api('/api/me',{method:'DELETE'});showUser(null);setMode('login');notice('账号已删除。');}));
$('email-code-button').addEventListener('click',()=>run(async()=>{const result=await api('/api/me/email-code',json('POST',{email:$('email-address').value}));$('email-delivery').textContent=`本地演示验证码：${result.dev_code}（10 分钟有效，单次使用）`;}));
$('email-form').addEventListener('submit',event=>{event.preventDefault();run(async()=>{await api('/api/me/email',json('POST',{email:$('email-address').value,code:$('email-code').value}));state.user=await api('/api/me');$('email-code').value='';$('email-delivery').textContent='';notice('邮箱绑定成功。');});});
$('password-form').addEventListener('submit',event=>{event.preventDefault();run(async()=>{try{await api('/api/me/password',json('POST',{old_password:$('old-password').value,new_password:$('new-password').value}));showUser(null);setMode('login');notice('密码已更新，请重新登录。');}finally{$('old-password').value='';$('new-password').value='';}});});
async function loadExports(){const result=await api('/api/nfs/exports');$('nfs-list').replaceChildren();for(const item of result.exports){const row=textElement('p',`导出 ${item.id.slice(0,8)} · 有效至 ${new Date(item.expires_at*1000).toLocaleString()} `);const revoke=textElement('button','撤销导出','secondary');revoke.type='button';revoke.addEventListener('click',()=>run(async()=>{await api('/api/nfs/exports/'+item.id,{method:'DELETE'});$('nfs-path').value='';await loadExports();notice('NFS 导出已撤销。');}));row.append(revoke);$('nfs-list').append(row);}}
$('nfs-create').addEventListener('click',()=>run(async()=>{const result=await api('/api/nfs/exports',{method:'POST'});$('nfs-path').value=result.mount_path;await loadExports();notice('NFS 导出已创建。');}));
