# Infisical Administrator Credentials Integration

> Layer 3 · Static administrator secret management

## Architecture Overview
The administrator account is managed via **Infisical secrets** instead of database records:
- `ADMIN_USER`: The administrative username (e.g. `admin` or configured username).
- `ADMIN_PASSWORD`: The administrative plaintext/secret password.

## Security & Runtime Flow
1. **Zero Secret Persistence in DB**: Admin credentials never touch the SQLite database.
2. **Environment Injection**: Infisical injects `ADMIN_USER` and `ADMIN_PASSWORD` as environment variables at container startup using the existing Universal Auth machine identity.
3. **Config Loading**: `internal/config/config.go` reads `ADMIN_USER` and `ADMIN_PASSWORD` during `config.Load()`.
4. **Authentication Check**:
   - When a login request comes in for `username == cfg.AdminUser`:
   - Perform constant-time password comparison: `subtle.ConstantTimeCompare([]byte(inputPass), []byte(cfg.AdminPassword)) == 1`.
   - On match, issue a session with `user_id = "admin"`, `username = cfg.AdminUser`, and `is_admin = 1`.
5. **Fallback Guard**: If `ADMIN_USER` or `ADMIN_PASSWORD` is blank in config, admin login is disabled and a warning is logged.
