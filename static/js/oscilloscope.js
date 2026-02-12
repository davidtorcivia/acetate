// Acetate — Oscilloscope visualizer (Web Audio API)
(function () {
    'use strict';

    var canvas, ctx;
    var audioCtx = null;
    var analyser = null;
    var gainNode = null;
    var sourceA = null, sourceB = null;
    var dataArray = null;
    var currentSource = null;
    var initialized = false;
    var drawWidth = 0, drawHeight = 0;
    var energyEMA = 0;
    var gainEMA = 1;

    window.AcetateOscilloscope = {
        init: initAudio,
        draw: draw,
        setActiveDeck: setActiveDeck,
        resumeContext: resumeContext,
        setVolume: setVolume
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
        ctx.setTransform(1, 0, 0, 1, 0, 0);
        ctx.scale(dpr, dpr);
        drawWidth = rect.width;
        drawHeight = rect.height;
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

            // Shared gain node for volume control (Safari ignores
            // element.volume once routed through Web Audio).
            gainNode = audioCtx.createGain();
            gainNode.connect(audioCtx.destination);

            // Create sources (once per element — cannot be recreated)
            sourceA = audioCtx.createMediaElementSource(deckA);
            sourceB = audioCtx.createMediaElementSource(deckB);

            // Connect both through gain node to destination (speakers)
            sourceA.connect(gainNode);
            sourceB.connect(gainNode);

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

    function setVolume(value) {
        if (gainNode) {
            gainNode.gain.value = value;
        }
    }

    function draw(isPlaying) {
        if (!canvas || !ctx) return;

        var width = drawWidth || canvas.getBoundingClientRect().width;
        var height = drawHeight || canvas.getBoundingClientRect().height;
        var midY = height / 2;

        ctx.clearRect(0, 0, width, height);
        var accent = getComputedStyle(document.documentElement)
            .getPropertyValue('--accent').trim() || '#8a7a5a';

        if (!isPlaying || !analyser || !dataArray) {
            // Flat line — held breath
            energyEMA *= 0.9;
            gainEMA += (1 - gainEMA) * 0.12;
            ctx.lineWidth = 1.15;
            ctx.strokeStyle = accent;
            ctx.beginPath();
            ctx.moveTo(0, midY);
            ctx.lineTo(width, midY);
            ctx.stroke();
            return;
        }

        analyser.getByteTimeDomainData(dataArray);

        var points = sampleWaveform(width, height, midY);
        if (points.length < 2) {
            return;
        }

        // A subtle under-stroke adds depth without glow/neon treatment.
        ctx.lineJoin = 'round';
        ctx.lineCap = 'round';
        ctx.strokeStyle = accent;
        ctx.globalAlpha = 0.24 + Math.min(0.2, energyEMA * 2.5);
        ctx.lineWidth = 2.15;
        strokeSmoothPath(points);

        ctx.globalAlpha = 0.95;
        ctx.lineWidth = 1.15;
        strokeSmoothPath(points);
        ctx.globalAlpha = 1;
    }

    function sampleWaveform(width, height, midY) {
        var length = dataArray.length;
        var maxPoints = Math.min(360, Math.max(120, Math.floor(width * 0.8)));
        var step = Math.max(1, Math.floor(length / maxPoints));

        var rmsAccum = 0;
        var count = 0;
        var i = 0;
        for (i = 0; i < length; i += step) {
            var centered = (dataArray[i] - 128) / 128;
            rmsAccum += centered * centered;
            count++;
        }

        var rms = count > 0 ? Math.sqrt(rmsAccum / count) : 0;
        energyEMA += (rms - energyEMA) * 0.16;

        // Adaptive gain keeps quiet passages visible while preserving loud transients.
        var targetGain = clamp(0.95 + (0.18 - energyEMA) * 2.2, 0.85, 1.8);
        gainEMA += (targetGain - gainEMA) * 0.11;

        var points = [];
        var x = 0;
        var sliceWidth = width / Math.ceil(length / step);

        for (i = 0; i < length; i += step) {
            var v = (dataArray[i] - 128) / 128;
            var y = midY + v * (height * 0.44) * gainEMA;
            points.push({ x: x, y: y });
            x += sliceWidth;
        }

        return points;
    }

    function strokeSmoothPath(points) {
        if (!points.length) return;

        ctx.beginPath();
        ctx.moveTo(points[0].x, points[0].y);

        for (var i = 1; i < points.length - 1; i++) {
            var xc = (points[i].x + points[i + 1].x) / 2;
            var yc = (points[i].y + points[i + 1].y) / 2;
            ctx.quadraticCurveTo(points[i].x, points[i].y, xc, yc);
        }

        var last = points[points.length - 1];
        ctx.lineTo(last.x, last.y);
        ctx.stroke();
    }

    function clamp(value, min, max) {
        return Math.min(max, Math.max(min, value));
    }

    document.addEventListener('DOMContentLoaded', initCanvas);
})();
