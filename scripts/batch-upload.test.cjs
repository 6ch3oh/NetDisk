// Run with: node --test scripts/batch-upload.test.cjs (Node built-ins only).
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const web = path.join(__dirname, '..', 'internal', 'netdisk', 'web');
const tick = () => new Promise(resolve => setImmediate(resolve));
const deferred = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return {promise, resolve, reject};
};
function queue() {
  const context = vm.createContext({});
  vm.runInContext(fs.readFileSync(path.join(web, 'upload-queue.js'), 'utf8'), context);
  return context.uploadBatch;
}

test('queue caps concurrency at three, continues after failures and reports every transition', async () => {
  const files = Array.from({length: 7}, (_, i) => ({name: `file-${i}`, size: 10}));
  const pending = new Map(), attempts = [], reports = [];
  let active = 0, peak = 0;
  const batch = queue()(files, {limit: 10, upload(file) {
    attempts.push(file.name);
    active++; peak = Math.max(peak, active);
    const gate = deferred(); pending.set(file.name, gate);
    return gate.promise.finally(() => active--);
  }, changed(p) {
    reports.push({completed: p.completed, total: p.total, statuses: Array.from(p.items, item => item.status)});
  }});
  assert.equal(active, 3);
  assert.equal(attempts.length, 3);
  assert.deepEqual(reports[0].statuses, Array(7).fill('waiting'));
  assert.equal(reports.at(-1).statuses.filter(s => s === 'waiting').length, 4);
  pending.get('file-1').reject(new Error('network disconnected'));
  await tick();
  assert.equal(attempts[3], 'file-3');
  assert.equal(active, 3);
  for (const name of ['file-0', 'file-2', 'file-3', 'file-4', 'file-5', 'file-6']) {
    assert.ok(pending.has(name), `${name} must get its turn`);
    pending.get(name).resolve();
    await tick();
  }
  const result = await batch;
  assert.equal(peak, 3);
  assert.equal(active, 0);
  assert.equal(new Set(attempts).size, 7);
  assert.equal(result.success, 6);
  assert.equal(result.failed, 1);
  assert.equal(result.items[1].error, 'network disconnected');
  assert.equal(result.completed, 7);
  assert.equal(result.total, 7);
  assert.ok(reports.every(p => p.total === 7));
  assert.ok(reports.every((p, i) => !i || p.completed >= reports[i - 1].completed));
  assert.equal(reports.at(-1).completed, 7);
});

test('oversize files are skipped without a request; equality and empty files are allowed', async () => {
  const files = [{name: 'oversize', size: 11}, {name: 'exact', size: 10}, {name: 'empty', size: 0}];
  const attempts = [], reports = [];
  const result = await queue()(files, {limit: 10, upload(file) { attempts.push(file.name); }, changed(p) {
    reports.push({completed: p.completed, status: p.items[0].status});
  }});
  assert.deepEqual(attempts, ['exact', 'empty']);
  assert.equal(reports[0].completed, 1);
  assert.equal(reports[0].status, 'failed');
  assert.equal(result.skipped, 1);
  assert.equal(result.failed, 1);
  assert.equal(result.completed, 3);
  assert.match(result.items[0].error, /已跳过/);
});

test('all skipped, empty selection and unknown limits cannot send requests', async () => {
  const upload = () => assert.fail('must not upload');
  const all = await queue()([{name: 'a', size: 20}, {name: 'b', size: 30}], {limit: 10, upload});
  assert.equal(all.completed, 2);
  assert.equal(all.skipped, 2);
  const empty = await queue()([], {limit: 10, upload});
  assert.equal(empty.total, 0);
  for (const limit of [null, 0, -1, Infinity, NaN]) {
    await assert.rejects(queue()([{name: 'a', size: 1}], {limit, upload}), /上传限制尚未加载/);
  }
});

class Element {
  constructor(id = '') {
    this.id = id; this.files = []; this.children = []; this.listeners = new Map();
    this.dataset = {}; this.hidden = false; this.disabled = false; this.open = false;
    this.textContent = ''; this._value = '';
    this.classList = {toggle() {}};
  }
  set value(v) { this._value = v; if (this.id === 'upload-input' && v === '') this.files = []; }
  get value() { return this._value; }
  addEventListener(name, callback) { this.listeners.set(name, callback); }
  setAttribute() {}
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; }
  close() { this.open = false; }
  submit() { this.listeners.get('submit')({preventDefault() {}}); }
}
async function page() {
  const elements = new Map(), pending = new Map(), requests = [], directoryReads = [];
  const get = id => {
    if (!elements.has(id)) elements.set(id, new Element(id));
    return elements.get(id);
  };
  const response = (code, body) => ({ok: code < 400, status: code, json: async () => body});
  const context = vm.createContext({TextEncoder, document: {
    getElementById: get,
    querySelectorAll: selector => Array.from(elements.values()).filter(element => selector==='[data-select-key]'?element.dataset.selectKey!==undefined:selector==='[data-resource-key]'?element.dataset.resourceKey!==undefined:true),
    createElement: () => new Element()
  }, async fetch(url, options) {
    if (url.startsWith('/api/files?')) {
      requests.push({url, options});
      const name = new URL(url, 'http://test').searchParams.get('name');
      const gate = deferred(); pending.set(name, gate);
      return gate.promise;
    }
    if (url === '/api/me') return response(401, {});
    if (url.startsWith('/api/directory?')) {
      directoryReads.push(url);
      return response(200, {files: [], folders: [], breadcrumbs: []});
    }
    if (url === '/api/folders') return response(200, {folders: []});
    if (url === '/api/me/stats') return response(200, {files: 0, used_bytes: 0, quota_bytes: 100, reserved_bytes: 0});
    assert.fail(`unexpected API: ${url}`);
  }});
  for (const name of ['upload-queue.js', 'selection.js', 'app.js']) {
    vm.runInContext(fs.readFileSync(path.join(web, name), 'utf8'), context, {filename: name});
  }
  await tick(); // Let the ordinary unauthenticated page bootstrap finish.
  vm.runInContext("state.user={id:1,username:'test'};state.limit=10;state.threshold=300000000;state.partSize=8388608;state.folder='folder & 汉';controls();", context);
  return {get, context, pending, requests, directoryReads, response};
}

test('real submit handler rejects duplicates, uses the single-file API and refreshes only after settling', async () => {
  const p = await page();
  const files = [{name: '<img>.txt', size: 10}, {name: 'bad.txt', size: 2}, {name: 'third & 汉.txt', size: 3}, {name: 'fourth', size: 1}];
  p.get('upload-input').files = files;
  p.get('upload-form').submit();
  p.get('upload-form').submit(); // Also bypass disabled controls: busy must still guard submission.
  assert.equal(p.requests.length, 3);
  assert.equal(p.get('upload-button').disabled, true);
  assert.equal(p.get('upload-input').disabled, true);
  assert.equal(p.get('upload-list').children[3].children[1].textContent, '等待');
  assert.equal(p.get('upload-list').children[0].children[0].textContent, '<img>.txt');
  assert.equal(p.directoryReads.length, 0);
  for (const req of p.requests) {
    const url = new URL(req.url, 'http://test');
    assert.equal(url.pathname, '/api/files');
    assert.equal(url.searchParams.get('folder_id'), 'folder & 汉');
    assert.equal(req.options.method, 'POST');
    assert.equal(req.options.headers['X-NetDisk-Request'], '1');
    assert.equal(req.options.headers['Content-Type'], 'application/octet-stream');
    assert.equal(req.options.credentials, 'same-origin');
    assert.ok(files.includes(req.options.body));
  }
  p.pending.get('bad.txt').resolve(p.response(400, {error: 'invalid file name'}));
  await tick();
  assert.equal(p.requests.length, 4);
  assert.equal(p.get('upload-list').children[1].dataset.status, 'failed');
  assert.equal(p.get('upload-list').children[3].dataset.status, 'uploading');
  p.pending.get('<img>.txt').resolve(p.response(201, {}));
  p.pending.get('third & 汉.txt').resolve(p.response(201, {}));
  await tick();
  assert.equal(p.directoryReads.length, 0);
  p.pending.get('fourth').resolve(p.response(201, {}));
  await tick();
  assert.deepEqual(p.directoryReads, ['/api/directory?folder_id=folder%20%26%20%E6%B1%89']);
  assert.equal(p.get('upload-status').textContent, '已完成 4/总数 4');
  assert.equal(p.get('upload-list').children.filter(row => row.dataset.status === 'success').length, 3);
  assert.match(p.get('notice').textContent, /成功 3，失败 1/);
  assert.equal(p.get('upload-input').files.length, 0);
  assert.equal(p.get('upload-button').disabled, true);
  assert.equal(p.get('upload-input').disabled, false);
  assert.equal(vm.runInContext('state.busy', p.context), false);
});

test('real submit handler skips every oversize file, refreshes and clears private results on logout', async () => {
  const p = await page();
  p.get('upload-input').files = [{name: 'too-big', size: 11}, {name: 'also-big', size: 12}];
  p.get('upload-form').submit();
  await tick();
  assert.equal(p.requests.length, 0);
  assert.equal(p.directoryReads.length, 1);
  assert.equal(p.get('upload-status').textContent, '已完成 2/总数 2');
  assert.match(p.get('notice').textContent, /2 个超限已跳过/);
  vm.runInContext('showUser(null)', p.context);
  assert.equal(p.get('upload-results').hidden, true);
  assert.equal(p.get('upload-list').children.length, 0);
  assert.equal(p.get('upload-status').textContent, '');
});
