// The "Rewrite with AI" helper under every style box, defined once for the
// generate form and the song page's Edit and Cover panels.
//
// The partial (web/templates/style-assist.html) names its box in data
// attributes: data-target is the style textarea's id, data-lyrics the lyrics
// textarea's (context for the editor, never rewritten), and data-engine the
// engine. An empty engine means "the form's engine selector", which is how the
// generate form follows its own choice without a second copy of it here.
//
// The rewrite replaces the box's text and keeps the old text for Undo. Nothing
// is submitted: the user still presses Generate or Re-render.
// Both assistant surfaces use the same error protocol, including proxy errors
// which have an HTML body rather than the app's JSON.
async function mm3AssistantError(res, kind = 'draft') {
  if (res.redirected && res.url && new URL(res.url).pathname === '/login') {
    return 'Your session expired. Sign in again before using the assistant.';
  }
  const decoded = await res.json().catch(() => null);
  const data = decoded && typeof decoded === 'object' ? decoded : {};
  const bytes = Number(data.max_input_bytes) || Number(window.MM3_ASSISTANT_MAX_BYTES);
  const limit = Number.isFinite(bytes) && bytes > 0 ? ` (${Math.floor(bytes / 1024)} KiB maximum)` : '';
  const messages = {
    'assistant-input-limit': `The idea or lyric context is too large${limit}. Shorten it before retrying; nothing was cut off.`,
    'assistant-output-limit': 'The AI reply hit its output limit. Ask for less output and retry; your draft has not changed.',
    'assistant-refused': 'The AI provider declined this request. Try a different request or edit the draft yourself.',
    'assistant-timeout': 'The assistant took too long. Retry in a moment or edit the draft yourself.',
    'assistant-unavailable': `The AI service is unavailable. Retry later or edit the ${kind} yourself.`,
    'assistant-unparseable': `The assistant returned an unreadable ${kind}. Your text is unchanged; try rephrasing the request.`,
    'assistant-rate-limited': 'The AI service is busy. Retry in a minute.',
    'assistant-bad-form': 'Could not read that request. Try again.',
    'empty-idea': 'Describe the song or changes you want.',
    'empty-change': 'Describe how you want the style to change.',
  };
  if (Object.hasOwn(messages, data.error)) return messages[data.error];
  if (res.status === 429) return 'Rate limit reached. Try again later.';
  if (res.status === 413) return `The request is too large${limit}. Shorten the instructions or pasted lyrics.`;
  if (res.status === 401 || res.status === 403) return 'Sign in again before using the assistant.';
  if (res.status === 504) return 'The AI service timed out. Retry later; your text is unchanged.';
  return `The AI service did not return a usable ${kind}. Retry later; your text is unchanged.`;
}

function mm3StyleRewrite() {
  return {
    open: false, change: '', busy: false, error: '', previous: null, cfg: {},

    init() {
      // $el is the component root only here; in a handler it is the element
      // that fired. Read attribute by attribute: spreading a DOMStringMap is
      // not reliable across engines, and a headless check caught it empty.
      const el = this.$el;
      this.cfg = {
        target: el.getAttribute('data-target'),
        lyrics: el.getAttribute('data-lyrics'),
        engine: el.getAttribute('data-engine'),
      };
    },

    box() { return document.getElementById(this.cfg.target); },

    engine() {
      if (this.cfg.engine) return this.cfg.engine;
      const box = this.box();
      const sel = box && box.form && box.form.elements.engine;
      return (sel && sel.value) || 'minimax';
    },

    toggle() {
      this.open = !this.open;
      this.error = '';
      if (this.open) this.$nextTick(() => this.$refs.change && this.$refs.change.focus());
    },

    // Writing through the textarea and an input event keeps an x-model bound
    // to it (the generate form's caption, and its saved draft) in step.
    put(text) {
      const box = this.box();
      box.value = text;
      box.dispatchEvent(new Event('input', { bubbles: true }));
    },

    async rewrite() {
      const box = this.box();
      if (!box || this.busy || !this.change.trim()) return;
      this.busy = true; this.error = '';
      const lyrics = document.getElementById(this.cfg.lyrics);
      try {
        const body = new URLSearchParams({
          engine: this.engine(), style: box.value, change: this.change,
          lyrics: lyrics ? lyrics.value : '',
        });
        const res = await fetch('/assistant/style', { method: 'POST', body });
        if (!res.ok) {
          this.error = await mm3AssistantError(res, 'style');
          return;
        }
        const d = await res.json().catch(() => null);
        if (!d || typeof d.style !== 'string' || !d.style.trim()) {
          this.error = await mm3AssistantError(res, 'style');
          return;
        }
        this.previous = box.value;
        this.put(d.style);
        this.change = ''; this.open = false;
      } catch { this.error = 'Network problem — try again.'; }
      finally { this.busy = false; }
    },

    undo() {
      if (this.previous === null) return;
      this.put(this.previous);
      this.previous = null;
    },
  };
}
