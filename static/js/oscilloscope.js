// Acetate — Oscilloscope visualizer (Web Audio API)
(function () {
    'use strict';

    var canvas, ctx;
    var audioCtx = null;
    var analyser = null;
    var sourceA = null, sourceB = null;
    var dataArray = null;
    var currentSource = null;
    var initialized = false;

    window.AcetateOscilloscope = {
        init: initAudio,
        draw: draw,
        setActiveDeck: setActiveDeck,
        resumeContext: resumeContext
    };

    function initCanvas() {
        canvas = document.getElementById('oscilloscope');
        if (!canvas) return;
        ctx = canvas.getContext('2d');
        resize();
        window.addEventListener('resize', resize);
    }

    function resize() {
        if (!canvas) return;
        var dpr = window.devicePixelRatio || 1;
        var rect = canvas.getBoundingClientRect();
        canvas.width = rect.width * dpr;
        canvas.height = rect.height * dpr;
        ctx.scale(dpr, dpr);
    }

    function initAudio(deckA, deckB) {
        if (initialized) return;
        initialized = true;

        try {
            audioCtx = new (window.AudioContext || window.webkitAudioContext)();
            analyser = audioCtx.createAnalyser();
            analyser.fftSize = 2048;
            analyser.smoothingTimeConstant = 0.8;

            dataArray = new Uint8Array(analyser.frequencyBinCount);

            // Create sources (once per element — cannot be recreated)
            sourceA = audioCtx.createMediaElementSource(deckA);
            sourceB = audioCtx.createMediaElementSource(deckB);

            // Connect both to destination (speakers)
            sourceA.connect(audioCtx.destination);
            sourceB.connect(audioCtx.destination);

            // Connect active source to analyser
            sourceA.connect(analyser);
            currentSource = sourceA;
        } catch (e) {
            // Web Audio not available — oscilloscope will be flat
        }
    }

    function setActiveDeck(deck) {
        if (!analyser || !sourceA || !sourceB) return;

        // Disconnect current from analyser
        if (currentSource) {
            try { currentSource.disconnect(analyser); } catch (e) { }
        }

        // Connect the new active deck's source
        var newSource = (deck === document.getElementById('audio-a')) ? sourceA : sourceB;
        try { newSource.connect(analyser); } catch (e) { }
        currentSource = newSource;
    }

    function resumeContext() {
        if (audioCtx && audioCtx.state === 'suspended') {
            audioCtx.resume();
        }
    }

    function draw(isPlaying) {
        if (!canvas || !ctx) return;

        var width = canvas.getBoundingClientRect().width;
        var height = canvas.getBoundingClientRect().height;

        ctx.clearRect(0, 0, width, height);
        ctx.lineWidth = 1.2;
        ctx.strokeStyle = getComputedStyle(document.documentElement)
            .getPropertyValue('--accent').trim() || '#8a7a5a';
        ctx.beginPath();

        if (!isPlaying || !analyser || !dataArray) {
            // Flat line — held breath
            var midY = height / 2;
            ctx.moveTo(0, midY);
            ctx.lineTo(width, midY);
            ctx.stroke();
            return;
        }

        analyser.getByteTimeDomainData(dataArray);

        var sliceWidth = width / dataArray.length;
        var x = 0;

        for (var i = 0; i < dataArray.length; i++) {
            var v = dataArray[i] / 128.0;
            var y = (v * height) / 2;

            if (i === 0) {
                ctx.moveTo(x, y);
            } else {
                ctx.lineTo(x, y);
            }
            x += sliceWidth;
        }

        ctx.stroke();
    }

    document.addEventListener('DOMContentLoaded', initCanvas);
})();
