// Acetate — Service Worker
const CACHE_NAME = 'acetate-static-v13';
const API_CACHE = 'acetate-api-v13';
const AUDIO_CACHE = 'acetate-audio-v13';
const MAX_AUDIO_CACHE_ENTRIES = 24;
let listenerAuthenticated = false;

const STATIC_ASSETS = [
    '/',
    '/index.html',
    '/css/style.css',
    '/js/app.js',
    '/js/gate.js',
    '/js/player.js',
    '/js/tracklist.js',
    '/js/oscilloscope.js',
    '/js/lyrics.js',
    '/js/analytics.js',
    '/manifest.json'
];

// Install — cache static assets
self.addEventListener('install', function (event) {
    event.waitUntil(
        caches.open(CACHE_NAME).then(function (cache) {
            return cache.addAll(STATIC_ASSETS);
        })
    );
    self.skipWaiting();
});

// Activate — clean old caches
self.addEventListener('activate', function (event) {
    event.waitUntil(
        caches.keys().then(function (keys) {
            return Promise.all(
                keys.filter(function (key) {
                    return key !== CACHE_NAME && key !== API_CACHE && key !== AUDIO_CACHE;
                }).map(function (key) {
                    return caches.delete(key);
                })
            );
        })
    );
    self.clients.claim();
});

// Track auth state from controlled pages so cached media is never served to logged-out clients.
self.addEventListener('message', function (event) {
    if (!event.data || !event.data.type) return;

    if (event.data.type === 'AUTHENTICATED') {
        listenerAuthenticated = true;
        return;
    }

    if (event.data.type === 'UNAUTHENTICATED') {
        listenerAuthenticated = false;
        event.waitUntil(Promise.all([caches.delete(AUDIO_CACHE), caches.delete(API_CACHE)]));
    }
});

// Fetch — strategy based on request type
self.addEventListener('fetch', function (event) {
    if (event.request.method !== 'GET') {
        event.respondWith(fetch(event.request));
        return;
    }

    var url = new URL(event.request.url);
    var path = url.pathname;

    // MP3 streaming — network first, cache fallback (supports ranged offline reads).
    if (path.startsWith('/api/stream/')) {
        event.respondWith(handleAudioRequest(event.request));
        return;
    }

    // Authenticated API content useful offline.
    if (isOfflineAPIPath(path)) {
        event.respondWith(handleOfflineAPIRequest(event.request));
        return;
    }

    // Static assets — stale while revalidate.
    if (isStaticAsset(path)) {
        event.respondWith(handleStaticAsset(event.request));
        return;
    }

    // Default — network only.
    event.respondWith(fetch(event.request));
});

function isOfflineAPIPath(path) {
    return path === '/api/tracks' || path === '/api/cover' || path.startsWith('/api/lyrics/');
}

function isStaticAsset(path) {
    return STATIC_ASSETS.indexOf(path) !== -1 ||
        path.startsWith('/css/') ||
        path.startsWith('/js/') ||
        path.startsWith('/fonts/');
}

function handleStaticAsset(request) {
    return caches.open(CACHE_NAME).then(function (cache) {
        return cache.match(request).then(function (cached) {
            var network = fetch(request).then(function (response) {
                if (response && response.ok) {
                    cache.put(request, response.clone());
                }
                return response;
            }).catch(function () {
                return cached || new Response('', { status: 503, statusText: 'Offline' });
            });

            return cached || network;
        });
    });
}

function handleOfflineAPIRequest(request) {
    if (!listenerAuthenticated) {
        return fetch(request);
    }

    return caches.open(API_CACHE).then(function (cache) {
        return fetch(request).then(function (response) {
            if (response && response.ok) {
                cache.put(request, response.clone());
            }
            return response;
        }).catch(function () {
            return cache.match(request).then(function (cached) {
                return cached || new Response('', { status: 503, statusText: 'Offline' });
            });
        });
    });
}

function handleAudioRequest(request) {
    var rangeHeader = request.headers.get('Range');
    var cacheKey = request.url;

    if (!listenerAuthenticated) {
        return fetch(request);
    }

    if (rangeHeader) {
        return fetch(request).catch(function () {
            return readRangeFromCachedAudio(cacheKey, rangeHeader).then(function (resp) {
                return resp || new Response('', { status: 503, statusText: 'Offline' });
            });
        });
    }

    return caches.open(AUDIO_CACHE).then(function (cache) {
        return fetch(request).then(function (response) {
            if (response && response.ok && response.status === 200) {
                cache.put(cacheKey, response.clone());
                trimAudioCache(cache);
            }
            return response;
        }).catch(function () {
            return cache.match(cacheKey).then(function (cached) {
                return cached || new Response('', { status: 503, statusText: 'Offline' });
            });
        });
    });
}

function trimAudioCache(cache) {
    cache.keys().then(function (keys) {
        if (keys.length <= MAX_AUDIO_CACHE_ENTRIES) return;
        var deletions = [];
        for (var i = 0; i < keys.length - MAX_AUDIO_CACHE_ENTRIES; i++) {
            deletions.push(cache.delete(keys[i]));
        }
        return Promise.all(deletions);
    });
}

function readRangeFromCachedAudio(cacheKey, rangeHeader) {
    return caches.open(AUDIO_CACHE).then(function (cache) {
        return cache.match(cacheKey).then(function (cachedResponse) {
            if (!cachedResponse) return null;

            return cachedResponse.arrayBuffer().then(function (buffer) {
                var total = buffer.byteLength;
                var range = parseRangeHeader(rangeHeader, total);
                if (!range) return null;

                var chunk = buffer.slice(range.start, range.end + 1);
                var headers = new Headers(cachedResponse.headers);
                headers.set('Content-Range', 'bytes ' + range.start + '-' + range.end + '/' + total);
                headers.set('Accept-Ranges', 'bytes');
                headers.set('Content-Length', String(range.end - range.start + 1));
                if (!headers.get('Content-Type')) {
                    headers.set('Content-Type', 'audio/mpeg');
                }

                return new Response(chunk, {
                    status: 206,
                    statusText: 'Partial Content',
                    headers: headers
                });
            });
        });
    });
}

function parseRangeHeader(rangeHeader, totalSize) {
    if (!rangeHeader || !/^bytes=/.test(rangeHeader)) return null;

    var parts = rangeHeader.replace(/^bytes=/, '').split('-');
    if (parts.length !== 2) return null;

    var start = parts[0] === '' ? NaN : parseInt(parts[0], 10);
    var end = parts[1] === '' ? NaN : parseInt(parts[1], 10);

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
