// Acetate — Service Worker
const CACHE_NAME = "acetate-static-v21";
const API_CACHE = "acetate-api-v18";
const AUDIO_CACHE = "acetate-audio-v19";
const MAX_AUDIO_CACHE_ENTRIES = 24;
// Resets whenever the browser stops this worker; the page re-posts AUTHENTICATED on every track load.
let listenerAuthenticated = false;
const audioFillsInFlight = new Set();

const STATIC_ASSETS = [
    "/",
    "/css/style.css",
    "/js/app.js",
    "/js/gate.js",
    "/js/player.js",
    "/js/tracklist.js",
    "/js/oscilloscope.js",
    "/js/lyrics.js",
    "/js/selector.js",
    "/js/analytics.js",
    "/manifest.json",
];

// Install — cache static assets
self.addEventListener("install", (event) => {
    event.waitUntil(
        caches
            .open(CACHE_NAME)
            .then((cache) =>
                cache.addAll(
                    STATIC_ASSETS.map((u) => new Request(u, { cache: "reload" })),
                ),
            ),
    );
    self.skipWaiting();
});

// Activate — clean old caches
self.addEventListener("activate", (event) => {
    event.waitUntil(
        caches
            .keys()
            .then((keys) =>
                Promise.all(
                    keys
                        .filter(
                            (key) =>
                                key !== CACHE_NAME &&
                                key !== API_CACHE &&
                                key !== AUDIO_CACHE,
                        )
                        .map((key) => caches.delete(key)),
                ),
            ),
    );
    self.clients.claim();
});

// Track auth state from controlled pages so cached media is never served to logged-out clients.
self.addEventListener("message", (event) => {
    if (!event.data || !event.data.type) return;

    if (event.data.type === "AUTHENTICATED") {
        listenerAuthenticated = true;
        return;
    }

    if (event.data.type === "UNAUTHENTICATED") {
        listenerAuthenticated = false;
        event.waitUntil(
            Promise.all([caches.delete(AUDIO_CACHE), caches.delete(API_CACHE)]),
        );
    }
});

// Fetch — strategy based on request type
self.addEventListener("fetch", (event) => {
    if (event.request.method !== "GET") return;

    var url;
    try {
        url = new URL(event.request.url);
    } catch (err) {
        return; // malformed URL — let the browser handle it
    }
    var path = url.pathname;

    // Every query variant of the SPA shell (/?album=&track=&t=) shares one cache entry.
    if (path === "/" || path === "/index.html") {
        event.respondWith(handleStaticAsset(event.request, "/"));
        return;
    }

    // Audio: cache first when a full copy exists, otherwise network plus one background fill.
    // Route shape: /api/albums/{slug}/stream/{stem}; downloads (?dl=1) bypass the worker.
    if (isAlbumStreamPath(path)) {
        if (!listenerAuthenticated || url.searchParams.has("dl")) return;
        handleAudioRequest(event);
        return;
    }

    // Authenticated API content useful offline (album-scoped routes).
    if (isOfflineAPIPath(path)) {
        if (!listenerAuthenticated) return;
        event.respondWith(handleOfflineAPIRequest(event.request));
        return;
    }

    // Static assets — stale while revalidate.
    if (isStaticAsset(path)) {
        event.respondWith(handleStaticAsset(event.request, event.request));
    }
});

// Album-scoped content routes: /api/albums/{slug}/…
function albumContentMatch(path) {
    if (!path.startsWith("/api/albums/")) return null;
    const rest = path.slice("/api/albums/".length);
    const slash = rest.indexOf("/");
    if (slash <= 0) return null; // no slug or no trailing part
    return {
        slug: rest.slice(0, slash),
        tail: rest.slice(slash + 1),
    };
}

function isAlbumStreamPath(path) {
    const m = albumContentMatch(path);
    return !!m && m.tail.startsWith("stream/");
}

function isOfflineAPIPath(path) {
    const m = albumContentMatch(path);
    if (!m) return false;
    return (
        m.tail === "tracks" ||
        m.tail === "cover" ||
        m.tail.startsWith("lyrics/")
    );
}

function isStaticAsset(path) {
    return (
        STATIC_ASSETS.indexOf(path) !== -1 ||
        path.startsWith("/css/") ||
        path.startsWith("/js/") ||
        path.startsWith("/fonts/")
    );
}

function handleStaticAsset(request, cacheKey) {
    return caches.open(CACHE_NAME).then((cache) =>
        cache.match(cacheKey).then((cached) => {
            var network = fetch(request, { cache: "no-cache" })
                .then((response) => {
                    if (response && response.ok) {
                        cache.put(cacheKey, response.clone());
                    }
                    return response;
                })
                .catch(
                    () =>
                        cached ||
                        new Response("", {
                            status: 503,
                            statusText: "Offline",
                        }),
                );

            return cached || network;
        }),
    );
}

function handleOfflineAPIRequest(request) {
    return caches.open(API_CACHE).then((cache) =>
        fetch(request)
            .then((response) => {
                if (response && response.ok) {
                    cache.put(request, response.clone());
                }
                return response;
            })
            .catch(() =>
                cache
                    .match(request)
                    .then(
                        (cached) =>
                            cached ||
                            new Response("", {
                                status: 503,
                                statusText: "Offline",
                            }),
                    ),
            ),
    );
}

// Network first: Chrome's media pipeline fails when one element's range
// requests switch from network responses to worker-built ones, so the cached
// copy is only an offline fallback.
function handleAudioRequest(event) {
    var request = event.request;
    var cacheKey = request.url;

    event.respondWith(
        fetch(request).catch(() =>
            caches
                .open(AUDIO_CACHE)
                .then((cache) => cache.match(cacheKey))
                .then((full) =>
                    full
                        ? serveCachedAudio(full, request.headers.get("Range"))
                        : new Response("", { status: 503, statusText: "Offline" }),
                ),
        ),
    );
    event.waitUntil(
        caches
            .open(AUDIO_CACHE)
            .then((cache) => cache.match(cacheKey))
            .then((full) => (full ? null : fillAudioCache(cacheKey)))
            .catch(() => {}),
    );
}

// <audio> only issues Range requests, which are never cacheable; one full GET per track fills the cache.
function fillAudioCache(cacheKey) {
    if (audioFillsInFlight.has(cacheKey)) return null;
    audioFillsInFlight.add(cacheKey);
    return fetch(cacheKey, { credentials: "same-origin" })
        .then((response) => {
            if (!response || response.status !== 200) return null;
            return caches
                .open(AUDIO_CACHE)
                .then((cache) =>
                    cache.put(cacheKey, response).then(() => trimAudioCache(cache)),
                );
        })
        .catch(() => {})
        .then(() => {
            audioFillsInFlight.delete(cacheKey);
        });
}

function trimAudioCache(cache) {
    return cache.keys().then((keys) => {
        if (keys.length <= MAX_AUDIO_CACHE_ENTRIES) return;
        var deletions = [];
        for (var i = 0; i < keys.length - MAX_AUDIO_CACHE_ENTRIES; i++) {
            deletions.push(cache.delete(keys[i]));
        }
        return Promise.all(deletions);
    });
}

function serveCachedAudio(cachedResponse, rangeHeader) {
    if (!rangeHeader) return cachedResponse;

    return cachedResponse.blob().then((blob) => {
        var total = blob.size;
        var range = parseRangeHeader(rangeHeader, total);
        if (!range) {
            return new Response("", {
                status: 416,
                headers: { "Content-Range": "bytes */" + total },
            });
        }

        var headers = new Headers(cachedResponse.headers);
        headers.set(
            "Content-Range",
            "bytes " + range.start + "-" + range.end + "/" + total,
        );
        headers.set("Accept-Ranges", "bytes");
        headers.set("Content-Length", String(range.end - range.start + 1));
        if (!headers.get("Content-Type")) {
            headers.set("Content-Type", "audio/mpeg");
        }

        return new Response(blob.slice(range.start, range.end + 1), {
            status: 206,
            statusText: "Partial Content",
            headers: headers,
        });
    });
}

function parseRangeHeader(rangeHeader, totalSize) {
    if (!rangeHeader || !/^bytes=/.test(rangeHeader)) return null;

    var parts = rangeHeader.replace(/^bytes=/, "").split("-");
    if (parts.length !== 2) return null;

    var start = parts[0] === "" ? NaN : parseInt(parts[0], 10);
    var end = parts[1] === "" ? NaN : parseInt(parts[1], 10);

    if (isNaN(start)) {
        if (isNaN(end) || end <= 0) return null;
        start = Math.max(0, totalSize - end);
        end = totalSize - 1;
    } else if (isNaN(end)) {
        end = totalSize - 1;
    }

    if (start < 0 || end < start || start >= totalSize) return null;
    end = Math.min(end, totalSize - 1);

    return { start: start, end: end };
}
