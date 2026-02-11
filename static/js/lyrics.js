// Acetate — Lyrics (LRC sync, plain text, markdown)
(function () {
    'use strict';

    var container, lyricsEl;
    var currentFormat = null;
    var lrcData = null;     // parsed LRC: [{time, lines}]
    var activeIndex = -1;

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
        lyricsEl.innerHTML = '';

        if (!format) {
            lyricsEl.innerHTML = '<div class="no-lyrics"></div>';
            return;
        }

        fetch('/api/lyrics/' + stem, { credentials: 'same-origin' })
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
        var entries = [];
        var timeRegex = /\[(\d{2}):(\d{2})\.(\d{2,3})\]\s*(.*)/;

        for (var i = 0; i < lines.length; i++) {
            var match = lines[i].match(timeRegex);
            if (!match) continue;

            var minutes = parseInt(match[1], 10);
            var seconds = parseInt(match[2], 10);
            var ms = match[3].length === 2 ? parseInt(match[3], 10) * 10 : parseInt(match[3], 10);
            var time = minutes * 60 + seconds + ms / 1000;
            var text = match[4].trim();

            if (!text) continue;

            // Group simultaneous timestamps
            if (entries.length > 0 && Math.abs(entries[entries.length - 1].time - time) < 0.05) {
                entries[entries.length - 1].lines.push(text);
            } else {
                entries.push({ time: time, lines: [text] });
            }
        }

        // Sort by time
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

        // Find active line by binary-ish search
        var newIndex = -1;
        for (var i = lrcData.length - 1; i >= 0; i--) {
            if (currentTime >= lrcData[i].time) {
                newIndex = i;
                break;
            }
        }

        if (newIndex === activeIndex) return;
        activeIndex = newIndex;

        // Update highlighting
        var groups = lyricsEl.querySelectorAll('.line-group');
        for (var g = 0; g < groups.length; g++) {
            var isActive = parseInt(groups[g].dataset.index) === activeIndex;
            var lines = groups[g].querySelectorAll('.line');
            for (var l = 0; l < lines.length; l++) {
                lines[l].classList.toggle('active', isActive);
            }
        }

        // Auto-scroll to center active line
        if (activeIndex >= 0 && container) {
            var activeGroup = lyricsEl.querySelector('.line-group[data-index="' + activeIndex + '"]');
            if (activeGroup) {
                var containerRect = container.getBoundingClientRect();
                var groupRect = activeGroup.getBoundingClientRect();
                var offset = groupRect.top - containerRect.top - (containerRect.height / 2) + (groupRect.height / 2);
                container.scrollBy({ top: offset, behavior: 'smooth' });
            }
        }
    }

    document.addEventListener('DOMContentLoaded', init);
})();
