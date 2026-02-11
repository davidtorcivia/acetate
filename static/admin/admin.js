// Acetate Admin
(function () {
    'use strict';

    var loginPanel, setupPanel, dashboard, loginForm, setupForm, usernameInput, passwordInput, loginError, passwordResetBanner;

    function init() {
        loginPanel = document.getElementById('admin-login');
        setupPanel = document.getElementById('admin-setup');
        dashboard = document.getElementById('admin-dashboard');
        loginForm = document.getElementById('login-form');
        setupForm = document.getElementById('setup-form');
        usernameInput = document.getElementById('admin-username');
        passwordInput = document.getElementById('admin-password');
        loginError = document.getElementById('login-error');
        passwordResetBanner = document.getElementById('password-reset-banner');

        loginForm.addEventListener('submit', handleLogin);
        setupForm.addEventListener('submit', handleSetup);
        document.getElementById('btn-logout').addEventListener('click', handleLogout);
        document.getElementById('password-form').addEventListener('submit', handlePasswordUpdate);
        document.getElementById('admin-password-form').addEventListener('submit', handleAdminPasswordUpdate);
        document.getElementById('cover-form').addEventListener('submit', handleCoverUpload);
        document.getElementById('btn-save-tracks').addEventListener('click', handleSaveTracks);

        checkSetupStatus();
    }

    function checkSetupStatus() {
        fetch('/admin/api/setup/status', { credentials: 'same-origin' })
            .then(function (r) {
                if (!r.ok) throw new Error('status');
                return r.json();
            })
            .then(function (data) {
                if (data && data.needs_setup) {
                    showSetup();
                } else {
                    checkSession();
                }
            })
            .catch(function () {
                showLogin();
            });
    }

    function checkSession() {
        fetch('/admin/api/config', { credentials: 'same-origin' })
            .then(function (r) {
                if (r.ok) {
                    showDashboard();
                } else {
                    showLogin();
                }
            })
            .catch(function () { showLogin(); });
    }

    function handleSetup(e) {
        e.preventDefault();
        var username = document.getElementById('setup-username').value.trim();
        var password = document.getElementById('setup-password').value;
        var confirm = document.getElementById('setup-password-confirm').value;
        var setupError = document.getElementById('setup-error');

        if (!username || !password || !confirm) return;
        setupError.classList.add('hidden');

        if (password !== confirm) {
            setupError.textContent = 'Passwords do not match';
            setupError.classList.remove('hidden');
            return;
        }

        fetch('/admin/api/setup', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            credentials: 'same-origin',
            body: JSON.stringify({ username: username, password: password })
        })
            .then(function (r) {
                if (r.ok) {
                    showDashboard();
                } else if (r.status === 409) {
                    checkSession();
                } else if (r.status === 400) {
                    setupError.textContent = 'Invalid username or password policy not met';
                    setupError.classList.remove('hidden');
                } else {
                    setupError.textContent = 'Setup failed';
                    setupError.classList.remove('hidden');
                }
            })
            .catch(function () {
                setupError.textContent = 'Connection error';
                setupError.classList.remove('hidden');
            });
    }

    function handleLogin(e) {
        e.preventDefault();
        var username = usernameInput.value.trim();
        var password = passwordInput.value;
        if (!username || !password) return;

        loginError.classList.add('hidden');

        fetch('/admin/api/auth', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            credentials: 'same-origin',
            body: JSON.stringify({ username: username, password: password })
        })
            .then(function (r) {
                if (r.ok) {
                    return r.json();
                } else {
                    loginError.textContent = 'Invalid credentials';
                    loginError.classList.remove('hidden');
                    return null;
                }
            })
            .then(function (payload) {
                if (!payload) return;
                showDashboard(!!payload.password_reset_required);
            })
            .catch(function () {
                loginError.textContent = 'Connection error';
                loginError.classList.remove('hidden');
            });
    }

    function handleLogout() {
        fetch('/admin/api/auth', {
            method: 'DELETE',
            credentials: 'same-origin'
        }).then(function () { checkSetupStatus(); });
    }

    function showLogin() {
        loginPanel.classList.remove('hidden');
        setupPanel.classList.add('hidden');
        dashboard.classList.add('hidden');
        usernameInput.value = '';
        passwordInput.value = '';
        setPasswordResetMode(false);
        usernameInput.focus();
    }

    function showSetup() {
        setupPanel.classList.remove('hidden');
        loginPanel.classList.add('hidden');
        dashboard.classList.add('hidden');
        document.getElementById('setup-username').focus();
    }

    function showDashboard(passwordResetRequired) {
        setupPanel.classList.add('hidden');
        loginPanel.classList.add('hidden');
        dashboard.classList.remove('hidden');
        if (passwordResetRequired) {
            setPasswordResetMode(true);
        }
        loadConfig().then(function (needsReset) {
            if (!needsReset) {
                loadTracks();
                loadAnalytics();
            } else {
                clearAnalyticsTables();
                document.getElementById('track-list').innerHTML = '';
            }
        });
    }

    function setPasswordResetMode(enabled) {
        var gatedSections = ['section-password', 'section-cover', 'section-tracks', 'section-analytics'];
        gatedSections.forEach(function (id) {
            var el = document.getElementById(id);
            if (!el) return;
            el.classList.toggle('hidden', !!enabled);
        });
        if (passwordResetBanner) {
            passwordResetBanner.classList.toggle('hidden', !enabled);
        }
    }

    function clearAnalyticsTables() {
        var trackStats = document.getElementById('track-stats-body');
        var sessions = document.getElementById('sessions-body');
        if (trackStats) {
            trackStats.innerHTML = '<tr><td colspan="6">Password reset required</td></tr>';
        }
        if (sessions) {
            sessions.innerHTML = '<tr><td colspan="4">Password reset required</td></tr>';
        }
    }

    // --- Config ---
    function loadConfig() {
        return fetch('/admin/api/config', { credentials: 'same-origin' })
            .then(function (r) {
                if (!r.ok) throw new Error('unauthorized');
                return r.json();
            })
            .then(function (data) {
                document.getElementById('cfg-title').textContent = data.title || '(not set)';
                document.getElementById('cfg-artist').textContent = data.artist || '(not set)';
                document.getElementById('cfg-admin-user').textContent = data.admin_user || '(unknown)';
                document.getElementById('cfg-password').textContent = data.password_set
                    ? data.password_hash
                    : '(not set)';
                document.getElementById('cfg-tracks').textContent = data.track_count + ' tracks';
                var needsReset = !!data.password_reset_required;
                setPasswordResetMode(needsReset);
                return needsReset;
            })
            .catch(function () {
                showLogin();
                return true;
            });
    }

    // --- Password ---
    function handlePasswordUpdate(e) {
        e.preventDefault();
        var pass = document.getElementById('new-password').value;
        var status = document.getElementById('password-status');

        if (!pass) return;

        fetch('/admin/api/password', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            credentials: 'same-origin',
            body: JSON.stringify({ passphrase: pass })
        })
            .then(function (r) {
                status.classList.remove('hidden');
                if (r.ok) {
                    status.textContent = 'Password updated';
                    status.className = 'status success';
                    document.getElementById('new-password').value = '';
                    loadConfig();
                } else {
                    status.textContent = 'Failed to update';
                    status.className = 'status error';
                }
            });
    }

    function handleAdminPasswordUpdate(e) {
        e.preventDefault();
        var currentPass = document.getElementById('current-admin-password').value;
        var newPass = document.getElementById('new-admin-password').value;
        var status = document.getElementById('admin-password-status');

        if (!currentPass || !newPass) return;

        fetch('/admin/api/admin-password', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            credentials: 'same-origin',
            body: JSON.stringify({
                current_password: currentPass,
                new_password: newPass
            })
        })
            .then(function (r) {
                status.classList.remove('hidden');
                if (r.ok) {
                    status.textContent = 'Admin password updated. Please log in again.';
                    status.className = 'status success';
                    document.getElementById('current-admin-password').value = '';
                    document.getElementById('new-admin-password').value = '';
                    setTimeout(showLogin, 500);
                } else {
                    status.textContent = 'Failed to update admin password';
                    status.className = 'status error';
                }
            });
    }

    // --- Cover ---
    function handleCoverUpload(e) {
        e.preventDefault();
        var fileInput = document.getElementById('cover-file');
        var status = document.getElementById('cover-status');

        if (!fileInput.files.length) return;

        var formData = new FormData();
        formData.append('cover', fileInput.files[0]);

        fetch('/admin/api/cover', {
            method: 'POST',
            credentials: 'same-origin',
            body: formData
        })
            .then(function (r) {
                status.classList.remove('hidden');
                if (r.ok) {
                    status.textContent = 'Cover uploaded';
                    status.className = 'status success';
                    fileInput.value = '';
                } else {
                    status.textContent = 'Upload failed';
                    status.className = 'status error';
                }
            });
    }

    // --- Tracks ---
    var currentTracks = [];

    function loadTracks() {
        fetch('/admin/api/tracks', { credentials: 'same-origin' })
            .then(function (r) { return r.json(); })
            .then(function (tracks) {
                currentTracks = tracks;
                renderTrackList(tracks);
                // Also load title/artist into form
                fetch('/admin/api/config', { credentials: 'same-origin' })
                    .then(function (r) { return r.json(); })
                    .then(function (cfg) {
                        document.getElementById('album-title').value = cfg.title || '';
                        document.getElementById('album-artist').value = cfg.artist || '';
                    });
            });
    }

    function renderTrackList(tracks) {
        var container = document.getElementById('track-list');
        container.innerHTML = '';

        tracks.forEach(function (track, index) {
            var item = document.createElement('div');
            item.className = 'track-item';
            item.draggable = true;
            item.dataset.index = index;

            item.innerHTML =
                '<span class="drag-handle">&#x2261;</span>' +
                '<span class="track-stem">' + escapeHtml(track.stem) + '</span>' +
                '<input type="text" class="track-title-input" value="' + escapeHtml(track.title) + '" placeholder="Title">' +
                '<input type="text" class="track-display-idx" value="' + escapeHtml(track.display_index || '') + '" placeholder="#">';

            // Drag events
            item.addEventListener('dragstart', onDragStart);
            item.addEventListener('dragover', onDragOver);
            item.addEventListener('drop', onDrop);
            item.addEventListener('dragend', onDragEnd);

            container.appendChild(item);
        });
    }

    var draggedItem = null;

    function onDragStart(e) {
        draggedItem = this;
        this.classList.add('dragging');
        e.dataTransfer.effectAllowed = 'move';
    }

    function onDragOver(e) {
        e.preventDefault();
        e.dataTransfer.dropEffect = 'move';
    }

    function onDrop(e) {
        e.preventDefault();
        if (draggedItem === this) return;

        var container = document.getElementById('track-list');
        var items = Array.from(container.children);
        var fromIdx = items.indexOf(draggedItem);
        var toIdx = items.indexOf(this);

        if (fromIdx < toIdx) {
            this.parentNode.insertBefore(draggedItem, this.nextSibling);
        } else {
            this.parentNode.insertBefore(draggedItem, this);
        }

        // Reorder currentTracks array
        var moved = currentTracks.splice(fromIdx, 1)[0];
        currentTracks.splice(toIdx, 0, moved);
    }

    function onDragEnd() {
        this.classList.remove('dragging');
        draggedItem = null;
    }

    function handleSaveTracks() {
        var status = document.getElementById('tracks-status');
        var items = document.querySelectorAll('.track-item');

        var tracks = [];
        items.forEach(function (item, i) {
            var titleInput = item.querySelector('.track-title-input');
            var idxInput = item.querySelector('.track-display-idx');
            tracks.push({
                stem: currentTracks[i].stem,
                title: titleInput.value,
                display_index: idxInput.value || undefined
            });
        });

        fetch('/admin/api/tracks', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            credentials: 'same-origin',
            body: JSON.stringify({
                title: document.getElementById('album-title').value,
                artist: document.getElementById('album-artist').value,
                tracks: tracks
            })
        })
            .then(function (r) {
                status.classList.remove('hidden');
                if (r.ok) {
                    status.textContent = 'Saved';
                    status.className = 'status success';
                    loadConfig();
                } else {
                    status.textContent = 'Save failed';
                    status.className = 'status error';
                }
            });
    }

    // --- Analytics ---
    function loadAnalytics() {
        fetch('/admin/api/analytics', { credentials: 'same-origin' })
            .then(function (r) { return r.json(); })
            .then(function (data) {
                renderOverview(data.overall);
                renderTrackStats(data.tracks, data.heatmaps);
                renderSessions(data.sessions);
            })
            .catch(function () { });
    }

    function renderOverview(overall) {
        if (!overall) return;
        var container = document.getElementById('analytics-overview');
        container.innerHTML =
            '<div class="stat-card"><div class="stat-value">' + (overall.total_sessions || 0) + '</div><div class="stat-label">Sessions</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + (overall.avg_tracks_per_session || 0).toFixed(1) + '</div><div class="stat-label">Avg Tracks/Session</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + escapeHtml(overall.most_completed || '-') + '</div><div class="stat-label">Most Completed</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + escapeHtml(overall.least_completed || '-') + '</div><div class="stat-label">Least Completed</div></div>';
    }

    function renderTrackStats(tracks, heatmaps) {
        var tbody = document.getElementById('track-stats-body');
        tbody.innerHTML = '';

        if (!tracks || !tracks.length) {
            tbody.innerHTML = '<tr><td colspan="6">No data yet</td></tr>';
            return;
        }

        tracks.forEach(function (t) {
            var tr = document.createElement('tr');
            tr.innerHTML =
                '<td>' + escapeHtml(t.stem) + '</td>' +
                '<td>' + t.total_plays + '</td>' +
                '<td>' + t.unique_sessions + '</td>' +
                '<td>' + t.completions + '</td>' +
                '<td>' + (t.completion_rate * 100).toFixed(0) + '%</td>' +
                '<td>' + renderHeatmap(heatmaps && heatmaps[t.stem]) + '</td>';
            tbody.appendChild(tr);
        });
    }

    function renderHeatmap(bins) {
        if (!bins) return '-';

        var maxCount = 0;
        bins.forEach(function (b) { if (b.count > maxCount) maxCount = b.count; });
        if (maxCount === 0) {
            return '<span class="heatmap">' + bins.map(function () {
                return '<span class="heatmap-bin heatmap-level-0"></span>';
            }).join('') + '</span>';
        }

        return '<span class="heatmap">' + bins.map(function (b) {
            var intensity = b.count / maxCount;
            var level = Math.max(1, Math.min(5, Math.ceil(intensity * 5)));
            return '<span class="heatmap-bin heatmap-level-' + level + '" title="' + (b.bin_start * 100) + '-' + (b.bin_end * 100) + '%: ' + b.count + '"></span>';
        }).join('') + '</span>';
    }

    function renderSessions(sessions) {
        var tbody = document.getElementById('sessions-body');
        tbody.innerHTML = '';

        if (!sessions || !sessions.length) {
            tbody.innerHTML = '<tr><td colspan="4">No sessions yet</td></tr>';
            return;
        }

        sessions.forEach(function (s) {
            var tr = document.createElement('tr');
            tr.innerHTML =
                '<td>' + escapeHtml(s.started_at) + '</td>' +
                '<td>' + escapeHtml(s.last_seen_at) + '</td>' +
                '<td>' + s.tracks_heard + '</td>' +
                '<td><code>' + escapeHtml(s.ip_hash || '') + '</code></td>';
            tbody.appendChild(tr);
        });
    }

    function escapeHtml(s) {
        if (!s) return '';
        var div = document.createElement('div');
        div.textContent = s;
        return div.innerHTML;
    }

    document.addEventListener('DOMContentLoaded', init);
})();
