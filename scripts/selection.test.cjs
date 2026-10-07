'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),path=require('node:path');
const tick=()=>new Promise(resolve=>setImmediate(resolve));
const id=letter=>letter.repeat(32);
async function page(){
  const nodes=new Map(),requests=[],downloads=[],pending=[];let reads=0,mutation,directory,tree;
  class Element{
    constructor(tag='div',name=''){this.tagName=tag;this.id=name;this.children=[];this.dataset={};this.handlers=new Map();this.textContent='';this.value='';this.files=[];this.hidden=false;this.disabled=false;this.checked=false;this.classList={toggle(){}};}
    addEventListener(event,fn){this.handlers.set(event,fn);}setAttribute(key,value){this[key]=value;}
    append(...children){this.children.push(...children);}replaceChildren(...children){this.children=children;}
    showModal(){this.open=true;}close(){this.open=false;}focus(){}select(){}remove(){}
    emit(event){return this.handlers.get(event)?.({preventDefault(){}});}
    click(){if(this.tagName==='a')downloads.push({url:this.href,name:this.download});else this.emit('click');}
  }
  const get=name=>{if(!nodes.has(name))nodes.set(name,new Element('div',name));return nodes.get(name);};
  const all=()=>{const result=new Set();const walk=node=>{result.add(node);node.children.forEach(walk);};nodes.forEach(walk);return [...result];};
  const find=selector=>all().filter(node=>selector==='[data-select-key]'?node.dataset.selectKey!==undefined:selector==='[data-resource-key]'?node.dataset.resourceKey!==undefined:selector==='[data-rename-key]'?node.dataset.renameKey!==undefined:true);
  const response=(status,body={})=>({status,ok:status<400,json:async()=>body});
  const fixtures={files:[{id:id('a'),name:'one.txt',size:1,created_at:1,folder_id:''},{id:id('b'),name:'two.txt',size:2,created_at:1,folder_id:''}],folders:[{id:id('c'),name:'box',parent_id:'',created_at:1},{id:id('d'),name:'other',parent_id:'',created_at:1}]};
  fixtures.tree=[...fixtures.folders,{id:id('e'),name:'child',parent_id:id('c')},{id:id('f'),name:'grandchild',parent_id:id('e')},{id:id('1'),name:'available',parent_id:id('d')}];
  const context=vm.createContext({TextEncoder,URL,fixtures,location:{origin:'http://test'},navigator:{clipboard:{writeText:async()=>{}}},document:{getElementById:get,querySelectorAll:find,createElement:tag=>new Element(tag),body:get('body')},fetch:async(url,options)=>{
    if(url==='/api/me')return response(401);
    if(url.startsWith('/api/directory')){reads++;return directory?directory(url):response(200,{files:fixtures.files,folders:fixtures.folders,breadcrumbs:[]});}
    if(url==='/api/folders'&&!options.method)return tree?tree():response(200,{folders:fixtures.tree});
    if(url==='/api/me/stats')return response(200,{used_bytes:3,quota_bytes:100,reserved_bytes:0,files:2});
    requests.push({url,options});
    if(mutation)return mutation(url,options);
    return new Promise(resolve=>pending.push({url,options,resolve}));
  }});
  for(const file of ['upload-queue.js','selection.js','app.js'])vm.runInContext(fs.readFileSync(path.join(__dirname,'../internal/netdisk/web',file),'utf8'),context);
  await tick();vm.runInContext('state.user={id:1};state.files=fixtures.files;state.folders=fixtures.folders;state.allFolders=fixtures.tree;renderFiles();controls();',context);
  const check=(key,checked=true)=>{const box=find('[data-select-key]').find(node=>node.dataset.selectKey===key);box.checked=checked;box.emit('change');};
  return {get,find,context,requests,downloads,pending,response,check,fixtures,get reads(){return reads;},set mutation(fn){mutation=fn;},set directory(fn){directory=fn;},set tree(fn){tree=fn;}};
}

test('checkboxes, partial/full select, clear and refreshed selection stay scoped to the visible directory',async()=>{
  const p=await page();assert.equal(p.find('[data-select-key]').length,4);assert.equal(p.get('batch-delete').disabled,true);
  p.check('file:'+id('a'));assert.equal(p.get('select-all').indeterminate,true);assert.equal(p.get('selection-count').textContent,'已选择 1 项');
  p.get('select-all').checked=true;p.get('select-all').emit('change');assert.equal(vm.runInContext('state.selected.size',p.context),4);assert.equal(p.get('select-all').checked,true);assert.equal(p.get('select-all').indeterminate,false);
  p.get('selection-clear').click();assert.equal(vm.runInContext('state.selected.size',p.context),0);
  p.check('file:'+id('a'));vm.runInContext('state.files=state.files.filter(f=>f.id!=="'+id('a')+'");renderFiles();',p.context);assert.equal(vm.runInContext('state.selected.size',p.context),0);
  p.check('folder:'+id('c'));await vm.runInContext('navigate("'+id('d')+'")',p.context);assert.equal(vm.runInContext('state.selected.size',p.context),0);assert.equal(p.get('batch-feedback').hidden,true);
});

test('parent navigation climbs one level, ancestor breadcrumbs and home return to root, and busy navigation cannot duplicate',async()=>{
  const p=await page(),visited=[],parent={id:id('c'),name:'box',parent_id:''},child={id:id('e'),name:'child',parent_id:id('c')};
  p.directory=async url=>{const folder=new URL(url,'http://test').searchParams.get('folder_id');visited.push(folder);return p.response(200,{files:[],folders:[],breadcrumbs:folder===child.id?[parent,child]:folder===parent.id?[parent]:[]});};
  assert.equal(p.get('up-button').disabled,true);
  await vm.runInContext('navigate("'+child.id+'")',p.context);
  assert.equal(p.get('up-button').disabled,false);assert.equal(p.get('directory-title').textContent,'child');
  p.get('up-button').click();p.get('up-button').click();await tick();
  assert.equal(p.get('directory-title').textContent,'box');assert.equal(vm.runInContext('state.folder',p.context),parent.id);assert.deepEqual(visited,[child.id,parent.id]);
  p.get('up-button').click();await tick();assert.equal(vm.runInContext('state.folder',p.context),'');assert.equal(p.get('up-button').disabled,true);assert.equal(p.get('directory-title').textContent,'我的文件');
  await vm.runInContext('navigate("'+child.id+'")',p.context);p.get('breadcrumbs').children.find(node=>node.textContent==='box').click();await tick();assert.equal(vm.runInContext('state.folder',p.context),parent.id);
  p.get('breadcrumbs').children.find(node=>node.textContent==='根目录').click();await tick();assert.equal(vm.runInContext('state.folder',p.context),'');
  await vm.runInContext('navigate("'+child.id+'")',p.context);p.get('home-button').click();await tick();assert.equal(vm.runInContext('state.folder',p.context),'');
});

test('failed navigation preserves the current directory, parent and selection until every directory read succeeds',async()=>{
  const p=await page(),parent={id:id('c'),name:'box',parent_id:''};
  vm.runInContext('state.folder="'+parent.id+'";state.crumbs=[{id:"'+parent.id+'",name:"box",parent_id:""}];renderNavigation();controls();',p.context);p.check('file:'+id('a'));
  p.directory=async()=>p.response(404,{error:'folder not found'});
  await vm.runInContext('navigate("'+id('e')+'")',p.context);
  assert.equal(vm.runInContext('state.folder',p.context),parent.id);assert.equal(p.get('directory-title').textContent,'box');assert.equal(vm.runInContext('state.selected.size',p.context),1);assert.equal(p.get('up-button').disabled,false);assert.match(p.get('notice').textContent,/目录不存在/);
  p.directory=async()=>p.response(200,{files:[],folders:[],breadcrumbs:[]});p.tree=async()=>p.response(503);
  await vm.runInContext('navigate("")',p.context);
  assert.equal(vm.runInContext('state.folder',p.context),parent.id);assert.equal(p.get('directory-title').textContent,'box');assert.equal(vm.runInContext('state.selected.size',p.context),1);
  p.tree=undefined;p.get('up-button').click();await tick();assert.equal(vm.runInContext('state.folder',p.context),'');assert.equal(vm.runInContext('state.selected.size',p.context),0);assert.equal(p.get('up-button').disabled,true);
});

test('move excludes selected directories and descendants; download uses one archive URL for exact selection',async()=>{
  const p=await page();p.check('folder:'+id('c'));p.check('file:'+id('a'));
  p.get('batch-move').click();assert.deepEqual(p.get('batch-target').children.map(option=>option.value),['',id('d'),id('1')]);
  p.get('batch-cancel').click();assert.equal(p.requests.length,0);
  p.mutation=async()=>p.response(200);p.get('batch-download').click();await tick();
  assert.equal(p.requests.length,1);assert.equal(p.requests[0].options.method,'HEAD');assert.equal(p.downloads.length,1);
  const url=new URL(p.downloads[0].url,'http://test');assert.equal(url.pathname,'/api/download-selection');assert.equal(url.searchParams.get('file_ids'),id('a'));assert.equal(url.searchParams.get('folder_ids'),id('c'));
  assert.throws(()=>vm.runInContext('selectionDownloadURL([])',p.context),/1–200/);
});

test('rename waits for confirmation, rejects duplicate submit, continues failures, refreshes and keeps only failed selection',async()=>{
  const p=await page();p.check('file:'+id('a'));p.check('file:'+id('b'));p.get('batch-rename').click();assert.equal(p.requests.length,0);
  const names=p.find('[data-rename-key]');names[0].value='changed-one';names[1].value='changed-two';
  p.get('batch-form').emit('submit');p.get('batch-form').emit('submit');assert.equal(p.pending.length,1);assert.equal(p.get('batch-submit').disabled,true);
  const first=p.pending.shift();assert.equal(first.options.headers['X-NetDisk-Request'],'1');assert.equal(first.options.credentials,'same-origin');assert.equal(JSON.parse(first.options.body).name,'changed-one');
  first.resolve(p.response(400,{error:'invalid file name'}));await tick();assert.equal(p.pending.length,1);assert.equal(p.reads,0);
  p.pending.shift().resolve(p.response(200));await tick();
  assert.equal(p.requests.length,2);assert.equal(p.reads,1);assert.equal(p.get('batch-progress').textContent,'已完成 2/总数 2');assert.equal(p.get('batch-dialog').open,false);
  assert.equal(vm.runInContext('Array.from(state.selected).join()',p.context),'file:'+id('a'));
  assert.deepEqual(p.get('batch-results').children.map(row=>row.dataset.status),['failed','success']);
  assert.match(p.get('notice').textContent,/成功 1，失败 1/);
});

test('mixed folder/file delete confirms contents, uses owner-checked endpoints and logout clears shares/results',async()=>{
  const p=await page();p.check('folder:'+id('c'));p.check('file:'+id('a'));p.get('batch-delete').click();assert.match(p.get('batch-description').textContent,/所有内容/);p.get('batch-cancel').click();assert.equal(p.requests.length,0);
  p.mutation=async()=>p.response(204);p.get('batch-delete').click();p.get('batch-form').emit('submit');await tick();
  assert.deepEqual(p.requests.map(request=>request.url),['/api/folders/'+id('c')+'/tree','/api/files/'+id('a')]);assert.ok(p.requests.every(request=>request.options.method==='DELETE'&&request.options.headers['X-NetDisk-Request']==='1'));
  p.check('file:'+id('b'));p.mutation=async()=>p.response(201,{url:'/s/synthetic-only'});p.get('batch-share').click();p.get('batch-form').emit('submit');await tick();
  const result=p.get('batch-results').children[0];assert.equal(result.children[2].value,'http://test/s/synthetic-only');
  vm.runInContext('showUser(null)',p.context);assert.equal(p.get('batch-results').children.length,0);assert.equal(p.get('batch-feedback').hidden,true);assert.equal(vm.runInContext('state.selected.size',p.context),0);
});

test('session expiry during a batch sends no further private mutations and displays no old resource results',async()=>{
  const p=await page();p.check('file:'+id('a'));p.check('file:'+id('b'));p.mutation=async()=>p.response(401);
  p.get('batch-delete').click();p.get('batch-form').emit('submit');await tick();
  assert.equal(p.requests.length,1);assert.equal(p.reads,0);assert.equal(p.get('batch-results').children.length,0);assert.equal(p.get('batch-feedback').hidden,true);assert.equal(vm.runInContext('state.user',p.context),null);
});

test('partial sharing leaves only failures selected so retry does not create links for completed items',async()=>{
  const p=await page();p.check('file:'+id('a'));p.check('file:'+id('b'));
  let call=0;p.mutation=async()=>++call===1?p.response(503):p.response(201,{url:'/s/synthetic-only'});
  p.get('batch-share').click();p.get('batch-form').emit('submit');await tick();
  assert.equal(p.requests.length,2);assert.equal(vm.runInContext('Array.from(state.selected).join()',p.context),'file:'+id('a'));
  assert.deepEqual(p.get('batch-results').children.map(row=>row.dataset.status),['failed','success']);
  assert.match(p.get('notice').textContent,/失败项已保留选择/);
  p.mutation=async()=>p.response(201,{url:'/s/synthetic-retry'});
  p.get('batch-share').click();p.get('batch-form').emit('submit');await tick();
  assert.equal(p.requests.length,3);
  const links=p.get('batch-results').children.map(row=>row.children.find(child=>child.readOnly).value).sort();
  assert.deepEqual(links,['http://test/s/synthetic-only','http://test/s/synthetic-retry']);
  assert.equal(p.get('batch-results').children[1].children[1].textContent,'已创建链接');
  vm.runInContext('showUser(null)',p.context);assert.equal(vm.runInContext('batchSharedLinks.size',p.context),0);
});
