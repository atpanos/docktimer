# Docktimer

Docktimer is a small, self-hosted web application that starts and stops Docker
containers on a schedule. It runs as a single Go binary with a SQLite database and
is designed for home servers where a full orchestration stack would be overkill.

**One process. One database file. No Node.js, no React, no CDN.**

- **Go 1.27** (requirement was ≥ 1.24), **Gin**, **SQLite** (`modernc.org/sqlite`, pure Go)
- **HTMX 2.0.8** served locally, **Tailwind CSS 4** compiled to a static stylesheet
- **robfig/cron/v3** with one cron instance per time zone
- **Official Docker client** (`github.com/moby/moby/client`)
- **bcrypt**, server-side sessions, CSRF protection, login rate limiting

---

## Features

| Area | Function |
|------|----------|
| **Auth** | First-run setup wizard, sign in/out, `ADMIN`/`VIEWER` roles, password change, viewer management, session cookies with idle and absolute timeouts |
| **Containers** | List all containers (name, image, status, state), manual start/stop/restart, HTMX auto-refresh every 10 s, locked containers stay visible but cannot be controlled |
| **Rules** | Container + action + 5-field cron + IANA time zone + active/inactive, CRUD, toggle without reload, next-run display, cron preview |
| **Scheduler** | Runs in-process, never overlaps a rule with itself, logs every run, survives Docker outages |
| **Logs** | Time, container, action, trigger, status, duration, error message; filters, cursor pagination, retention |
| **Dashboard** | Running/stopped counts, active rules, upcoming actions, recent actions, prominent error box |
| **Security** | HttpOnly/SameSite cookies, CSRF on every POST, login rate limit (5/15 min), CSP, no shell execution, container allow/deny list, self-protection |
| **Operations** | `/healthz`, `/readyz`, multi-stage Dockerfile, Compose with resource limits, socket-proxy variant, backup guide |

---

## Requirements

- Docker ≥ 24 and Docker Compose v2 for container deployment
- Go ≥ 1.24 for local development (tested with 1.27.4)
- Optional: the Tailwind CSS standalone CLI, only when changing the stylesheet
- A Docker host reachable via `unix:///var/run/docker.sock` or a socket proxy

---

## Quick start with Docker Compose

```bash
git clone <your-repo> docktimer
cd docktimer

cp .env.example .env
# SESSION_SECRET is mandatory (at least 32 characters):
sed -i "s|^SESSION_SECRET=.*|SESSION_SECRET=$(openssl rand -base64 48)|" .env

# Recommended: Docker access through the socket proxy
docker compose -f docker-compose.yml -f docker-compose.socket-proxy.yml up -d --build
```

Then open **http://localhost:8080** → setup form → create the admin account.

Alternative with direct socket access (simpler, but much broader permissions):

```bash
stat -c '%g' /var/run/docker.sock     # e.g. 999 → set DOCKER_GID in .env
docker compose -f docker-compose.yml -f docker-compose.socket.yml up -d --build
```

The base `docker-compose.yml` deliberately does **not** mount the Docker socket.
Without one of the two overrides the app still starts, but shows
“Docker daemon is not reachable”.

### Start command (local, without Compose)

```bash
export SESSION_SECRET="$(openssl rand -base64 48)"
export DATABASE_PATH="$PWD/data/docktimer.db"
export APP_PORT=8080
export DEV_MODE=1
go run ./cmd/server
```

---

## First admin login

1. Open `http://localhost:8080/setup` (shown automatically on first start).
2. Enter a username (3–32 characters) and a password (at least 10 characters).
3. After creation you are signed in immediately. Setup is then permanently
   closed (`/setup` redirects to `/login`).
4. Create more users under **Settings → Users** (admins only).

There is **no** self-registration and no email-based password reset. If the admin
password is lost, only a database intervention by the operator helps (see
“Operations”).

---

## Local development

```bash
# Dependencies
go mod download

# Tests (work without a Docker daemon)
go test ./...
go vet ./...

# Rebuild the stylesheet (only needed after template/CSS changes)
tailwindcss --input web/tailwind/input.css --output web/static/css/app.css --minify

# Run it
cp .env.example .env
sed -i "s|^SESSION_SECRET=.*|SESSION_SECRET=$(openssl rand -base64 48)|" .env
sed -i "s|^DATABASE_PATH=.*|DATABASE_PATH=$PWD/data/docktimer.db|" .env
set -a; . ./.env; set +a
DEV_MODE=1 go run ./cmd/server
```

`DEV_MODE=1` enables text logs, reloads templates on every request and permits a
weak session secret (development only!). Production uses `DEV_MODE=0`.

### Project structure

```
cmd/server            composition root
internal/config       env parsing and validation
internal/database     SQLite, migrations, repositories
internal/auth         bcrypt, sessions, CSRF, rate limiting
internal/docker       engine client, scope, error mapping
internal/rules        rule validation and CRUD
internal/scheduler    cron per time zone, execution
internal/logging      execution log, retention
internal/web          Gin router, middleware, handlers, templates, view
migrations            embedded SQL migrations
web/static            app.css, htmx.min.js, app.js (embedded via go:embed)
```


---

## Docker socket security

The Docker socket is **root-equivalent**. Anyone who can write to it can start
privileged containers and mount host directories. Docktimer only uses:

- `GET /containers/json`
- `POST /containers/{id}/start`
- `POST /containers/{id}/stop`
- `POST /containers/{id}/restart`
- `PING` / `VERSION`

There is **no code** for exec, images, networks, volumes, builds or secrets.

### Recommendation: docker-socket-proxy

`docker-compose.socket-proxy.yml` starts Tecnativa's proxy:

- only on the internal `docktimer-net` network, **no published port**
- `CONTAINERS=1`, `POST=1`, `ALLOW_START/STOP/RESTARTS=1`
- every other API section stays disabled
- socket mounted read-only

```bash
docker compose -f docker-compose.yml -f docker-compose.socket-proxy.yml up -d
```

The proxy is therefore unreachable from the LAN; only Docktimer talks to it.

### Direct socket (alternative)

If you do not want a second container:

```bash
stat -c '%g' /var/run/docker.sock   # put the group ID into DOCKER_GID
docker compose -f docker-compose.yml -f docker-compose.socket.yml up -d
```

The container runs as UID 10001 and receives the socket group via `group_add`.
You could set `user: "0:0"` instead — this is discouraged.

For a **bind mount** of `/data`, the host directory must be owned by the
container user, otherwise SQLite fails to start:

```bash
mkdir -p data && sudo chown -R 10001:10001 data
```

The default Compose setup uses a named volume and does not need this step.

### Further hardening

- `CONTAINER_ALLOWLIST`/`CONTAINER_DENYLIST`: Docktimer only controls approved
  containers. Its own container (`HOSTNAME`) and `docker-socket-proxy` are always
  locked.
- Never expose the app to the internet without protection. If you need external
  access, use a reverse proxy with TLS, set `APP_URL=https://…` and
  `COOKIE_SECURE=true` (or `auto`).
- Only set `TRUSTED_PROXIES` when a proxy actually sits in front of the app.

---

## Environment variables

| Variable | Default | Required | Description |
|----------|---------|:--------:|-------------|
| `APP_PORT` | `8080` | – | HTTP port |
| `DATABASE_PATH` | `/data/docktimer.db` | – | SQLite file; directory is created |
| `SESSION_SECRET` | – | **yes** | ≥ 32 characters, signs CSRF tokens. `change-me` is rejected in production |
| `DEFAULT_TIMEZONE` | `Europe/Berlin` | – | IANA time zone for display and new rules |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | – | Docker endpoint, e.g. `tcp://docker-socket-proxy:2375` |
| `CONTAINER_STOP_TIMEOUT` | `30` | – | Graceful stop timeout in seconds (1–600) |
| `LOG_RETENTION` | `10000` | – | Maximum number of log entries (global) |
| `APP_URL` | `http://localhost:8080` | – | Base URL; decides `Secure` when `COOKIE_SECURE=auto` |
| `LOG_RETENTION_PER_CONTAINER` | `0` | – | Max entries per container (0 = off) |
| `SESSION_TTL_HOURS` | `12` | – | Absolute session lifetime |
| `SESSION_IDLE_MINUTES` | `60` | – | Idle timeout |
| `COOKIE_SECURE` | `auto` | – | `auto`/`true`/`false` |
| `TRUSTED_PROXIES` | empty | – | Comma-separated IPs/CIDRs for `X-Forwarded-*` |
| `CONTAINER_ALLOWLIST` | empty | – | Only control these names/ID prefixes (empty = all) |
| `CONTAINER_DENYLIST` | `docktimer,docker-socket-proxy` | – | Never control these |
| `LOG_LEVEL` | `info` | – | `debug`/`info`/`warn`/`error` |
| `DEV_MODE` | `0` | – | `1` = development (text logs, template reload, weak secret allowed) |
| `DOCKER_GID` | `999` | – | Docker socket group (Compose override only) |

---

## Cron examples

Format: **minute hour day month weekday** (5 fields, `Europe/Berlin` or the rule's
time zone). Seconds and `@daily` shorthands are rejected on purpose.

| Expression | Meaning |
|------------|---------|
| `0 8 * * 1-5` | Start on weekdays at 08:00 |
| `0 23 * * *` | Stop daily at 23:00 |
| `30 6 * * 6,7` | Weekends at 06:30 |
| `*/15 * * * *` | Every 15 minutes |
| `0 */4 * * *` | Every 4 hours on the hour |
| `0 9 1 * *` | 1st of every month at 09:00 |
| `45 22 * * 0` | Sundays at 22:45 |

**Example start rule:** container `nextcloud`, action `START`,
`0 8 * * 1-5`, `Europe/Berlin`, active.
**Example stop rule:** container `nextcloud`, action `STOP`,
`0 23 * * *`, `Europe/Berlin`, active.

The form previews the next three runs before saving and the rules table shows the
next scheduled run for every active rule.

---

## Backing up the SQLite database

Online (without stopping the app):

```bash
mkdir -p backups
sqlite3 data/docktimer.db "VACUUM INTO 'backups/docktimer-$(date +%F-%H%M).db'"
```

With Compose, on the same host (volume from `docker volume inspect`):

```bash
docker run --rm \
  -v docktimer_docktimer-data:/data \
  -v "$PWD/backups:/backup" \
  alpine:3.24 sh -c '
    apk add -q sqlite
    sqlite3 /data/docktimer.db "VACUUM INTO /backup/docktimer-$(date +%F-%H%M).db"
  '
```

`VACUUM INTO` creates a consistent copy in WAL mode without stopping the app.

**Restore**

```bash
docker compose down
# find the volume path: docker volume inspect docktimer_docktimer-data
sudo cp backups/docktimer-YYYY-MM-DD-HHMM.db /var/lib/docker/volumes/<volume>/_data/docktimer.db
sudo rm -f /var/lib/docker/volumes/<volume>/_data/docktimer.db-wal \
           /var/lib/docker/volumes/<volume>/_data/docktimer.db-shm
docker compose -f docker-compose.yml -f docker-compose.socket-proxy.yml up -d
```

Backups contain password and session hashes: treat them like secrets
(permissions `0600`, encrypted storage).

---

## Operations

- **Health:** `GET /healthz` (liveness, 200) and `GET /readyz` (DB + Docker, 503 when degraded)
- **Logs:** `docker compose logs -f docktimer` (JSON, one line per request)
- **Update:** `git pull && docker compose ... up -d --build`; migrations run automatically
- **Database work:** `sqlite3 /data/docktimer.db` (mind WAL mode)
- **Forgotten admin password:** stop the container, back up the DB, then replace
  `users.password_hash` with a temporary bcrypt hash — or recreate the database.
  There is intentionally no recovery endpoint.

### Reverse proxy

```caddy
docktimer.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Then set `APP_URL=https://docktimer.example.com`, `COOKIE_SECURE=auto` and
`TRUSTED_PROXIES=127.0.0.1/32` (or the proxy network).

---

## Tests & quality

```bash
go fmt ./...
go vet ./...
go test ./...
go test -race ./...
```

Covered:

- Rule validation (cron, time zone, action, next run including weekends/time zones)
- Password hashing, policies, CSRF binding, rate limiting
- Configuration validation
- Repository operations (users, sessions, rules, logs, retention, pagination, FK behavior)
- Docker service with a fake API (scope, self-protection, timeouts, error mapping)
- Scheduler (success, failure, skip on overlap, next run)
- HTTP integration test: setup → sign in → create rule → disable → manual stop → log → viewer cannot control

There are **no** Docker-dependent tests in the default run; the Docker layer is
mocked through an interface.

---

## Known limitations

- **One Docker host**, one process. No horizontal scaling (SQLite + in-memory cron).
- **5-field cron only** (minute granularity), no seconds, no `@` shorthands.
- **No Compose support**: projects are neither read nor validated or applied.
- **No image/network/volume management**, no pull/build/exec.
- **No notifications** (email/webhook/ntfy) and no password reset.
- **No audit of read access**; only actions are logged.
- **Missed runs are not caught up** after downtime.
- Container names are stored as a snapshot; a manually renamed container is only
  refreshed the next time the rule is edited.
- `VIEWER` sees the full dashboard and logs, but no user management.
- The socket proxy protects the Docker API, not against application misconfiguration.

---

## Roadmap (not in the MVP)

1. Read, validate and apply Compose projects (with diff preview)
2. Git history for Compose files
3. Notifications (ntfy, webhook, email) on failures
4. Multiple Docker hosts with host selection per rule
5. Catch-up rules after downtime, retry with backoff
6. Two-factor authentication, OIDC
7. Prometheus metrics and audit log
8. `templ` migration of templates (optional, cosmetic only)

---

## Releases and beta versions

Docktimer uses two channels:

| Trigger | Published tags | `latest`? |
|---------|----------------|:---------:|
| push to `beta` | `beta`, `sha-<short>` | no |
| tag `v0.0.1-beta.1` | `0.0.1-beta.1`, `sha-…` | no |
| push to `main` | `main`, `sha-…` | no |
| tag `v0.0.1` (stable) | `0.0.1`, `0.0`, `latest`, `sha-…` | yes |

Pin a specific image with `DOCKTIMER_TAG`:

```bash
# beta channel
DOCKTIMER_TAG=beta docker compose -f docker-compose.yml \
  -f docker-compose.socket-proxy.yml -f docker-compose.release.yml up -d

# fixed prerelease / release
DOCKTIMER_TAG=0.0.1-beta.1 docker compose -f docker-compose.yml \
  -f docker-compose.socket-proxy.yml -f docker-compose.release.yml up -d
```

`latest` is only moved by stable `v*` tags. Do not run beta versions against
production data without a backup: migrations are forward-only.

## License

MIT — see [LICENSE](LICENSE).

Copyright (c) 2026 atpanos. Version 0.0.1.

