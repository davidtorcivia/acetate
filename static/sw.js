// Acetate — Service Worker
const CACHE_NAME = 'acetate-static-v2';
const AUDIO_CACHE = 'acetate-audio-v2';
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
                    return key !== CACHE_NAME && key !== AUDIO_CACHE;
                }).map(function (key) {
                    return caches.delete(key);
                })
            );
        })
    );
    self.clients.claim();
});

// Track auth state from controlled pages so cached audio is never served to logged-out clients.
self.addEventListener('message', function (event) {
    if (!event.data || !event.data.type) return;

    if (event.data.type === 'AUTHENTICATED') {
        listenerAuthenticated = true;
        return;
    }

    if (event.data.type === 'UNAUTHENTICATED') {
        listenerAuthenticated = false;
        event.waitUntil(caches.delete(AUDIO_CACHE));
    }
});

// Fetch — strategy based on request type
self.addEventListener('fetch', function (event) {
    var url = new URL(event.request.url);

    // MP3 streaming — cache current + next track only (whole file)
    if (url.pathname.startsWith('/api/stream/') && event.request.method === 'GET') {
        var hasRange = event.request.headers.has('Range');
        var accept = event.request.headers.get('Accept') || '';
        var isAudioRequest = event.request.destination === 'audio' || accept.indexOf('audio/') !== -1;

        // Never serve cached audio when logged out, non-audio, or handling byte ranges.
        if (!listenerAuthenticated || !isAudioRequest || hasRange) {
            event.respondWith(fetch(event.request));
            return;
        }

        event.respondWith(
            caches.open(AUDIO_CACHE).then(function (cache) {
                return cache.match(event.request).then(function (cached) {
                    if (cached) return cached;

                    return fetch(event.request).then(function (response) {
                        // Only cache successful full responses (not 206 partials)
                        if (response.ok && response.status === 200) {
                            cache.put(event.request, response.clone());

                            // Evict old entries — keep max 2 tracks
                            cache.keys().then(function (keys) {
                                if (keys.length > 2) {
                                    cache.delete(keys[0]);
                                }
                            });
                        }
                        return response;
                    });
                });
            })
        );
        return;
    }

    // Static assets — cache first, network fallback
    if (STATIC_ASSETS.indexOf(url.pathname) !== -1 ||
        url.pathname.startsWith('/css/') ||
        url.pathname.startsWith('/js/') ||
        url.pathname.startsWith('/fonts/')) {
        event.respondWith(
            caches.match(event.request).then(function (cached) {
                return cached || fetch(event.request).then(function (response) {
                    if (response.ok) {
                        var cache = caches.open(CACHE_NAME).then(function (c) {
                            c.put(event.request, response.clone());
                        });
                    }
                    return response;
                });
            })
        );
        return;
    }

    // API requests — network only
    event.respondWith(fetch(event.request));
});
