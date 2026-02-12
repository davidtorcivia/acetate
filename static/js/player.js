// Acetate — Player (double-deck gapless playback)
(function () {
    'use strict';

    var deckA, deckB;
    var activeDeck = null;      // currently playing <audio>
    var inactiveDeck = null;    // preloaded <audio>
    var isPlaying = false;
    var isSeeking = false;
    var warmedUp = false;
    var rafId = null;
    var lastPlayEventTrack = -1;
    var pendingSeekTime = null;
    var lastPersistAt = 0;
    var currentVolume = 1;
    var lastNonZeroVolume = 1;
    var isMuted = false;
    var prefetchedTracks = Object.create(null);
    var PLAYBACK_STATE_KEY = 'acetate-playback-v1';
    var SEEK_STEP_SECONDS = 5;
    var VOLUME_STEP = 0.05;
    var URL_SYNC_MIN_INTERVAL_MS = 1800;
    var lastURLSyncAt = 0;

    var btnPlay, btnPrev, btnNext, btnMute, progress, volumeSlider, timeCurrent, timeTotal;

    window.AcetatePlayer = {
        loadTrack: loadTrack,
        play: play,
        pause: pause,
        isPlaying: function () { return isPlaying; },
        getActiveDeck: function () { return activeDeck; },
        getCurrentTrackIndex: function () { return Acetate.currentTrackIndex; },
        seekTo: seekTo,
        seekBy: seekBy,
        adjustVolume: adjustVolume,
        getStoredPlaybackState: getStoredPlaybackState,
        persistPlaybackState: persistPlaybackState
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

        btnPlay.addEventListener('click', togglePlay);
        btnPrev.addEventListener('click', prevTrack);
        btnNext.addEventListener('click', nextTrack);
        if (btnMute) btnMute.addEventListener('click', toggleMute);

        progress.addEventListener('input', onSeekInput);
        progress.addEventListener('change', onSeekChange);
        if (volumeSlider) {
            volumeSlider.addEventListener('input', onVolumeInput);
        }
        restoreVolumeState(getStoredPlaybackState());

        // Track ended — advance to next
        deckA.addEventListener('ended', onTrackEnded);
        deckB.addEventListener('ended', onTrackEnded);

        // Duration available
        deckA.addEventListener('loadedmetadata', onMetadata);
        deckB.addEventListener('loadedmetadata', onMetadata);

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
        startRAF();
    }

    function warmUp() {
        if (warmedUp) return;
        warmedUp = true;

        // iOS warm-up: play/pause both decks at volume 0
        var origVolA = deckA.volume;
        var origVolB = deckB.volume;
        deckA.volume = 0;
        deckB.volume = 0;

        var pA = deckA.play();
        if (pA) pA.catch(function () { });
        deckA.pause();
        deckA.volume = origVolA;

        var pB = deckB.play();
        if (pB) pB.catch(function () { });
        deckB.pause();
        deckB.volume = origVolB;

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
        pendingSeekTime = null;
        var targetURL = streamURL(track.stem);

        // Set source on active deck (reuse preloaded deck when possible for instant transitions).
        if (!isDeckSource(activeDeck, targetURL)) {
            activeDeck.src = targetURL;
            activeDeck.load();
        }

        // Update UI
        document.getElementById('track-title').textContent = track.title;
        updateMediaSession(track);

        // Preload next track on inactive deck, and prefetch one more track for instant transitions.
        preloadUpcoming(index);

        // Load lyrics
        if (typeof AcetateLyrics !== 'undefined') {
            AcetateLyrics.load(track.stem, track.lyric_format);
        }

        // Update tracklist highlight
        if (typeof AcetateTracklist !== 'undefined') {
            AcetateTracklist.setActive(index);
        }

        // Reset progress
        progress.value = 0;
        timeCurrent.textContent = '0:00';
        timeTotal.textContent = '0:00';

        if (typeof options.startTime === 'number' && options.startTime > 0) {
            pendingSeekTime = options.startTime;
        }

        syncPlaybackURL(true, pendingSeekTime || 0);
        persistPlaybackState(false);
    }

    function play() {
        warmUp();

        if (typeof AcetateOscilloscope !== 'undefined') {
            AcetateOscilloscope.resumeContext();
        }

        var p = activeDeck.play();
        if (p && typeof p.then === 'function') {
            p.then(function () {
                onPlaybackStarted();
            }).catch(function () {
                onPlaybackBlocked();
            });
            return;
        }
        onPlaybackStarted();
    }

    function pause() {
        activeDeck.pause();
        isPlaying = false;
        setPlayIcon();
        persistPlaybackState(false);

        if (typeof AcetateAnalytics !== 'undefined' && Acetate.albumData) {
            var track = Acetate.albumData.tracks[Acetate.currentTrackIndex];
            if (track) {
                AcetateAnalytics.record('pause', track.stem, activeDeck.currentTime);
            }
        }
    }

    function onPlaybackStarted() {
        var playTrackIndex = Acetate.currentTrackIndex;
        var shouldRecordPlay = !isPlaying || playTrackIndex !== lastPlayEventTrack;

        isPlaying = true;
        setPauseIcon();

        if (typeof AcetateOscilloscope !== 'undefined') {
            AcetateOscilloscope.setActiveDeck(activeDeck);
        }

        if (shouldRecordPlay && typeof AcetateAnalytics !== 'undefined' && Acetate.albumData) {
            var track = Acetate.albumData.tracks[playTrackIndex];
            if (track) {
                AcetateAnalytics.record('play', track.stem, activeDeck.currentTime || 0);
                lastPlayEventTrack = playTrackIndex;
            }
        }
        persistPlaybackState(false);
    }

    function onPlaybackBlocked() {
        isPlaying = false;
        setPlayIcon();
    }

    function togglePlay() {
        if (isPlaying) {
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
        if (isPlaying) play();
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
        if (isPlaying) play();
    }

    function onTrackEnded() {
        if (!Acetate.albumData) return;
        var track = Acetate.albumData.tracks[Acetate.currentTrackIndex];
        if (track && typeof AcetateAnalytics !== 'undefined') {
            AcetateAnalytics.record('complete', track.stem);
        }

        var next = Acetate.currentTrackIndex + 1;
        if (next < Acetate.albumData.tracks.length) {
            // Swap decks for gapless transition
            var temp = activeDeck;
            activeDeck = inactiveDeck;
            inactiveDeck = temp;

            Acetate.currentTrackIndex = next;
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
            isPlaying = false;
            setPlayIcon();
            persistPlaybackState(true);
        }
    }

    function onMetadata() {
        if (this === activeDeck && activeDeck.duration) {
            if (pendingSeekTime !== null) {
                var bounded = clamp(pendingSeekTime, 0, Math.max(0, activeDeck.duration - 0.05));
                activeDeck.currentTime = bounded;
                pendingSeekTime = null;
            }
            timeTotal.textContent = formatTime(activeDeck.duration);
            progress.max = activeDeck.duration;
            progress.setAttribute('aria-valuemax', String(Math.floor(activeDeck.duration)));
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
        var effective = isMuted ? 0 : currentVolume;
        // Prefer Web Audio gain node (required for Safari once audio is
        // routed through createMediaElementSource). Falls back to
        // element.volume for when Web Audio hasn't been initialised yet.
        if (typeof AcetateOscilloscope !== 'undefined' && AcetateOscilloscope.setVolume) {
            AcetateOscilloscope.setVolume(effective);
        }
        if (deckA) deckA.volume = effective;
        if (deckB) deckB.volume = effective;
    }

    function updateVolumeUI() {
        if (volumeSlider) {
            volumeSlider.value = String(currentVolume);
            volumeSlider.setAttribute('aria-valuenow', String(Math.round(currentVolume * 100)));
        }
        if (btnMute) {
            var muted = isMuted || currentVolume === 0;
            btnMute.textContent = muted ? 'MUTE' : 'VOL';
            btnMute.setAttribute('aria-label', muted ? 'Unmute' : 'Mute');
            btnMute.setAttribute('aria-pressed', muted ? 'true' : 'false');
        }
    }

    function startRAF() {
        function tick() {
            if (activeDeck && !isSeeking) {
                var t = activeDeck.currentTime || 0;
                var d = activeDeck.duration || 0;
                progress.value = t;
                timeCurrent.textContent = formatTime(t);
                progress.setAttribute('aria-valuenow', String(Math.floor(t)));
                if (d > 0) {
                    progress.max = d;
                    timeTotal.textContent = formatTime(d);
                }
            }

            // Update oscilloscope
            if (typeof AcetateOscilloscope !== 'undefined') {
                AcetateOscilloscope.draw(isPlaying);
            }

            // Update lyrics (60fps precision)
            if (typeof AcetateLyrics !== 'undefined' && activeDeck) {
                AcetateLyrics.update(activeDeck.currentTime);
            }

            if (isPlaying) {
                persistPlaybackState(false);
            }

            rafId = requestAnimationFrame(tick);
        }
        rafId = requestAnimationFrame(tick);
    }

    function updateMediaSession(track) {
        if (!('mediaSession' in navigator) || !Acetate.albumData) return;

        navigator.mediaSession.metadata = new MediaMetadata({
            title: track.title,
            artist: Acetate.albumData.artist,
            album: Acetate.albumData.title,
            artwork: [{ src: '/api/cover', sizes: '512x512', type: 'image/jpeg' }]
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

            prefetchTrack(tracks[nextIdx].stem);
            if (nextIdx + 1 < tracks.length) {
                prefetchTrack(tracks[nextIdx + 1].stem);
            }
            return;
        }

        inactiveDeck.removeAttribute('src');
        inactiveDeck.load();
    }

    function prefetchTrack(stem) {
        if (!stem || prefetchedTracks[stem]) return;
        if (navigator.connection && navigator.connection.saveData) return;

        prefetchedTracks[stem] = true;
        fetch(streamURL(stem), { credentials: 'same-origin' })
            .then(function (resp) {
                if (!resp || !resp.ok) throw new Error('prefetch failed');
                return drainResponse(resp);
            })
            .catch(function () {
                delete prefetchedTracks[stem];
            });
    }

    function drainResponse(resp) {
        if (!resp.body || !resp.body.getReader) {
            return resp.blob().then(function () { });
        }

        var reader = resp.body.getReader();
        function pump() {
            return reader.read().then(function (chunk) {
                if (chunk.done) return;
                return pump();
            });
        }
        return pump();
    }

    function seekTo(seconds, shouldRecord) {
        if (!activeDeck) return;

        var from = activeDeck.currentTime || 0;
        var hasDuration = activeDeck.duration && !isNaN(activeDeck.duration) && activeDeck.duration > 0;
        var limit = hasDuration ? Math.max(0, activeDeck.duration - 0.05) : Math.max(from, seconds || 0);
        var to = clamp((typeof seconds === 'number' && isFinite(seconds)) ? seconds : from, 0, limit);

        activeDeck.currentTime = to;
        progress.value = to;
        timeCurrent.textContent = formatTime(to);

        if (shouldRecord && typeof AcetateAnalytics !== 'undefined' && Acetate.albumData) {
            var track = Acetate.albumData.tracks[Acetate.currentTrackIndex];
            if (track) {
                AcetateAnalytics.record('seek', track.stem, to, JSON.stringify({ from: from, to: to }));
            }
        }

        syncPlaybackURL(true, to);
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
        if (!state) {
            currentVolume = volumeSlider ? (parseFloat(volumeSlider.value) || 1) : 1;
            if (currentVolume > 0) lastNonZeroVolume = currentVolume;
            return;
        }

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

        if (Acetate.albumData && Acetate.albumData.tracks && Acetate.currentTrackIndex >= 0) {
            var track = Acetate.albumData.tracks[Acetate.currentTrackIndex];
            if (track) {
                state.track_stem = track.stem;
                state.time_seconds = activeDeck ? Math.max(0, activeDeck.currentTime || 0) : 0;
                state.album_fingerprint = getAlbumFingerprint(Acetate.albumData);
                syncPlaybackURL(force, state.time_seconds);
            }
        }

        try {
            localStorage.setItem(PLAYBACK_STATE_KEY, JSON.stringify(state));
            lastPersistAt = now;
        } catch (err) {
            // Ignore storage failures (private mode/quota).
        }
    }

    function getAlbumFingerprint(albumData) {
        if (!albumData || !albumData.tracks) return '';
        var stems = [];
        for (var i = 0; i < albumData.tracks.length; i++) {
            stems.push(albumData.tracks[i].stem);
        }
        return stems.join('|');
    }

    function syncPlaybackURL(force, timeSeconds) {
        if (!Acetate.albumData || !Acetate.albumData.tracks || Acetate.currentTrackIndex < 0) return;
        var now = Date.now();
        if (!force && now - lastURLSyncAt < URL_SYNC_MIN_INTERVAL_MS) return;

        var track = Acetate.albumData.tracks[Acetate.currentTrackIndex];
        if (!track) return;

        var url = new URL(window.location.href);
        var nextTime = Math.max(0, Math.floor((typeof timeSeconds === 'number' ? timeSeconds : (activeDeck ? activeDeck.currentTime : 0)) || 0));

        url.searchParams.set('track', track.stem);
        url.searchParams.set('t', String(nextTime));

        var next = url.pathname + '?' + url.searchParams.toString() + url.hash;
        var current = window.location.pathname + window.location.search + window.location.hash;
        if (next !== current) {
            history.replaceState(history.state || { screen: 'player' }, '', next);
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
        return '/api/stream/' + encodePathSegment(stem);
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

    function encodePathSegment(value) {
        return encodeURIComponent(value).replace(/[!'()*]/g, function (ch) {
            return '%' + ch.charCodeAt(0).toString(16).toUpperCase();
        });
    }

    function setPlayIcon() {
        btnPlay.innerHTML = '<svg width="18" height="20" viewBox="0 0 18 20" fill="currentColor"><polygon points="2,0 18,10 2,20"/></svg>';
        btnPlay.setAttribute('aria-label', 'Play');
        btnPlay.setAttribute('aria-pressed', 'false');
        btnPlay.classList.remove('is-paused');
    }

    function setPauseIcon() {
        btnPlay.innerHTML = '<svg width="16" height="20" viewBox="0 0 16 20" fill="currentColor"><rect x="1" y="0" width="4.5" height="20"/><rect x="10.5" y="0" width="4.5" height="20"/></svg>';
        btnPlay.setAttribute('aria-label', 'Pause');
        btnPlay.setAttribute('aria-pressed', 'true');
        btnPlay.classList.add('is-paused');
    }

    document.addEventListener('DOMContentLoaded', init);
})();
