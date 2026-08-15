# Voice — minmaxmusic3-web

Voice and style rules for outputs (Layer 3).

- Audience: the site's users (songwriters) and the maintaining developer.
- UI copy: short, warm, plain English; no jargon in buttons/labels —
  "Write me a song about…" not "Submit generation payload".
- Errors tell the user what to do next ("Try again in a minute"), never a
  raw stack/API error.
- Docs and stage outputs: dense agent notes, terse bullets, invariants over
  prose — match the ICM house style.
- The AI assistant's voice is defined separately in
  `shared/llm-assistant-system-prompt.md` (creative, decisive, fills gaps
  rather than interrogating the user).
