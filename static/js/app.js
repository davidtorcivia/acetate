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
                        Acetate.showGate();
                    }
                })
                .catch(function () {
                    Acetate.showGate();
                });
        },

        onAuthenticated: function () {
            // Push history state for back button protection
            history.pushState({ screen: 'player' }, '', '/');

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
            document.getElementById('cover').src = '/api/cover';
            document.getElementById('cover').onerror = function () {
                this.classList.add('hidden');
            };
        }
    };

    document.addEventListener('DOMContentLoaded', function () {
        Acetate.init();
    });
})();
