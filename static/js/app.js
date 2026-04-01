// Acetate — SPA state machine
(function () {
    'use strict';

    window.Acetate = {
        state: 'gate', // 'gate', 'selector', or 'player'
        albums: null,       // list of accessible albums from auth response
        currentAlbum: null, // {slug, title, artist}
        albumData: null,
        currentTrackIndex: -1,
        offlineMode: false,
        playbackStateKey: 'acetate-playback-v1',
        pendingDeepLinkSearch: '',

        init: function () {
            this.pendingDeepLinkSearch = this.extractDeepLinkSearch(window.location.search);

            // Always clear any existing listener session so visitors must re-enter the passphrase
            fetch('/api/auth', { method: 'DELETE', credentials: 'same-origin' }).catch(function () {});
            this.showGate();

            // Register service worker
            if ('serviceWorker' in navigator) {
                navigator.serviceWorker.register('/sw.js').catch(function () { });
            }

            // Handle back button
            window.addEventListener('popstate', function () {
                if (Acetate.state === 'player' && Acetate.albums && Acetate.albums.length > 1) {
                    Acetate.showAlbumSelector();
                } else if (Acetate.state === 'player' || Acetate.state === 'selector') {
                    Acetate.showGate();
                }
            });
        },

        // Returns the album-scoped API base, e.g. "/api/albums/my-album"
        albumApiBase: function () {
            if (!this.currentAlbum) return '/api/albums/_';
            return '/api/albums/' + this.encodePathSegment(this.currentAlbum.slug);
        },

        offlineAlbumKey: function () {
            var slug = this.currentAlbum ? this.currentAlbum.slug : 'default';
            return 'acetate-offline-album-' + slug;
        },

        checkSession: function () {
            fetch('/api/session', { credentials: 'same-origin' })
                .then(function (r) {
                    if (r.ok) return r.json();
                    throw new Error('unauthorized');
                })
                .then(function (data) {
                    if (data.albums && data.albums.length > 0) {
                        Acetate.onAuthenticated(data);
                    } else {
                        Acetate.showGate();
                    }
                })
                .catch(function () {
                    Acetate.notifyServiceWorker('UNAUTHENTICATED');
                    Acetate.showGate();
                });
        },

        onAuthenticated: function (authData) {
            this.notifyServiceWorker('AUTHENTICATED');

            if (authData && authData.albums) {
                this.albums = authData.albums;
            }

            if (!this.albums || this.albums.length === 0) {
                // No albums accessible — shouldn't happen but handle gracefully
                this.showGate();
                return;
            }

            if (this.albums.length === 1) {
                this.selectAlbum(this.albums[0]);
            } else {
                this.showAlbumSelector();
            }
        },

        selectAlbum: function (album) {
            this.currentAlbum = album;
            this.offlineMode = false;
            document.body.classList.remove('offline-mode');

            var nextSearch = window.location.search;
            if (this.pendingDeepLinkSearch && nextSearch !== this.pendingDeepLinkSearch) {
                nextSearch = this.pendingDeepLinkSearch;
            }
            history.pushState({ screen: 'player' }, '', window.location.pathname + nextSearch + window.location.hash);

            this.loadAlbumData();
        },

        showAlbumSelector: function () {
            this.state = 'selector';
            this.currentAlbum = null;
            if (typeof AcetatePlayer !== 'undefined' && AcetatePlayer.isPlaying && AcetatePlayer.isPlaying()) {
                AcetatePlayer.pause();
            }
            history.pushState({ screen: 'selector' }, '', window.location.pathname);
            document.getElementById('gate').classList.remove('active');
            document.getElementById('player').classList.remove('active');
            document.getElementById('selector').classList.add('active');

            if (typeof AcetateSelector !== 'undefined') {
                AcetateSelector.render(this.albums);
            }
        },

        loadAlbumData: function () {
            var base = this.albumApiBase();
            fetch(base + '/tracks', { credentials: 'same-origin' })
                .then(function (r) {
                    if (!r.ok) {
                        if (r.status === 401 || r.status === 403) {
                            throw new Error('unauthorized');
                        }
                        throw new Error('tracks_unavailable');
                    }
                    return r.json();
                })
                .then(function (data) {
                    Acetate.offlineMode = false;
                    document.body.classList.remove('offline-mode');
                    Acetate.storeOfflineAlbumSnapshot(data);
                    Acetate.onAlbumDataLoaded(data, false);
                })
                .catch(function (err) {
                    if (err && err.message === 'unauthorized') {
                        Acetate.notifyServiceWorker('UNAUTHENTICATED');
                        Acetate.showGate();
                        return;
                    }

                    var offlineData = Acetate.readOfflineAlbumSnapshot();
                    if (offlineData) {
                        Acetate.offlineMode = true;
                        document.body.classList.add('offline-mode');
                        Acetate.notifyServiceWorker('AUTHENTICATED');
                        Acetate.onAlbumDataLoaded(offlineData, true);
                        return;
                    }

                    Acetate.notifyServiceWorker('UNAUTHENTICATED');
                    Acetate.showGate();
                });
        },

        onAlbumDataLoaded: function (data, isOffline) {
            Acetate.albumData = data;
            document.title = data.title + ' — Acetate';
            Acetate.showPlayer();

            if (typeof AcetateTracklist !== 'undefined') {
                AcetateTracklist.setDownloadsEnabled(!!data.downloads_enabled);
                AcetateTracklist.render(data.tracks);
            }

            if (!data.tracks || data.tracks.length === 0) {
                return;
            }

            var target = Acetate.resolveInitialPlaybackTarget(data);
            AcetatePlayer.loadTrack(target.index, { startTime: target.time });

            if (!isOffline) {
                Acetate.warmOfflineCaches(data);
            }
        },

        showGate: function () {
            this.state = 'gate';
            this.offlineMode = false;
            this.currentAlbum = null;
            this.albums = null;
            document.body.classList.remove('offline-mode');
            if (typeof AcetatePlayer !== 'undefined' && AcetatePlayer.isPlaying && AcetatePlayer.isPlaying()) {
                AcetatePlayer.pause();
            }
            document.getElementById('gate').classList.add('active');
            document.getElementById('player').classList.remove('active');
            document.getElementById('selector').classList.remove('active');
            var input = document.getElementById('passphrase');
            input.value = '';
            setTimeout(function () { input.focus(); }, 100);
        },

        showPlayer: function () {
            this.state = 'player';
            document.getElementById('gate').classList.remove('active');
            document.getElementById('selector').classList.remove('active');
            document.getElementById('player').classList.add('active');

            // Load cover
            var cover = document.getElementById('cover');
            cover.classList.remove('hidden', 'fallback');
            cover.src = this.albumApiBase() + '/cover';
            cover.onerror = function () {
                this.onerror = null;
                this.classList.add('fallback');
                this.src = Acetate.makeCoverFallback();
            };
        },

        resolveInitialPlaybackTarget: function (albumData) {
            var tracks = (albumData && albumData.tracks) ? albumData.tracks : [];
            if (!tracks.length) return { index: 0, time: 0 };

            if (this.pendingDeepLinkSearch) {
                var deepLink = this.parseDeepLinkTarget(tracks, this.pendingDeepLinkSearch);
                if (deepLink) {
                    this.pendingDeepLinkSearch = '';
                    return deepLink;
                }
            }

            var playbackState = this.readPlaybackState();
            if (playbackState) {
                if (playbackState.album_fingerprint && playbackState.album_fingerprint !== this.makeAlbumFingerprint(albumData)) {
                    return { index: 0, time: 0 };
                }

                var resumeIdx = this.findTrackIndexByStem(tracks, playbackState.track_stem);
                if (resumeIdx >= 0) {
                    return {
                        index: resumeIdx,
                        time: this.normalizeTime(playbackState.time_seconds)
                    };
                }
            }

            return { index: 0, time: 0 };
        },

        parseDeepLinkTarget: function (tracks, search) {
            var params = new URLSearchParams(search || window.location.search || '');
            var hasTrack = params.has('track');
            var hasTime = params.has('t');
            if (!hasTrack && !hasTime) return null;

            var idx = 0;
            if (hasTrack) {
                idx = this.parseTrackIndexParam(params.get('track'), tracks);
                if (idx < 0) idx = 0;
            }

            return {
                index: idx,
                time: this.parseTimeParam(params.get('t'))
            };
        },

        parseTrackIndexParam: function (raw, tracks) {
            if (!raw || !tracks || !tracks.length) return -1;
            var value = String(raw).trim();
            if (!value) return -1;

            if (/^\d+$/.test(value)) {
                var n = parseInt(value, 10);
                if (n >= 1 && n <= tracks.length) return n - 1;
                if (n >= 0 && n < tracks.length) return n;
            }

            var lowered = value.toLowerCase();
            if (lowered.slice(-4) === '.mp3') {
                lowered = lowered.slice(0, -4);
            }
            for (var i = 0; i < tracks.length; i++) {
                if (String(tracks[i].stem || '').toLowerCase() === lowered) return i;
                if (String(tracks[i].title || '').toLowerCase() === lowered) return i;
            }

            return -1;
        },

        parseTimeParam: function (raw) {
            if (!raw) return 0;
            var value = String(raw).trim();
            if (!value) return 0;

            if (/^\d+(\.\d+)?$/.test(value)) {
                return this.normalizeTime(parseFloat(value));
            }

            var parts = value.split(':');
            if (parts.length >= 2 && parts.length <= 3) {
                var total = 0;
                for (var i = 0; i < parts.length; i++) {
                    if (!/^\d+(\.\d+)?$/.test(parts[i])) return 0;
                    total = total * 60 + parseFloat(parts[i]);
                }
                return this.normalizeTime(total);
            }
            return 0;
        },

        normalizeTime: function (seconds) {
            if (typeof seconds !== 'number' || !isFinite(seconds) || seconds < 0) return 0;
            return seconds;
        },

        findTrackIndexByStem: function (tracks, stem) {
            if (!stem) return -1;
            var needle = String(stem).toLowerCase();
            for (var i = 0; i < tracks.length; i++) {
                if (String(tracks[i].stem || '').toLowerCase() === needle) return i;
            }
            return -1;
        },

        makeAlbumFingerprint: function (albumData) {
            if (!albumData || !albumData.tracks) return '';
            var stems = albumData.tracks.map(function (t) { return t.stem; }).join('|');
            return stems;
        },

        readPlaybackState: function () {
            if (typeof AcetatePlayer !== 'undefined' && typeof AcetatePlayer.getStoredPlaybackState === 'function') {
                return AcetatePlayer.getStoredPlaybackState();
            }
            try {
                var raw = localStorage.getItem(this.playbackStateKey);
                return raw ? JSON.parse(raw) : null;
            } catch (err) {
                return null;
            }
        },

        storeOfflineAlbumSnapshot: function (data) {
            try {
                localStorage.setItem(this.offlineAlbumKey(), JSON.stringify({
                    saved_at: Date.now(),
                    album: data
                }));
            } catch (err) {
                // Ignore storage quota errors.
            }
        },

        readOfflineAlbumSnapshot: function () {
            try {
                var raw = localStorage.getItem(this.offlineAlbumKey());
                if (!raw) return null;
                var parsed = JSON.parse(raw);
                if (!parsed || !parsed.album || !parsed.album.tracks || !parsed.album.tracks.length) {
                    return null;
                }
                return parsed.album;
            } catch (err) {
                return null;
            }
        },

        warmOfflineCaches: function (albumData) {
            if (!albumData || !albumData.tracks) return;
            var base = this.albumApiBase();

            fetch(base + '/cover', { credentials: 'same-origin' }).catch(function () { });

            albumData.tracks.forEach(function (track) {
                fetch(base + '/lyrics/' + Acetate.encodePathSegment(track.stem), { credentials: 'same-origin' }).catch(function () { });
            });
        },

        encodePathSegment: function (value) {
            return encodeURIComponent(value).replace(/[!'()*]/g, function (ch) {
                return '%' + ch.charCodeAt(0).toString(16).toUpperCase();
            });
        },

        extractDeepLinkSearch: function (search) {
            var params = new URLSearchParams(search || '');
            if (!params.has('track') && !params.has('t')) {
                return '';
            }
            return search || '';
        },

        makeCoverFallback: function (overrideTitle) {
            var title = overrideTitle || ((this.albumData && this.albumData.title) ? this.albumData.title : 'Acetate');
            title = String(title).replace(/[<>&]/g, '').slice(0, 28);
            var svg =
                "<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 300 300'>" +
                "<rect width='300' height='300' fill='%23110f0d'/>" +
                "<rect x='12' y='12' width='276' height='276' fill='none' stroke='%23292620' stroke-width='2'/>" +
                "<text x='150' y='155' text-anchor='middle' fill='%23d4cfc4' font-size='26' font-family='serif'>" + title + "</text>" +
                "</svg>";
            return 'data:image/svg+xml;utf8,' + encodeURIComponent(svg);
        },

        notifyServiceWorker: function (state) {
            if (!('serviceWorker' in navigator)) return;

            var message = { type: state };
            if (navigator.serviceWorker.controller) {
                navigator.serviceWorker.controller.postMessage(message);
                return;
            }

            navigator.serviceWorker.ready.then(function (reg) {
                if (reg.active) {
                    reg.active.postMessage(message);
                }
            }).catch(function () { });
        }
    };

    document.addEventListener('DOMContentLoaded', function () {
        Acetate.init();
    });
})();
