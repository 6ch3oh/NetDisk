'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const {File} = require('node:buffer');
const {webcrypto, createHash} = require('node:crypto');
const source = fs.readFileSync(path.join(__dirname, '../internal/netdisk/web/upload-queue.js'), 'utf8');
function client() {
  const context = vm.createContext({crypto: webcrypto, Uint8Array, setTimeout: fn => fn()});
  vm.runInContext(source, context);
  return vm.runInContext('({uploadFile,uploadBatch,uploadResumeStore})', context);
}
const digest = bytes => createHash('sha256').update(bytes).digest('hex');
test('blocked browser storage still loads the upload feature', () => {
  const context = vm.createContext({});
  Object.defineProperty(context, 'sessionStorage', {get() {throw new Error('storage disabled');}});
  vm.runInContext(source, context);
  assert.equal(vm.runInContext('browserUploadStorage()', context), null);
  assert.equal(vm.runInContext('const store=uploadResumeStore(browserUploadStorage());store.set("key",{id:1});store.get("key").id', context), 1);
});
const disconnected = () => Object.assign(new Error('disconnected'), {status: 0});
function server() {
  const calls = [], parts = new Map(); let session, completed, publishes = 0;
  const api = async (url, options = {}) => {
    calls.push({url, options});
    if (url === '/api/uploads') {
      const body = JSON.parse(options.body);
      if (!session) session = {...body, id: 'a'.repeat(32)};
      assert.equal(body.request_key, session.request_key);
      return {...session, ...(completed ? {file: completed} : {})};
    }
    if (options.method === 'DELETE') {parts.clear(); return null;}
    if (url.endsWith('/complete')) {
      if (!completed) {
        publishes++;
        completed = {id: 'f'.repeat(32), size: session.expected_size};
      }
      return completed;
    }
    if (url.includes('/parts/')) {
      const index = Number(url.split('/').at(-1));
      const bytes = Buffer.from(await options.body.arrayBuffer());
      parts.set(index, {index, size: bytes.length, sha256: digest(bytes), bytes});
      return {};
    }
    return {session, parts: Array.from(parts.values())};
  };
  return {api, calls, parts, get publishes() {return publishes;}};
}
function options(c, api, overrides = {}) {
  return {folder: '', owner: 1, threshold: 10, partSize: 4, resumes: c.uploadResumeStore(null), api, ...overrides};
}

test('300MB boundary uses one ordinary request; larger files use bounded sequential parts and progress', async () => {
  const c = client(), single = [];
  // A boundary-sized descriptor avoids allocating 300MB in a unit test.
  const exact = {name: 'boundary.bin', size: 300_000_000};
  await c.uploadFile(exact, options(c, async (url, opts) => single.push({url, opts}), {threshold: 300_000_000}));
  assert.equal(single.length, 1);
  assert.match(single[0].url, /^\/api\/files\?/);
  assert.equal(single[0].opts.body, exact);
  const s = server(), progress = [], file = new File(['abcdefghijk'], 'large.bin');
  await c.uploadFile(file, options(c, s.api, {progress: value => progress.push(value)}));
  assert.equal(s.publishes, 1);
  assert.deepEqual(s.calls.filter(call => call.options.method === 'PUT').map(call => call.options.body.size), [4, 4, 3]);
  assert.equal(Buffer.concat(Array.from(s.parts.values(), part => part.bytes)).toString(), 'abcdefghijk');
  assert.equal(progress.at(-1), 1);
  assert.ok(progress.every((value, i) => value > 0 && (!i || value >= progress[i - 1])));
  assert.ok(s.calls.every(call => !call.url.startsWith('/api/files')));
});

test('lost creation, part and completion responses retry the same operation without publishing twice', async () => {
  const c = client(), s = server(), lost = new Set(), file = new File(['abcdefghijk'], 'retry.bin');
  const api = async (url, opts) => {
    const result = await s.api(url, opts);
    if ((url === '/api/uploads' || url.endsWith('/parts/0') || url.endsWith('/complete')) && !lost.has(url)) {
      lost.add(url); throw disconnected();
    }
    return result;
  };
  await c.uploadFile(file, options(c, api));
  assert.equal(s.publishes, 1);
  assert.equal(s.parts.size, 3);
  const creations = s.calls.filter(call => call.url === '/api/uploads');
  assert.equal(creations.length, 2);
  assert.equal(creations[0].options.body, creations[1].options.body);
  assert.equal(s.calls.filter(call => call.url.endsWith('/complete')).length, 2);
});

test('a failed transfer survives page reload, reselecting verifies and skips existing parts; changed source is rejected', async () => {
  const storage = new Map();
  const adapter = {getItem: key => storage.get(key), setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key)};
  const c = client(), s = server(), file = new File(['abcdefghijk'], 'resume.bin', {lastModified: 1});
  const interrupted = async (url, opts) => {if (url.endsWith('/parts/1')) throw disconnected(); return s.api(url, opts);};
  await assert.rejects(c.uploadFile(file, options(c, interrupted, {resumes: c.uploadResumeStore(adapter)})), /disconnected/);
  assert.equal(s.parts.size, 1);
  assert.equal(storage.size, 1);
  const reloaded = client();
  await reloaded.uploadFile(file, options(reloaded, s.api, {resumes: reloaded.uploadResumeStore(adapter)}));
  assert.equal(s.calls.filter(call => call.url.endsWith('/parts/0')).length, 1);
  assert.equal(s.publishes, 1);
  assert.equal(storage.size, 0);
  const changed = server(), resumes = c.uploadResumeStore(null);
  await assert.rejects(c.uploadFile(file, options(c, async (url, opts) => {if (url.endsWith('/parts/1')) throw disconnected(); return changed.api(url, opts);}, {resumes})), /disconnected/);
  const replacement = new File(['XXXXXXXXXXX'], file.name, {lastModified: 1});
  await assert.rejects(c.uploadFile(replacement, options(c, changed.api, {resumes})), /内容已变化/);
  assert.equal(changed.publishes, 0);
  assert.equal(changed.parts.size, 0);
  assert.ok(changed.calls.some(call => call.options.method === 'DELETE'));
});

test('durable completion receipt verifies source after reload; logout removes only upload metadata', async () => {
  const storage = new Map([['unrelated', 'preserved']]);
  const adapter = {get length() {return storage.size;}, key: index => Array.from(storage.keys())[index], getItem: key => storage.get(key), setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key)};
  const c = client(), s = server(), file = new File(['abcdefghijk'], 'receipt.bin', {lastModified: 1});
  const opts = options(c, async (url, request) => {const value = await s.api(url, request); if (url.endsWith('/complete')) throw disconnected(); return value;}, {resumes: c.uploadResumeStore(adapter)});
  await assert.rejects(c.uploadFile(file, opts), /disconnected/);
  const reloaded = client();
  const result = await reloaded.uploadFile(file, options(reloaded, s.api, {resumes: reloaded.uploadResumeStore(adapter)}));
  assert.equal(result.id, 'f'.repeat(32));
  assert.equal(s.publishes, 1);
  assert.equal(s.calls.filter(call => call.options.method === 'PUT').length, 3);
  assert.equal(storage.size, 1);
  const store = c.uploadResumeStore(adapter);
  store.set('synthetic', {requestKey: 'b'.repeat(32)});
  store.clear();
  assert.deepEqual(Array.from(storage), [['unrelated', 'preserved']]);
});
