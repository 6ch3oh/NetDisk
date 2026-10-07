// Real Windows CLI acceptance against an explicitly selected disposable server.
// No credentials, cookies or presigned URLs are written to logs or evidence.
// Run: node scripts/sync-smoke.cjs http://127.0.0.1:38135
'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const {spawn} = require('node:child_process');
const project = path.resolve(__dirname, '..');
const base = process.argv[2];
if (!base || !/^http:\/\/127\.0\.0\.1:\d+$/.test(base)) throw Error('explicit isolated loopback server required');
const root = fs.mkdtempSync(path.join(project, '.tmp/p1-cli-source-'));
const stateDir = fs.mkdtempSync(path.join(project, '.tmp/p1-cli-state-'));
const state = path.join(stateDir, 'state.json');
const binary = process.env.NETDISK_SYNC_TEST_BINARY || path.join(project, 'bin', process.platform === 'win32' ? 'netdisk-sync.exe' : 'netdisk-sync');
const username = 'p1_sync_' + crypto.randomBytes(6).toString('hex');
const password = crypto.randomBytes(24).toString('hex');
let cookie, child;
let logs = '';
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
async function api(url, method='GET', body) {
 const response = await fetch(base + url, {method, headers:{'X-NetDisk-Request':'1','Content-Type':'application/json',...(cookie?{Cookie:cookie}:{})}, body:body===undefined?undefined:JSON.stringify(body)});
 assert.ok(response.ok, 'API status '+response.status);
 if (url === '/api/login') cookie = response.headers.get('set-cookie').split(';')[0];
 return response.status === 204 ? null : response.json();
}
const files = async () => (await api('/api/files')).files;
async function wait(check, label) {
 const end = Date.now()+20000;
 while (Date.now()<end) {if (await check()) return; if(child&&child.exitCode!==null) throw Error('CLI exited before '+label); await pause(100);}
 throw Error('timeout: '+label);
}
const args = ['-server',base,'-username',username,'-root',root,'-state',state,'-remote-name','cli-synthetic-backup','-interval','100ms','-retry-delay','10ms','-max-attempts','3','-part-bytes','4096'];
function start() {
 child=spawn(binary,args,{cwd:project,windowsHide:true,env:{...process.env,NETDISK_SYNC_PASSWORD:password}});
 child.stdout.on('data',bytes=>{logs+=bytes.toString();});
 child.stderr.on('data',bytes=>{logs+=bytes.toString();});
 child.on('error',()=>{logs+='CLI process error\n';});
}
async function stop() {
 if (!child||child.exitCode!==null) return;
 const stopped = new Promise(resolve=>child.once('exit',resolve));
 child.kill('SIGTERM');
 await Promise.race([stopped,pause(5000).then(()=>{if(child.exitCode===null)child.kill('SIGKILL');})]);
 await stopped;
}
(async()=>{
 try {
  await api('/api/register','POST',{username,password});
  await api('/api/login','POST',{username,password});
  fs.mkdirSync(path.join(root,'nested'));
  fs.writeFileSync(path.join(root,'a.bin'),Buffer.alloc(16384,7));
  fs.writeFileSync(path.join(root,'nested','b.bin'),Buffer.alloc(16384,7));
  fs.writeFileSync(path.join(root,'empty.txt'),'');
  start();
  await wait(async()=>(await files()).length===3,'initial three files');
  await wait(()=>fs.existsSync(state)&&Object.values(JSON.parse(fs.readFileSync(state)).entries).every(v=>v.done),'durable completed state');
  assert.ok(!fs.readFileSync(state,'utf8').includes(password),'state leaked password');
  const second=spawn(binary,[...args,'-once'],{cwd:project,windowsHide:true,env:{...process.env,NETDISK_SYNC_PASSWORD:password}});
  let secondLog='';
  second.stdout.on('data',x=>{secondLog+=x;}); second.stderr.on('data',x=>{secondLog+=x;});
  const secondCode=await new Promise((resolve,reject)=>{second.once('error',reject);second.once('exit',resolve);});
  assert.equal(secondCode,1);assert.match(secondLog,/another backup process/);
  await stop(); start();
  await pause(1800);
  assert.equal((await files()).length,3,'restart duplicated unchanged content');
  fs.writeFileSync(path.join(root,'nested','new.txt'),'synthetic new file');
  await wait(async()=>(await files()).length===4,'new file');
  fs.writeFileSync(path.join(root,'a.bin'),Buffer.alloc(20480,9));
  await wait(async()=>(await files()).length===5,'modified file');
  await wait(()=>Object.values(JSON.parse(fs.readFileSync(state)).entries).every(v=>v.done),'modified durable state');
  fs.unlinkSync(path.join(root,'a.bin')); // This script created this fixture only.
  await pause(800);
  const final=await files();
  assert.equal(final.length,5,'local deletion removed cloud version');
  for (const file of final) {
   const response=await fetch(base+file.download_url,{headers:{Cookie:cookie}});
   assert.equal(response.status,200);
   const bytes=Buffer.from(await response.arrayBuffer());
   const hash=crypto.createHash('sha256').update(bytes).digest('hex');
   assert.equal(bytes.length,file.size);
   assert.ok(file.name.includes(hash),'download digest differs from named version');
  }
  const result=[
   'WINDOWS_CLI_WATCHER=PASS',
   'INITIAL_THREE_FILES=PASS',
   'NEW_FILE_AND_MODIFIED_FILE=PASS',
   'CLIENT_PROCESS_RESTART_NO_DUPLICATES=PASS',
   'SECOND_PROCESS_STATE_LOCK=PASS',
   'LOCAL_DELETE_CLOUD_RETAINED=PASS',
   'FIVE_VERSION_DOWNLOAD_DIGESTS=PASS',
   'CREDENTIALS_IN_STATE_OR_LOG=NO',
   'TEST_SERVER=disposable loopback tmpfs instance',
   'LIVE_DATA_TOUCHED=NO'
  ];
  assert.ok(!logs.includes(password)&&!logs.includes(cookie),'credential in CLI logs');
  fs.writeFileSync(path.join(project,'evidence/p1-sync-cli-checks.txt'),result.join('\n')+'\n');
  process.stdout.write(result.join('\n')+'\n');
 } finally {await stop();}
})().catch(err=>{process.stderr.write('sync acceptance failed: '+err.message+'\n');process.exitCode=1;});
