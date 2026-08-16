# Stage 07 — UI Templates & Acceptance Testing

> Layer 2 · Stage Contract

## Inputs
| Layer | Path | Purpose |
|-------|------|---------|
| 3 | `../../_config/voice.md` | UI copy and notification messaging standards |
| 4 | `../06-admin-management/output/admin-spec.md` | Admin dashboard and badge endpoints |

## Process
1. Create `web/templates/login.html` and `web/templates/register.html` with clean responsive forms, validation feedback, and theme support.
2. Update `web/templates/layout.html` to show authenticated username, logout button, and the Admin tab with pending badge pill.
3. Update `web/templates/history.html` to render two distinct sections: Personal Songs ("My Songs") and Public Songs ("Community Songs").
4. Create `web/templates/admin.html` with user management table, approval/disable/delete action buttons, and status indicators.
5. Add public toggle component with HTMX trigger to `web/templates/song.html` and history song cards.
6. Execute full end-to-end acceptance test suite covering registration, approval gate, admin actions, song isolation, and public sharing.
7. Run `go test ./...` to verify all unit and integration tests pass with zero failures.

## Outputs
| Artifact | Location | Format |
|----------|----------|--------|
| Multi-User Acceptance Report | `output/acceptance-report.md` | Markdown |
