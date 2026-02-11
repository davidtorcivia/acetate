// Acetate — Client-side analytics
(function () {
    'use strict';

    var buffer = [];
    var flushInterval = 10000;  // 10 seconds
    var heartbeatInterval = 30000; // 30 seconds
    var maxBufferSize = 5000;
    var flushTimer = null;
    var heartbeatTimer = null;

    window.AcetateAnalytics = {
        record: record,
        flush: flush
    };

    function init() {
        // Periodic flush
        flushTimer = setInterval(flush, flushInterval);

        // Heartbeat while playing
        heartbeatTimer = setInterval(function () {
            if (typeof AcetatePlayer !== 'undefined' && AcetatePlayer.isPlaying()) {
                var deck = AcetatePlayer.getActiveDeck();
                var data = Acetate.albumData;
                if (deck && data && data.tracks) {
                    var track = data.tracks[AcetatePlayer.getCurrentTrackIndex()];
                    if (track) {
                        record('heartbeat', track.stem, deck.currentTime);
                    }
                }
            }
        }, heartbeatInterval);

        // Flush on page hide (most reliable cross-browser)
        document.addEventListener('pagehide', function () {
            // Record dropout if playing
            if (typeof AcetatePlayer !== 'undefined' && AcetatePlayer.isPlaying()) {
                var deck = AcetatePlayer.getActiveDeck();
                var data = Acetate.albumData;
                if (deck && data && data.tracks) {
                    var track = data.tracks[AcetatePlayer.getCurrentTrackIndex()];
                    if (track) {
                        record('dropout', track.stem, deck.currentTime);
                    }
                }
            }
            flushBeacon();
        });

        // Also try visibilitychange
        document.addEventListener('visibilitychange', function () {
            if (document.visibilityState === 'hidden') {
                flush();
            }
        });
    }

    function record(eventType, trackStem, position, metadata) {
        var event = { event_type: eventType };
        if (trackStem) event.track_stem = trackStem;
        if (position !== undefined) event.position_seconds = position;
        if (metadata) {
            try {
                event.metadata = typeof metadata === 'string' ? JSON.parse(metadata) : metadata;
            } catch (e) {
                event.metadata = {};
            }
        }

        if (buffer.length >= maxBufferSize) {
            if (eventType === 'play' || eventType === 'complete' || eventType === 'session_start' || eventType === 'session_end') {
                buffer.shift();
            } else {
                return;
            }
        }
        buffer.push(event);
    }

    function flush() {
        if (buffer.length === 0) return;

        var events = buffer.slice();
        buffer = [];

        fetch('/api/analytics', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            credentials: 'same-origin',
            body: JSON.stringify(events)
        }).catch(function () {
            // Put events back on failure
            buffer = events.concat(buffer);
            if (buffer.length > maxBufferSize) {
                buffer = buffer.slice(buffer.length - maxBufferSize);
            }
        });
    }

    function flushBeacon() {
        if (buffer.length === 0) return;

        var events = buffer.slice();
        buffer = [];

        var blob = new Blob([JSON.stringify(events)], { type: 'application/json' });
        navigator.sendBeacon('/api/analytics', blob);
    }

    document.addEventListener('DOMContentLoaded', init);
})();
