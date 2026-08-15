# Setup questionnaire — minmaxmusic3-web

Answered 2026-08-14 at workspace creation (do not re-ask).

1. **Goal:** Turn a rough song idea into a finished, playable MiniMax Music 3
   song — and ship/maintain the web app that does it.
2. **Domain:** minmaxmusic3-web (confirmed).
3. **Audience:** the site's users (songwriters) and the maintaining developer.
4. **Voice:** UI copy short/warm/plain (see `_config/voice.md`); docs dense
   and terse; assistant voice per `shared/llm-assistant-system-prompt.md`.
5. **Stages:** 01 blueprint → 02 API contracts → 03 design system →
   04 Go foundation → 05 generation + assistant → 06 history →
   07 containerization → 08 acceptance.
6. **Inputs:** `.goals/goal-mm3-icm-scaffold.md` (verified facts),
   `DESIGN.md`, `index-dark.html` / `index-light.html`, `favicon_io.zip`,
   the worker README, the HF model card.
7. **Review gates:** after 01 (features + htmx pin), after 03 (themes),
   after 07 (secret-delivery decision), at 08 (acceptance report).
8. **Output:** the deployed app at `https://mm3.gemneye.xyz` plus each
   stage's `output/` evidence trail.
