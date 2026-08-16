# Identity — Multi-User Feature (minmaxmusic3-web)

> Layer 0 · "Where am I?"

## Purpose & Scope
This workspace specifies the multi-user architecture and implementation pipeline for **minmaxmusic3-web**. It transitions the application from a single-user homelab tool into an authenticated, multi-user system with role-based access control and song library partitioning.

## Core Invariants
1. **Admin via Infisical**: Admin credentials (`ADMIN_USER`, `ADMIN_PASSWORD`) are loaded securely from Infisical environment variables at runtime, never stored in the database.
2. **Database-Backed Users**: All standard user accounts are stored in SQLite (`users` table) with bcrypt-hashed passwords.
3. **Approval Gating**: New registrations are created in `pending` status and cannot access generation or history until approved by an administrator.
4. **Song Isolation & Public Sharing**: User songs default to private (`is_public = 0`). Users can toggle songs to `is_public = 1`.
5. **Partitioned Library**: The song history UI separates personal songs from public community songs.
6. **Admin Power**: Administrators have access to an `/admin` dashboard with a pending request badge, allowing user approval, disable, or deletion.

## Stack & Environment
- **Runtime**: Go 1.26+ standard library HTTP server + SQLite database
- **Frontend**: Go `html/template` + HTMX + Alpine.js + Tailwind CSS
- **Configuration & Secrets**: Infisical Universal Auth machine identity
- **Authentication**: HTTP-only session cookies with secure server-side session tracking
