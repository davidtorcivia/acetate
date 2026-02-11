# Acetate

A self-hosted, password-gated album listening room.

Acetate serves one album from local files, protects playback behind a shared passphrase, includes synchronized lyrics and an oscilloscope visualizer, and records listener analytics for the admin dashboard.

## Highlights

- Go backend (`net/http` + `chi`) with embedded static frontend.
- SQLite persistence for sessions and analytics.
- Password gate with bcrypt verification and server-side sessions.
- Admin dashboard for track order, passphrase update, cover upload, and analytics.
- MP3 range streaming for seek support and iOS playback compatibility.
- Lyrics priority: `.lrc` -> `.txt` -> `.md`.
- Service worker for static caching and bounded audio cache behavior.
- Docker-ready deployment.

## Architecture

- `cmd/server`: app entrypoint.
- `internal/server`: HTTP routes, middleware, auth gating, admin APIs.
- `internal/config`: `config.json` load/save and first-boot generation from album files.
- `internal/auth`: listener/admin sessions, rate limit, Cloudflare IP trust logic.
- `internal/analytics`: event ingestion buffer and aggregated queries.
- `internal/database`: SQLite open + migrations.
- `internal/album`: track streaming, cover serving, lyric resolution.
- `static/`: listener SPA and admin UI (embedded via `embed.go`).

Persistent storage model:

- `/album` (read-only): audio + lyrics + default cover.
- `/data` (read-write): `acetate.db`, `config.json`, `cover_override.jpg`.

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

### 3) Start the server

```bash
go run ./cmd/server
```

Then open:

- Listener UI: `http://localhost:8080`
- Admin UI: `http://localhost:8080/admin`

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

- `data/config.json`
- `.env`

Notes:

- Wizard output stores `ADMIN_PASSWORD_HASH`, not plaintext admin passwords.
- You can rerun the wizard safely to update metadata and credentials.

## Manual Setup (Without Wizard)

### 1) Start once to auto-generate config

```bash
go run ./cmd/server
```

If `data/config.json` does not exist, Acetate scans `album/*.mp3` and creates a default config.

### 2) Set listener passphrase hash

```bash
go run ./cmd/hashpass "your passphrase"
```

Paste the output hash into `data/config.json` under `"password"`.

### 3) Set bootstrap admin credentials

Set env vars before first startup:

```bash
ADMIN_USERNAME=admin ADMIN_PASSWORD_HASH='$2a$10$...' go run ./cmd/server
```

If you skip bootstrap env vars, the server still starts and `/admin` shows a one-time UI setup form to create the first admin account.

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
| `ALBUM_PATH` | `./album` | Album media directory |
| `DATA_PATH` | `./data` | Writable state directory |
| `ADMIN_USERNAME` | `admin` | Bootstrap username when `admin_users` is empty |
| `ADMIN_PASSWORD_HASH` | empty | Bootstrap bcrypt hash when `admin_users` is empty |
| `ADMIN_PASSWORD` | empty | Bootstrap plaintext password alternative (hashed on startup; avoid in prod) |

### `data/config.json`

Example:

```json
{
  "title": "Album Title",
  "artist": "Artist Name",
  "password": "$2a$10$...",
  "tracks": [
    { "stem": "01-gathering", "title": "Gathering" },
    { "stem": "02-hollow", "title": "Hollow", "display_index": "II" }
  ]
}
```

Field behavior:

- `tracks` order is canonical playback order.
- `stem` maps to `{stem}.mp3` and lyric files in album folder.
- `password` must be a bcrypt hash.

## API Surface

Listener endpoints:

- `POST /api/auth`
- `DELETE /api/auth`
- `GET /api/session`
- `GET /api/tracks`
- `GET /api/cover`
- `GET /api/stream/{stem}`
- `GET /api/lyrics/{stem}`
- `POST /api/analytics`

Admin endpoints:

- `POST /admin/api/auth`
- `DELETE /admin/api/auth`
- `GET /admin/api/config`
- `GET /admin/api/tracks`
- `PUT /admin/api/tracks`
- `PUT /admin/api/password`
- `PUT /admin/api/admin-password`
- `POST /admin/api/cover`
- `GET /admin/api/analytics`
- `GET /admin/api/reconcile`
- `POST /admin/api/reconcile`
- `GET /admin/api/ops/health`
- `GET /admin/api/ops/stats`
- `POST /admin/api/ops/maintenance`
- `GET /admin/api/export/events`
- `GET /admin/api/export/backup`

## Security Model

- Listener and admin auth use separate HttpOnly cookies.
- Admin auth uses DB-backed `admin_users` with bcrypt password hashes.
- Passphrase is verified with bcrypt hash from config.
- Session IDs are cryptographically random and server-stored.
- Session expiry:
  - listener: 7 days, sliding
  - admin: 1 hour, fixed
- Admin mutating endpoints enforce same-origin `Origin` check.
- Stem validation blocks traversal and only allows configured tracks.
- Cover upload validates image type/dimensions before storage.
- Cloudflare client-IP trust only applies when request source is in Cloudflare IP ranges.
- Global security headers include CSP, `X-Frame-Options`, and `nosniff`.

## Analytics

Client submits event batches to `/api/analytics`.

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

### Back up state

Back up `data/`:

- `data/acetate.db`
- `data/config.json`
- `data/cover_override.jpg` (if present)

### Restore

1. Stop server/container.
2. Restore files into `data/`.
3. Start server/container.

## Troubleshooting

- `album path does not exist`: check `ALBUM_PATH` or wizard path.
- Login always fails: verify `config.json.password` is a bcrypt hash, not plaintext.
- Admin login fails on first boot: ensure `ADMIN_USERNAME` + `ADMIN_PASSWORD_HASH` (or `ADMIN_PASSWORD`) are set.
- If no admin exists yet, open `/admin` and complete the first-time setup form.
- No tracks shown: confirm `.mp3` files exist at album root and track stems match config.
- Cover upload rejected: use valid JPEG/PNG with reasonable dimensions.
- Rate limited on auth: wait for the limiter window to reset.

## File Tree

```text
cmd/
  hashpass/
  server/
  setupwizard/
internal/
  album/
  analytics/
  auth/
  config/
  database/
  server/
static/
design_doc.md
docker-compose.yml
Dockerfile
```
