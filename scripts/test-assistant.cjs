const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const root = path.join(__dirname, '..');
let response, calls = 0;
const styleBox = { value: 'my existing style', dispatchEvent() {} };
const wordsBox = { value: 'my existing words' };
const context = vm.createContext({
  window: { MM3_USER: 'fixture', MM3_ASSISTANT_MAX_BYTES: 32768 }, TextEncoder, URL, URLSearchParams, AbortController, Event,
  document: { addEventListener() {}, getElementById: id => ({ style: styleBox, words: wordsBox })[id] },
  fetch: async () => { calls++; return response; },
});
for (const name of ['draft.js', 'lyrics.js', 'style-assist.js']) vm.runInContext(fs.readFileSync(path.join(root, 'web/static', name), 'utf8'), context);
const html = fs.readFileSync(path.join(root, 'web/templates/index.html'), 'utf8');
vm.runInContext([...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].pop()[1], context);
function page() {
  const p = vm.runInContext('mm3Page()', context);
  p.idea = 'Modify my song\n' + 'words '.repeat(1500);
  p.lyrics = 'keep my words'; p.caption = 'keep my style';
  return p;
}
function failure(status, error) {
  return { ok: false, status, json: async () => error ? { error, max_input_bytes: 32768 } : Promise.reject(new Error('HTML proxy error')) };
}
async function run() {
  const p = page();
  for (const [status, key, expected] of [
    [502, null, 'AI service'], [504, null, 'timed out'], [429, null, 'Rate limit'],
    [413, 'assistant-input-limit', '32 KiB'], [502, 'assistant-output-limit', 'output limit'],
    [422, 'assistant-refused', 'declined'], [502, 'assistant-unparseable', 'unreadable'],
  ]) {
    response = failure(status, key); await p.ask();
    assert.match(p.error, new RegExp(expected));
    assert.equal(p.lyrics, 'keep my words'); assert.equal(p.caption, 'keep my style'); assert.equal(p.thinking, false);
  }
  const before = calls;
  p.idea = '詞'.repeat(11000); assert.equal(p.ideaBytes, 33000);
  await p.ask(); assert.equal(calls, before); assert.match(p.error, /limit/);
  p.idea = 'words '.repeat(1500);
  response = { ok: true, status: 200, json: async () => ({ input: 'updated words', instructions: 'new style', audio_duration: 45 }) };
  await p.ask(); assert.equal(p.lyrics, 'updated words'); assert.equal(p.caption, 'new style');

  p.lyrics = 'still my words'; p.caption = 'still my style';
  response = { ok: true, status: 200, redirected: true, url: 'https://app.example.test/login', json: async () => { throw new Error('HTML login'); } };
  await p.ask(); assert.match(p.error, /session expired/); assert.equal(p.lyrics, 'still my words');
  response = { ok: true, status: 200, json: async () => ({ unexpected: true }) };
  await p.ask(); assert.equal(p.lyrics, 'still my words'); assert.equal(p.caption, 'still my style');

  const style = vm.runInContext('mm3StyleRewrite()', context);
  style.cfg = { target: 'style', lyrics: 'words', engine: 'minimax' }; style.change = 'make it softer';
  response = failure(502, null); await style.rewrite();
  assert.match(style.error, /AI service/); assert.equal(styleBox.value, 'my existing style'); assert.equal(style.previous, null);
  response = { ok: true, status: 200, json: async () => ({ unexpected: true }) }; await style.rewrite();
  assert.equal(styleBox.value, 'my existing style');
  response = { ok: true, status: 200, json: async () => ({ style: 'soft piano' }) }; await style.rewrite();
  assert.equal(styleBox.value, 'soft piano'); assert.equal(style.previous, 'my existing style');
  style.undo(); assert.equal(styleBox.value, 'my existing style');

  context.res = { ok: false, status: 502, json: async () => ({ error: 'constructor' }) };
  assert.equal(typeof await vm.runInContext('mm3AssistantError(res)', context), 'string');
  context.res = { ok: false, status: 502, json: async () => null };
  assert.equal(typeof await vm.runInContext('mm3AssistantError(res)', context), 'string');
  assert.match(html, /aria-describedby="assistant-input-help"/);
  console.log('Assistant UI checks passed: full input, UTF-8 limits, error messages, draft preservation, style undo and expired sessions.');
}
run().catch(error => { console.error(error); process.exitCode = 1; });
