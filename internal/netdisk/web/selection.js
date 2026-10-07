'use strict';
const batchSharedLinks=new Map();
function visibleResources() {
  return [...state.folders.map(value=>({kind:'folder',value,key:'folder:'+value.id})),...state.files.map(value=>({kind:'file',value,key:'file:'+value.id}))];
}
function selectedResources() {return visibleResources().filter(item=>state.selected.has(item.key));}
function renderSelection() {
  const resources=visibleResources(),available=new Set(resources.map(item=>item.key));
  for(const key of state.selected)if(!available.has(key))state.selected.delete(key);
  const count=state.selected.size,total=resources.length;
  $('selection-count').textContent=count?`已选择 ${count} 项`:'未选择文件';
  $('select-all').checked=total>0&&count===total;
  $('select-all').indeterminate=count>0&&count<total;
  $('select-all').disabled=state.busy||!total;
  $('selection-clear').disabled=state.busy||!count;
  for(const action of ['download','share','rename','move','delete'])$('batch-'+action).disabled=state.busy||!count;
  document.querySelectorAll('[data-select-key]').forEach(box=>{box.checked=state.selected.has(box.dataset.selectKey);box.disabled=state.busy;});
  document.querySelectorAll('[data-resource-key]').forEach(row=>{row.dataset.selected=String(state.selected.has(row.dataset.resourceKey));});
}
function selectionCell(row,kind,value) {
  const key=kind+':'+value.id,cell=textElement('td','','selection-cell'),box=document.createElement('input');
  row.dataset.resourceKey=key;box.type='checkbox';box.dataset.selectKey=key;
  box.setAttribute('aria-label',`选择${kind==='folder'?'文件夹':'文件'} ${value.name}`);
  box.addEventListener('change',()=>{if(state.busy)return;if(box.checked)state.selected.add(key);else state.selected.delete(key);renderSelection();});
  cell.append(box);return cell;
}
function clearSelection() {state.selected.clear();renderSelection();}
function clearBatchFeedback() {batchSharedLinks.clear();$('batch-feedback').hidden=true;$('batch-progress').textContent='';$('batch-results').replaceChildren();$('batch-live-status').textContent='';}
function selectionDownloadURL(items) {
  if(!items.length||items.length>200)throw new Error('每次打包请选择 1–200 项。');
  const fileIDs=items.filter(item=>item.kind==='file').map(item=>item.value.id).join(',');
  const folderIDs=items.filter(item=>item.kind==='folder').map(item=>item.value.id).join(',');
  return '/api/download-selection?file_ids='+encodeURIComponent(fileIDs)+'&folder_ids='+encodeURIComponent(folderIDs);
}
function allowedMoveTargets(items) {
  const selectedFolders=new Set(items.filter(item=>item.kind==='folder').map(item=>item.value.id));
  const tree=new Map(state.allFolders.map(folder=>[folder.id,folder]));
  return [{id:'',name:'根目录'},...state.allFolders.filter(folder=>{
    let id=folder.id;const seen=new Set();
    while(id){if(selectedFolders.has(id)||seen.has(id))return false;seen.add(id);id=tree.get(id)?.parent_id||'';}
    return true;
  })];
}
function openBatch(action) {
  if(state.busy)return;
  const items=selectedResources();if(!items.length)return;
  notice('');state.batch={action,items,owner:state.user.id};
  const labels={rename:'批量重命名',move:'批量移动',delete:'确认批量删除？',share:'批量分享'};
  $('batch-title').textContent=labels[action];
  $('batch-description').textContent=action==='delete'?`永久删除已选择的 ${items.length} 项。选中的文件夹及其所有内容也会删除，此操作无法撤销。`:action==='rename'?'为每一项填写新名称，保存后逐项显示结果。':action==='move'?`将已选择的 ${items.length} 项移动到同一目录。`:'为每一项创建独立分享链接。新链接仅在本次结果中显示，请及时复制。';
  $('batch-items').replaceChildren();$('batch-rename-fields').replaceChildren();
  for(const item of items){
    if(action==='rename'){
      const label=textElement('label',item.value.name),input=document.createElement('input');
      input.value=item.value.name;input.required=true;input.maxLength=255;input.dataset.renameKey=item.key;
      label.append(input);$('batch-rename-fields').append(label);
    }else $('batch-items').append(textElement('li',item.value.name));
  }
  const moving=action==='move';$('batch-target').hidden=!moving;$('batch-target-label').hidden=!moving;
  if(moving){$('batch-target').replaceChildren();for(const folder of allowedMoveTargets(items)){const option=textElement('option',folder.id?folderPath(folder):folder.name);option.value=folder.id;$('batch-target').append(option);}$('batch-target').value=state.folder;}
  $('batch-live-status').textContent='';$('batch-submit').textContent=action==='delete'?'确认删除':action==='share'?'创建分享':action==='rename'?'保存名称':'确认移动';
  $('batch-submit').className=action==='delete'?'primary danger':'primary';
  $('batch-dialog').showModal();$('batch-cancel').focus();
}
function renderBatchResults(items,completed) {
  $('batch-feedback').hidden=false;$('batch-results').replaceChildren();
  const summary=`已完成 ${completed}/总数 ${items.length}`;$('batch-progress').textContent=summary;$('batch-live-status').textContent=summary;
  const labels={waiting:'等待',running:'处理中',success:'成功',failed:'失败'};
  for(const item of items)if(item.status==='success'&&item.url)batchSharedLinks.set(item.key,{...item});
  const current=new Set(items.map(item=>item.key));
  const display=[...items,...Array.from(batchSharedLinks.values()).filter(item=>!current.has(item.key)).map(item=>({...item,retained:true}))];
  for(const item of display){
    const row=textElement('li','','batch-result');row.dataset.status=item.status;
    row.append(textElement('span',item.value.name,'batch-result-name'),textElement('span',item.retained?'已创建链接':labels[item.status],'batch-result-state'));
    if(item.error)row.append(textElement('span',item.error,'batch-result-error'));
    if(item.url){
      const link=document.createElement('input');link.readOnly=true;link.value=item.url;link.setAttribute('aria-label','分享链接 '+item.value.name);
      const copy=textElement('button','复制链接','secondary');copy.type='button';
      copy.addEventListener('click',async()=>{try{await navigator.clipboard.writeText(item.url);notice('链接已复制。');}catch{link.focus();link.select();notice('请复制已选中的链接。');}});
      row.append(link,copy);
    }
    $('batch-results').append(row);
  }
}
async function executeBatch(batch,names,target) {
  if(batch.action!=='share')batchSharedLinks.clear();
  const results=batch.items.map(item=>({...item,status:'waiting',error:''}));
  let completed=0;renderBatchResults(results,completed);
  for(const item of results){
    item.status='running';if(state.user?.id===batch.owner)renderBatchResults(results,completed);
    try{
      if(state.user?.id!==batch.owner)throw new Error('登录已过期，请重新登录。');
      const endpoint=(item.kind==='folder'?'/api/folders/':'/api/files/')+item.value.id;
      if(batch.action==='delete')await api(endpoint+(item.kind==='folder'?'/tree':''),{method:'DELETE'});
      else if(batch.action==='rename')await api(endpoint,json('PATCH',{name:names.get(item.key)}));
      else if(batch.action==='move')await api(endpoint+'/move',json('POST',item.kind==='folder'?{parent_id:target}:{folder_id:target}));
      else if(batch.action==='share'){
        const created=await api('/api/shares',json('POST',{resource_type:item.kind,resource_id:item.value.id}));
        item.url=new URL(created.url,location.origin).href;
      }
      item.status='success';
      if(batch.action!=='share')state.selected.delete(item.key);
    }catch(err){item.status='failed';item.error=err.message||'操作未完成，请重试。';}
    completed++;if(state.user?.id===batch.owner)renderBatchResults(results,completed);
  }
  $('batch-dialog').close();state.batch=null;
  if(state.user?.id!==batch.owner)return;
  const failed=results.filter(item=>item.status==='failed').length;
  if(batch.action==='share'&&failed)for(const item of results)if(item.status==='success')state.selected.delete(item.key);
  await refresh();
  const retained=results.filter(item=>item.status==='failed'&&state.selected.has(item.key)).length;
  notice(`批量操作结束：成功 ${results.length-failed}，失败 ${failed}。${failed?(retained?'失败项已保留选择，可检查结果后重试。':'请查看逐项结果。'):''}`,failed>0);
}
function initSelection() {
  $('select-all').addEventListener('change',()=>{if(state.busy)return;state.selected.clear();if($('select-all').checked)for(const item of visibleResources())state.selected.add(item.key);renderSelection();});
  $('selection-clear').addEventListener('click',()=>{if(!state.busy)clearSelection();});
  $('batch-download').addEventListener('click',()=>run(async()=>{const items=selectedResources();if(!items.length)return;await download({name:'网盘文件.zip',download_url:selectionDownloadURL(items)});}));
  for(const action of ['rename','move','delete','share'])$('batch-'+action).addEventListener('click',()=>openBatch(action));
  $('batch-cancel').addEventListener('click',()=>{if(!state.busy){$('batch-dialog').close();state.batch=null;}});
  $('batch-dialog').addEventListener('cancel',event=>{if(state.busy)event.preventDefault();else state.batch=null;});
  $('batch-form').addEventListener('submit',event=>{event.preventDefault();if(state.busy||!state.batch)return;const batch=state.batch,names=new Map();document.querySelectorAll('[data-rename-key]').forEach(input=>names.set(input.dataset.renameKey,input.value));const target=$('batch-target').value;run(()=>executeBatch(batch,names,target));});
}
