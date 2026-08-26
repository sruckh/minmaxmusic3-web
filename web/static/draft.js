// The generate form is a scratchpad a song is written in, not a fill-and-submit
// form. Two pages read and write the same draft: the form itself, which restores
// what was typed after a trip to History, and a song's detail page, which drops
// a finished song back into the form to be reworked. So the key, the field list
// and the defaults live here rather than in whichever template needed them first.
//
// The draft is scoped per user and dropped on logout (see layout.html), so a
// shared browser never hands one account's lyrics to the next.

const MM3_DRAFT_FIELDS = ['idea', 'title', 'lyrics', 'caption', 'dur', 'seed', 'bpm', 'key', 'vocals'];

function mm3Defaults() {
  return { idea: '', title: '', lyrics: '', caption: '', dur: 30, seed: null, bpm: 96, key: 'C Major', vocals: true };
}

// window.MM3_USER is set by the head template; the empty name is the signed-out
// case, which never reaches a page that stores a draft.
function mm3DraftKey() {
  return 'mm3-draft:' + (window.MM3_USER || '');
}

// Every storage call is wrapped: private mode and a full quota both throw, and
// neither is a reason for the form to stop working.
function mm3ReadDraft() {
  try {
    const saved = JSON.parse(localStorage.getItem(mm3DraftKey()));
    return saved && typeof saved === 'object' ? saved : null;
  } catch { return null; }
}

function mm3WriteDraft(draft) {
  try { localStorage.setItem(mm3DraftKey(), JSON.stringify(draft)); } catch { /* the form still works */ }
}

function mm3ClearDraft() {
  try { localStorage.removeItem(mm3DraftKey()); } catch { /* nothing to drop */ }
}

// Dirty means "differs from a fresh page" — the test for whether there is
// anything worth saving, and whether overwriting it needs a warning first.
function mm3DraftIsDirty(draft) {
  if (!draft) return false;
  const d = mm3Defaults();
  return MM3_DRAFT_FIELDS.some(f => draft[f] !== undefined && draft[f] !== d[f]);
}

// The slider and the stepper both read dur, so it has to land in range.
function mm3ClampDur(sec) {
  return Math.min(300, Math.max(15, parseInt(sec, 10) || 30));
}
