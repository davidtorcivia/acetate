// Acetate — Client-side analytics
(function () {
    'use strict';

    var buffer = []; // [{url, event}]; the URL is fixed at record time so events reach their own album
    var flushInterval = 10000;  // 10 seconds
    var heartbeatInterval = 30000; // 30 seconds
    var maxBufferSize = 5000;
    var maxBatchSize = 200; // server caps at 500; smaller keeps a keepalive/beacon body under 64KB

    window.AcetateAnalytics = {
        recordTrackEvent: recordTrackEvent
    };

    function init() {
        setInterval(function () { flush(false); }, flushInterval);

        setInterval(function () {
            if (typeof AcetatePlayer !== 'undefined' && AcetatePlayer.isPlaying()) {
                recordTrackEvent('heartbeat');
            }
        }, heartbeatInterval);

        // Flush on page hide (most reliable cross-browser)
        document.addEventListener('pagehide', function () {
            if (typeof AcetatePlayer !== 'undefined' && AcetatePlayer.isPlaying()) {
                recordTrackEvent('dropout');
            }
            flush(true);
        });

        document.addEventListener('visibilitychange', function () {
            if (document.visibilityState === 'hidden') {
                flush(true);
            }
        });
    }

    // Records an event for the current track at the active deck's position.
    function recordTrackEvent(eventType, metadata) {
        var track = Acetate.currentTrack();
        var deck = typeof AcetatePlayer !== 'undefined' ? AcetatePlayer.getActiveDeck() : null;
        if (!track || !deck) return;

        var duration = deck.duration;
        if (!metadata && (eventType === 'pause' || eventType === 'dropout' || eventType === 'heartbeat') &&
            isFinite(duration) && duration > 0) {
            metadata = { duration: duration };
        }
        record(eventType, track.stem, deck.currentTime || 0, metadata);
    }

    function record(eventType, trackStem, position, metadata) {
        if (!Acetate.currentAlbum) return;

        var event = { event_type: eventType };
        if (trackStem) event.track_stem = trackStem;
        if (position !== undefined) event.position_seconds = position;
        if (metadata) event.metadata = metadata;

        if (buffer.length >= maxBufferSize) {
            if (eventType === 'play' || eventType === 'complete' || eventType === 'session_start' || eventType === 'session_end') {
                buffer.shift();
            } else {
                return;
            }
        }
        buffer.push({ url: Acetate.albumApiBase() + '/analytics', event: event });
    }

    function flush(onHide) {
        if (buffer.length === 0) return;

        var entries = buffer;
        buffer = [];

        var batches = [];
        var open = Object.create(null);
        entries.forEach(function (entry) {
            var batch = open[entry.url];
            if (!batch || batch.events.length >= maxBatchSize) {
                batch = open[entry.url] = { url: entry.url, events: [] };
                batches.push(batch);
            }
            batch.events.push(entry.event);
        });

        batches.forEach(function (batch) {
            send(batch.url, batch.events, onHide);
        });
    }

    function send(url, events, onHide) {
        var body = JSON.stringify(events);
        if (onHide && navigator.sendBeacon) {
            if (!navigator.sendBeacon(url, new Blob([body], { type: 'application/json' }))) {
                requeue(url, events);
            }
            return;
        }

        // A non-2xx response is dropped; only network failures are retried.
        fetch(url, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            credentials: 'same-origin',
            keepalive: body.length < 60000,
            body: body
        }).catch(function () {
            requeue(url, events);
        });
    }

    function requeue(url, events) {
        var entries = events.map(function (event) { return { url: url, event: event }; });
        buffer = entries.concat(buffer);
        if (buffer.length > maxBufferSize) {
            buffer = buffer.slice(buffer.length - maxBufferSize);
        }
    }

    document.addEventListener('DOMContentLoaded', init);
})();
