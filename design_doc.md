# Acetate — Design Document

*A password-gated album listening room. Sacred harp meets witch house.*

---

## Overview

A self-hosted, single-album web player behind a shared password gate. Serves MP3s from a local folder alongside synced LRC lyrics, an oscilloscope visualizer, and a prescribed listening order. Tracks listener engagement (plays, dropoffs, completion). Deployed via Docker, exposed through Cloudflare Tunnel.

The aesthetic is austere and intentional: Appalachian sacred harp colliding with witch house. No decoration for its own sake. Every element earns its place.

---

## Architecture

```
┌──────────────────────────────────────────────────┐
│  Docker Container                                │
│                                                  │
│  ┌───────────────┐    ┌───────────────────────┐  │
│  │   Go Server   │───▶│  SQLite (/data/)      │  │
│  │   (Chi/std)   │    │  + config.json        │  │
│  └──────┬────────┘    │  + cover override     │  │
│         │             └───────────────────────┘  │
│         │                                        │
│         ├── /api/auth      (session mgmt)        │
│         ├── /api/tracks    (tracklist+meta)       │
│         ├── /api/stream/*  (mp3 streaming)       │
│         ├── /api/lyrics/*  (lrc content)         │
│         ├── /api/analytics (event ingest)        │
│         ├── /admin/*       (admin UI+API)        │
│         └── /*             (SPA static)          │
│                                                  │
│  ┌────────────────────────────────────────────┐  │
│  │  /album/               (bind mount, ro)    │  │
│  │    cover.jpg            (default artwork)  │  │
│  │    01-gathering.mp3                        │  │
│  │    01-gathering.lrc                        │  │
│  │    02-hollow.mp3                           │  │
│  │    02-hollow.txt                           │  │
│  │    ...                                     │  │
│  └────────────────────────────────────────────┘  │
│                                                  │
│  ┌────────────────────────────────────────────┐  │
│  │  /data/                 (bind mount, rw)   │  │
│  │    acetate.db           (sessions, events) │  │
│  │    config.json          (order, password,  │  │
│  │                          track metadata)   │  │
│  │    cover_override.jpg   (optional)         │  │
│  └────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────┘
         │
    Cloudflare Tunnel
         │
    ┌────┴────┐
    │ Browser │  (SPA — vanilla JS or Svelte)
    └─────────┘
```

### Why Go

Single binary. No runtime dependencies. Compiles to a small Docker image (`scratch` or `alpine`). The server is essentially a static file server with a few authenticated JSON endpoints and an SQLite connection — Go handles this with zero overhead and the standard library covers most of it.

### Tech Choices

| Layer | Choice | Rationale |
|---|---|---|
| Backend | Go + `net/http` (or Chi for routing) | Minimal deps, fast, single binary |
| Database | SQLite via `modernc.org/sqlite` (pure Go) | No CGo, zero config, portable |
| Frontend | Vanilla JS or Svelte (compiled to static) | Small bundle, full control over rendering |
| Audio | HTML5 `<audio>` + Web Audio API | Oscilloscope needs AnalyserNode; `<audio>` handles background playback |
| Styling | Hand-written CSS, no framework | Total aesthetic control, no fighting defaults |
| Auth | Session cookie, bcrypt-hashed password | Simple, stateless-ish, secure enough for a speakeasy |
| Container | Docker, multi-stage build | Go builds to ~15MB binary; final image is tiny |

---

## File Structure

### `/album` (Read-Only Bind Mount)

The album folder is mounted read-only. It contains only immutable media assets — audio files, lyrics files, and a default cover image. The server never writes to this directory.

```
album/
  cover.jpg              # default album artwork
  01-gathering.mp3
  01-gathering.lrc       # preferred: time-synced lyrics
  02-hollow.mp3
  02-hollow.txt          # fallback: plain text lyrics
  03-descent.mp3
  03-descent.md          # also acceptable
  ...
```

### `/data` (Read-Write Bind Mount)

All mutable state lives here — configuration, sessions, analytics, and any admin overrides. This is the only directory the server writes to.

```
data/
  acetate.db             # SQLite: sessions, analytics events
  config.json            # track order, password, metadata (admin-editable)
  cover_override.jpg     # optional: replaces /album/cover.jpg if present
```

### `config.json`

Created on first boot by scanning `/album` for MP3s. Editable via the admin UI thereafter.

```json
{
  "title": "Album Title",
  "artist": "Artist Name",
  "password": "$2a$10$...",
  "tracks": [
    { "stem": "01-gathering", "title": "Gathering" },
    { "stem": "02-hollow", "title": "Hollow" },
    { "stem": "03-descent", "title": "The Descent", "display_index": "III" }
  ]
}
```

The `tracks` array is the canonical listening order. Each entry has a `stem` (used to resolve `{stem}.mp3`, `{stem}.lrc`/`.txt`/`.md` in `/album`), a `title` (displayed in the UI and Media Session metadata), and an optional `display_index` for custom numbering. The password is stored as a bcrypt hash. A CLI tool generates the hash: `go run ./cmd/hashpass "your passphrase"`.

**First-boot behavior:** If `/data/config.json` does not exist on startup, the server scans `/album` for `*.mp3` files, generates a default config with alphabetical ordering and titles derived from filenames (strip numeric prefix, replace hyphens with spaces, title-case), and writes it to `/data/config.json`. The password field is left empty — the admin must set it via the CLI tool or admin UI before listeners can authenticate.

### Cover Art Precedence

`/api/cover` serves the cover image with the following priority:

1. `/data/cover_override.jpg` — if present (uploaded via admin UI)
2. `/album/cover.jpg` — default

The response includes an `ETag` header derived from the file's modification time and size. On cover replacement, the ETag changes, which busts any client-side cache. The response also sets `Cache-Control: private, max-age=3600` — cached per-client for an hour, but re-validated on next session.

---

## Authentication

### The Gate

The entry screen is the first aesthetic moment. It is not a login form — it is a threshold.

A single input field. No label, no "password" placeholder, no submit button text like "Login." The field accepts the passphrase; pressing Enter opens the room.

**Error states** (two distinct conditions, two distinct responses):

- **Wrong passphrase (401):** The field shakes subtly, clears, waits. No error message. You either know the word or you don't.
- **Server error/timeout (5xx/network):** The input field's border pulses slowly with a dull warmth. The room is locked from the inside — it's not that you don't have the key, it's that something is wrong on the other side. This distinction matters for usability without breaking the aesthetic.

### Back Button Protection

Since this is an SPA behind a gate, mobile users often swipe "back" to leave the browser entirely, killing playback. On successful authentication, push a history state (`history.pushState`). If the user accidentally swipes back, they hit the gate screen rather than exiting the app. The audio may pause, but they haven't lost the URL or their session. This is preferable to `beforeunload` confirmation dialogs, which are intrusive and break the atmosphere.

### Implementation

**Decision: Server-side sessions (not JWT).** The rest of the system (analytics session tracking, sliding expiry, logout/revocation) all require server-side state anyway. JWT adds signing key management, rotation, and revocation complexity for zero benefit here.

- `POST /api/auth` — accepts `{ "passphrase": "..." }`, compares against bcrypt hash in `config.json`
- On success: generates a cryptographically random session ID (`crypto/rand`, 32 bytes, hex-encoded), stores it in the `sessions` table with creation time, and sets it as an `HttpOnly`, `Secure`, `SameSite=Strict` cookie
- **Session ID rotation:** A new session ID is generated on every successful login, even if a valid session already exists. This prevents session fixation.
- Session expiry: 7 days, sliding window (each authenticated request updates `last_seen_at`; sessions where `last_seen_at` is older than 7 days are rejected and cleaned up)
- **Session cleanup:** A background goroutine runs every hour, deleting expired sessions from SQLite
- All `/api/*` endpoints (except `/api/auth` and static SPA assets) check the session cookie; 401 if missing/invalid/expired
- `DELETE /api/auth` — invalidates the session by deleting it from SQLite and clearing the cookie

### SPA Gate Model

**Decision: Option B — SPA static assets are public; all content endpoints are gated.**

The JS/CSS/HTML bundle is served to anyone. This is fine — the bundle contains no sensitive content, just the application shell and the gate UI. All actual content (tracks, lyrics, cover, analytics) requires a valid session.

This is simpler to implement (no conditional asset serving), plays well with service worker registration, and the "gate" experience is preserved because the SPA renders only the passphrase field until authentication succeeds. Inspecting the JS reveals API endpoint paths, which is acceptable — the endpoints return nothing without a session.

### Rate Limiting

5 attempts per IP per minute. After that, the input field simply stops responding for 60 seconds. No explanation.

**Client IP behind Cloudflare Tunnel:** The rate limiter must extract the real client IP from the `CF-Connecting-IP` header, not from the TCP connection (which will always be the tunnel process). The server must verify that requests actually originate from Cloudflare's IP ranges before trusting this header — otherwise an attacker can spoof it. On startup, fetch Cloudflare's published IP list (https://www.cloudflare.com/ips/) and only trust `CF-Connecting-IP` from those source addresses. For local development (no tunnel), fall back to `RemoteAddr`.

---

## The Player

### Layout (Single Screen)

The player is a single, full-viewport screen. No scrolling on the main layout. The composition is vertical and centered:

```
┌─────────────────────────────────────┐
│                                     │
│          [ Album Cover ]            │
│          (constrained,              │
│           not dominant)             │
│                                     │
│     ─── oscilloscope waveform ───   │
│                                     │
│          Track Title                │
│                                     │
│   ┌─────────────────────────────┐   │
│   │                             │   │
│   │     Lyrics Area             │   │
│   │     (scrollable region,     │   │
│   │      synced or static)      │   │
│   │                             │   │
│   └─────────────────────────────┘   │
│                                     │
│       ◁◁    ▷ / ▮▮    ▷▷           │
│       ────────●──────────           │
│       0:00            3:42          │
│                                     │
│          ▽ Track List               │
│                                     │
└─────────────────────────────────────┘
```

Design notes:

- The album cover is present but restrained — not a hero image. It grounds the identity of the album without dominating the composition. Consider it at roughly 120–160px, slightly desaturated or with a subtle treatment (vignette, grain).
- The oscilloscope sits between the cover and the lyrics — a living line that responds to the audio. It is the heartbeat of the page.
- The lyrics area is the largest region. When LRC is available, the current line is highlighted and the region auto-scrolls. When plain text, the full lyrics are shown statically, scrollable by the listener.
- Controls are minimal and use Unicode glyphs, not icon libraries. The progress bar is a thin line with a small circular scrubber.
- The track list is collapsed by default, expandable. It shows the prescribed order with the current track indicated.

### Responsive Behavior

Mobile is the primary context (listeners with phones). The layout compresses gracefully: cover shrinks, oscilloscope thins, lyrics area takes priority. Controls remain thumb-accessible at the bottom.

---

## Oscilloscope Visualizer

### Implementation

```
HTML5 <audio> element (source of truth for playback)
        │
        ▼
MediaElementSourceNode
        │
        ▼
    AnalyserNode ──▶ getByteTimeDomainData() ──▶ <canvas> (requestAnimationFrame loop)
        │
        ▼
AudioContext.destination (speakers)
```

The `<audio>` element handles all playback (background, lock screen, media session). The Web Audio API is layered on top solely for visualization — it does not replace the audio pipeline.

### Visual Treatment

The waveform is rendered as a single continuous line on a `<canvas>` element. The line is thin (1–1.5px), drawn in a muted tone that complements the palette. When playing, it breathes with the audio.

No glow effects. No neon. No FFT bars. Just the waveform.

When audio is silent or paused, the line is flat — a held breath. **Implementation note:** the `AnalyserNode` sometimes retains ghost data in its buffer for several frames after pause. Don't rely on the analyser to naturally zero out. When `audio.paused` is true, skip `getByteTimeDomainData()` entirely and draw a straight horizontal line at the vertical midpoint. This ensures the held-breath state is crisp and immediate, not a slow decay of the last waveform.

The canvas is full-width, roughly 48–64px tall. It sits in negative space between the cover and lyrics, acting as a visual separator that also carries information.

The `AnalyserNode` should be configured with `smoothingTimeConstant = 0.8` (or tuned to taste). Without smoothing, a raw waveform is jittery and distracting on dynamic material — with it, the line behaves more like a fluid response than a voltage meter, which fits the atmospheric aesthetic.

**Important**: The Web Audio API `AudioContext` must be created/resumed in response to a user gesture. Be aggressive about this — bind `AudioContext.resume()` to both the gate's Enter keypress *and* the Play button. If a user authenticates, waits, then taps play, the context may still be in a `suspended` state from the original gesture expiring. Resume on every interaction that could lead to audio.

---

## Lyrics

### Format Priority

The server resolves lyrics for each track stem in order:

1. `{stem}.lrc` — time-synced LRC
2. `{stem}.txt` — plain text
3. `{stem}.md` — markdown (rendered to HTML)

The API response includes the format so the frontend knows how to render:

```json
{
  "format": "lrc",
  "content": "[00:00.00] In the hollow of the mountain\n[00:04.32] Where the old ones sang..."
}
```

or

```json
{
  "format": "text",
  "content": "In the hollow of the mountain\nWhere the old ones sang..."
}
```

### LRC Rendering (Synced Mode)

- Parse LRC timestamps into an ordered list of `{ time: float, line: string }` entries
- **Do not rely on `timeupdate` for lyric highlighting.** The `timeupdate` event fires only 3–4 times per second, which makes lyrics feel stepped and laggy. Instead:
  - Use `timeupdate` only as a baseline re-sync (corrects drift)
  - Run the lyric check inside the existing `requestAnimationFrame` loop (shared with the oscilloscope), reading `audio.currentTime` each frame for 60fps precision
- Highlight the active line with opacity or weight shift, not color
- Auto-scroll the lyrics container so the active line is vertically centered, with smooth CSS easing
- Lines above and below the active line are dimmed (opacity ~0.35–0.5)
- Transitions between lines are gentle — no snapping

**Simultaneous timestamps:** LRC files sometimes contain multiple lines at the same timestamp (background vocals, call-and-response, overlapping parts). The parser must group lines sharing a timestamp and display them as a vertical stack — both lines highlighted simultaneously. This maintains the "studio documentation" quality of the lyrics display rather than arbitrarily dropping one line. The data structure should be `{ time: float, lines: string[] }` rather than `{ time: float, line: string }`.

### Plain Text / Markdown Rendering

- Full text displayed statically
- User scrolls manually
- Markdown is rendered to HTML server-side using Go's `goldmark`. **Security: raw HTML in markdown must be disabled** (`goldmark` does this by default — do not enable the `WithUnsafe` option). Even though you control the album folder, a malformed or maliciously crafted `.md` file could inject scripts into the listener's browser. The rendered HTML should be plain text with `<p>`, `<em>`, `<strong>`, and `<br>` tags only. Sanitize or strip anything else.

### LRC Authoring (Tooling Recommendation)

For creating `.lrc` files:

- **lrc-maker** (https://lrc-maker.github.io) — browser-based, tap-to-timestamp while audio plays. Zero install. This is the recommendation.
- **Subtitle Edit** — desktop app, more features, steeper learning curve
- **LRC Editor for VS Code** — if you prefer staying in your editor

The workflow: load the MP3 into lrc-maker, play it, tap spacebar at each line break. Export `.lrc`. Drop it in the album folder.

---

## Background / Lock Screen Playback

### The Problem

Browsers aggressively suspend tabs and audio when the screen locks or the tab backgrounds. This is the single hardest constraint for a web-based player.

### The Strategy

1. **HTML5 `<audio>` element as the source of truth.** Browsers are more lenient with `<audio>` than with Web Audio API nodes. The `<audio>` element plays the MP3 directly; Web Audio is only used for the oscilloscope visualization (and can be disconnected/reconnected).

2. **Media Session API.** Register metadata and playback handlers so the OS-level media controls (lock screen, notification shade, Control Center) can control playback:

```javascript
navigator.mediaSession.metadata = new MediaMetadata({
  title: 'Track Name',
  artist: 'Artist Name',
  album: 'Album Title',
  artwork: [{ src: '/api/cover', sizes: '512x512', type: 'image/jpeg' }]
});

navigator.mediaSession.setActionHandler('play', () => audio.play());
navigator.mediaSession.setActionHandler('pause', () => audio.pause());
navigator.mediaSession.setActionHandler('previoustrack', () => prevTrack());
navigator.mediaSession.setActionHandler('nexttrack', () => nextTrack());
```

3. **PWA manifest + service worker.** Register as a PWA so browsers treat the app as "important" — this helps prevent aggressive tab suspension. The service worker also handles MP3 caching (see below).

4. **Double-deck audio for gapless transitions.** Two `<audio>` elements live in the DOM at all times. While Track A plays, Track B is loaded into the hidden element. When Track A ends, `play()` is called on Track B immediately and the UI focus swaps. This avoids the 200ms–1s buffer gap that occurs when changing `src` on a single element.

   **Web Audio wiring constraint:** `createMediaElementSource()` can only be called *once* per `<audio>` element per `AudioContext`. Create both `MediaElementSourceNode`s at initialization time (during the unlock ritual). On deck swap, don't recreate the source — instead, disconnect the old deck's source from the `AnalyserNode` and connect the new deck's source. The node graph is: `sourceA/sourceB → AnalyserNode → destination`, and you swap which source feeds the analyser.

   **iOS Safari unlock ritual:** iOS ties audio playback permission to user gesture tokens, and a gesture consumed by one element does not transfer to a second. On the *first* user interaction (gate Enter or first Play tap), both `<audio>` elements must be "warmed up" — call `play()` then immediately `pause()` at volume 0 on each, and create both `MediaElementSourceNode`s at this time. If Deck B isn't unlocked during that initial gesture, the automatic swap later will silently fail on iPhones. This is non-optional.

   **Warm-up side effects:** The brief `play()`/`pause()` on both elements may flash "playing" state in the Media Session API or OS media controls. Mitigate by deferring `navigator.mediaSession.metadata` registration until actual playback begins (not during warm-up), and by setting the `<audio>` elements to empty/silent sources during the unlock rather than real tracks.

5. **Service worker caching strategy.** The service worker caches *whole MP3 files* (not partial ranges) for the current and next track only. Partial range caching creates fragmentation nightmares and inconsistent seeks. Whole-file caching is simpler and storage is bounded to ~2 tracks worth of MP3s (~20MB typical). The service worker intercepts fetch requests for `/api/stream/*` and serves from cache if available, falling back to network. Cached entries are evicted when a new "next track" is determined. Do not attempt to cache the entire album — storage quotas vary by browser and device.

### Limitations (Honest Assessment)

- **iOS Safari** is the most restrictive. Background audio works if playback was initiated by user gesture and the `<audio>` element is actively producing output. It generally works, but there are edge cases (e.g., if the OS is under memory pressure, the tab may still be killed).
- **Android Chrome** is more reliable with PWAs installed to the home screen.
- **Desktop browsers** generally have no issues.

This is not a native app — there will be edge cases. But for the common scenario (listener starts a track, locks phone, keeps listening), it works.

---

## Analytics

### Events Tracked

| Event | Data | When |
|---|---|---|
| `play` | track stem, timestamp | Track starts playing |
| `pause` | track stem, position, timestamp | Listener pauses |
| `seek` | track stem, from_position, to_position, timestamp | Listener seeks |
| `complete` | track stem, timestamp | Track reaches end |
| `dropout` | track stem, position, timestamp | Session ends mid-track (detected via `beforeunload` or heartbeat timeout) |
| `session_start` | timestamp | Successful authentication |
| `session_end` | timestamp, tracks_heard | Tab close / timeout |

### Implementation

- Frontend sends events to `POST /api/analytics` as JSON
- Events are batch-buffered on the client (flush every 10s or on `visibilitychange`)
- Server writes to SQLite

**SQLite concurrency strategy** (the pure Go SQLite port is especially prone to locking under concurrent writes):

1. **Enable WAL mode** on database open: `PRAGMA journal_mode=WAL;` — allows simultaneous readers and writers, which is critical when the admin dashboard is reading while listeners are generating events.
2. **Server-side write buffer.** Analytics events are not written to disk on receipt. Instead, the HTTP handler pushes events into a Go channel. A single background goroutine consumes the channel and flushes to SQLite in batches (every 5 seconds or 50 events, whichever comes first). This eliminates "database is locked" errors under load and reduces disk I/O. The write goroutine is the *only* writer to the database.

   **Graceful shutdown:** The server must handle `SIGTERM` (Docker's stop signal) cleanly. On signal: stop accepting new HTTP requests via `http.Server.Shutdown()`, close the analytics channel, let the flusher goroutine drain any remaining buffered events to disk, then close the SQLite connection. Without this, a `docker compose down` loses the last ~5 seconds of analytics data. The flush-on-shutdown path should have a hard timeout (e.g., 10 seconds) to prevent hanging if the DB is in a bad state.

### Schema

```sql
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,         -- random 32-byte hex token (the cookie value)
    started_at DATETIME NOT NULL,
    last_seen_at DATETIME NOT NULL,
    ip_hash TEXT                  -- SHA-256 + salt, not raw
);

CREATE TABLE admin_sessions (
    id TEXT PRIMARY KEY,
    created_at DATETIME NOT NULL  -- expires after 1 hour, no sliding
);

CREATE TABLE events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    event_type TEXT NOT NULL,      -- play, pause, seek, complete, dropout, heartbeat, session_start, session_end
    track_stem TEXT,
    position_seconds REAL,
    metadata TEXT,                 -- JSON for extra fields (e.g., seek from/to)
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_events_track ON events(track_stem);
CREATE INDEX idx_events_type ON events(event_type);
CREATE INDEX idx_events_session ON events(session_id);
CREATE INDEX idx_sessions_last_seen ON sessions(last_seen_at);  -- for cleanup queries
```

IP addresses are hashed (SHA-256 + salt) — this is a listening room, not a surveillance system.

### Dropout Detection

A "dropout" is when a listener stops mid-track without completing it. This requires a precise definition because browser background behavior creates ambiguity.

**Definition:** A dropout is recorded when ALL of the following are true:

1. A `play` event exists for a track with no corresponding `complete` event
2. The session has ended (determined by one of the triggers below)
3. The last known position is less than 95% of track duration

**Triggers (in order of reliability):**

1. **`pagehide` event** (preferred over `beforeunload`) — send a final position event via `navigator.sendBeacon()`. This is the most reliable cross-browser signal that the page is being unloaded.
2. **Heartbeat timeout** — client sends a heartbeat every 30s while playing. If the server sees no heartbeat for 90s, it marks the session as ended.

**Background tab throttling:** Browsers aggressively throttle `setInterval`/`setTimeout` in background tabs (often to once per minute). This means heartbeats may arrive at 60s intervals instead of 30s when the tab is backgrounded. The 90s server-side timeout accounts for this — it's 3x the normal heartbeat interval, giving background throttling room to breathe. **Do not mark a session as a dropout solely because heartbeats slowed down.** Only mark it when heartbeats stop entirely for 90s AND no `pagehide`/`complete` event was received.

A `pause` event without a subsequent `play` or `pagehide` within 30 minutes is also treated as a session end (the listener walked away). This is a "soft dropout" — tracked separately from hard dropouts for analytics clarity.

### Backpressure Policy

The analytics write channel has a buffer of 1000 events. If the channel is full (burst traffic exceeding the flush rate):

- **`play`, `complete`, `session_start`, `session_end`** events block briefly (100ms timeout), then drop with a server-side counter increment. These are high-value events worth brief backpressure.
- **`heartbeat`, `seek`, `pause`** events are dropped immediately if the channel is full. These are lower-value and their loss doesn't meaningfully degrade analytics.
- A `dropped_events` counter is logged periodically so you can detect if this is happening in practice. If it is, increase the buffer or flush frequency.

---

## Admin Interface

### Access

`/admin` — separate authentication from the listener gate. Browsers can't attach custom `Authorization` headers to a navigation request, so bearer tokens don't work for serving an HTML page. Query param tokens leak via logs, history, and referrers. Instead:

**Decision: `/admin/login` sets an admin session cookie.**

1. `GET /admin` serves a minimal login page (separate from the listener gate — no shared aesthetic)
2. `POST /admin/api/auth` accepts `{ "token": "..." }`, compares against the `ADMIN_TOKEN` env var using `crypto/subtle.ConstantTimeCompare`
3. On success, sets a separate `HttpOnly`, `Secure`, `SameSite=Strict` cookie (`acetate_admin`) with a server-side session, distinct from listener sessions
4. All `/admin/api/*` endpoints check this admin cookie; 401 if missing/invalid
5. Admin session expiry: 1 hour, no sliding window (short-lived by design)

**Security hardening:**

- Never log the `ADMIN_TOKEN` value or listener passphrases. Audit request logging middleware to ensure POST bodies on auth endpoints are excluded or redacted.
- Admin responses set `Cache-Control: no-store` to prevent browser caching of admin data.
- Since admin auth is cookie-based, all state-mutating admin endpoints (`PUT`, `POST`, `DELETE`) must check the `Origin` header against the expected domain or use a CSRF token. A simple `Origin` check is sufficient at this scale.
- The admin UI stores nothing in `localStorage` — the cookie is the only credential.

The admin UI is a separate, minimal page. It does not share the listener aesthetic — it is purely functional.

### Hot-Swap (No Restart Required)

The Go server caches `config.json` in an in-memory struct protected by a `sync.RWMutex`. Listener requests read from the cached struct (fast, no disk I/O). When the admin API writes changes (reorder tracks, update password), it:

1. Writes the updated JSON to `/data/config.json`
2. Calls an internal `reloadConfig()` function that re-reads the file and swaps the cached struct under a write lock

This means track order changes, password updates, and other config edits take effect immediately for new requests — no container restart needed.

### Features

**Track Order Management**
- Drag-and-drop reordering of the tracklist
- Edit track titles and display indices
- Saves to `/data/config.json`
- Changes take effect immediately for new requests

**Analytics Dashboard**
- Per-track: total plays, unique sessions, completion rate, average listen duration
- Dropout heatmap: for each track, a simple bar showing where listeners stop (binned into 10% segments)
- Session timeline: chronological list of sessions showing which tracks were played and for how long
- Overall: total sessions, average tracks per session, most/least completed tracks

**Album Management**
- Current password hash display (truncated) + ability to set a new passphrase (hashed server-side before storage)
- Upload/replace cover art (stored to `/data/cover_override.jpg`, does not modify `/album`)
- View current track files and their lyric format (lrc/txt/md/none)

### Implementation

The admin UI is a separate HTML page with vanilla JS. It calls admin-only API endpoints (`/admin/api/*`) that require the admin session cookie. Nothing fancy — tables, a drag list, and a few SVG-based visualizations for the heatmaps.

---

## Aesthetic Direction

### Palette

Derived from the collision of sacred harp hymnals and witch house visual language:

- **Background**: Near-black with warmth. Not pure `#000` — something like `#0a0908` or `#0d0b09`. The darkness of a room lit by a single lamp.
- **Primary text**: Not white. A warm off-white, like aged paper: `#d4cfc4` or `#c8c0b0`.
- **Active/accent**: A single muted tone. Candidates: desaturated ochre (`#8a7a5a`), deep rust (`#6b3a2a`), or muted sage (`#4a5a3a`). One accent color only.
- **Dimmed text**: The primary text at 35–50% opacity.
- **Lines/borders**: Near-invisible. 1px, rgba white at 5–8%.

### Typography

Two typefaces maximum:

- **Body/lyrics**: A serif with character. Candidates: EB Garamond, Cormorant, or Source Serif. The lyrics should feel typeset, not displayed.
- **UI/labels**: A clean sans-serif at small sizes. Inter, IBM Plex Sans, or just the system font stack. Understated.

Track titles in the serif. Controls labels (if any) in the sans. Never mix within a single text block.

### Principles

1. **Silence is a design element.** Generous negative space. The page breathes.
2. **No decoration.** Every visual element is either functional or structural. No borders for borders' sake. No gradients. No shadows (with one possible exception: a very subtle vignette on the cover image).
3. **Typography carries the weight.** The lyrics are the visual center of gravity. Their typographic treatment — size, leading, opacity transitions — is where the craft shows.
4. **Motion is earned.** The oscilloscope moves because it represents live audio data. The lyrics scroll because the song is progressing. The gate field shakes because the passphrase was wrong. Nothing else moves.
5. **One texture, maybe.** A very subtle paper grain or noise overlay on the background, at near-invisible opacity (2–4%), can add warmth without becoming a "texture effect." Test it; remove it if it feels like an affectation.

### What This Is Not

- Not dark mode Spotify. No rounded cards, no green accents, no "now playing" animation.
- Not a witch house Tumblr page. No inverted crosses, no triangles, no glitch effects, no purple.
- Not a folk/Americana pastiche. No wood textures, no hand-drawn type, no mason jars.

It is a room. Quiet, warm-dark, intentional. You enter, you sit, you listen.

---

## Docker & Deployment

### Dockerfile (Multi-Stage)

```dockerfile
# Build stage
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /app ./cmd/server

# If using Svelte/JS frontend build:
FROM node:20-alpine AS frontend
WORKDIR /src
COPY frontend/ .
RUN npm ci && npm run build

# Final stage
FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=build /app /app
COPY --from=frontend /src/dist /static
EXPOSE 8080
ENTRYPOINT ["/app"]
```

### docker-compose.yml

```yaml
version: "3.8"
services:
  acetate:
    build: .
    ports:
      - "8080:8080"
    volumes:
      - ./album:/album:ro
      - ./data:/data
    environment:
      - ADMIN_TOKEN=${ADMIN_TOKEN}
      - LISTEN_ADDR=:8080
      - ALBUM_PATH=/album
      - DATA_PATH=/data
    restart: unless-stopped
```

The `data/` volume is read-write and persists the SQLite database, `config.json`, and any cover override across container restarts. The `album/` folder is read-only — the server never writes to it. Admin-initiated changes (track order, password, cover art) are written to `/data`.

### Cloudflare Tunnel

Standard `cloudflared` setup. The app listens on `8080`; the tunnel maps it to your domain. HTTPS is handled by Cloudflare. The session cookie's `Secure` flag works because Cloudflare terminates TLS.

---

## API Surface

| Method | Path | Auth | Description |
|---|---|---|---|
| `POST` | `/api/auth` | none | Submit passphrase, receive listener session cookie |
| `DELETE` | `/api/auth` | session | Logout (deletes session from SQLite, clears cookie) |
| `GET` | `/api/tracks` | session | Track list with order, titles, lyric format per track |
| `GET` | `/api/cover` | session | Album cover image (with ETag) |
| `GET` | `/api/stream/{stem}` | session | MP3 audio stream (supports Range requests) |
| `GET` | `/api/lyrics/{stem}` | session | Lyrics content + format |
| `POST` | `/api/analytics` | session | Batch event submission |
| `POST` | `/admin/api/auth` | none | Submit admin token, receive admin session cookie |
| `DELETE` | `/admin/api/auth` | admin | Admin logout |
| `GET` | `/admin/api/analytics` | admin | Aggregated analytics data |
| `GET` | `/admin/api/tracks` | admin | Track list with stats |
| `PUT` | `/admin/api/tracks` | admin | Update track order, titles, metadata |
| `PUT` | `/admin/api/password` | admin | Update listener passphrase |
| `POST` | `/admin/api/cover` | admin | Upload new cover (saved to `/data/cover_override.jpg`) |

### MP3 Streaming

The `/api/stream/{stem}` endpoint must support HTTP Range requests. This is critical for:

- Seeking (the browser requests byte ranges)
- iOS audio playback (Safari requires Range support)
- Resuming after network interruption

Go's `http.ServeContent` handles this natively.

### Path Traversal Protection

The `{stem}` parameter in `/api/stream/{stem}` and `/api/lyrics/{stem}` is the most obvious injection vector. Strict validation is required:

- **Allowed characters:** `[a-zA-Z0-9_-]` only. No dots, slashes, backslashes, spaces, or unicode.
- **Validation:** Reject any stem that doesn't match the allowlist regex *before* any filesystem operation. Return 400, not 404 (don't reveal path resolution behavior).
- **Resolution:** The server joins the validated stem with the known `/album` base path and the expected extension (`.mp3`, `.lrc`, `.txt`, `.md`). Use `filepath.Join` + verify the result starts with the expected base path to prevent any edge case where `filepath.Join` normalizes something unexpected.
- **Cross-check:** The stem must also exist in the current `config.json` track list. If someone requests a valid-looking stem that isn't in the config, reject it.

### Cache-Control Headers

All gated content must be served with headers that prevent Cloudflare's edge or browser caches from storing authenticated content:

| Endpoint | Cache-Control | Notes |
|---|---|---|
| `/api/stream/*` | `private, no-store` | Prevents edge caching of audio behind auth. Cloudflare can cache 206 responses by default — `no-store` prevents this. |
| `/api/lyrics/*` | `private, max-age=3600` | Lyrics don't change during a session; per-client caching is fine. |
| `/api/cover` | `private, max-age=3600` + `ETag` | See Cover Art Precedence section. |
| `/api/tracks` | `private, no-cache` | Always re-validate (track order may change mid-session via admin). |
| `/admin/api/*` | `no-store` | Never cache admin data. |
| `/*` (static SPA) | `public, max-age=86400` | SPA assets are public and cacheable. Cache-bust via filename hashing in the build step. |

---

## Project Structure (Go)

```
acetate/
  cmd/
    server/
      main.go             # entry point, config, server startup
    hashpass/
      main.go             # CLI tool: generate bcrypt hash from passphrase
  internal/
    auth/
      auth.go             # session management, passphrase verification
    album/
      album.go            # album loading, track resolution, file serving
    analytics/
      analytics.go        # event ingestion, aggregation queries
      schema.go           # SQLite schema + migrations
    admin/
      admin.go            # admin API handlers
    handler/
      handler.go          # HTTP route setup, middleware
  frontend/
    src/                  # Svelte or vanilla JS source
    public/
    package.json
  static/                 # compiled frontend (gitignored, built in Docker)
  album/                  # example album folder (gitignored)
  data/                   # SQLite database (gitignored)
  Dockerfile
  docker-compose.yml
  go.mod
  go.sum
```

---

## Development Workflow

1. **Album prep**: Drop MP3s, lyrics files, and `cover.jpg` into `album/`
2. **First run**: `ADMIN_TOKEN=dev ALBUM_PATH=./album DATA_PATH=./data go run ./cmd/server` — the server auto-generates `/data/config.json` from the album folder contents
3. **Set password**: `go run ./cmd/hashpass "your passphrase"` — paste the hash into `data/config.json` (or set it via the admin UI)
4. **Edit track order/titles**: Use the admin UI at `/admin`, or edit `data/config.json` directly (the server hot-reloads on admin API writes; manual edits require a restart)
5. **Frontend dev**: In `frontend/`, `npm run dev` with a proxy to the Go server
6. **Deploy**: `docker compose up -d --build`

---

## Future Extensions (Out of Scope Now)

These are noted to inform architectural decisions, not as commitments:

- Multi-album support (album selector behind the gate, or different passphrases per album)
- Listener feedback (per-track reactions, text responses)
- Invite codes instead of shared passphrase
- Visualizer options beyond oscilloscope (spectrum, particles)
- Offline mode via service worker caching full album
- Embeddable single-track widget
- RSS/podcast feed generation for the album