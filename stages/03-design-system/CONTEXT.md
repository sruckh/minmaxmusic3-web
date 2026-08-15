# Stage 03 — design-system

> Layer 2 · "What do I do?"

**Purpose:** Translate `DESIGN.md` and the two theme examples into Tailwind
v4 tokens and the component patterns every page reuses.

## Inputs
| Source | File/Location | Section/Scope | Why |
|--------|---------------|---------------|-----|
| Design spec | ../../DESIGN.md | full (read-only) | palette, type, components |
| Dark theme | ../../index-dark.html | full (read-only) | working dark reference |
| Light theme | ../../index-light.html | full (read-only) | working light reference |
| Favicons | ../../favicon_io.zip | contents | extract to static assets |
| Blueprint | ../01-project-blueprint/output/blueprint.md | page map | which components are needed |

## Process
1. Extract `favicon_io.zip` → `static/favicon/` (favicon.ico, PNGs,
   site.webmanifest); wire the manifest + icon links into the base layout.
2. Define the Tailwind v4 `@theme` block from the 7 palette tokens
   (burgundy-900 #5E1226, crimson-700 #A62043, crimson-600 #B12247,
   pearl-100 #E5DCDF, dusty-300 #CBBBBF, petrol-800 #00486F,
   meadow-600 #418B1B) — no colors outside the palette.
3. Theme pair: dark (canvas #5E1226, text #E5DCDF) and light (canvas
   #E5DCDF, text #5E1226) via a `data-theme`/class toggle; system font
   stack; H1 2.25rem/1.2 → caption 0.85rem/1.4 scale.
4. Component patterns: nav bar, metric cards, primary/secondary/ghost
   buttons, forms with focus states, data tables, audio player chrome,
   progress indicator — each as a named template partial spec.
5. Verify Tailwind v4 syntax (`@theme`, `@custom-variant`) against
   Context7 before writing the token file.

## Outputs
| Artifact | Location | Format |
|----------|----------|--------|
| Design tokens + component specs | output/design-system.md | markdown |
| Favicon pack | ../../static/favicon/ | binary + manifest |

## Checkpoints
- [ ] Human signs off both themes rendered side-by-side before stage 04.

## Audits
- [ ] Every color used traces to a DESIGN.md token.
- [ ] `sha256sum -c ../../.goals/baseline.sha256` still passes (assets untouched).
