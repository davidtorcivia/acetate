// Acetate — Player (double-deck gapless playback)
(function () {
    'use strict';

    var deckA, deckB;
    var activeDeck = null;      // currently playing <audio>
    var inactiveDeck = null;    // preloaded <audio>
    var isPlaying = false;      // the active deck is actually producing audio
    var playRequested = false;  // the listener asked to play (may still be buffering)
    var isSeeking = false;
    var warmedUp = false;
    var loopRunning = false;
    var playRecorded = false;
    var pendingSeekTime = null;
    var lastPersistAt = 0;
    var currentVolume = 1;
    var lastNonZeroVolume = 1;
    var isMuted = false;
    var PLAYBACK_STATE_KEY = 'acetate-playback-v1';
    var SEEK_STEP_SECONDS = 5;
    var VOLUME_STEP = 0.05;
    var URL_SYNC_MIN_INTERVAL_MS = 1800;
    var lastURLSyncAt = 0;
    var urlSyncTimer = null;
    var shownSecond = -1;
    var shownDuration = -1;

    var btnPlay, btnPrev, btnNext, btnMute, progress, volumeSlider, timeCurrent, timeTotal, statusEl;

    window.AcetatePlayer = {
        loadTrack: loadTrack,
        play: play,
        pause: pause,
        isPlaying: function () { return isPlaying; },
        getActiveDeck: function () { return activeDeck; },
        seekTo: seekTo,
        seekBy: seekBy,
        adjustVolume: adjustVolume,
        getStoredPlaybackState: getStoredPlaybackState
    };

    function init() {
        deckA = document.getElementById('audio-a');
        deckB = document.getElementById('audio-b');
        activeDeck = deckA;
        inactiveDeck = deckB;

        btnPlay = document.getElementById('btn-play');
        btnPrev = document.getElementById('btn-prev');
        btnNext = document.getElementById('btn-next');
        btnMute = document.getElementById('btn-mute');
        progress = document.getElementById('progress');
        volumeSlider = document.getElementById('volume');
        timeCurrent = document.getElementById('time-current');
        timeTotal = document.getElementById('time-total');
        statusEl = document.getElementById('player-status');

        btnPlay.addEventListener('click', togglePlay);
        btnPrev.addEventListener('click', prevTrack);
        btnNext.addEventListener('click', nextTrack);
        if (btnMute) btnMute.addEventListener('click', toggleMute);

        progress.addEventListener('input', onSeekInput);
        progress.addEventListener('change', onSeekChange);
        // iOS ignores programmatic volume (reads back 1); mute still works there.
        deckA.volume = 0.5;
        if (deckA.volume !== 0.5) {
            volumeSlider.hidden = true;
        } else {
            volumeSlider.addEventListener('input', onVolumeInput);
            restoreVolumeState(getStoredPlaybackState());
        }

        // Track ended — advance to next
        deckA.addEventListener('ended', onTrackEnded);
        deckB.addEventListener('ended', onTrackEnded);

        // Duration available
        deckA.addEventListener('loadedmetadata', onMetadata);
        deckB.addEventListener('loadedmetadata', onMetadata);

        // OS pauses (interruptions, unplugged headphones), resumes and stream errors.
        [deckA, deckB].forEach(function (deck) {
            deck.addEventListener('pause', onDeckStateChange);
            deck.addEventListener('playing', onDeckStateChange);
            deck.addEventListener('error', onDeckError);
        });

        document.addEventListener('keydown', onPlayerKeydown);
        window.addEventListener('beforeunload', function () { persistPlaybackState(true); });
        document.addEventListener('visibilitychange', function () {
            if (document.visibilityState === 'hidden') {
                persistPlaybackState(true);
            }
        });

        applyVolume();
        updateVolumeUI();
        setPlayIcon();
        renderFrame();
    }

    function warmUp() {
        if (warmedUp) return;
        warmedUp = true;

        // iOS warm-up: play/pause both decks muted
        [deckA, deckB].forEach(function (deck) {
            deck.muted = true;
            var p = deck.play();
            if (p) p.catch(function () { });
            deck.pause();
        });
        applyVolume();

        // Initialize oscilloscope after warm-up
        if (typeof AcetateOscilloscope !== 'undefined') {
            AcetateOscilloscope.init(deckA, deckB);
        }
    }

    function loadTrack(index, options) {
        options = options || {};
        if (!Acetate.albumData || !Acetate.albumData.tracks) return;
        var tracks = Acetate.albumData.tracks;
        if (index < 0 || index >= tracks.length) return;

        var track = tracks[index];
        Acetate.currentTrackIndex = index;
        Acetate.notifyServiceWorker('AUTHENTICATED');
        pendingSeekTime = null;
        playRecorded = false;
        setStatus('');
        var targetURL = streamURL(track.stem);

        // Set source on active deck (reuse preloaded deck when possible for instant
        // transitions); a deck that errored must reload to recover.
        if (!isDeckSource(activeDeck, targetURL) || activeDeck.error) {
            activeDeck.src = targetURL;
            activeDeck.load();
        }

        // Update UI
        document.getElementById('track-title').textContent = track.title;
        updateMediaSession(track);

        preloadUpcoming(index);

        // Load lyrics
        if (typeof AcetateLyrics !== 'undefined') {
            AcetateLyrics.load(track.stem, track.lyric_format);
        }

        // Update tracklist highlight
        if (typeof AcetateTracklist !== 'undefined') {
            AcetateTracklist.setActive(index);
        }

        var startTime = (typeof options.startTime === 'number' && options.startTime > 0) ? options.startTime : 0;
        if (startTime > 0) {
            pendingSeekTime = startTime;
            // Reused preloaded deck: loadedmetadata already fired and will not fire again.
            if (activeDeck.readyState >= 1) applyPendingSeek();
        }

        renderFrame();
        syncPlaybackURL(true, startTime);
        persistPlaybackState(false);
    }

    function applyPendingSeek() {
        if (pendingSeekTime === null || !activeDeck.duration) return;
        activeDeck.currentTime = clamp(pendingSeekTime, 0, Math.max(0, activeDeck.duration - 0.05));
        pendingSeekTime = null;
    }

    function play() {
        playRequested = true;
        warmUp();

        if (typeof AcetateOscilloscope !== 'undefined') {
            AcetateOscilloscope.resumeContext();
        }

        // An errored element stays errored until it reloads; resume where it stopped.
        if (activeDeck.error && activeDeck.src) {
            pendingSeekTime = activeDeck.currentTime || null;
            activeDeck.src = activeDeck.src;
            activeDeck.load();
        }

        var deck = activeDeck;
        var p = deck.play();
        if (p && typeof p.then === 'function') {
            p.then(function () {
                onPlaybackStarted();
            }).catch(function (err) {
                // A deck swap or reload aborts the old promise; that is not a block.
                if (deck !== activeDeck || (err && err.name === 'AbortError')) return;
                onPlaybackBlocked();
            });
            return;
        }
        onPlaybackStarted();
    }

    // Always stops the active deck, even while it is still buffering.
    function pause() {
        if (!activeDeck) return; // app.js shows the gate before player init runs
        var wasPlaying = isPlaying;
        playRequested = false;
        isPlaying = false;
        activeDeck.pause();
        setPlayIcon();
        renderFrame();
        persistPlaybackState(true);

        if (wasPlaying) recordTrackEvent('pause');
    }

    function onPlaybackStarted() {
        if (!playRequested || activeDeck.paused) return;

        isPlaying = true;
        setPauseIcon();
        setStatus('');
        startLoop();

        if (typeof AcetateOscilloscope !== 'undefined') {
            AcetateOscilloscope.setActiveDeck(activeDeck);
        }

        if (!playRecorded) {
            playRecorded = true;
            recordTrackEvent('play');
        }
        persistPlaybackState(false);
    }

    function onPlaybackBlocked() {
        if (activeDeck.error) {
            onDeckError({ target: activeDeck });
            return;
        }
        playRequested = false;
        isPlaying = false;
        setPlayIcon();
    }

    // Reads element state rather than trusting the event: pause events queued by our own
    // src swaps arrive after the synchronous play() that follows them.
    function onDeckStateChange(e) {
        if (e.target !== activeDeck || activeDeck.ended) return;
        if (!activeDeck.paused) {
            if (!isPlaying) {
                playRequested = true;
                onPlaybackStarted();
            }
            return;
        }
        if (!playRequested) return;

        var wasPlaying = isPlaying;
        playRequested = false;
        isPlaying = false;
        setPlayIcon();
        renderFrame();
        persistPlaybackState(true);
        if (wasPlaying) recordTrackEvent('pause');
    }

    function onDeckError(e) {
        if (e.target !== activeDeck || !activeDeck.getAttribute('src')) return;
        playRequested = false;
        isPlaying = false;
        setPlayIcon();
        setStatus('Playback failed. Check your connection and try again.');
        Acetate.checkSession();
    }

    function setStatus(message) {
        if (statusEl && statusEl.textContent !== message) statusEl.textContent = message;
    }

    function recordTrackEvent(eventType, metadata) {
        if (typeof AcetateAnalytics !== 'undefined') {
            AcetateAnalytics.recordTrackEvent(eventType, metadata);
        }
    }

    function togglePlay() {
        if (playRequested) {
            pause();
        } else {
            play();
        }
    }

    function nextTrack() {
        if (!Acetate.albumData) return;
        var next = Acetate.currentTrackIndex + 1;
        if (next >= Acetate.albumData.tracks.length) return;

        // Swap decks for gapless
        var temp = activeDeck;
        activeDeck = inactiveDeck;
        inactiveDeck = temp;

        loadTrack(next);
        if (playRequested) play();
    }

    function prevTrack() {
        if (!Acetate.albumData) return;
        // If more than 3 seconds in, restart current track
        if (activeDeck.currentTime > 3) {
            seekTo(0, true);
            return;
        }
        var prev = Acetate.currentTrackIndex - 1;
        if (prev < 0) {
            seekTo(0, true);
            return;
        }

        loadTrack(prev);
        if (playRequested) play();
    }

    function onTrackEnded(e) {
        if (!Acetate.albumData || e.target !== activeDeck) return;
        recordTrackEvent('complete');
        pendingSeekTime = null;
        playRecorded = false;

        var next = Acetate.currentTrackIndex + 1;
        if (next < Acetate.albumData.tracks.length) {
            // Swap decks for gapless transition
            var temp = activeDeck;
            activeDeck = inactiveDeck;
            inactiveDeck = temp;

            Acetate.currentTrackIndex = next;
            Acetate.notifyServiceWorker('AUTHENTICATED');
            var nextTrack = Acetate.albumData.tracks[next];
            document.getElementById('track-title').textContent = nextTrack.title;
            updateMediaSession(nextTrack);

            if (typeof AcetateLyrics !== 'undefined') {
                AcetateLyrics.load(nextTrack.stem, nextTrack.lyric_format);
            }
            if (typeof AcetateTracklist !== 'undefined') {
                AcetateTracklist.setActive(next);
            }

            play();
            preloadUpcoming(next);
            syncPlaybackURL(true, 0);
            persistPlaybackState(true);
        } else {
            // Album finished
            playRequested = false;
            isPlaying = false;
            setPlayIcon();
            renderFrame();
            persistPlaybackState(true);
        }
    }

    function onMetadata() {
        if (this === activeDeck && activeDeck.duration) {
            applyPendingSeek();
            renderFrame();
        }
    }

    function onSeekInput() {
        isSeeking = true;
        timeCurrent.textContent = formatTime(parseFloat(progress.value));
    }

    function onSeekChange() {
        isSeeking = false;
        seekTo(parseFloat(progress.value), true);
    }

    function onVolumeInput() {
        var value = parseFloat(volumeSlider.value);
        if (isNaN(value)) return;

        currentVolume = clamp(value, 0, 1);
        if (currentVolume > 0) {
            isMuted = false;
            lastNonZeroVolume = currentVolume;
        } else {
            isMuted = true;
        }

        applyVolume();
        updateVolumeUI();
        persistPlaybackState(false);
    }

    function toggleMute() {
        if (isMuted || currentVolume === 0) {
            isMuted = false;
            if (currentVolume === 0) {
                currentVolume = lastNonZeroVolume > 0 ? lastNonZeroVolume : 0.8;
            }
        } else {
            isMuted = true;
        }

        applyVolume();
        updateVolumeUI();
        persistPlaybackState(false);
    }

    function applyVolume() {
        [deckA, deckB].forEach(function (deck) {
            if (!deck) return;
            deck.volume = currentVolume;
            deck.muted = isMuted || currentVolume === 0;
        });
    }

    function updateVolumeUI() {
        if (volumeSlider) {
            volumeSlider.value = String(currentVolume);
        }
        if (btnMute) {
            var muted = isMuted || currentVolume === 0;
            btnMute.textContent = muted ? 'MUTE' : 'VOL';
            btnMute.setAttribute('aria-label', muted ? 'Unmute' : 'Mute');
        }
    }

    // The frame loop runs only while playing; paused states render a single frame.
    function startLoop() {
        if (loopRunning) return;
        loopRunning = true;
        requestAnimationFrame(tick);
    }

    function tick() {
        renderFrame();
        if (isPlaying) {
            persistPlaybackState(false);
            requestAnimationFrame(tick);
        } else {
            loopRunning = false;
        }
    }

    function renderFrame() {
        if (!activeDeck) return;
        var t = activeDeck.currentTime || 0;
        var d = activeDeck.duration || 0;

        if (d !== shownDuration) {
            shownDuration = d;
            progress.max = d > 0 ? d : 100;
            timeTotal.textContent = formatTime(d);
            shownSecond = -1;
        }
        if (!isSeeking && Math.floor(t) !== shownSecond) {
            shownSecond = Math.floor(t);
            progress.value = t;
            timeCurrent.textContent = formatTime(t);
            progress.setAttribute('aria-valuetext', formatTime(t) + ' of ' + formatTime(d));
        }

        if (typeof AcetateOscilloscope !== 'undefined') {
            AcetateOscilloscope.draw(isPlaying);
        }
        if (typeof AcetateLyrics !== 'undefined') {
            AcetateLyrics.update(t);
        }
    }

    function updateMediaSession(track) {
        if (!('mediaSession' in navigator) || !Acetate.albumData) return;

        navigator.mediaSession.metadata = new MediaMetadata({
            title: track.title,
            artist: Acetate.albumData.artist,
            album: Acetate.albumData.title,
            artwork: [{ src: Acetate.albumApiBase() + '/cover', sizes: '512x512', type: 'image/jpeg' }]
        });

        navigator.mediaSession.setActionHandler('play', play);
        navigator.mediaSession.setActionHandler('pause', pause);
        navigator.mediaSession.setActionHandler('previoustrack', prevTrack);
        navigator.mediaSession.setActionHandler('nexttrack', nextTrack);
    }

    function preloadUpcoming(currentIndex) {
        if (!Acetate.albumData || !Acetate.albumData.tracks) return;
        var tracks = Acetate.albumData.tracks;
        var nextIdx = currentIndex + 1;

        if (nextIdx < tracks.length) {
            var nextURL = streamURL(tracks[nextIdx].stem);
            inactiveDeck.preload = 'auto';
            if (!isDeckSource(inactiveDeck, nextURL)) {
                inactiveDeck.src = nextURL;
                inactiveDeck.load();
            }
            return;
        }

        inactiveDeck.removeAttribute('src');
        inactiveDeck.load();
    }

    function seekTo(seconds, shouldRecord) {
        if (!activeDeck) return;

        var from = activeDeck.currentTime || 0;
        var hasDuration = activeDeck.duration && !isNaN(activeDeck.duration) && activeDeck.duration > 0;
        var limit = hasDuration ? Math.max(0, activeDeck.duration - 0.05) : Math.max(from, seconds || 0);
        var to = clamp((typeof seconds === 'number' && isFinite(seconds)) ? seconds : from, 0, limit);

        activeDeck.currentTime = to;
        renderFrame();

        if (shouldRecord) {
            recordTrackEvent('seek', { from: from, to: to });
        }

        syncPlaybackURL(false, to);
        persistPlaybackState(false);
    }

    function seekBy(deltaSeconds) {
        var current = (activeDeck && activeDeck.currentTime) ? activeDeck.currentTime : 0;
        seekTo(current + deltaSeconds, true);
    }

    function adjustVolume(delta) {
        var value = clamp(currentVolume + delta, 0, 1);
        currentVolume = value;
        if (value > 0) {
            isMuted = false;
            lastNonZeroVolume = value;
        } else {
            isMuted = true;
        }
        applyVolume();
        updateVolumeUI();
        persistPlaybackState(false);
    }

    function restoreVolumeState(state) {
        if (!state) return;

        if (typeof state.volume === 'number' && isFinite(state.volume)) {
            currentVolume = clamp(state.volume, 0, 1);
        }
        if (typeof state.is_muted === 'boolean') {
            isMuted = state.is_muted;
        } else {
            isMuted = currentVolume === 0;
        }
        if (currentVolume > 0) {
            lastNonZeroVolume = currentVolume;
        }
    }

    function onPlayerKeydown(e) {
        if (!window.Acetate || window.Acetate.state !== 'player') return;
        if (e.altKey || e.ctrlKey || e.metaKey) return;
        if (shouldIgnoreShortcutTarget(e.target)) return;

        var key = e.key;
        if (key === ' ' || key === 'Spacebar') {
            e.preventDefault();
            togglePlay();
            return;
        }
        if (key === 'ArrowLeft') {
            e.preventDefault();
            seekBy(-SEEK_STEP_SECONDS);
            return;
        }
        if (key === 'ArrowRight') {
            e.preventDefault();
            seekBy(SEEK_STEP_SECONDS);
            return;
        }
        if (key === 'ArrowUp') {
            e.preventDefault();
            adjustVolume(VOLUME_STEP);
            return;
        }
        if (key === 'ArrowDown') {
            e.preventDefault();
            adjustVolume(-VOLUME_STEP);
            return;
        }
        if (key === 'l' || key === 'L') {
            e.preventDefault();
            if (typeof AcetateLyrics !== 'undefined' && typeof AcetateLyrics.toggleVisibility === 'function') {
                AcetateLyrics.toggleVisibility();
            }
        }
    }

    function shouldIgnoreShortcutTarget(target) {
        if (!target || !target.tagName) return false;
        if (target.isContentEditable) return true;
        if (target.closest && (target.closest('#tracklist-items li') || target.closest('.lyrics .line-group.timed'))) {
            return true;
        }
        if (target.getAttribute && target.getAttribute('role') === 'button') {
            return true;
        }

        var tag = target.tagName.toLowerCase();
        return tag === 'input' || tag === 'textarea' || tag === 'select' || tag === 'button';
    }

    function getStoredPlaybackState() {
        try {
            var raw = localStorage.getItem(PLAYBACK_STATE_KEY);
            if (!raw) return null;
            var parsed = JSON.parse(raw);
            if (!parsed || typeof parsed !== 'object') return null;
            return parsed;
        } catch (err) {
            return null;
        }
    }

    function persistPlaybackState(force) {
        var now = Date.now();
        if (!force && now - lastPersistAt < 1000) {
            return;
        }

        var state = getStoredPlaybackState() || {};
        state.volume = currentVolume;
        state.is_muted = isMuted;
        state.updated_at = now;

        var track = Acetate.currentTrack();
        if (track) {
            state.track_stem = track.stem;
            state.time_seconds = activeDeck ? Math.max(0, activeDeck.currentTime || 0) : 0;
            state.album_fingerprint = Acetate.makeAlbumFingerprint(Acetate.albumData);
            syncPlaybackURL(force, state.time_seconds);
        }

        try {
            localStorage.setItem(PLAYBACK_STATE_KEY, JSON.stringify(state));
            lastPersistAt = now;
        } catch (err) {
            // Ignore storage failures (private mode/quota).
        }
    }

    function syncPlaybackURL(force, timeSeconds) {
        var track = Acetate.currentTrack();
        if (!track || !Acetate.currentAlbum) return;
        var now = Date.now();
        var wait = URL_SYNC_MIN_INTERVAL_MS - (now - lastURLSyncAt);
        if (!force && wait > 0) {
            // Trailing update so the URL converges after a burst of seeks.
            if (!urlSyncTimer) {
                urlSyncTimer = setTimeout(function () {
                    urlSyncTimer = null;
                    if (Acetate.state === 'player') syncPlaybackURL(true);
                }, wait);
            }
            return;
        }

        var url = new URL(window.location.href);
        var nextTime = Math.max(0, Math.floor((typeof timeSeconds === 'number' ? timeSeconds : (activeDeck ? activeDeck.currentTime : 0)) || 0));

        url.searchParams.set('album', Acetate.currentAlbum.slug);
        url.searchParams.set('track', track.stem);
        url.searchParams.set('t', String(nextTime));

        var next = url.pathname + '?' + url.searchParams.toString() + url.hash;
        var current = window.location.pathname + window.location.search + window.location.hash;
        if (next !== current) {
            try {
                history.replaceState(history.state || { screen: 'player' }, '', next);
            } catch (err) {
                // Safari throws SecurityError past its replaceState rate limit.
            }
        }
        lastURLSyncAt = now;
    }

    function formatTime(seconds) {
        if (!seconds || isNaN(seconds)) return '0:00';
        var m = Math.floor(seconds / 60);
        var s = Math.floor(seconds % 60);
        return m + ':' + (s < 10 ? '0' : '') + s;
    }

    function streamURL(stem) {
        return Acetate.albumApiBase() + '/stream/' + Acetate.encodePathSegment(stem);
    }

    function isDeckSource(deck, relativeURL) {
        if (!deck || !deck.src) return false;
        try {
            var target = new URL(relativeURL, window.location.origin).href;
            return deck.src === target;
        } catch (err) {
            return false;
        }
    }

    function clamp(value, min, max) {
        return Math.min(max, Math.max(min, value));
    }

    function setPlayIcon() {
        btnPlay.innerHTML = '<svg width="18" height="20" viewBox="0 0 18 20" fill="currentColor"><polygon points="2,0 18,10 2,20"/></svg>';
        btnPlay.setAttribute('aria-label', 'Play');
    }

    function setPauseIcon() {
        btnPlay.innerHTML = '<svg width="16" height="20" viewBox="0 0 16 20" fill="currentColor"><rect x="1" y="0" width="4.5" height="20"/><rect x="10.5" y="0" width="4.5" height="20"/></svg>';
        btnPlay.setAttribute('aria-label', 'Pause');
    }

    document.addEventListener('DOMContentLoaded', init);
})();
