# Gauntlet progress — minmaxmusic3-web implementation

Live page — updated as work evolves. Bar: timbre (backend/infra) + the two
theme files (UI) + mechanical checks. Exit: critic picks ours blind, every
check passes, all stages 01–08 complete. Boundaries: 4h / day's model spend.

| Stage | Status | Critic verdict | Checks |
|-------|--------|----------------|--------|
| 01 blueprint | ✅ done (human-approved) | **B (ours)** blind, r2 | icm audit OK |
| 02 api-contracts | ✅ done | **B (ours)** blind, r1 decisive | state machine closed |
| 03 design-system | ✅ done (human-approved) | **B (ours)** blind, r1 | palette check ✅ on real build |
| 04 go-foundation | ✅ done | **B (ours)** blind, r2 | r1 loss fixed: timeouts+graceful shutdown+buffered templates+abs WebDir |
| 05 generation+assistant | ✅ done | **B (ours)** blind, r3 | 16 tests pass incl. E2E + ambiguity/cap tests |
| 06 song-history | ✅ done | **B (ours)** blind, r1 | paging/detail/replay/regenerate tests ✅ |
| 07 containerization | ✅ done (human-approved) | **B (ours)** blind, r1 | runtime image live; zero ports, non-root, tmpfs secret |
| 08 acceptance | 🔄 live checks running | — | offline all PASS; live generation PASS; assistant in progress |
| Integration pass | ⬜ pending | — | — |

## Log
- [build 01] started: feature blueprint + htmx pin decision
- [build 01] htmx pin decided: 4.0.0-beta4 + Alpine 3.14.8 + Tailwind v4 CLI (timbre's proven pins); JS vendored not CDN
- [build 01] blueprint.md written (features, page map, non-goals, invariants)
- [critic 01] fresh-context critic comparing blind A/B vs the timbre brief — running
- [critic 01] verdict: **A (timbre) wins**. Biggest gap in ours: synchronous /runsync collides with the ~100 s proxy cap on 300 s generations — architecture-breaking. Also: no definition of done, no data model, assistant LLM unpinned, no abuse limits on the open site.
- [build 01 v2] rewritten: async job queue (POST /run + GET /status polling, /runsync banned from request path), data model (jobs/songs), definition of done, assistant pinned (60 s timeout, 2k token cap), per-IP rate limits + global in-flight cap
- [critic 01 r2] re-running blind against the same bar
- [critic 01 r2] verdict: **B (ours) wins** — "a developer could build this without asking questions." Remaining gap: external inference dependency unpinned in the brief itself.
- [build 01 final] added §2.1 External dependencies (RunPod endpoint/model/schema pointer, LLM assistant pinning) — gap closed
- **STAGE 01 COMPLETE: ours won blind in r2. Paused at human checkpoint (approve features + htmx pin).**
- [human 01] blueprint + stack pins **approved** — continuing
- [build 02] api-contracts.md: RunPod async schema + 6-class error taxonomy + closed job state machine + LLM assistant contract with parse rules
- [critic 02] verdict: **B (ours) wins, r1 decisive** — "an actual contract." Gap named in ours: transient-exhaustion + CANCELLED lacked terminal classification.
- [build 02 final] taxonomy rows added (transient-exhausted, cancelled) + every RunPod status mapped to exactly one transition — machine closed. **STAGE 02 COMPLETE.**
- [build 03] favicons extracted to static/favicon/ (7 files, zip untouched); Tailwind v4 @theme/@custom-variant syntax verified via Context7; design-system.md writing
- [critic 03] verdict: **B (ours) wins r1** — "an implementation contract; A is an art-direction essay." Gap in ours: light theme swapped only 2 vars; no contrast ratios; disabled = 40% opacity (sub-AA).
- [build 03 final] light theme now redefines ALL surface tokens on pearl base; AA contrast table added (computed, incl. meadow 3.17:1 = never a text color); disabled = full-opacity text-dim + aria + label. **STAGE 03 WON — human gate pending.**
- [build 04] go mod init; htmx 4.0.0-beta4 + alpine 3.14.8 vendored to web/static; internal/config (presence-only secret logging) + internal/server (routes /, /history, /healthz, /static) + templates (theme-before-paint script, nav + toggle) + input.css per design contract
- [build 04] `go build/vet` clean, `gofmt -l` empty after -w; Tailwind v4.3.3 CSS built; **exhaustive-palette check PASSES on real build output: exactly the 7 DESIGN.md hexes in app.css**
- [build 04] smoke test running (server + route curls)
- [build 04] all routes 200 incl. favicons (moved static/favicon → web/static/favicon)
- [critic 04 r1] verdict: **A (timbre) wins** — ours had no server timeouts/graceful shutdown, streamed templates (truncated 200s), CWD-relative paths, dead validation
- [build 04 v2] fully-timed http.Server + signal shutdown + stage-attributed boot errors + buffered page renders + MM3_WEB_DIR abs default + strconv.Atoi
- [critic 04 r2] verdict: **B (ours) wins** — "attributes every boot failure to its stage." Residual nits (Sscanf, comment) fixed. **STAGE 04 COMPLETE.**
- [build 05] store (SQLite WAL, CAS transitions) + runpod client (async + taxonomy) + llm assistant (last-fenced-JSON parser) + worker (closed state machine, restart recovery, s3/base64 audio capture) + handlers (assistant/jobs/audio + per-IP limiters 6/h + 20/day) + full UI templates
- [build 05] test bugs fixed (missing form Content-Type, {{define}} vs file-named templates, unset in-memory State) → **`go test ./...` 8/8 pass, TestEndToEnd proves submit→async→poll→player→audio with stubs**
- [critic 05 r1] verdict: **A (timbre) wins** — dead per-call timeout, in-flight cap not enforced, result could duplicate after transition error, retries never reset
- [build 05 v2] per-call 30s budget, active-count cap, idempotent unique song/job, reset retries, bounded 64MiB status + 2m S3 fetch
- [critic 05 r2] verdict: **A wins again** — remote/local submit window, CAS-first orphaned success, unbounded S3 fetch, 1MiB response truncation
- [build 05 v3] result-store-before-success CAS, S3 timeout, 64MiB JSON; submit CAS errors cancel + record id
- [critic 05 r3] verdict: **B (ours) wins**. Residual: remote submit/local id write is inherently non-atomic
- [build 05 final] added durable `submitting` claim; crash recovery fails ambiguous submissions (never resubmits); explicit 429-only submit retry; returned ids cancelled+durably recorded on CAS error. Added ambiguity, in-flight-cap, restart, orphan-id, one-song-per-job tests. **STAGE 05 COMPLETE.**
- [build 06] history list/detail/audio/regenerate + named-volume persistence; 11 tests initially green
- [critic 06] verdict: **B (ours) wins r1**. Fixed store-error→500 (not masked 404), pageSize+1 exact HasNext, semantic hx-boost paging, direct SongForJob index. **STAGE 06 COMPLETE.**
- [build 07] pinned-digest Go + Infisical images, tests hard-dependency of runtime build, non-root alpine runtime, zero ports, shared_net, named volume, healthcheck, .dockerignore
- [critic 07] verdict: **B (ours) wins r1**. Gap: bootstrap client secret was visible in docker inspect
- [build 07 final] client secret now tmpfs-backed Compose secret at /run/secrets; host path unlinked after up; entrypoint strips client identity and minted token before Go exec; remote curl|sh replaced by pinned vendor-image binary. Test-only image built successfully with suite inside. **STAGE 07 WON — human gate pending.**
- [verification] current expanded suite: 16 named tests, `go test ./...`, vet, gofmt, icm audit all green
