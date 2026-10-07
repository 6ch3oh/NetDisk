'use strict';

// One failed request must not reject the other files in the batch.
async function uploadBatch(files, {limit, upload, changed = () => {}}) {
  if (!Number.isFinite(limit) || limit <= 0) throw new Error('上传限制尚未加载，请刷新后重试。');
  const items = Array.from(files, file => ({file, status: 'waiting', error: '', skipped: false, progress: 0}));
  let completed = 0, next = 0;
  const report = () => changed({items, completed, total: items.length});
  for (const item of items) {
    if (item.file.size > limit) {
      item.status = 'failed';
      item.error = '超过单文件上传限制，已跳过。';
      item.skipped = true;
      completed++;
    }
  }
  report();
  async function worker() {
    while (next < items.length) {
      const item = items[next++];
      if (item.skipped) continue;
      item.status = 'uploading';
      report();
      try {
        await upload(item.file, value => {item.progress = Math.max(0, Math.min(1, value)); report();});
        item.status = 'success';
      } catch (err) {
        item.status = 'failed';
        item.error = err?.message || '上传失败，请重试。';
      } finally {
        completed++;
        report();
      }
    }
  }
  await Promise.all(Array.from({length: Math.min(3, items.length)}, worker));
  return {
    items, completed, total: items.length,
    success: items.filter(item => item.status === 'success').length,
    failed: items.filter(item => item.status === 'failed').length,
    skipped: items.filter(item => item.skipped).length
  };
}

// Upload metadata, opaque IDs and content digests are cached; never login tokens.
// Storage is optional (e.g. private browser mode); the in-memory fallback works.
function browserUploadStorage() {
  try {return typeof sessionStorage==='undefined'?null:sessionStorage;} catch {return null;}
}
function uploadResumeStore(storage) {
  const memory = new Map(), prefix = 'netdisk-upload:';
  return {
    get(key) {try {return JSON.parse(storage?.getItem(prefix + key) || 'null') || memory.get(key);} catch {return memory.get(key);}},
    set(key, value) {memory.set(key, value); try {storage?.setItem(prefix + key, JSON.stringify(value));} catch {}},
    remove(key) {memory.delete(key); try {storage?.removeItem(prefix + key);} catch {}},
    clear() {memory.clear(); try {for(let i=storage.length-1;i>=0;i--){const key=storage.key(i);if(key.startsWith(prefix))storage.removeItem(key);}} catch {}}
  };
}
function uploadRequestKey() {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, '0')).join('');
}
async function uploadDigest(blob) {
  const digest = await crypto.subtle.digest('SHA-256', await blob.arrayBuffer());
  return Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, '0')).join('');
}
async function retryUpload(operation) {
  for(let attempt=0;;attempt++){
    try {return await operation();} catch(err) {
      const transient=err.status===0 || err.status===408 || err.status===429 || err.status>=500;
      if(!transient || attempt===2)throw err;
      await new Promise(resolve=>setTimeout(resolve,200*(attempt+1)));
    }
  }
}
async function uploadFile(file, {folder, owner, api, threshold, partSize, resumes, progress = () => {}}) {
  if(!Number.isFinite(threshold) || threshold<=0)throw new Error('上传设置尚未加载，请刷新后重试。');
  if(file.size<=threshold){
    return api('/api/files?name='+encodeURIComponent(file.name)+'&folder_id='+encodeURIComponent(folder),{method:'POST',headers:{'Content-Type':'application/octet-stream'},body:file});
  }
  if(!Number.isInteger(partSize) || partSize<=0 || partSize>8388608)throw new Error('上传设置无效，请刷新后重试。');
  const key=JSON.stringify([owner,folder,file.name,file.size,file.lastModified]);
  let record=resumes.get(key);
  if(!record || !/^[a-f0-9]{32}$/.test(record.requestKey) || !record.hashes || typeof record.hashes!=='object' || !Number.isInteger(record.partSize) || record.partSize<=0 || record.partSize>8388608)record={requestKey:uploadRequestKey(),hashes:{},partSize};
  partSize=record.partSize;
  if(!Number.isInteger(partSize) || partSize<=0 || partSize>8388608)throw new Error('上传记录无效，请重新选择文件。');
  resumes.set(key,record);
  let session;
  try {
    session=await retryUpload(()=>api('/api/uploads',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:file.name,folder_id:folder,expected_size:file.size,part_size:partSize,request_key:record.requestKey})}));
    if(!/^[a-f0-9]{32}$/.test(session.id) || session.name!==file.name || session.folder_id!==folder || session.expected_size!==file.size || session.part_size!==partSize)throw new Error('上传状态不匹配，请重新选择文件。');
    record.id=session.id;
    const count=Math.ceil(file.size/partSize);
    if(session.file){
      // A lost completion response may return a durable receipt. Check that the
      // newly selected source still matches every previously uploaded part.
      for(let index=0;index<count;index++){
        if(!record.hashes[index] || await uploadDigest(file.slice(index*partSize,Math.min((index+1)*partSize,file.size)))!==record.hashes[index]){
          resumes.remove(key);throw new Error('文件内容已变化，请重新选择后再上传。');
        }
      }
      resumes.remove(key);progress(1);return session.file;
    }
    const status=await retryUpload(()=>api('/api/uploads/'+session.id));
    const existing=new Map(status.parts.map(part=>[part.index,part]));
    let done=0;
    for(let index=0;index<count;index++){
      const blob=file.slice(index*partSize,Math.min((index+1)*partSize,file.size));
      const hash=await uploadDigest(blob);
      const previous=existing.get(index);
      if(previous && (previous.size!==blob.size || previous.sha256!==hash)){
        await api('/api/uploads/'+session.id,{method:'DELETE'});
        resumes.remove(key);throw new Error('文件内容已变化，请重新选择后再上传。');
      }
      if(!previous){
        await retryUpload(()=>api('/api/uploads/'+session.id+'/parts/'+index,{method:'PUT',headers:{'Content-Type':'application/octet-stream'},body:blob}));
      }
      record.hashes[index]=hash;resumes.set(key,record);
      done+=blob.size;progress(done/file.size);
    }
    const result=await retryUpload(()=>api('/api/uploads/'+session.id+'/complete',{method:'POST'}));
    resumes.remove(key);return result;
  } catch(err) {
    // Permanent validation/quota errors cannot be fixed by retrying the same
    // session; release its reservation. Transport failures retain resumability.
    if(err.status===404)resumes.remove(key);
    if(session?.id && [400,409,413,415].includes(err.status)){
      try {await api('/api/uploads/'+session.id,{method:'DELETE'});resumes.remove(key);} catch {}
    }
    throw err;
  }
}
