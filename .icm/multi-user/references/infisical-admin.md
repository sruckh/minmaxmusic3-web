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
   - On match, issue a session with `user_id = server.ConfigAdminUserID` (`"config:admin"`), `username = cfg.AdminUser`, and `config_admin = 1`.
   - The username match is itself constant-time, and the admin branch performs
     one decoy bcrypt comparison, so the administrator's username does not
     stand out by response time. Registration refuses `ADMIN_USER`, so no
     database account can shadow it.
5. **Fallback Guard**: If `ADMIN_USER` or `ADMIN_PASSWORD` is blank in config, admin login is disabled and a warning is logged.

### Why `config:admin` and not `admin`

The static administrator has no `users` row, so its session `user_id` must never
collide with a generated one. Generated ids are 32 lowercase hex characters and
hex cannot contain a colon; `TestConfigAdminIDCannotCollide` pins that. The
sessions table therefore carries a stored `config_admin` flag for this one
identity, while every other session resolves privilege and status live from
`users` on each request.

Because it has no row, the configured administrator is also untargetable by the
admin actions: approve, disable, and delete on `config:admin` are refused with
an explanatory notice rather than falling through to "not found".

## Operating this (deployment)

`ADMIN_USER` and `ADMIN_PASSWORD` are ordinary secrets in the Infisical project
(`mini-max-music3-z96r`, env `dev`), alongside `RUNPOD_*` and `LLM_*`. They are
injected by `docker/entrypoint.sh` via `infisical run`; nothing in this
repository ever holds a value.

**The failure mode is silent.** Missing `RUNPOD_*`/`LLM_*` breaks the first
generation loudly. Missing admin credentials breaks nothing visible: the
container passes its health check, `/register` keeps accepting accounts, and no
one can ever approve them. There is no default administrator, no bootstrap flag,
and no role-promotion endpoint to escape with — the only recovery is to set both
secrets and restart.

The three places this is written down for an operator, all of which must stay in
step:

- `README.md` § *Authentication & Administration* — the full flow.
- `docker-compose.yml` — the comment enumerating what arrives via `infisical run`.
- `scripts/up.sh` — warns after bring-up when `admin_login=true` is absent from
  the `config loaded` log line.

`Config.Summary()` prints `admin_user=set|(unset) admin_password=true|false
admin_login=true|false` — presence only, never a value.
