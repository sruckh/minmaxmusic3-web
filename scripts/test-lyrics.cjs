const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const root = path.join(__dirname, '..');
let reply;
const context = vm.createContext({ window: { MM3_LYRICS_ENABLED: true, MM3_USER: 'user' }, AbortController,
  FormData: class { append() {} }, fetch: (...args) => reply(...args), confirm: () => false });
vm.runInContext(fs.readFileSync(path.join(root, 'web/static/draft.js'), 'utf8'), context);
vm.runInContext(fs.readFileSync(path.join(root, 'web/static/lyrics.js'), 'utf8'), context);
function page() {
  const p = vm.runInContext('({...mm3Defaults(), ...mm3Cover()})', context);
  p.isCover = true; p.engine = 'yue2'; p.mode = 'cover'; p.findLyrics = true;
  p.sourceFile = { size: 100 }; p.$refs = { sourceUpload: { value: 'recording' } };
  return p;
}
const match = { title: 'Song', artist: 'Artist', lyrics: 'the words', instrumental: false };
const success = () => ({ ok: true, json: async () => ({ candidates: [match], autoSelect: 0, warnings: [] }) });
async function run() {
  const p = page(); let calls = 0;
  reply = async () => { calls++; return success(); };
  p.findLyrics = false; await p.lookupLyrics(); assert.equal(calls, 0);
  p.findLyrics = true; await p.lookupLyrics(); assert.equal(p.lyrics, 'the words'); assert.equal(p.lookingUp, false);
  p.lyrics = 'my own words'; await p.lookupLyrics(); assert.equal(p.lyrics, 'my own words');
  p.useFoundLyrics(); assert.equal(p.lyrics, 'my own words', 'replacement requires confirmation');
  p.lyrics = ''; p.useFoundLyrics(); assert.equal(p.lyrics, 'the words');

  const q = page(); let resolve;
  reply = () => new Promise(done => { resolve = done; });
  const pending = q.lookupLyrics(); assert.equal(q.lookingUp, true);
  q.lyrics = 'typed while waiting'; resolve(success()); await pending;
  assert.equal(q.lyrics, 'typed while waiting');

  const stale = page(); reply = () => new Promise(done => { resolve = done; });
  const old = stale.lookupLyrics(); stale.recordingChanged(null); resolve(success()); await old;
  assert.equal(stale.lyrics, ''); assert.equal(stale.lyricCandidates.length, 0); assert.equal(stale.lookingUp, false);

  const failed = page(); reply = async () => ({ ok: false, json: async () => ({ error: 'service busy' }) });
  await failed.lookupLyrics(); assert.equal(failed.lookupMessage, 'service busy'); assert.equal(failed.lookingUp, false);
  failed.recordingChanged({ size: 65 * 1024 * 1024 }); assert.equal(failed.sourceFile, null);

  const ambiguous = page(); reply = async () => ({ ok: true, json: async () => ({ candidates: [match], autoSelect: -1 }) });
  await ambiguous.lookupLyrics(); assert.equal(ambiguous.lyrics, ''); assert.equal(ambiguous.selectedLyrics(), 'the words');
  ambiguous.lyricCandidates = [{ ...match, instrumental: true }]; assert.equal(ambiguous.selectedLyrics(), '');
  ambiguous.describeLyricsMatch(); assert.match(ambiguous.lookupMessage, /instrumental/);
  ambiguous.resetRecording(); assert.equal(ambiguous.sourceFile, null); assert.equal(ambiguous.$refs.sourceUpload.value, '');

  const defaults = vm.runInContext('mm3Defaults()', context);
  assert.equal(defaults.mode, 'create'); assert.equal(defaults.findLyrics, false);
  assert.equal(vm.runInContext("MM3_DRAFT_FIELDS.includes('sourceFile')", context), false);
  for (const field of ['mode', 'findLyrics', 'styleStrength']) assert.equal(vm.runInContext(`MM3_DRAFT_FIELDS.includes('${field}')`, context), true);
  context.document = { addEventListener() {} };
  context.localStorage = { getItem: () => JSON.stringify({ engine: 'yue2', mode: 'cover', caption: 'saved style' }) };
  const html = fs.readFileSync(path.join(root, 'web/templates/index.html'), 'utf8');
  const inline = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].pop()[1];
  vm.runInContext(inline, context);
  context.window.MM3_YUE2_ENABLED = false;
  const staleDraft = vm.runInContext('mm3Page()', context);
  staleDraft.restore();
  assert.equal(staleDraft.engine, 'minimax'); assert.equal(staleDraft.mode, 'create');
  context.window.MM3_YUE2_ENABLED = true;
  const coverDraft = vm.runInContext('mm3Page()', context);
  coverDraft.restore();
  assert.equal(coverDraft.isCover, true); assert.equal(coverDraft.sourceFile, null);
  console.log('Lyrics UI checks passed: opt-in, review, edits, ambiguity, cancellation, failure, upload bounds and draft defaults.');
}
run().catch(error => { console.error(error); process.exitCode = 1; });
