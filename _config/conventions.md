# Conventions — minmaxmusic3-web

Project-specific conventions (Layer 3). See the `icm` skill's
`references/icm-conventions.md` for the full methodology rules.

## Code
- Go server + `html/template`; htmx + Alpine.js on the client; Tailwind CSS v4.
- No JS build pipeline beyond what Tailwind v4 requires; prefer CDN-free,
  vendored assets so the container is self-contained.
- Every user-visible string lives in the template layer; Go handlers return
  template fragments for htmx swaps.

## Secrets
- Never a secret literal in repo, compose, or image layer. Client ID,
  project ID, and slug are not secrets; the client secret travels only the
  gpg path (`scripts/seed-infisical-secret.sh` → `~/.config/mm3-web-infisical/`).

## Design assets
- `DESIGN.md`, `index-dark.html`, `index-light.html`, `favicon_io.zip` are
  read-only. Integrity check: `sha256sum -c .goals/baseline.sha256`.

## Scratch
- All scratch/working files go to `.goals/`, never the repo root.
