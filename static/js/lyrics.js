// Acetate — Lyrics (LRC sync, plain text, markdown)
(function () {
    'use strict';

    var container, lyricsEl;
    var currentFormat = null;
    var lrcData = null;     // parsed LRC: [{time, lines}]
    var activeIndex = -1;
    var lrcGroups = [];
    var lyricsVisible = true;
    var HIGHLIGHT_LEAD_SECONDS = 0.17;

    window.AcetateLyrics = {
        load: load,
        update: update,
        toggleVisibility: toggleVisibility,
        setVisible: setVisible,
        isVisible: function () { return lyricsVisible; }
    };

    function init() {
        container = document.getElementById('lyrics-container');
        lyricsEl = document.getElementById('lyrics');
        setVisible(true);
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
                    renderLRC(data.content, data.structure_content || '');
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
        var lines = normalizeNewlines(content).split('\n');
        var grouped = Object.create(null);
        var timeRegex = /\[(\d{1,2}):(\d{2})(?:\.(\d{1,3}))?\]/g;
        var pendingBreak = false;

        for (var i = 0; i < lines.length; i++) {
            var line = lines[i];
            var trimmed = line.trim();
            if (!trimmed) {
                pendingBreak = true;
                continue;
            }

            var text = cleanDisplayLine(trimmed);
            if (!text) continue;

            var match = null;
            var hasTimestamp = false;
            timeRegex.lastIndex = 0;
            while ((match = timeRegex.exec(line)) !== null) {
                hasTimestamp = true;
                var minutes = parseInt(match[1], 10);
                var seconds = parseInt(match[2], 10);
                var rawMs = match[3] || '0';
                var ms = parseInt(rawMs, 10);
                if (rawMs.length === 1) ms *= 100;
                else if (rawMs.length === 2) ms *= 10;
                var timeMs = (minutes * 60 * 1000) + (seconds * 1000) + ms;
                var key = String(timeMs);

                if (!grouped[key]) {
                    grouped[key] = { time: timeMs / 1000, lines: [], breakBefore: pendingBreak };
                } else if (pendingBreak) {
                    grouped[key].breakBefore = true;
                }
                grouped[key].lines.push(text);
            }

            if (hasTimestamp) {
                pendingBreak = false;
            }
        }

        var entries = Object.keys(grouped).map(function (k) { return grouped[k]; });
        entries.sort(function (a, b) { return a.time - b.time; });
        return entries;
    }

    function parseSRT(content) {
        var blocks = normalizeNewlines(content).split(/\n{2,}/);
        var entries = [];
        var timeRangeRegex = /^(\d{1,2}):(\d{2}):(\d{2})[,.](\d{1,3})\s*-->\s*(\d{1,2}):(\d{2}):(\d{2})[,.](\d{1,3})(?:\s+.*)?$/;

        for (var i = 0; i < blocks.length; i++) {
            var rawLines = blocks[i].split('\n').map(function (line) { return line.trim(); }).filter(Boolean);
            if (rawLines.length === 0) continue;

            if (/^\d+$/.test(rawLines[0])) {
                rawLines.shift();
            }
            if (rawLines.length === 0) continue;

            var match = rawLines[0].match(timeRangeRegex);
            if (!match) continue;

            var cueLines = [];
            for (var j = 1; j < rawLines.length; j++) {
                var cleaned = cleanDisplayLine(rawLines[j]);
                if (cleaned) cueLines.push(cleaned);
            }
            if (cueLines.length === 0) continue;

            entries.push({
                time: parseSRTTime(match[1], match[2], match[3], match[4]),
                lines: cueLines,
                breakBefore: entries.length > 0
            });
        }

        return entries;
    }

    function parseSRTTime(h, m, s, msRaw) {
        var ms = parseInt(msRaw, 10);
        if (msRaw.length === 1) ms *= 100;
        else if (msRaw.length === 2) ms *= 10;
        return (parseInt(h, 10) * 3600) + (parseInt(m, 10) * 60) + parseInt(s, 10) + (ms / 1000);
    }

    function parseStructure(content) {
        var lines = normalizeNewlines(content).split('\n');
        var items = [];
        var pendingBreak = false;
        var pendingLabel = '';

        for (var i = 0; i < lines.length; i++) {
            var raw = lines[i];
            var trimmed = raw.trim();

            if (!trimmed) {
                pendingBreak = true;
                continue;
            }

            var sectionLabel = extractSectionLabel(trimmed);
            if (sectionLabel) {
                pendingLabel = sectionLabel;
                pendingBreak = true;
                continue;
            }

            var cleaned = cleanDisplayLine(trimmed);
            if (!cleaned) continue;

            items.push({
                text: cleaned,
                breakBefore: pendingBreak,
                sectionLabel: pendingLabel
            });
            pendingBreak = false;
            pendingLabel = '';
        }

        return items;
    }

    function extractSectionLabel(line) {
        var bracket = line.match(/^\[([^\]]{1,80})\]$/);
        if (bracket) {
            return normalizeSectionLabel(bracket[1]);
        }

        var markdownHeading = line.match(/^#{1,6}\s+(.{1,80})$/);
        if (markdownHeading) {
            return normalizeSectionLabel(markdownHeading[1]);
        }

        var markdownStrong = line.match(/^\*\*(.{1,80})\*\*$/);
        if (markdownStrong) {
            return normalizeSectionLabel(markdownStrong[1]);
        }

        return '';
    }

    function normalizeSectionLabel(label) {
        var text = String(label || '').trim();
        text = text.replace(/[*_`~]/g, '');
        return text;
    }

    function normalizeNewlines(content) {
        return String(content || '').replace(/\r\n?/g, '\n');
    }

    function cleanDisplayLine(line) {
        var text = String(line || '').trim();
        if (!text) return '';

        // Drop SRT sequence/timecode lines and common LRC metadata headers.
        if (/^\d+$/.test(text)) return '';
        if (/^\d{1,2}:\d{2}:\d{2}[,.]\d{1,3}\s*-->\s*\d{1,2}:\d{2}:\d{2}[,.]\d{1,3}(?:\s+.*)?$/.test(text)) return '';
        if (/^\[(ti|ar|al|by|offset|length|re|tool):.*\]$/i.test(text)) return '';

        // Remove inline time tags while preserving lyric text.
        text = text.replace(/\[(\d{1,2}):(\d{2})(?:[.:]\d{1,3})?\]/g, '');
        text = text.replace(/\[[^\]]+\]/g, '').trim();
        return text;
    }

    function normalizeLineKey(text) {
        return String(text || '')
            .toLowerCase()
            .replace(/&/g, 'and')
            .replace(/[^a-z0-9]+/g, '');
    }

    function lineKeysMatch(a, b) {
        if (!a || !b) return false;
        if (a === b) return true;
        if (a.length >= 6 && b.length >= 6) {
            return a.indexOf(b) !== -1 || b.indexOf(a) !== -1;
        }
        return false;
    }

    function applyStructureHints(entries, structureContent) {
        if (!entries || entries.length === 0 || !structureContent) return;

        var structureItems = parseStructure(structureContent);
        if (!structureItems || structureItems.length === 0) return;

        var refs = [];
        for (var i = 0; i < entries.length; i++) {
            for (var j = 0; j < entries[i].lines.length; j++) {
                refs.push({
                    entryIndex: i,
                    key: normalizeLineKey(entries[i].lines[j])
                });
            }
        }
        if (refs.length === 0) return;

        var pointer = 0;
        for (var s = 0; s < structureItems.length && pointer < refs.length; s++) {
            var item = structureItems[s];
            var key = normalizeLineKey(item.text);
            var found = -1;

            for (var r = pointer; r < refs.length; r++) {
                if (lineKeysMatch(key, refs[r].key)) {
                    found = r;
                    break;
                }
            }

            if (found === -1) {
                // Fallback: map by order when text matching is imperfect.
                found = pointer;
            }

            var entry = entries[refs[found].entryIndex];
            if (!entry) continue;

            if (item.breakBefore) {
                entry.breakBefore = true;
            }
            if (item.sectionLabel && !entry.sectionLabel) {
                entry.sectionLabel = item.sectionLabel;
            }
            pointer = found + 1;
        }
    }

    function renderLRC(content, structureContent) {
        lrcData = parseLRC(content);
        if (!lrcData || lrcData.length === 0) {
            lrcData = parseSRT(content);
        }
        if (!lrcData || lrcData.length === 0) {
            // Fallback so listeners still see text even if timestamps are malformed.
            renderPlainText(content);
            currentFormat = 'text';
            return;
        }

        applyStructureHints(lrcData, structureContent);

        lyricsEl.innerHTML = '';

        lrcGroups = [];
        for (var i = 0; i < lrcData.length; i++) {
            var entry = lrcData[i];
            var group = document.createElement('div');
            group.className = 'line-group';
            if (entry.breakBefore && i > 0) {
                group.classList.add('section-break');
            }
            group.dataset.index = i;
            group.dataset.time = String(entry.time || 0);
            group.classList.add('timed');
            group.tabIndex = 0;
            group.setAttribute('role', 'button');
            group.setAttribute('aria-label', 'Seek lyrics to ' + formatTimeLabel(entry.time));
            group.addEventListener('click', onLineGroupActivate);
            group.addEventListener('keydown', onLineGroupKeydown);

            if (entry.sectionLabel) {
                var label = document.createElement('div');
                label.className = 'section-label';
                label.textContent = entry.sectionLabel;
                group.appendChild(label);
            }

            for (var j = 0; j < entry.lines.length; j++) {
                var line = document.createElement('div');
                line.className = 'line';
                line.textContent = entry.lines[j];
                group.appendChild(line);
            }

            lyricsEl.appendChild(group);
            lrcGroups.push(group);
        }

        // Do not highlight until playback reaches the first timestamp.
        activeIndex = -1;

        if (container) {
            container.scrollTop = 0;
        }
    }

    function renderPlainText(content) {
        lyricsEl.innerHTML = '';
        var lines = normalizeNewlines(content).split('\n');
        var group = document.createElement('div');
        group.className = 'line-group';
        var hasAny = false;
        var groupHasLines = false;

        for (var i = 0; i < lines.length; i++) {
            var raw = lines[i];
            if (!raw.trim()) {
                if (groupHasLines) {
                    lyricsEl.appendChild(group);
                    group = document.createElement('div');
                    group.className = 'line-group section-break';
                    groupHasLines = false;
                }
                continue;
            }

            var cleaned = cleanDisplayLine(raw);
            if (!cleaned) continue;

            var line = document.createElement('div');
            line.className = 'line';
            line.textContent = cleaned;
            group.appendChild(line);
            groupHasLines = true;
            hasAny = true;
        }

        if (groupHasLines) {
            lyricsEl.appendChild(group);
        }

        if (!hasAny) {
            lyricsEl.innerHTML = '<div class="no-lyrics"></div>';
        }
    }

    function renderMarkdown(content) {
        lyricsEl.innerHTML = '<div class="markdown-content">' + content + '</div>';
    }

    function update(currentTime) {
        if (currentFormat !== 'lrc' || !lrcData || lrcData.length === 0) return;

        var adjustedTime = Math.max(0, (currentTime || 0) + HIGHLIGHT_LEAD_SECONDS);
        var newIndex = findActiveIndex(adjustedTime);
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
        group.classList.toggle('active-group', isActive);
        var lines = group.querySelectorAll('.line');
        for (var i = 0; i < lines.length; i++) {
            lines[i].classList.toggle('active', isActive);
        }
    }

    function findActiveIndex(currentTime) {
        if (!lrcData || lrcData.length === 0) return -1;
        if (currentTime < lrcData[0].time) return -1;

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

    function onLineGroupActivate(e) {
        var group = e.currentTarget;
        if (!group || !group.dataset) return;
        var time = parseFloat(group.dataset.time);
        if (isNaN(time) || time < 0) return;

        if (typeof window.AcetatePlayer !== 'undefined' && typeof window.AcetatePlayer.seekTo === 'function') {
            window.AcetatePlayer.seekTo(time, true);
        }
    }

    function onLineGroupKeydown(e) {
        if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            onLineGroupActivate(e);
        }
    }

    function toggleVisibility() {
        setVisible(!lyricsVisible);
    }

    function setVisible(visible) {
        lyricsVisible = !!visible;
        if (!container) return;

        container.classList.toggle('hidden-by-shortcut', !lyricsVisible);
        container.setAttribute('aria-hidden', lyricsVisible ? 'false' : 'true');
        var player = document.getElementById('player');
        if (player) {
            player.classList.toggle('lyrics-hidden', !lyricsVisible);
        }
    }

    function formatTimeLabel(seconds) {
        var total = Math.max(0, Math.floor(seconds || 0));
        var mins = Math.floor(total / 60);
        var secs = total % 60;
        return mins + ':' + (secs < 10 ? '0' : '') + secs;
    }

    function encodePathSegment(value) {
        return encodeURIComponent(value).replace(/[!'()*]/g, function (ch) {
            return '%' + ch.charCodeAt(0).toString(16).toUpperCase();
        });
    }

    document.addEventListener('DOMContentLoaded', init);
})();
