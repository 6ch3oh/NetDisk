'use strict';
// Explicitly targets a disposable loopback server and project-local synthetic data.
const fs=require('node:fs');const path=require('node:path');const crypto=require('node:crypto');const {spawnSync}=require('node:child_process');
const base=process.argv[2];
if(!/^http:\/\/127\.0\.0\.1:\d+$/.test(base||''))throw Error('explicit isolated loopback origin required');
const project=path.resolve(__dirname,'..');const work=fs.mkdtempSync(path.join(project,'.tmp','disk-native-'));
const binary=process.env.NETDISK_PULL_TEST_BINARY||path.join(project,'bin',process.platform==='win32'?'netdisk-sync.exe':'netdisk-sync');
const disk=path.join(work,'removable');fs.mkdirSync(disk);const state=path.join(work,'binding.json');
const username='disk_'+crypto.randomBytes(6).toString('hex');const password='synthetic-disk-smoke-password';let cookie='';
function assert(value,message){if(!value)throw Error(message);}
async function api(method,route,body,expected=200){
 const raw=Buffer.isBuffer(body);const response=await fetch(base+route,{method,headers:{'X-NetDisk-Request':'1','Content-Type':raw?'application/octet-stream':'application/json',...(cookie?{Cookie:cookie}:{})},body:body===undefined?undefined:raw?body:JSON.stringify(body),redirect:'manual'});
 assert(response.status===expected,`HTTP fixture status ${response.status}, expected ${expected}`);
 if(route==='/api/login')cookie=response.headers.get('set-cookie').split(';')[0];
 if(expected===204)return null;if(route.endsWith('/download'))return Buffer.from(await response.arrayBuffer());return response.json();
}
function cli(mode,extra=[]){
 const out=spawnSync(binary,['-mode',mode,'-server',base,'-username',username,'-root',disk,'-state',state,'-once',...extra],{cwd:project,env:{...process.env,NETDISK_SYNC_PASSWORD:password},encoding:'utf8',windowsHide:true,timeout:120000});
 assert(!out.error&&out.status===0,`native ${mode} failed: ${out.stderr||out.error||out.status}`);return out.stdout;
}
function manifest(){return JSON.parse(fs.readFileSync(path.join(disk,'.netdisk-manifest.json'),'utf8'));}
function local(id){const s=manifest();return s.versions[s.latest[id]];}
(async()=>{
 await api('POST','/api/register',{username,password},201);await api('POST','/api/login',{username,password});
 const folder=await api('POST','/api/folders',{name:'硬盘备份演示',parent_id:''},201);
 const payload=Buffer.alloc(1048613,90);const digest=crypto.createHash('sha256').update(payload).digest('hex');
 const big=await api('POST','/api/files?name=synthetic-large.bin',payload,201);
 const empty=await api('POST','/api/files?name=empty.txt',Buffer.alloc(0),201);
 const small=await api('POST','/api/files?folder_id='+folder.id+'&name='+encodeURIComponent('归档演示.txt'),Buffer.from('synthetic archived text'),201);
 assert(cli('download',['-init-disk','-disk-label','Smoke removable disk']).includes('downloaded=3'),'initial native download failed');
 const saved=fs.readFileSync(path.join(disk,local(big.id).local_path));assert(crypto.createHash('sha256').update(saved).digest('hex')===digest,'native download hash differs');
 assert(cli('download').includes('skipped=3'),'restart duplicated downloads');
 await api('DELETE','/api/files/'+empty.id,undefined,204);cli('download');assert(fs.existsSync(path.join(disk,local(empty.id).local_path)),'remote deletion removed local copy');
 cli('archive',['-file-id',big.id]);await api('GET','/api/files/'+big.id+'/download',undefined,404);
 cli('restore',['-file-id',big.id]);const restored=local(big.id).restored_file_id;
 const downloaded=await api('GET','/api/files/'+restored+'/download');assert(crypto.createHash('sha256').update(downloaded).digest('hex')===digest,'native restore hash differs');
 cli('restore',['-file-id',big.id]);assert(local(big.id).restored_file_id===restored,'native restore duplicated file');
 cli('archive',['-file-id',small.id]);assert(cli('status').includes('archived=true'),'status lacks archive state');
 const records=await api('GET','/api/archives');assert(records.archives.length===2&&records.archives.some(x=>x.restored_file_id===restored),'server archive records differ');
 const fixture={base,username,work,archivedName:small.name};fs.writeFileSync(path.join(work,'browser-fixture.json'),JSON.stringify(fixture,null,2));
 console.log('NATIVE_DOWNLOAD_ARCHIVE_RESTORE=PASS; files=3; binary_hash_match=true; restart=true; remote_delete_retained=true; restore_idempotent=true; archives=2');
})().catch(err=>{console.error(err.message);process.exitCode=1;});
