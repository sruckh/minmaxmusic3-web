# Stage 04 — go-foundation

> Layer 2 · "What do I do?"

**Purpose:** Stand up the Go server skeleton — layout, configuration,
templates, and the htmx + Alpine.js + Tailwind wiring — before any feature.

## Inputs
| Source | File/Location | Section/Scope | Why |
|--------|---------------|---------------|-----|
| Blueprint | ../01-project-blueprint/output/blueprint.md | page map, htmx pin | structure |
| Design system | ../03-design-system/output/design-system.md | tokens, partials | base layout |
| Conventions | ../../_config/conventions.md | code rules | house style |

## Process
1. `go mod init` + module layout: `cmd/server/main.go`, `internal/config`,
   `internal/handlers`, `internal/runpod`, `internal/llm`, `internal/store`,
   `web/templates`, `web/static`.
2. Config loader reads env (`LLM_API_KEY`, `LLM_BASE_URL`, `LLM_MODEL_ID`,
   `RUNPOD_API_KEY`, `RUNPOD_ENDPOINT`, `PORT`, `PUBLIC_URL`); fail-fast on
   missing required vars; no defaults for secrets.
3. Base template layout (head, nav, theme toggle, footer) with the favicon
   links and Tailwind v4 build; htmx + Alpine vendored locally.
4. Route table: `GET /` (generate page), `GET /history`, plus empty
   handler stubs that stages 05–06 fill in.
5. `go build ./...`, `go vet ./...`, `gofmt -l .` clean; smoke-run with
   stub env and curl the routes.

## Outputs
| Artifact | Location | Format |
|----------|----------|--------|
| Go skeleton | ../../cmd/, ../../internal/, ../../web/ | Go + templates |
| Foundation notes | output/go-foundation.md | markdown |

## Audits
- [ ] Build/vet/gofmt clean; server boots and serves all stub routes.
- [ ] No secret literal anywhere in the tree.
