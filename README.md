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
- Service worker caching so playback survives a dropped connection mid-session.
- Docker-ready deployment.

## Listener UX

- Password gate: enter a passphrase to access linked album(s).
- Album selector: when a password grants access to multiple albums, choose from a grid of album covers.
- Resume from last playback position per device.
- Deep-link support: `?album=<slug>&track=<stem|title|index>&t=<seconds|mm:ss|hh:mm:ss>`.
- Gapless double-deck playback with next-track preloading.
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

Listeners enter the passphrase on every visit, so there is no offline startup. Within a session the service worker keeps playback going through a dropped connection:

- Static assets are cached (stale-while-revalidate) for the app shell.
- Album track lists, covers, and lyrics are cached network-first.
- Each played track is cached once in full (the newest 24 are kept) and served from cache, including ranged reads.

## Architecture

- `cmd/server`: app entrypoint.
- `internal/server`: HTTP routes, middleware, auth gating, admin APIs.
- `internal/albums`: album, track, and password storage (SQLite-backed), legacy `config.json` import.
- `internal/auth`: listener/admin sessions, rate limiting, trusted-proxy client IP resolution.
- `internal/analytics`: event ingestion buffer, per-album queries, exports, retention.
- `internal/database`: SQLite open + migrations.
- `internal/album`: folder scanning (ID3 titles), track streaming, cover serving, lyric resolution.
- `static/`: listener SPA and admin UI (embedded via `embed.go`).

### Data model

Albums, tracks, passwords, and their relationships are stored in SQLite:

- **albums**: id, slug, title, artist, album_path
- **album_tracks**: album_id, stem, title, display_index, sort_order
- **listener_passwords**: id, label, password_hash
- **password_album_access**: password_id, album_id (many-to-many)

Each album points to a directory on disk containing MP3 files, lyrics, and cover art.

### Persistent storage

- Each album directory (read-only): audio + lyrics + default cover. Album directories must live inside `ALBUM_PATH`.
- `/data` (read-write): `acetate.db` and uploaded covers in `covers/<album id>/`.

### Migration from single-album

Existing single-album installations using `config.json` are migrated on the first startup with an empty albums table. The album title, artist, tracks, and password are imported into the database, and `config.json` is renamed to `config.json.migrated`. A legacy `data/cover_override.jpg` is moved to the first album's cover directory.

## Requirements

- Go 1.24+ (for local run)
- Docker + Docker Compose (for containerized run)

## Quick Start

### 1) Prepare album files

Create an `album/` folder at repo root with one subfolder per album (or put a single album directly in `album/`):

```text
album/
  my-record/
    cover.jpg
    01-gathering.mp3
    01-gathering.lrc
    02-hollow.mp3
    02-hollow.txt
```

Only files ending in lowercase `.mp3` are imported. Track titles come from the MP3's ID3 title tag, falling back to the file name.

### 2) Start the server

```bash
go run ./cmd/server
```

Then open:

- Listener UI: `http://localhost:8080`
- Admin UI: `http://localhost:8080/admin`

### 3) Create the first admin

On first visit `/admin` shows a one-time setup form. Alternatively, set `ADMIN_USERNAME` and `ADMIN_PASSWORD_HASH` (a bcrypt hash) before the first startup:

```bash
ADMIN_USERNAME=admin ADMIN_PASSWORD_HASH='$2a$10$...' go run ./cmd/server
```

Admin passwords must be 12-72 characters with at least one letter and one digit.

### 4) Add albums and passwords

1. In the Albums section, create an album by picking its folder. Tracks are imported from the folder.
2. In the Passwords section, create a listener passphrase and link it to one or more albums.
3. Share the passphrase with listeners.

## Docker

### Build and run

```bash
docker compose up -d --build
```

Compose mounts:

- `./album:/album:ro`
- `./data:/data`

Expose:

- `127.0.0.1:8087:8080` (loopback only; a tunnel or reverse proxy on the host fronts it)

Environment in `docker-compose.yml` (read from `.env`):

- `ADMIN_USERNAME`, `ADMIN_PASSWORD_HASH` (optional; `/admin` setup works without them)
- `TRUSTED_PROXIES` (see below)
- `LISTEN_ADDR=:8080`, `ALBUM_PATH=/album`, `DATA_PATH=/data`

Compose interpolates `$` in `.env` values, so quote a bcrypt hash with single quotes:

```env
ADMIN_USERNAME=admin
ADMIN_PASSWORD_HASH='$2a$10$replace-with-bcrypt-hash'
```

### Behind a reverse proxy or Cloudflare Tunnel

Rate limiting, admin lockout, and admin session binding all key on the client IP. Behind `cloudflared`, Caddy, or nginx the TCP peer is the proxy, so every visitor looks the same until you tell Acetate which peers to trust:

`docker-compose.yml` publishes the port on loopback only and defaults to `TRUSTED_PROXIES=172.16.0.0/12`, which covers the Docker bridge gateway that host processes such as `cloudflared` connect through. For a local run behind a proxy, set it yourself, for example `TRUSTED_PROXIES=127.0.0.1`.

From a trusted peer Acetate uses the rightmost `X-Forwarded-For` hop that is not itself trusted (Cloudflare, cloudflared, Caddy, and nginx all append the client there). `CF-Connecting-IP` is ignored, since a non-Cloudflare proxy would pass a client-forged copy through. Requests from any other peer use the TCP address and their forwarding headers are ignored.

## Configuration Reference

### Environment variables

| Variable | Default | Description |
| --- | --- | --- |
| `LISTEN_ADDR` | `:8080` | HTTP bind address |
| `ALBUM_PATH` | `./album` | Album base directory; album folders must be inside it |
| `DATA_PATH` | `./data` | Writable state directory (database, uploaded covers) |
| `ADMIN_USERNAME` | `admin` | Bootstrap username when `admin_users` is empty |
| `ADMIN_PASSWORD_HASH` | empty | Bootstrap bcrypt hash when `admin_users` is empty |
| `ADMIN_PASSWORD` | empty | Bootstrap plaintext password alternative (hashed on startup; avoid in prod) |
| `TRUSTED_PROXIES` | empty | Comma-separated IPs/CIDRs whose forwarding headers are trusted |
| `ANALYTICS_RETENTION_DAYS` | `0` | Delete raw analytics events older than this many days (`0` keeps them) |
| `ANALYTICS_MAINTENANCE_INTERVAL` | `12h` | How often retention pruning runs |

## API Surface

Listener endpoints:

- `POST /api/auth` — authenticate with passphrase, returns accessible albums
- `DELETE /api/auth` — logout (always succeeds)
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
- `GET /admin/api/album-folders` — list candidate album folders under the album base path
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
- `POST /admin/api/ops/maintenance` — run retention pruning now (optional body `{"retention_days": N}`)
- `GET /admin/api/export/events` — stream raw events (`format=json|csv`, optional `album_id`, `from`, `to`, `stems`, `event_types`, `limit`)
- `GET /admin/api/export/backup` — zip of a database snapshot plus uploaded covers

## Security Model

- Listener and admin auth use separate HttpOnly cookies.
- Admin auth uses DB-backed `admin_users` with bcrypt password hashes.
- First-run admin setup flow exists when no admin users are present.
- Listener passwords are verified with bcrypt against the `listener_passwords` table.
- Each listener session is bound to the password used, enforcing per-album access control.
- Session tokens are 32 random bytes; the database stores only their SHA-256, so a leaked backup holds no usable cookies.
- Rotating or deleting a listener passphrase signs out every listener who used it.
- Session expiry:
  - listener: 7 days, sliding
  - admin: 1 hour, fixed
- Admin sessions are bound to coarse client fingerprint (IP hash + user-agent hash).
- Login attempts are rate limited per client IP (IPv6 per /64), and repeated failed admin logins trigger lockout/backoff.
- Forced admin password reset mode can restrict admin actions until password rotation is completed.
- Admin mutating endpoints enforce same-origin `Origin` check.
- Stem validation blocks traversal and only allows configured tracks; albums a passphrase cannot open answer 404 like missing ones.
- Cover upload validates image type/dimensions before storage.
- Forwarding headers are only trusted from `TRUSTED_PROXIES`.
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
- per-album scoping (each event tagged with album_id; stems must belong to the album)
- bounded batch/metadata validation
- the dropout heatmap bins positions by track duration, which the client sends with pause/dropout/heartbeat events
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
- `data/covers/`

`GET /admin/api/export/backup` produces a consistent zip of both while the server runs.

### Restore

1. Stop server/container.
2. Restore `data/acetate.db` and `data/covers/`.
3. Start server/container.

## Troubleshooting

- Login always fails: verify a listener password has been created in the admin dashboard.
- Admin login fails on first boot: ensure `ADMIN_USERNAME` + `ADMIN_PASSWORD_HASH` (or `ADMIN_PASSWORD`) are set, or open `/admin` and complete the first-time setup form.
- No tracks shown: confirm `.mp3` files exist in the album directory and stems match the database track list.
- Album not accessible: verify a password is linked to the album and the listener is using the correct passphrase.
- Deep link not applying: verify URL includes `track`/`t` parameters and stems/titles match current album tracks.
- Resume point not restoring: ensure browser storage is enabled (private modes may block or purge local storage).
- Cover upload rejected: use valid JPEG/PNG with reasonable dimensions.
- Rate limited on auth: wait for the limiter window (one minute) to reset. If every visitor is limited together, set `TRUSTED_PROXIES`.

## File Tree

```text
cmd/
  server/
internal/
  album/
  albums/
  analytics/
  auth/
  database/
  server/
static/
docker-compose.yml
Dockerfile
```
