// Acetate — Lyrics (LRC sync, plain text, markdown)
(function () {
    'use strict';

    var container, lyricsEl;
    var currentFormat = null;
    var lrcData = null;     // parsed LRC: [{time, lines}]
    var activeIndex = -1;
    var lrcGroups = [];

    window.AcetateLyrics = {
        load: load,
        update: update
    };

    function init() {
        container = document.getElementById('lyrics-container');
        lyricsEl = document.getElementById('lyrics');
    }

    function load(stem, format) {
        if (!lyricsEl) return;

        currentFormat = null;
        lrcData = null;
        activeIndex = -1;
        lrcGroups = [];
        lyricsEl.innerHTML = '';

        if (!format) {
            lyricsEl.innerHTML = '<div class="no-lyrics"></div>';
            return;
        }

        fetch('/api/lyrics/' + encodePathSegment(stem), { credentials: 'same-origin' })
            .then(function (r) {
                if (!r.ok) throw new Error('not found');
                return r.json();
            })
            .then(function (data) {
                currentFormat = data.format;
                if (data.format === 'lrc') {
                    renderLRC(data.content);
                } else if (data.format === 'markdown') {
                    renderMarkdown(data.content);
                } else {
                    renderPlainText(data.content);
                }
            })
            .catch(function () {
                lyricsEl.innerHTML = '<div class="no-lyrics"></div>';
            });
    }

    function parseLRC(content) {
        var lines = content.split('\n');
        var grouped = Object.create(null);
        var timeRegex = /\[(\d{2}):(\d{2})\.(\d{2,3})\]/g;

        for (var i = 0; i < lines.length; i++) {
            var line = lines[i];
            var text = line.replace(/\[[^\]]+\]/g, '').trim();
            if (!text) continue;

            var match = null;
            timeRegex.lastIndex = 0;
            while ((match = timeRegex.exec(line)) !== null) {
                var minutes = parseInt(match[1], 10);
                var seconds = parseInt(match[2], 10);
                var ms = match[3].length === 2 ? parseInt(match[3], 10) * 10 : parseInt(match[3], 10);
                var timeMs = (minutes * 60 * 1000) + (seconds * 1000) + ms;
                var key = String(timeMs);

                if (!grouped[key]) {
                    grouped[key] = { time: timeMs / 1000, lines: [] };
                }
                grouped[key].lines.push(text);
            }
        }

        var entries = Object.keys(grouped).map(function (k) { return grouped[k]; });
        entries.sort(function (a, b) { return a.time - b.time; });
        return entries;
    }

    function renderLRC(content) {
        lrcData = parseLRC(content);
        lyricsEl.innerHTML = '';

        // Add spacer at top for centering
        var topSpacer = document.createElement('div');
        topSpacer.style.height = '40%';
        lyricsEl.appendChild(topSpacer);

        lrcGroups = [];
        for (var i = 0; i < lrcData.length; i++) {
            var entry = lrcData[i];
            var group = document.createElement('div');
            group.className = 'line-group';
            group.dataset.index = i;

            for (var j = 0; j < entry.lines.length; j++) {
                var line = document.createElement('div');
                line.className = 'line';
                line.textContent = entry.lines[j];
                group.appendChild(line);
            }

            lyricsEl.appendChild(group);
            lrcGroups.push(group);
        }

        // Add spacer at bottom
        var bottomSpacer = document.createElement('div');
        bottomSpacer.style.height = '40%';
        lyricsEl.appendChild(bottomSpacer);
    }

    function renderPlainText(content) {
        lyricsEl.innerHTML = '';
        var lines = content.split('\n');
        for (var i = 0; i < lines.length; i++) {
            var line = document.createElement('div');
            line.className = 'line';
            line.style.opacity = '0.7';
            line.textContent = lines[i];
            lyricsEl.appendChild(line);
        }
    }

    function renderMarkdown(content) {
        lyricsEl.innerHTML = '<div class="markdown-content">' + content + '</div>';
    }

    function update(currentTime) {
        if (currentFormat !== 'lrc' || !lrcData || lrcData.length === 0) return;

        var newIndex = activeIndex;

        if (newIndex < 0 || currentTime < lrcData[newIndex].time) {
            newIndex = findActiveIndex(currentTime);
        } else {
            while (newIndex + 1 < lrcData.length && currentTime >= lrcData[newIndex + 1].time) {
                newIndex++;
            }
        }

        if (newIndex === activeIndex) return;

        if (activeIndex >= 0 && lrcGroups[activeIndex]) {
            setGroupActive(lrcGroups[activeIndex], false);
        }
        activeIndex = newIndex;
        if (activeIndex >= 0 && lrcGroups[activeIndex]) {
            setGroupActive(lrcGroups[activeIndex], true);
        }

        // Auto-scroll to center active line
        if (activeIndex >= 0 && container) {
            var activeGroup = lrcGroups[activeIndex];
            if (activeGroup) {
                var target = activeGroup.offsetTop - (container.clientHeight / 2) + (activeGroup.offsetHeight / 2);
                container.scrollTo({ top: Math.max(0, target), behavior: 'smooth' });
            }
        }
    }

    function setGroupActive(group, isActive) {
        var lines = group.querySelectorAll('.line');
        for (var i = 0; i < lines.length; i++) {
            lines[i].classList.toggle('active', isActive);
        }
    }

    function findActiveIndex(currentTime) {
        var low = 0;
        var high = lrcData.length - 1;
        var idx = -1;

        while (low <= high) {
            var mid = (low + high) >> 1;
            if (lrcData[mid].time <= currentTime) {
                idx = mid;
                low = mid + 1;
            } else {
                high = mid - 1;
            }
        }

        return idx;
    }

    function encodePathSegment(value) {
        return encodeURIComponent(value).replace(/[!'()*]/g, function (ch) {
            return '%' + ch.charCodeAt(0).toString(16).toUpperCase();
        });
    }

    document.addEventListener('DOMContentLoaded', init);
})();
