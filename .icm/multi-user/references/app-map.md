# Application Route & Access Control Map

> Layer 3 · Endpoint routing and authorization matrix

## Route Definitions & Access Levels

| Path | Method | Auth Level | Purpose |
|------|--------|------------|---------|
| `/` | GET | Authenticated | Main studio & generation UI |
| `/login` | GET, POST | Public | User/Admin authentication |
| `/register` | GET, POST | Public | New user signup (creates pending status) |
| `/logout` | POST | Authenticated | Session invalidation and cookie clear |
| `/history` | GET | Authenticated | Partitioned song library (Personal vs Public) |
| `/history/personal` | GET | Authenticated | HTMX fragment for personal songs |
| `/history/public` | GET | Authenticated | HTMX fragment for community public songs |
| `/songs/{id}` | GET | Owner / Public / Admin | Song detail view |
| `/songs/{id}/delete` | POST | Owner / Admin | Delete song and audio files |
| `/songs/{id}/toggle-public` | POST | Owner / Admin | Toggle `is_public` boolean flag |
| `/api/assistant` | POST | Authenticated | AI assistant prompt proxy |
| `/api/jobs` | POST | Authenticated | Submit music generation job |
| `/api/jobs/{id}/fragment` | GET | Owner / Admin | HTMX polling fragment for active job |
| `/audio/{id}` | GET | Owner / Public / Admin | Audio stream/playback |
| `/admin` | GET | Admin Only | User administration dashboard with pending badge |
| `/admin/users/{id}/approve` | POST | Admin Only | Approve pending user |
| `/admin/users/{id}/disable` | POST | Admin Only | Disable user account |
| `/admin/users/{id}/delete` | POST | Admin Only | Permanently delete user |

## Middleware Enforcement
- **`RequireAuth`**: Wraps protected endpoints, redirects unauthenticated browser requests to `/login`.
- **`RequireAdmin`**: Enforces admin session role for `/admin` subpaths.
- **Context Injection**: Injects current `UserContext` (`UserID`, `Username`, `IsAdmin`, `PendingCount`) into all request contexts.
