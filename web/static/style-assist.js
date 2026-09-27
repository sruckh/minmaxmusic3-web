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
          if (res.status === 429) { this.error = 'Rate limit reached — try again in a little while.'; return; }
          const map = { 'assistant-timeout': 'The assistant took too long — try again in a minute.',
                        'assistant-unavailable': 'The assistant is unavailable right now. You can still edit the style yourself.',
                        'assistant-unparseable': 'The assistant gave an unusable style — try rephrasing the change.' };
          const j = await res.json().catch(() => ({}));
          this.error = map[j.error] || 'Something went wrong — try again.';
          return;
        }
        const d = await res.json();
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
