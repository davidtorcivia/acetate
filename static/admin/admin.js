// Acetate Admin
(function () {
    'use strict';

    var loginPanel, setupPanel, dashboard, loginForm, setupForm, usernameInput, passwordInput, loginError, passwordResetBanner;
    var trackMetaByStem = {};
    var adminUsers = [];
    var currentAdminUser = '';
    var heatmapTooltipEl = null;
    var heatmapTooltipTarget = null;

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
        document.getElementById('admin-user-create-form').addEventListener('submit', handleCreateAdminUser);
        document.getElementById('cover-form').addEventListener('submit', handleCoverUpload);
        document.getElementById('btn-save-tracks').addEventListener('click', handleSaveTracks);
        document.getElementById('admin-users-list').addEventListener('click', handleAdminUserAction);

        setupHeatmapTooltip();
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
                }
                return parseErrorResponse(r).then(function (msg) {
                    loginError.textContent = msg || 'Invalid credentials';
                    loginError.classList.remove('hidden');
                    return null;
                });
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
        hideHeatmapTooltip();
        usernameInput.value = '';
        passwordInput.value = '';
        setPasswordResetMode(false);
        usernameInput.focus();
    }

    function showSetup() {
        setupPanel.classList.remove('hidden');
        loginPanel.classList.add('hidden');
        dashboard.classList.add('hidden');
        hideHeatmapTooltip();
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
                loadTracks().then(function () {
                    loadAdminUsers();
                    loadAnalytics();
                });
            } else {
                clearAnalyticsTables();
                document.getElementById('track-list').innerHTML = '';
                document.getElementById('admin-users-list').innerHTML = '';
            }
        });
    }

    function setPasswordResetMode(enabled) {
        var gatedSections = ['section-password', 'section-cover', 'section-tracks', 'section-analytics', 'section-admin-users'];
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

    function setupHeatmapTooltip() {
        if (heatmapTooltipEl) return;

        heatmapTooltipEl = document.createElement('div');
        heatmapTooltipEl.id = 'heatmap-tooltip';
        heatmapTooltipEl.className = 'heatmap-tooltip hidden';
        document.body.appendChild(heatmapTooltipEl);

        document.addEventListener('pointerover', onHeatmapPointerOver);
        document.addEventListener('pointermove', onHeatmapPointerMove);
        document.addEventListener('pointerout', onHeatmapPointerOut);
        document.addEventListener('scroll', hideHeatmapTooltip, true);
    }

    function findHeatmapBin(target) {
        if (!target || !target.closest) return null;
        return target.closest('.heatmap-bin[data-tooltip]');
    }

    function onHeatmapPointerOver(e) {
        var bin = findHeatmapBin(e.target);
        if (!bin) return;

        var relatedBin = findHeatmapBin(e.relatedTarget);
        if (bin === relatedBin) return;

        showHeatmapTooltip(bin, e.clientX, e.clientY);
    }

    function onHeatmapPointerMove(e) {
        if (!heatmapTooltipTarget || !heatmapTooltipEl) return;
        positionHeatmapTooltip(e.clientX, e.clientY);
    }

    function onHeatmapPointerOut(e) {
        if (!heatmapTooltipTarget) return;

        var toBin = findHeatmapBin(e.relatedTarget);
        if (toBin && toBin !== heatmapTooltipTarget) {
            showHeatmapTooltip(toBin, e.clientX, e.clientY);
            return;
        }
        if (toBin === heatmapTooltipTarget) {
            return;
        }

        hideHeatmapTooltip();
    }

    function showHeatmapTooltip(bin, x, y) {
        if (!heatmapTooltipEl || !bin) return;

        var text = bin.getAttribute('data-tooltip') || '';
        if (!text) return;

        heatmapTooltipTarget = bin;
        heatmapTooltipEl.textContent = text;
        heatmapTooltipEl.classList.remove('hidden');
        positionHeatmapTooltip(x, y);
    }

    function positionHeatmapTooltip(x, y) {
        if (!heatmapTooltipEl || heatmapTooltipEl.classList.contains('hidden')) return;

        var viewportPad = 10;
        var offset = 14;
        var rect = heatmapTooltipEl.getBoundingClientRect();

        var left = x + offset;
        var top = y + offset;

        if (left+rect.width+viewportPad > window.innerWidth) {
            left = window.innerWidth - rect.width - viewportPad;
        }
        if (left < viewportPad) {
            left = viewportPad;
        }

        if (top+rect.height+viewportPad > window.innerHeight) {
            top = y - rect.height - offset;
        }
        if (top < viewportPad) {
            top = viewportPad;
        }

        heatmapTooltipEl.style.left = left + 'px';
        heatmapTooltipEl.style.top = top + 'px';
    }

    function hideHeatmapTooltip() {
        heatmapTooltipTarget = null;
        if (!heatmapTooltipEl) return;
        heatmapTooltipEl.classList.add('hidden');
    }

    function setStatus(el, message, type) {
        if (!el) return;
        if (!message) {
            el.textContent = '';
            el.className = 'status hidden';
            return;
        }
        el.textContent = message;
        el.className = 'status ' + (type === 'error' ? 'error' : 'success');
    }

    function parseErrorResponse(response) {
        return response.text().then(function (text) {
            if (!text) return '';
            try {
                var payload = JSON.parse(text);
                if (payload && typeof payload.error === 'string') {
                    return payload.error;
                }
            } catch (e) {
                // Ignore JSON parse errors and fall through to raw text.
            }
            return text;
        });
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
                currentAdminUser = data.admin_user || '';
                document.getElementById('cfg-admin-user').textContent = currentAdminUser || '(unknown)';
                document.getElementById('cfg-password').textContent = data.password_set
                    ? (data.password || '(empty)')
                    : 'Not set';
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
                if (r.ok) {
                    setStatus(status, 'Listening password updated', 'success');
                    document.getElementById('new-password').value = '';
                    loadConfig();
                    return;
                }
                return parseErrorResponse(r).then(function (msg) {
                    setStatus(status, msg || 'Failed to update listening password', 'error');
                });
            })
            .catch(function () {
                setStatus(status, 'Failed to update listening password', 'error');
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
                if (r.ok) {
                    setStatus(status, 'Admin password updated. Please log in again.', 'success');
                    document.getElementById('current-admin-password').value = '';
                    document.getElementById('new-admin-password').value = '';
                    setTimeout(showLogin, 500);
                    return;
                }
                return parseErrorResponse(r).then(function (msg) {
                    setStatus(status, msg || 'Failed to update admin password', 'error');
                });
            })
            .catch(function () {
                setStatus(status, 'Failed to update admin password', 'error');
            });
    }

    // --- Admin users ---
    function loadAdminUsers() {
        var list = document.getElementById('admin-users-list');
        var status = document.getElementById('admin-users-status');

        list.innerHTML = '<div class="empty-state">Loading admin users...</div>';

        return fetch('/admin/api/admin-users', { credentials: 'same-origin' })
            .then(function (r) {
                if (!r.ok) {
                    return parseErrorResponse(r).then(function (msg) {
                        throw new Error(msg || 'Failed to load admin users');
                    });
                }
                return r.json();
            })
            .then(function (payload) {
                adminUsers = payload && payload.users ? payload.users : [];
                renderAdminUsers(adminUsers);
                setStatus(status, '', 'success');
            })
            .catch(function (err) {
                adminUsers = [];
                list.innerHTML = '<div class="empty-state">Unable to load admin users.</div>';
                setStatus(status, err.message || 'Unable to load admin users', 'error');
            });
    }

    function renderAdminUsers(users) {
        var list = document.getElementById('admin-users-list');
        if (!users || !users.length) {
            list.innerHTML = '<div class="empty-state">No admin users found.</div>';
            return;
        }

        var sorted = users.slice().sort(function (a, b) {
            if (!!a.is_founder !== !!b.is_founder) {
                return a.is_founder ? -1 : 1;
            }
            return String(a.username || '').localeCompare(String(b.username || ''));
        });

        var rows = sorted.map(function (u) {
            var username = String(u.username || '');
            var isSelf = currentAdminUser && username.toLowerCase() === currentAdminUser.toLowerCase();
            var disableActive = !!u.is_founder || !!isSelf;
            var badges = '';
            if (u.is_founder) {
                badges += '<span class="admin-badge founder">Original</span>';
            }
            if (isSelf) {
                badges += '<span class="admin-badge self">You</span>';
            }
            if (!u.is_active) {
                badges += '<span class="admin-badge inactive">Inactive</span>';
            }

            var disableHint = '';
            if (u.is_founder) {
                disableHint = 'Original admin cannot be deactivated';
            } else if (isSelf) {
                disableHint = 'You cannot deactivate your own account';
            }

            return '' +
                '<tr class="admin-user-row ' + (u.is_active ? '' : 'is-inactive') + '" data-user-id="' + Number(u.id) + '">' +
                '<td>' +
                '<input type="text" class="admin-user-username" value="' + escapeAttr(username) + '" autocomplete="off">' +
                '</td>' +
                '<td>' +
                '<div class="admin-badges">' + (badges || '<span class="admin-badge standard">Standard</span>') + '</div>' +
                (disableHint ? '<div class="inline-note">' + escapeHtml(disableHint) + '</div>' : '') +
                '</td>' +
                '<td>' +
                '<label class="switch-label">' +
                '<input type="checkbox" class="admin-user-active" ' + (u.is_active ? 'checked' : '') + (disableActive ? ' disabled' : '') + '>' +
                '<span>Active</span>' +
                '</label>' +
                '</td>' +
                '<td>' +
                '<label class="switch-label">' +
                '<input type="checkbox" class="admin-user-reset" ' + (u.require_password_reset ? 'checked' : '') + '>' +
                '<span>Force reset</span>' +
                '</label>' +
                '</td>' +
                '<td>' + escapeHtml(formatDateTime(u.last_login_at) || 'Never') + '</td>' +
                '<td>' +
                '<button type="button" class="btn-small admin-user-save" data-user-id="' + Number(u.id) + '">Save</button>' +
                '</td>' +
                '</tr>';
        }).join('');

        list.innerHTML = '' +
            '<div class="admin-users-table-wrap">' +
            '<table class="data-table admin-users-table">' +
            '<thead>' +
            '<tr>' +
            '<th>Username</th>' +
            '<th>Role</th>' +
            '<th>Status</th>' +
            '<th>Reset Policy</th>' +
            '<th>Last Login</th>' +
            '<th>Action</th>' +
            '</tr>' +
            '</thead>' +
            '<tbody>' + rows + '</tbody>' +
            '</table>' +
            '</div>';
    }

    function handleCreateAdminUser(e) {
        e.preventDefault();

        var usernameEl = document.getElementById('new-admin-username');
        var passwordEl = document.getElementById('new-admin-user-password');
        var forceResetEl = document.getElementById('new-admin-force-reset');
        var status = document.getElementById('admin-users-status');
        var submitBtn = e.target.querySelector('button[type="submit"]');

        var username = usernameEl.value.trim();
        var password = passwordEl.value;
        var requirePasswordReset = !!forceResetEl.checked;

        if (!username || !password) {
            setStatus(status, 'Username and temporary password are required', 'error');
            return;
        }

        submitBtn.disabled = true;

        fetch('/admin/api/admin-users', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            credentials: 'same-origin',
            body: JSON.stringify({
                username: username,
                password: password,
                require_password_reset: requirePasswordReset
            })
        })
            .then(function (r) {
                if (r.ok) {
                    return r.json().then(function () {
                        usernameEl.value = '';
                        passwordEl.value = '';
                        forceResetEl.checked = true;
                        setStatus(status, 'Admin user created', 'success');
                        return loadAdminUsers();
                    });
                }
                return parseErrorResponse(r).then(function (msg) {
                    throw new Error(msg || 'Failed to create admin user');
                });
            })
            .catch(function (err) {
                setStatus(status, err.message || 'Failed to create admin user', 'error');
            })
            .finally(function () {
                submitBtn.disabled = false;
            });
    }

    function handleAdminUserAction(e) {
        var target = e.target;
        if (!target || !target.classList.contains('admin-user-save')) {
            return;
        }

        var row = target.closest('tr.admin-user-row');
        if (!row) return;

        var userID = Number(row.getAttribute('data-user-id') || target.getAttribute('data-user-id') || 0);
        var usernameEl = row.querySelector('.admin-user-username');
        var activeEl = row.querySelector('.admin-user-active');
        var resetEl = row.querySelector('.admin-user-reset');
        var status = document.getElementById('admin-users-status');

        var username = usernameEl ? usernameEl.value.trim() : '';
        var isActive = activeEl ? !!activeEl.checked : false;
        var requireReset = resetEl ? !!resetEl.checked : false;

        if (!userID || !username) {
            setStatus(status, 'Username is required', 'error');
            return;
        }

        target.disabled = true;

        fetch('/admin/api/admin-users/' + encodeURIComponent(String(userID)), {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            credentials: 'same-origin',
            body: JSON.stringify({
                username: username,
                is_active: isActive,
                require_password_reset: requireReset
            })
        })
            .then(function (r) {
                if (r.ok) {
                    setStatus(status, 'Admin user updated', 'success');
                    return loadConfig().then(function (needsReset) {
                        if (!needsReset) {
                            return loadAdminUsers();
                        }
                    });
                }
                return parseErrorResponse(r).then(function (msg) {
                    throw new Error(msg || 'Failed to update admin user');
                });
            })
            .catch(function (err) {
                setStatus(status, err.message || 'Failed to update admin user', 'error');
            })
            .finally(function () {
                target.disabled = false;
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
                if (r.ok) {
                    setStatus(status, 'Cover uploaded', 'success');
                    fileInput.value = '';
                    return;
                }
                return parseErrorResponse(r).then(function (msg) {
                    setStatus(status, msg || 'Upload failed', 'error');
                });
            })
            .catch(function () {
                setStatus(status, 'Upload failed', 'error');
            });
    }

    // --- Tracks ---
    var currentTracks = [];

    function loadTracks() {
        trackMetaByStem = {};
        return fetch('/admin/api/tracks', { credentials: 'same-origin' })
            .then(function (r) {
                if (!r.ok) {
                    throw new Error('tracks');
                }
                return r.json();
            })
            .then(function (tracks) {
                currentTracks = tracks;
                tracks.forEach(function (track) {
                    trackMetaByStem[track.stem] = {
                        title: track.title || '',
                        duration: Number(track.duration || 0)
                    };
                });
                renderTrackList(tracks);
                // Also load title/artist into form.
                return fetch('/admin/api/config', { credentials: 'same-origin' })
                    .then(function (r) {
                        if (!r.ok) {
                            throw new Error('config');
                        }
                        return r.json();
                    })
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
                '<input type="text" class="track-title-input" value="' + escapeAttr(track.title) + '" placeholder="Title">' +
                '<input type="text" class="track-display-idx" value="' + escapeAttr(track.display_index || '') + '" placeholder="#">';

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
                if (r.ok) {
                    setStatus(status, 'Saved', 'success');
                    loadConfig();
                    return;
                }
                return parseErrorResponse(r).then(function (msg) {
                    setStatus(status, msg || 'Save failed', 'error');
                });
            })
            .catch(function () {
                setStatus(status, 'Save failed', 'error');
            });
    }

    // --- Analytics ---
    function loadAnalytics() {
        fetch('/admin/api/analytics', { credentials: 'same-origin' })
            .then(function (r) {
                if (!r.ok) {
                    throw new Error('analytics');
                }
                return r.json();
            })
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
            var meta = trackMetaByStem[t.stem] || {};
            var displayTitle = meta.title || t.stem;
            var subline = (meta.title && meta.title !== t.stem)
                ? '<div class="stem-subline">' + escapeHtml(t.stem) + '</div>'
                : '';

            var tr = document.createElement('tr');
            tr.innerHTML =
                '<td><div class="track-name-cell">' + escapeHtml(displayTitle) + subline + '</div></td>' +
                '<td>' + Number(t.total_plays || 0) + '</td>' +
                '<td>' + Number(t.unique_sessions || 0) + '</td>' +
                '<td>' + Number(t.completions || 0) + '</td>' +
                '<td>' + (Number(t.completion_rate || 0) * 100).toFixed(0) + '%</td>' +
                '<td>' + renderHeatmap(heatmaps && heatmaps[t.stem], t.stem) + '</td>';
            tbody.appendChild(tr);
        });
    }

    function renderHeatmap(bins, stem) {
        if (!bins || !bins.length) return '-';

        var maxCount = 0;
        var totalCount = 0;
        bins.forEach(function (b) {
            var c = Number(b.count || 0);
            totalCount += c;
            if (c > maxCount) maxCount = c;
        });

        var meta = trackMetaByStem[stem] || {};
        var duration = Number(meta.duration || 0);

        if (maxCount === 0) {
            return '<span class="heatmap heatmap-empty" title="No dropout events for this track">' + bins.map(function () {
                return '<span class="heatmap-bin heatmap-level-0"></span>';
            }).join('') + '</span>';
        }

        return '<span class="heatmap">' + bins.map(function (b) {
            var count = Number(b.count || 0);
            var intensity = count / maxCount;
            var level = Math.max(1, Math.min(5, Math.ceil(intensity * 5)));
            var tooltip = buildHeatmapTooltip(stem, b, count, totalCount, duration);
            return '<span class="heatmap-bin heatmap-level-' + level + '" data-tooltip="' + escapeAttr(tooltip) + '" aria-label="' + escapeAttr(tooltip) + '"></span>';
        }).join('') + '</span>';
    }

    function buildHeatmapTooltip(stem, bin, count, totalCount, durationSeconds) {
        var meta = trackMetaByStem[stem] || {};
        var title = meta.title || stem;
        var startPct = Math.round(Number(bin.bin_start || 0) * 100);
        var endPct = Math.round(Number(bin.bin_end || 0) * 100);

        var rangeLabel;
        if (durationSeconds > 0) {
            var startSec = Number(bin.bin_start || 0) * durationSeconds;
            var endSec = Number(bin.bin_end || 0) * durationSeconds;
            rangeLabel = 'Range: ' + formatDuration(startSec) + ' - ' + formatDuration(endSec) + ' (' + startPct + '%-' + endPct + '%)';
        } else {
            rangeLabel = 'Range: ' + startPct + '%-' + endPct + '% of track';
        }

        var share = totalCount > 0 ? Math.round((count / totalCount) * 100) : 0;
        var countLabel = 'Dropouts: ' + count + (totalCount > 0 ? ' (' + share + '% of track dropouts)' : '');

        return title + '\n' + rangeLabel + '\n' + countLabel;
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
                '<td>' + Number(s.tracks_heard || 0) + '</td>' +
                '<td><code>' + escapeHtml(s.ip_hash || '') + '</code></td>';
            tbody.appendChild(tr);
        });
    }

    function formatDuration(secondsRaw) {
        var total = Math.max(0, Math.round(Number(secondsRaw) || 0));
        var hours = Math.floor(total / 3600);
        var minutes = Math.floor((total % 3600) / 60);
        var seconds = total % 60;

        if (hours > 0) {
            return hours + ':' + pad2(minutes) + ':' + pad2(seconds);
        }
        return minutes + ':' + pad2(seconds);
    }

    function pad2(n) {
        n = Number(n) || 0;
        return n < 10 ? '0' + n : String(n);
    }

    function formatDateTime(value) {
        if (!value) return '';
        var parsed = new Date(value);
        if (isNaN(parsed.getTime())) {
            return String(value);
        }
        return parsed.toLocaleString();
    }

    function escapeHtml(s) {
        if (s === null || s === undefined) return '';
        var div = document.createElement('div');
        div.textContent = String(s);
        return div.innerHTML;
    }

    function escapeAttr(s) {
        return escapeHtml(s)
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#39;')
            .replace(/\n/g, '&#10;');
    }

    document.addEventListener('DOMContentLoaded', init);
})();
