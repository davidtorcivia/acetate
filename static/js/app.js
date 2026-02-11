// Acetate — SPA state machine
(function () {
    'use strict';

    window.Acetate = {
        state: 'gate', // 'gate' or 'player'
        albumData: null,
        currentTrackIndex: -1,

        init: function () {
            this.checkSession();

            // Register service worker
            if ('serviceWorker' in navigator) {
                navigator.serviceWorker.register('/sw.js').catch(function () { });
            }

            // Handle back button
            window.addEventListener('popstate', function () {
                if (Acetate.state === 'player') {
                    Acetate.showGate();
                }
            });
        },

        checkSession: function () {
            fetch('/api/session', { credentials: 'same-origin' })
                .then(function (r) {
                    if (r.ok) {
                        Acetate.onAuthenticated();
                    } else {
                        Acetate.notifyServiceWorker('UNAUTHENTICATED');
                        Acetate.showGate();
                    }
                })
                .catch(function () {
                    Acetate.notifyServiceWorker('UNAUTHENTICATED');
                    Acetate.showGate();
                });
        },

        onAuthenticated: function () {
            // Push history state for back button protection
            history.pushState({ screen: 'player' }, '', '/');
            this.notifyServiceWorker('AUTHENTICATED');

            this.loadAlbumData();
        },

        loadAlbumData: function () {
            fetch('/api/tracks', { credentials: 'same-origin' })
                .then(function (r) { return r.json(); })
                .then(function (data) {
                    Acetate.albumData = data;
                    document.title = data.title + ' — Acetate';
                    Acetate.showPlayer();
                    if (typeof AcetateTracklist !== 'undefined') {
                        AcetateTracklist.render(data.tracks);
                    }
                    // Auto-play first track
                    if (data.tracks && data.tracks.length > 0) {
                        AcetatePlayer.loadTrack(0);
                    }
                })
                .catch(function () {
                    Acetate.notifyServiceWorker('UNAUTHENTICATED');
                    Acetate.showGate();
                });
        },

        showGate: function () {
            this.state = 'gate';
            document.getElementById('gate').classList.add('active');
            document.getElementById('player').classList.remove('active');
            var input = document.getElementById('passphrase');
            input.value = '';
            setTimeout(function () { input.focus(); }, 100);
        },

        showPlayer: function () {
            this.state = 'player';
            document.getElementById('gate').classList.remove('active');
            document.getElementById('player').classList.add('active');

            // Load cover
            var cover = document.getElementById('cover');
            cover.classList.remove('hidden', 'fallback');
            cover.src = '/api/cover';
            cover.onerror = function () {
                this.onerror = null;
                this.classList.add('fallback');
                this.src = Acetate.makeCoverFallback();
            };
        },

        makeCoverFallback: function () {
            var title = (this.albumData && this.albumData.title) ? this.albumData.title : 'Acetate';
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
