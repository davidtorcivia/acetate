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

    var btnPlay, btnPrev, btnNext, progress, timeCurrent, timeTotal;

    window.AcetatePlayer = {
        loadTrack: loadTrack,
        play: play,
        pause: pause,
        isPlaying: function () { return isPlaying; },
        getActiveDeck: function () { return activeDeck; },
        getCurrentTrackIndex: function () { return Acetate.currentTrackIndex; }
    };

    function init() {
        deckA = document.getElementById('audio-a');
        deckB = document.getElementById('audio-b');
        activeDeck = deckA;
        inactiveDeck = deckB;

        btnPlay = document.getElementById('btn-play');
        btnPrev = document.getElementById('btn-prev');
        btnNext = document.getElementById('btn-next');
        progress = document.getElementById('progress');
        timeCurrent = document.getElementById('time-current');
        timeTotal = document.getElementById('time-total');

        btnPlay.addEventListener('click', togglePlay);
        btnPrev.addEventListener('click', prevTrack);
        btnNext.addEventListener('click', nextTrack);

        progress.addEventListener('input', onSeekInput);
        progress.addEventListener('change', onSeekChange);

        // Track ended — advance to next
        deckA.addEventListener('ended', onTrackEnded);
        deckB.addEventListener('ended', onTrackEnded);

        // Duration available
        deckA.addEventListener('loadedmetadata', onMetadata);
        deckB.addEventListener('loadedmetadata', onMetadata);

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

    function loadTrack(index) {
        if (!Acetate.albumData || !Acetate.albumData.tracks) return;
        var tracks = Acetate.albumData.tracks;
        if (index < 0 || index >= tracks.length) return;

        var track = tracks[index];
        Acetate.currentTrackIndex = index;

        // Set source on active deck
        activeDeck.src = '/api/stream/' + track.stem;
        activeDeck.load();

        // Update UI
        document.getElementById('track-title').textContent = track.title;
        updateMediaSession(track);

        // Preload next track on inactive deck
        var nextIdx = index + 1;
        if (nextIdx < tracks.length) {
            inactiveDeck.src = '/api/stream/' + tracks[nextIdx].stem;
            inactiveDeck.load();
        } else {
            inactiveDeck.src = '';
        }

        // Load lyrics
        if (typeof AcetateLyrics !== 'undefined') {
            AcetateLyrics.load(track.stem, track.lyric_format);
        }

        // Update tracklist highlight
        if (typeof AcetateTracklist !== 'undefined') {
            AcetateTracklist.setActive(index);
        }

        // Record analytics
        if (typeof AcetateAnalytics !== 'undefined') {
            AcetateAnalytics.record('play', track.stem);
        }

        // Reset progress
        progress.value = 0;
        timeCurrent.textContent = '0:00';
        timeTotal.textContent = '0:00';
    }

    function play() {
        warmUp();

        if (typeof AcetateOscilloscope !== 'undefined') {
            AcetateOscilloscope.resumeContext();
        }

        var p = activeDeck.play();
        if (p) p.catch(function () { });
        isPlaying = true;
        btnPlay.innerHTML = '&#x25AE;&#x25AE;';
        btnPlay.setAttribute('aria-label', 'Pause');

        if (typeof AcetateOscilloscope !== 'undefined') {
            AcetateOscilloscope.setActiveDeck(activeDeck);
        }
    }

    function pause() {
        activeDeck.pause();
        isPlaying = false;
        btnPlay.innerHTML = '&#x25B7;';
        btnPlay.setAttribute('aria-label', 'Play');

        if (typeof AcetateAnalytics !== 'undefined' && Acetate.albumData) {
            var track = Acetate.albumData.tracks[Acetate.currentTrackIndex];
            if (track) {
                AcetateAnalytics.record('pause', track.stem, activeDeck.currentTime);
            }
        }
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
            activeDeck.currentTime = 0;
            return;
        }
        var prev = Acetate.currentTrackIndex - 1;
        if (prev < 0) {
            activeDeck.currentTime = 0;
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
            if (typeof AcetateAnalytics !== 'undefined') {
                AcetateAnalytics.record('play', nextTrack.stem);
            }

            play();

            // Preload the one after
            var preloadIdx = next + 1;
            if (preloadIdx < Acetate.albumData.tracks.length) {
                inactiveDeck.src = '/api/stream/' + Acetate.albumData.tracks[preloadIdx].stem;
                inactiveDeck.load();
            }
        } else {
            // Album finished
            isPlaying = false;
            btnPlay.innerHTML = '&#x25B7;';
        }
    }

    function onMetadata() {
        if (this === activeDeck && activeDeck.duration) {
            timeTotal.textContent = formatTime(activeDeck.duration);
            progress.max = activeDeck.duration;
        }
    }

    function onSeekInput() {
        isSeeking = true;
        timeCurrent.textContent = formatTime(parseFloat(progress.value));
    }

    function onSeekChange() {
        var from = activeDeck.currentTime;
        activeDeck.currentTime = parseFloat(progress.value);
        isSeeking = false;

        if (typeof AcetateAnalytics !== 'undefined' && Acetate.albumData) {
            var track = Acetate.albumData.tracks[Acetate.currentTrackIndex];
            if (track) {
                AcetateAnalytics.record('seek', track.stem, activeDeck.currentTime, JSON.stringify({ from: from, to: activeDeck.currentTime }));
            }
        }
    }

    function startRAF() {
        function tick() {
            if (activeDeck && !isSeeking) {
                var t = activeDeck.currentTime || 0;
                var d = activeDeck.duration || 0;
                progress.value = t;
                timeCurrent.textContent = formatTime(t);
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

    function formatTime(seconds) {
        if (!seconds || isNaN(seconds)) return '0:00';
        var m = Math.floor(seconds / 60);
        var s = Math.floor(seconds % 60);
        return m + ':' + (s < 10 ? '0' : '') + s;
    }

    document.addEventListener('DOMContentLoaded', init);
})();
