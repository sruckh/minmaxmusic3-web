# Conventions — Multi-User minmaxmusic3-web

> Layer 3 · Stable conventions & design patterns

## Authentication & Session Management
- **Password Storage**: Standard users store passwords hashed with `golang.org/x/crypto/bcrypt` (cost >= 12).
- **Session Tokens**: 32-byte cryptographically secure random hex strings stored in `sessions` table.
- **Cookies**: Session cookies named `mm3_session` must be `HttpOnly`, `SameSite=Lax`, and `Path=/`.
- **Admin Verification**: If username matches `ADMIN_USER` from Infisical config, verify password against `ADMIN_PASSWORD` using constant-time comparison.

## Authorization & Status Checking
- **User Status Enum**: `pending`, `approved`, `disabled`.
- **Middleware Chain**:
  - `RequireAuth`: Verifies active session token, resolves user context, rejects unapproved users.
  - `RequireAdmin`: Verifies active session belongs to the admin role.
- **Unapproved Behavior**: Users with status `pending` or `disabled` are redirected to `/login` with an informational notice.

## Data Ownership & Multi-Tenancy
- **User ID Foreign Keys**: All `jobs` and `songs` records must have a non-null `user_id` column.
- **Query Scoping**:
  - Personal library queries filter strictly by `WHERE user_id = ?`.
  - Public library queries filter by `WHERE is_public = 1`.
  - Deletion/Update operations must enforce `WHERE id = ? AND (user_id = ? OR is_admin)`.

## Error Handling & Responses
- Protected JSON endpoints return HTTP 401/403 with standard error JSON.
- Protected HTML endpoints redirect unauthenticated requests to `/login?next=...`.
