# Acetate

![](https://images.disinfo.zone/uploads/PfZMnuFULcoAW8QyRLUJ3q7LbWLXJ4WGL5XUZc9H.jpg)

A self-hosted, password-gated album listening room.

Acetate serves multiple albums from local directories, protects playback behind password-gated access, includes synchronized lyrics and an oscilloscope visualizer, and records per-album listener analytics for the admin dashboard.

## Highlights

- Go backend (`net/http` + `chi`) with embedded static frontend.
- Multi-album support with per-album directories, tracks, cover art, and analytics.
- Password-gated access: each password can grant access to one or more albums.
- Album selector UI when a password unlocks multiple albums.
- SQLite persistence for albums, tracks, passwords, sessions, and analytics.
- Admin dashboard for album management, password management, track order, cover upload, and analytics.
- MP3 range streaming for seek support and iOS playback compatibility.
- Lyrics priority: `.lrc` -> `.txt` -> `.md`.
- Listener UX: persistent resume state (track/time/volume), deep links, keyboard shortcuts, clickable timed lyrics.
- Service worker for static + API + audio caching (offline playback for already-opened albums).
- Docker-ready deployment.

## Listener UX

- Password gate: enter a passphrase to access linked album(s).
- Album selector: when a password grants access to multiple albums, choose from a grid of album covers.
- Resume from last playback position per device.
- Deep-link support: `?track=<stem|title|index>&t=<seconds|mm:ss|hh:mm:ss>`.
- Gapless double-deck playback with next-track preloading and prefetch.
- Timed lyrics:
  - line highlighting with lead offset
  - click/keyboard seek from lyric lines
  - section-aware formatting from optional `.txt`/`.md` structure hints
- Keyboard shortcuts:
  - `Space`: play/pause
  - `Left/Right`: seek -/+5 seconds
  - `Up/Down`: volume +/-
  - `L`: toggle lyrics visibility
- Mobile ergonomics:
  - sticky control area
  - larger touch targets
  - safe-area-aware padding
  - improved scroll containment

## PWA / Offline

- Static assets are cached for app shell startup.
- Authenticated API responses for album tracks, cover, and lyrics are cached.
- Audio streams are cached (bounded LRU-style size) and can be served offline, including ranged playback from cache.
- App keeps a local album snapshot for offline startup after a successful authenticated session.

## Architecture

- `cmd/server`: app entrypoint.
- `internal/server`: HTTP routes, middleware, auth gating, admin APIs.
- `internal/albums`: album, track, and password storage (SQLite-backed).
- `internal/config`: MP3 metadata scanning and legacy config.json support.
- `internal/auth`: listener/admin sessions, rate limit, Cloudflare IP trust logic.
- `internal/analytics`: event ingestion buffer and aggregated queries (per-album).
- `internal/database`: SQLite open + migrations.
- `internal/album`: track streaming, cover serving, lyric resolution.
- `static/`: listener SPA and admin UI (embedded via `embed.go`).

### Data model

Albums, tracks, passwords, and their relationships are stored in SQLite:

- **albums**: id, slug, title, artist, album_path
- **album_tracks**: album_id, stem, title, display_index, sort_order
- **listener_passwords**: id, label, password_hash
- **password_album_access**: password_id, album_id (many-to-many)

Each album points to a directory on disk containing MP3 files, lyrics, and cover art.

### Persistent storage

- Each album directory (read-only): audio + lyrics + default cover.
- `/data` (read-write): `acetate.db`.

### Migration from single-album

Existing single-album installations using `config.json` are automatically migrated on first startup. The album title, artist, tracks, and password are imported into the database, and `config.json` is renamed to `config.json.migrated`.

## Requirements

- Go 1.24+ (for local run)
- Docker + Docker Compose (for containerized run)

## Quick Start (Recommended)

### 1) Prepare album files

Create an `album/` folder at repo root:

```text
album/
  cover.jpg
  01-gathering.mp3
  01-gathering.lrc
  02-hollow.mp3
  02-hollow.txt
```

### 2) Run setup wizard

```bash
go run ./cmd/setupwizard
```

The wizard will:

- validate album/data paths,
- generate/load `data/config.json`,
- let you set title/artist,
- hash and store listener passphrase,
- configure bootstrap admin username/password (bcrypt-hashed),
- write `.env` with runtime values.

On first server startup, the config.json data is migrated into the database.

### 3) Start the server

```bash
go run ./cmd/server
```

Then open:

- Listener UI: `http://localhost:8080`
- Admin UI: `http://localhost:8080/admin`

### 4) Add more albums (via admin UI)

1. Open `/admin` and log in.
2. In the Albums section, create a new album (title, artist, path to album directory).
3. In the Passwords section, create a password and link it to one or more albums.
4. Share the password with listeners.

## Setup Wizard

Command:

```bash
go run ./cmd/setupwizard
```

Prompts:

- album folder path (default `./album`)
- data folder path (default `./data`)
- listen address (default `:8080`)
- album title and artist
- listener passphrase (bcrypt-hashed before saving)
- admin username
- admin password (bcrypt-hashed before writing `.env`)

Outputs:

- `data/config.json` (migrated to database on first server boot)
- `.env`

Notes:

- Wizard output stores `ADMIN_PASSWORD_HASH`, not plaintext admin passwords.
- You can rerun the wizard safely to update metadata and credentials.

## Manual Setup (Without Wizard)

### 1) Start once to bootstrap

```bash
go run ./cmd/server
```

If `data/config.json` exists, Acetate migrates it into the database. Otherwise, it scans `album/*.mp3` and creates a default album.

### 2) Set bootstrap admin credentials

Set env vars before first startup:

```bash
ADMIN_USERNAME=admin ADMIN_PASSWORD_HASH='$2a$10$...' go run ./cmd/server
```

If you skip bootstrap env vars, the server still starts and `/admin` shows a one-time UI setup form to create the first admin account.

### 3) Configure albums and passwords via admin UI

Open `/admin`, create albums pointing to directories with MP3 files, and create passwords linked to those albums.

## Docker

### Build and run

```bash
docker compose up -d --build
```

### Run setup wizard via Compose

```bash
docker compose --profile tools run --rm wizard
```

This launches the interactive wizard in a one-off container and writes `.env` / `data/config.json` into your local repo.

Compose mounts:

- `./album:/album:ro`
- `./data:/data`

Expose:

- `8080:8080`

Environment in `docker-compose.yml`:

- `ADMIN_USERNAME=${ADMIN_USERNAME}`
- `ADMIN_PASSWORD_HASH=${ADMIN_PASSWORD_HASH}`
- `LISTEN_ADDR=:8080`
- `ALBUM_PATH=/album`
- `DATA_PATH=/data`

Create a local `.env` with at least:

```env
ADMIN_USERNAME=admin
ADMIN_PASSWORD_HASH=$2a$10$replace-with-bcrypt-hash
```

## Configuration Reference

### Environment variables

| Variable | Default | Description |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | HTTP bind address |
| `ALBUM_PATH` | `./album` | Default album directory (used for initial migration) |
| `DATA_PATH` | `./data` | Writable state directory (database) |
| `ADMIN_USERNAME` | `admin` | Bootstrap username when `admin_users` is empty |
| `ADMIN_PASSWORD_HASH` | empty | Bootstrap bcrypt hash when `admin_users` is empty |
| `ADMIN_PASSWORD` | empty | Bootstrap plaintext password alternative (hashed on startup; avoid in prod) |

## API Surface

Listener endpoints:

- `POST /api/auth` — authenticate with passphrase, returns accessible albums
- `DELETE /api/auth` — logout
- `GET /api/session` — verify session, returns accessible albums
- `GET /api/albums` — list accessible albums
- `GET /api/albums/{slug}/tracks` — album track list
- `GET /api/albums/{slug}/cover` — album cover art
- `GET /api/albums/{slug}/stream/{stem}` — stream MP3
- `GET /api/albums/{slug}/lyrics/{stem}` — fetch lyrics
- `POST /api/albums/{slug}/analytics` — submit event batch

Admin endpoints:

- `POST /admin/api/auth` — admin login
- `DELETE /admin/api/auth` — admin logout
- `GET /admin/api/setup/status` — first-run setup check
- `POST /admin/api/setup` — create first admin account
- `GET /admin/api/config` — dashboard overview
- `GET /admin/api/admin-users` — list admin users
- `POST /admin/api/admin-users` — create admin user
- `PUT /admin/api/admin-users/{id}` — update admin user
- `PUT /admin/api/admin-password` — change own admin password
- `GET /admin/api/albums` — list all albums
- `POST /admin/api/albums` — create album
- `GET /admin/api/albums/{id}` — get album
- `PUT /admin/api/albums/{id}` — update album
- `DELETE /admin/api/albums/{id}` — delete album
- `GET /admin/api/albums/{id}/tracks` — get album tracks
- `PUT /admin/api/albums/{id}/tracks` — update album tracks
- `POST /admin/api/albums/{id}/cover` — upload album cover
- `GET /admin/api/albums/{id}/analytics` — album analytics
- `GET /admin/api/albums/{id}/reconcile` — preview track reconciliation
- `POST /admin/api/albums/{id}/reconcile` — apply track reconciliation
- `GET /admin/api/passwords` — list listener passwords
- `POST /admin/api/passwords` — create listener password
- `PUT /admin/api/passwords/{id}` — update listener password
- `DELETE /admin/api/passwords/{id}` — delete listener password
- `GET /admin/api/ops/health` — server health
- `GET /admin/api/ops/stats` — system statistics
- `POST /admin/api/ops/maintenance` — trigger analytics maintenance
- `GET /admin/api/export/events` — export raw events
- `GET /admin/api/export/backup` — export database backup

## Security Model

- Listener and admin auth use separate HttpOnly cookies.
- Admin auth uses DB-backed `admin_users` with bcrypt password hashes.
- First-run admin setup flow exists when no admin users are present.
- Listener passwords are verified with bcrypt against the `listener_passwords` table.
- Each listener session is bound to the password used, enforcing per-album access control.
- Session IDs are cryptographically random and server-stored.
- Session expiry:
  - listener: 7 days, sliding
  - admin: 1 hour, fixed
- Admin sessions are bound to coarse client fingerprint (IP hash + user-agent hash).
- Repeated failed admin logins trigger lockout/backoff throttling.
- Forced admin password reset mode can restrict admin actions until password rotation is completed.
- Admin mutating endpoints enforce same-origin `Origin` check.
- Stem validation blocks traversal and only allows configured tracks.
- Cover upload validates image type/dimensions before storage.
- Cloudflare client-IP trust only applies when request source is in Cloudflare IP ranges.
- Global security headers include strict CSP (`style-src 'self'`), `X-Frame-Options`, and `nosniff`.

## Analytics

Client submits event batches to the album-scoped analytics endpoint.

Tracked event types:

- `play`
- `pause`
- `seek`
- `complete`
- `dropout`
- `heartbeat`
- `session_start`
- `session_end`

Server ingestion behavior:

- buffered channel + periodic batch flush to SQLite
- per-album scoping (each event tagged with album_id)
- bounded batch/metadata validation
- backpressure with high-value event priority
- graceful shutdown flush

## Development

### Run tests

```bash
go test ./...
go test -race ./...
```

### Useful checks

```bash
node --check static/js/app.js
node --check static/js/player.js
node --check static/js/lyrics.js
node --check static/sw.js
```

## Operations

### Upgrades

- Database schema migrations run automatically on startup.
- Existing single-album installations are auto-migrated to multi-album on first boot.

### Back up state

Back up `data/`:

- `data/acetate.db`

### Restore

1. Stop server/container.
2. Restore `data/acetate.db`.
3. Start server/container.

## Troubleshooting

- Login always fails: verify a listener password has been created in the admin dashboard.
- Admin login fails on first boot: ensure `ADMIN_USERNAME` + `ADMIN_PASSWORD_HASH` (or `ADMIN_PASSWORD`) are set, or open `/admin` and complete the first-time setup form.
- No tracks shown: confirm `.mp3` files exist in the album directory and stems match the database track list.
- Album not accessible: verify a password is linked to the album and the listener is using the correct passphrase.
- Deep link not applying: verify URL includes `track`/`t` parameters and stems/titles match current album tracks.
- Resume point not restoring: ensure browser storage is enabled (private modes may block or purge local storage).
- Cover upload rejected: use valid JPEG/PNG with reasonable dimensions.
- Rate limited on auth: wait for the limiter window to reset.
- After frontend updates, hard-refresh once so the latest service worker and JS are active.

## File Tree

```text
cmd/
  hashpass/
  server/
  setupwizard/
internal/
  album/
  albums/
  analytics/
  auth/
  config/
  database/
  server/
static/
docker-compose.yml
Dockerfile
```
