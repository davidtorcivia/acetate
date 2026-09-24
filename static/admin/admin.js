// Acetate Admin
(function () {
    'use strict';

    var loginPanel, setupPanel, dashboard, loginForm, setupForm, usernameInput, passwordInput, loginError, passwordResetBanner;
    var trackMetaByStem = {};
    var currentAdminUser = '';
    var heatmapTooltipEl = null;
    var heatmapTooltipTarget = null;
    var selectedAlbumId = null;
    var albumsCache = [];
    var albumsLoaded = false;
    var passwordsCache = null;
    var resetPending = false;
    // Bumped on every album selection; responses carrying an older value are dropped.
    var loadSeq = 0;
    var reconcileReport = null;

    var GATED_SECTIONS = ['section-albums', 'section-passwords', 'section-admin-users'];
    var DETAIL_SECTIONS = ['section-album-settings', 'section-cover', 'section-tracks', 'section-analytics'];

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
        document.getElementById('admin-password-form').addEventListener('submit', handleAdminPasswordUpdate);
        document.getElementById('admin-user-create-form').addEventListener('submit', handleCreateAdminUser);
        document.getElementById('cover-form').addEventListener('submit', handleCoverUpload);
        document.getElementById('btn-save-tracks').addEventListener('click', handleSaveTracks);
        document.getElementById('btn-reconcile-apply').addEventListener('click', handleReconcileApply);
        document.getElementById('admin-users-list').addEventListener('click', handleAdminUserAction);
        document.getElementById('albums-list').addEventListener('click', handleAlbumListClick);
        document.getElementById('passwords-list').addEventListener('click', handlePasswordListClick);
        document.getElementById('track-list').addEventListener('click', handleTrackMove);
        document.getElementById('album-create-form').addEventListener('submit', handleCreateAlbum);
        document.getElementById('password-create-form').addEventListener('submit', handleCreatePassword);

        document.getElementById('downloads-enabled-toggle').addEventListener('change', handleDownloadsToggle);

        setupHeatmapTooltip();
        checkSetupStatus();
    }

    // api sends a JSON request and resolves with the parsed body, or rejects with
    // an Error carrying the server's message and HTTP status.
    function api(method, url, body, keepSession) {
        var opts = { method: method, credentials: 'same-origin', headers: { 'Accept': 'application/json' } };
        if (body instanceof FormData) {
            opts.body = body;
        } else if (body !== undefined) {
            opts.headers['Content-Type'] = 'application/json';
            opts.body = JSON.stringify(body);
        }
        return fetch(url, opts).then(function (r) {
            return r.text().then(function (text) {
                var data = null;
                try {
                    data = text ? JSON.parse(text) : null;
                } catch (e) {
                    data = null;
                }
                if (r.ok) return data;

                var msg = data && typeof data.error === 'string' ? data.error : (text || 'Request failed (' + r.status + ')');
                // The login and change-password endpoints use 401 for wrong credentials.
                if (r.status === 401 && !keepSession) {
                    // Parallel requests all 401; only the first may reset the login form.
                    if (loginPanel.classList.contains('hidden')) {
                        showLogin(dashboard.classList.contains('hidden') ? '' : 'Your session expired. Please log in again.');
                    }
                } else if (r.status === 403 && msg === 'password reset required') {
                    setPasswordResetMode(true);
                }
                var err = new Error(msg);
                err.status = r.status;
                throw err;
            });
        }, function () {
            throw new Error('Connection error');
        });
    }

    function albumUrl(albumId) {
        return '/admin/api/albums/' + encodeURIComponent(String(albumId));
    }

    function showDashboardError(err) {
        setStatus(document.getElementById('dashboard-status'), err.message, 'error');
    }

    function handleDownloadsToggle() {
        var albumId = selectedAlbumId;
        if (!albumId) return;
        var toggle = document.getElementById('downloads-enabled-toggle');
        var status = document.getElementById('album-settings-status');
        var enabled = toggle.checked;
        toggle.disabled = true;

        api('PUT', albumUrl(albumId), { downloads_enabled: enabled })
            .then(function () {
                var album = findAlbumById(albumId);
                if (album) album.downloads_enabled = enabled;
                if (albumId === selectedAlbumId) {
                    setStatus(status, 'Downloads ' + (enabled ? 'enabled' : 'disabled'), 'success');
                }
            })
            .catch(function (err) {
                if (albumId !== selectedAlbumId) return;
                toggle.checked = !enabled;
                setStatus(status, err.message, 'error');
            })
            .finally(function () {
                toggle.disabled = false;
            });
    }

    function checkSetupStatus() {
        api('GET', '/admin/api/setup/status')
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
        loadConfig().then(showDashboard, function () { showLogin(); });
    }

    function handleSetup(e) {
        e.preventDefault();
        var passwordEl = document.getElementById('setup-password');
        var confirmEl = document.getElementById('setup-password-confirm');
        var username = document.getElementById('setup-username').value.trim();
        var password = passwordEl.value;
        var confirmPass = confirmEl.value;
        var setupError = document.getElementById('setup-error');
        var submitBtn = e.target.querySelector('button[type="submit"]');

        if (!username || !password || !confirmPass) return;
        setStatus(setupError, '');

        if (password !== confirmPass) {
            setStatus(setupError, 'Passwords do not match', 'error');
            return;
        }

        submitBtn.disabled = true;
        api('POST', '/admin/api/setup', { username: username, password: password })
            .then(function () {
                passwordEl.value = '';
                confirmEl.value = '';
                return loadConfig().then(showDashboard);
            })
            .catch(function (err) {
                if (err.status === 409) {
                    checkSession();
                    return;
                }
                setStatus(setupError, err.message, 'error');
            })
            .finally(function () {
                submitBtn.disabled = false;
            });
    }

    function handleLogin(e) {
        e.preventDefault();
        var username = usernameInput.value.trim();
        var password = passwordInput.value;
        var submitBtn = e.target.querySelector('button[type="submit"]');
        if (!username || !password) return;

        setStatus(loginError, '');
        submitBtn.disabled = true;

        api('POST', '/admin/api/auth', { username: username, password: password }, true)
            .then(function () {
                return loadConfig().then(showDashboard);
            })
            .catch(function (err) {
                setStatus(loginError, err.status === 401 ? 'Invalid username or password' : err.message, 'error');
            })
            .finally(function () {
                passwordInput.value = '';
                submitBtn.disabled = false;
            });
    }

    function handleLogout() {
        api('DELETE', '/admin/api/auth')
            .then(function () { showLogin(); })
            .catch(showDashboardError);
    }

    function showLogin(message, type) {
        loginPanel.classList.remove('hidden');
        setupPanel.classList.add('hidden');
        dashboard.classList.add('hidden');
        hideHeatmapTooltip();
        clearSelection();
        albumsCache = [];
        albumsLoaded = false;
        passwordsCache = null;
        usernameInput.value = '';
        passwordInput.value = '';
        setStatus(loginError, message || '', type || 'error');
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

    function showDashboard(needsReset) {
        setupPanel.classList.add('hidden');
        loginPanel.classList.add('hidden');
        dashboard.classList.remove('hidden');
        clearSelection();
        if (!needsReset) {
            loadAlbums().then(loadPasswords);
            loadAlbumFolders();
            loadAdminUsers();
        } else {
            document.getElementById('admin-users-list').innerHTML = '';
            document.getElementById('albums-list').innerHTML = '';
            document.getElementById('passwords-list').innerHTML = '';
        }
    }

    function setPasswordResetMode(enabled) {
        resetPending = !!enabled;
        GATED_SECTIONS.forEach(function (id) {
            document.getElementById(id).classList.toggle('hidden', resetPending);
        });
        updateDetailSections();
        passwordResetBanner.classList.toggle('hidden', !resetPending);
    }

    function updateDetailSections() {
        var show = !!selectedAlbumId && !resetPending;
        DETAIL_SECTIONS.forEach(function (id) {
            document.getElementById(id).classList.toggle('hidden', !show);
        });
    }

    function clearSelection() {
        selectedAlbumId = null;
        loadSeq++;
        updateDetailSections();
    }

    function setupHeatmapTooltip() {
        if (heatmapTooltipEl) return;

        heatmapTooltipEl = document.createElement('div');
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
        el.textContent = message || '';
        el.className = 'status' + (message ? (type === 'error' ? ' error' : ' success') : '');
    }

    // --- Config ---
    function loadConfig() {
        return api('GET', '/admin/api/config').then(function (data) {
            currentAdminUser = data.admin_user || '';
            document.getElementById('cfg-admin-user').textContent = currentAdminUser || '(unknown)';
            document.getElementById('cfg-album-count').textContent = (data.album_count || 0) + ' albums';
            document.getElementById('cfg-password-count').textContent = (data.password_count || 0) + ' passwords';
            var needsReset = !!data.password_reset_required;
            setPasswordResetMode(needsReset);
            return needsReset;
        });
    }

    function refreshConfig() {
        loadConfig().catch(showDashboardError);
    }

    // --- Albums ---
    function loadAlbumFolders() {
        var select = document.getElementById('new-album-path');

        return api('GET', '/admin/api/album-folders')
            .then(function (data) {
                var folders = data && data.folders ? data.folders : [];
                var html = '<option value="">Select album folder\u2026</option>';
                folders.forEach(function (f) {
                    html += '<option value="' + escapeAttr(f) + '">' + escapeHtml(f) + '</option>';
                });
                select.innerHTML = html;
            })
            .catch(function (err) {
                setStatus(document.getElementById('albums-status'), 'Unable to list album folders: ' + err.message, 'error');
            });
    }

    function loadAlbums() {
        var list = document.getElementById('albums-list');
        var status = document.getElementById('albums-status');

        list.innerHTML = '<div class="empty-state">Loading albums...</div>';

        return api('GET', '/admin/api/albums')
            .then(function (payload) {
                albumsCache = payload && payload.albums ? payload.albums : [];
                albumsLoaded = true;
                if (selectedAlbumId && !findAlbumById(selectedAlbumId)) {
                    clearSelection();
                }
                renderAlbumsList();
                updateAlbumLabels();
            })
            .catch(function (err) {
                albumsCache = [];
                albumsLoaded = false;
                list.innerHTML = '<div class="empty-state">Unable to load albums.</div>';
                setStatus(status, err.message, 'error');
            })
            .then(function () {
                updatePasswordAlbumCheckboxes();
                renderPasswordsList();
            });
    }

    function renderAlbumsList() {
        var list = document.getElementById('albums-list');
        if (!albumsCache.length) {
            list.innerHTML = '<div class="empty-state">No albums found. Create one below.</div>';
            return;
        }

        list.innerHTML = albumsCache.map(function (a) {
            var isSelected = selectedAlbumId === a.id;
            var title = a.title || '(untitled)';
            return '' +
                '<div class="list-row' + (isSelected ? ' is-selected' : '') + '" data-album-id="' + Number(a.id) + '">' +
                '<span class="album-row-title">' + escapeHtml(title) + '</span>' +
                '<span class="row-meta">' + escapeHtml(a.artist || '') + '</span>' +
                '<span class="row-meta album-row-count">' + Number(a.track_count || 0) + ' tracks</span>' +
                '<button type="button" class="btn-small album-select-btn" aria-label="' + escapeAttr((isSelected ? 'Selected: ' : 'Select ') + title) + '"' + (isSelected ? ' aria-pressed="true"' : '') + '>' + (isSelected ? 'Selected' : 'Select') + '</button>' +
                '<button type="button" class="btn-small album-delete-btn" aria-label="' + escapeAttr('Delete album ' + title) + '">Delete</button>' +
                '</div>';
        }).join('');
    }

    function handleAlbumListClick(e) {
        var btn = e.target.closest('button');
        var row = btn && btn.closest('[data-album-id]');
        if (!row) return;
        var albumId = Number(row.getAttribute('data-album-id'));
        if (btn.classList.contains('album-select-btn')) {
            selectAlbum(albumId);
        } else if (btn.classList.contains('album-delete-btn')) {
            handleDeleteAlbum(albumId, btn);
        }
    }

    function updateAlbumLabels() {
        var album = findAlbumById(selectedAlbumId);
        var label = album ? (album.title || 'Album #' + album.id) : '';
        ['cover-album-label', 'tracks-album-label', 'analytics-album-label', 'settings-album-label'].forEach(function (id) {
            document.getElementById(id).textContent = label ? '- ' + label : '';
        });
    }

    function selectAlbum(albumId) {
        var seq = ++loadSeq;
        selectedAlbumId = albumId;
        var album = findAlbumById(albumId);

        document.getElementById('downloads-enabled-toggle').checked = !!(album && album.downloads_enabled);
        ['album-settings-status', 'cover-status', 'tracks-status'].forEach(function (id) {
            setStatus(document.getElementById(id), '');
        });
        document.getElementById('cover-file').value = '';

        updateAlbumLabels();
        updateDetailSections();
        renderAlbumsList();

        var section = document.getElementById('section-album-settings');
        section.scrollIntoView({ behavior: 'smooth', block: 'start' });
        section.querySelector('h2').focus({ preventScroll: true });

        loadAlbumDetail(albumId, seq);
    }

    // Tracks load before analytics so the stats tables can show track titles.
    function loadAlbumDetail(albumId, seq) {
        loadTracks(albumId, seq).then(function () {
            if (seq === loadSeq) loadAnalytics(albumId, seq);
        });
    }

    function reloadSelected() {
        if (selectedAlbumId) loadAlbumDetail(selectedAlbumId, ++loadSeq);
    }

    function findAlbumById(id) {
        for (var i = 0; i < albumsCache.length; i++) {
            if (albumsCache[i].id === id) return albumsCache[i];
        }
        return null;
    }

    function handleCreateAlbum(e) {
        e.preventDefault();
        var titleEl = document.getElementById('new-album-title');
        var artistEl = document.getElementById('new-album-artist');
        var pathEl = document.getElementById('new-album-path');
        var status = document.getElementById('albums-status');
        var submitBtn = e.target.querySelector('button[type="submit"]');

        var title = titleEl.value.trim();
        var artist = artistEl.value.trim();
        var albumPath = pathEl.value.trim();

        if (!title || !albumPath) {
            setStatus(status, 'Title and album path are required', 'error');
            return;
        }

        submitBtn.disabled = true;

        api('POST', '/admin/api/albums', { title: title, artist: artist, album_path: albumPath })
            .then(function () {
                titleEl.value = '';
                artistEl.value = '';
                pathEl.selectedIndex = 0;
                setStatus(status, 'Album created', 'success');
                loadAlbums();
                loadAlbumFolders();
                refreshConfig();
            })
            .catch(function (err) {
                setStatus(status, err.message, 'error');
            })
            .finally(function () {
                submitBtn.disabled = false;
            });
    }

    function handleDeleteAlbum(albumId, btn) {
        var album = findAlbumById(albumId);
        var albumTitle = album && album.title ? album.title : 'this album';
        if (!confirm('Delete album "' + albumTitle + '"? This cannot be undone.')) return;
        var status = document.getElementById('albums-status');
        btn.disabled = true;

        api('DELETE', albumUrl(albumId))
            .then(function () {
                setStatus(status, 'Album deleted', 'success');
                if (selectedAlbumId === albumId) {
                    clearSelection();
                }
                loadAlbums();
                refreshConfig();
            })
            .catch(function (err) {
                btn.disabled = false;
                setStatus(status, err.message, 'error');
            });
    }

    // --- Passwords ---
    function loadPasswords() {
        var list = document.getElementById('passwords-list');
        var status = document.getElementById('passwords-status');

        if (passwordsCache === null) {
            list.innerHTML = '<div class="empty-state">Loading passwords...</div>';
        }

        return api('GET', '/admin/api/passwords')
            .then(function (payload) {
                passwordsCache = payload && payload.passwords ? payload.passwords : [];
                renderPasswordsList();
            })
            .catch(function (err) {
                list.innerHTML = '<div class="empty-state">Unable to load passwords.</div>';
                setStatus(status, err.message, 'error');
            });
    }

    function findPasswordById(id) {
        for (var i = 0; i < (passwordsCache || []).length; i++) {
            if (passwordsCache[i].id === id) return passwordsCache[i];
        }
        return null;
    }

    function albumCheckboxesHtml(cbClass, checkedIds) {
        return albumsCache.map(function (a) {
            var checked = checkedIds.indexOf(a.id) !== -1;
            return '<label class="checkbox-label">' +
                '<input type="checkbox" class="' + cbClass + '" data-album-id="' + Number(a.id) + '"' + (checked ? ' checked' : '') + '>' +
                escapeHtml(a.title || 'Album #' + a.id) +
                '</label>';
        }).join('');
    }

    function renderPasswordsList() {
        var list = document.getElementById('passwords-list');
        if (passwordsCache === null) return;
        if (!passwordsCache.length) {
            list.innerHTML = '<div class="empty-state">No listening passwords. Create one below.</div>';
            return;
        }

        var albumsNote = albumsLoaded ? 'No albums yet' : 'Albums unavailable; reload the page before saving';
        list.innerHTML = passwordsCache.map(function (p) {
            var label = p.label || '';
            return '' +
                '<div class="list-row password-row" data-password-id="' + Number(p.id) + '">' +
                '<input type="text" class="pw-label-input" value="' + escapeAttr(label) + '" placeholder="Label" aria-label="' + escapeAttr('Label for password ' + label) + '" required>' +
                '<input type="password" class="pw-passphrase-input" value="" placeholder="New passphrase (leave blank to keep)" aria-label="' + escapeAttr('New passphrase for ' + label) + '" autocomplete="new-password">' +
                '<div class="checkbox-grid">' +
                '<span class="muted-note">Albums:</span>' +
                (albumsCache.length ? albumCheckboxesHtml('pw-album-cb', p.album_ids || []) : '<span class="muted-note">' + albumsNote + '</span>') +
                '</div>' +
                '<button type="button" class="btn-small pw-save-btn" aria-label="' + escapeAttr('Save password ' + label) + '"' + (albumsLoaded ? '' : ' disabled') + '>Save</button>' +
                '<button type="button" class="btn-small pw-delete-btn" aria-label="' + escapeAttr('Delete password ' + label) + '">Delete</button>' +
                '</div>';
        }).join('');
    }

    function handlePasswordListClick(e) {
        var btn = e.target.closest('button');
        var row = btn && btn.closest('[data-password-id]');
        if (!row) return;
        var pwId = Number(row.getAttribute('data-password-id'));
        if (btn.classList.contains('pw-save-btn')) {
            handleUpdatePassword(pwId, row, btn);
        } else if (btn.classList.contains('pw-delete-btn')) {
            handleDeletePassword(pwId, btn);
        }
    }

    function updatePasswordAlbumCheckboxes() {
        var container = document.getElementById('password-album-checkboxes');

        if (!albumsCache.length) {
            container.innerHTML = '<span class="muted-note">' + (albumsLoaded ? 'No albums yet. Create an album first.' : 'Albums unavailable.') + '</span>';
            return;
        }

        container.innerHTML = '<span class="muted-note">Link to albums:</span>' + albumCheckboxesHtml('new-pw-album-cb', []);
    }

    function handleCreatePassword(e) {
        e.preventDefault();
        var labelEl = document.getElementById('new-password-label');
        var passphraseEl = document.getElementById('new-password-passphrase');
        var status = document.getElementById('passwords-status');
        var submitBtn = e.target.querySelector('button[type="submit"]');

        var label = labelEl.value.trim();
        var passphrase = passphraseEl.value;

        if (!label || !passphrase) {
            setStatus(status, 'Label and passphrase are required', 'error');
            return;
        }

        var albumIds = [];
        document.querySelectorAll('.new-pw-album-cb:checked').forEach(function (cb) {
            albumIds.push(Number(cb.getAttribute('data-album-id')));
        });

        if (!albumIds.length && !confirm('No albums selected. This password will not unlock anything until you link an album. Create it anyway?')) {
            return;
        }

        submitBtn.disabled = true;

        api('POST', '/admin/api/passwords', { label: label, passphrase: passphrase, album_ids: albumIds })
            .then(function () {
                labelEl.value = '';
                passphraseEl.value = '';
                document.querySelectorAll('.new-pw-album-cb').forEach(function (cb) { cb.checked = false; });
                setStatus(status, 'Password created', 'success');
                loadPasswords();
                refreshConfig();
            })
            .catch(function (err) {
                setStatus(status, err.message, 'error');
            })
            .finally(function () {
                submitBtn.disabled = false;
            });
    }

    function handleUpdatePassword(pwId, row, btn) {
        var status = document.getElementById('passwords-status');
        var passphraseInput = row.querySelector('.pw-passphrase-input');
        var label = row.querySelector('.pw-label-input').value.trim();
        var passphrase = passphraseInput.value;

        if (!label) {
            setStatus(status, 'Label is required', 'error');
            return;
        }

        var body = { label: label };
        // An absent album_ids keeps the server's links; an empty array removes them all.
        if (albumsLoaded) {
            body.album_ids = [];
            row.querySelectorAll('.pw-album-cb:checked').forEach(function (cb) {
                body.album_ids.push(Number(cb.getAttribute('data-album-id')));
            });
        }
        if (passphrase) {
            body.passphrase = passphrase;
        }

        btn.disabled = true;

        api('PUT', '/admin/api/passwords/' + encodeURIComponent(String(pwId)), body)
            .then(function () {
                var cached = findPasswordById(pwId);
                if (cached) {
                    cached.label = label;
                    if (body.album_ids) cached.album_ids = body.album_ids;
                }
                passphraseInput.value = '';
                setStatus(status, 'Password "' + label + '" updated', 'success');
            })
            .catch(function (err) {
                setStatus(status, err.message, 'error');
            })
            .finally(function () {
                btn.disabled = !albumsLoaded;
            });
    }

    function handleDeletePassword(pwId, btn) {
        var pw = findPasswordById(pwId);
        var label = pw && pw.label ? pw.label : 'this password';
        if (!confirm('Delete password "' + label + '"? This cannot be undone.')) return;
        var status = document.getElementById('passwords-status');
        btn.disabled = true;

        api('DELETE', '/admin/api/passwords/' + encodeURIComponent(String(pwId)))
            .then(function () {
                setStatus(status, 'Password deleted', 'success');
                loadPasswords();
                refreshConfig();
            })
            .catch(function (err) {
                btn.disabled = false;
                setStatus(status, err.message, 'error');
            });
    }

    // --- Admin Password ---
    function handleAdminPasswordUpdate(e) {
        e.preventDefault();
        var currentEl = document.getElementById('current-admin-password');
        var newEl = document.getElementById('new-admin-password');
        var confirmEl = document.getElementById('confirm-admin-password');
        var status = document.getElementById('admin-password-status');
        var submitBtn = e.target.querySelector('button[type="submit"]');

        if (!currentEl.value || !newEl.value) return;
        if (newEl.value !== confirmEl.value) {
            setStatus(status, 'New passwords do not match', 'error');
            return;
        }

        submitBtn.disabled = true;
        api('PUT', '/admin/api/admin-password', {
            current_password: currentEl.value,
            new_password: newEl.value
        }, true)
            .then(function () {
                currentEl.value = '';
                newEl.value = '';
                confirmEl.value = '';
                showLogin('Admin password updated. Please log in again.', 'success');
            })
            .catch(function (err) {
                setStatus(status, err.status === 401 ? 'Current password is incorrect' : err.message, 'error');
            })
            .finally(function () {
                submitBtn.disabled = false;
            });
    }

    // --- Admin users ---
    function loadAdminUsers() {
        var list = document.getElementById('admin-users-list');
        var status = document.getElementById('admin-users-status');

        list.innerHTML = '<div class="empty-state">Loading admin users...</div>';

        return api('GET', '/admin/api/admin-users')
            .then(function (payload) {
                renderAdminUsers(payload && payload.users ? payload.users : []);
            })
            .catch(function (err) {
                list.innerHTML = '<div class="empty-state">Unable to load admin users.</div>';
                setStatus(status, err.message, 'error');
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

        list.innerHTML = '' +
            '<div class="table-wrap admin-users-table-wrap">' +
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
            '<tbody>' + sorted.map(adminUserRowHtml).join('') + '</tbody>' +
            '</table>' +
            '</div>';
    }

    function adminUserRowHtml(u) {
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
            '<input type="text" class="admin-user-username" value="' + escapeAttr(username) + '" aria-label="' + escapeAttr('Username for ' + username) + '" autocomplete="off" required>' +
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
            '<button type="button" class="btn-small admin-user-save" aria-label="' + escapeAttr('Save admin user ' + username) + '">Save</button>' +
            '</td>' +
            '</tr>';
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

        api('POST', '/admin/api/admin-users', {
            username: username,
            password: password,
            require_password_reset: requirePasswordReset
        })
            .then(function () {
                usernameEl.value = '';
                passwordEl.value = '';
                forceResetEl.checked = true;
                setStatus(status, 'Admin user created', 'success');
                return loadAdminUsers();
            })
            .catch(function (err) {
                setStatus(status, err.message, 'error');
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

        var userID = Number(row.getAttribute('data-user-id') || 0);
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

        api('PUT', '/admin/api/admin-users/' + encodeURIComponent(String(userID)), {
            username: username,
            is_active: isActive,
            require_password_reset: requireReset
        })
            .then(function (user) {
                setStatus(status, 'Admin user updated', 'success');
                // Loaded first: renaming yourself changes which row shows the "You" badge.
                return loadConfig().then(function (needsReset) {
                    if (!needsReset && user && row.parentNode) {
                        row.outerHTML = adminUserRowHtml(user);
                    }
                });
            })
            .catch(function (err) {
                setStatus(status, err.message, 'error');
            })
            .finally(function () {
                target.disabled = false;
            });
    }

    // --- Cover ---
    function handleCoverUpload(e) {
        e.preventDefault();
        var albumId = selectedAlbumId;
        var fileInput = document.getElementById('cover-file');
        var status = document.getElementById('cover-status');
        var submitBtn = e.target.querySelector('button[type="submit"]');

        if (!albumId) {
            setStatus(status, 'No album selected', 'error');
            return;
        }

        if (!fileInput.files.length) return;

        var formData = new FormData();
        formData.append('cover', fileInput.files[0]);

        submitBtn.disabled = true;
        setStatus(status, 'Uploading...', 'success');

        api('POST', albumUrl(albumId) + '/cover', formData)
            .then(function () {
                if (albumId !== selectedAlbumId) return;
                setStatus(status, 'Cover uploaded', 'success');
                fileInput.value = '';
            })
            .catch(function (err) {
                if (albumId !== selectedAlbumId) return;
                setStatus(status, err.message, 'error');
            })
            .finally(function () {
                submitBtn.disabled = false;
            });
    }

    // --- Tracks ---
    function loadTracks(albumId, seq) {
        var saveBtn = document.getElementById('btn-save-tracks');
        var status = document.getElementById('tracks-status');

        trackMetaByStem = {};
        reconcileReport = null;
        hideReconcileBar();
        saveBtn.disabled = true;
        document.getElementById('track-list').innerHTML = '<div class="empty-state">Loading tracks...</div>';
        document.getElementById('album-title').value = '';
        document.getElementById('album-artist').value = '';

        return Promise.all([
            api('GET', albumUrl(albumId) + '/tracks'),
            api('GET', albumUrl(albumId))
        ])
            .then(function (results) {
                if (seq !== loadSeq) return;
                var tracks = results[0] || [];
                var album = results[1] || {};
                setTrackMeta(tracks);
                renderTrackList(tracks);
                document.getElementById('album-title').value = album.title || '';
                document.getElementById('album-artist').value = album.artist || '';
                document.getElementById('downloads-enabled-toggle').checked = !!album.downloads_enabled;
                saveBtn.disabled = false;
                loadReconcilePreview(albumId, seq);
            })
            .catch(function (err) {
                if (seq !== loadSeq) return;
                document.getElementById('track-list').innerHTML = '<div class="empty-state">Unable to load tracks.</div>';
                setStatus(status, err.message, 'error');
            });
    }

    function setTrackMeta(tracks) {
        trackMetaByStem = {};
        tracks.forEach(function (track) {
            trackMetaByStem[track.stem] = { title: track.title || '' };
        });
    }

    function trackTitle(stem) {
        var meta = trackMetaByStem[stem];
        return meta && meta.title ? meta.title : stem;
    }

    function hideReconcileBar() {
        document.getElementById('reconcile-bar').classList.add('hidden');
    }

    function plural(n, word) {
        return n + ' ' + word + (n === 1 ? '' : 's');
    }

    function loadReconcilePreview(albumId, seq) {
        return api('GET', albumUrl(albumId) + '/reconcile')
            .then(function (report) {
                if (seq !== loadSeq || !report) return;
                var newCount = report.album_only ? report.album_only.length : 0;
                var missingCount = report.config_only ? report.config_only.length : 0;
                var mismatchCount = report.title_mismatches ? report.title_mismatches.length : 0;

                // Titles are never adopted from metadata, so mismatches alone need no action.
                if (newCount === 0 && missingCount === 0) {
                    hideReconcileBar();
                    return;
                }
                reconcileReport = report;

                var parts = [];
                if (newCount > 0) parts.push(plural(newCount, 'new track') + ' found on disk');
                if (missingCount > 0) parts.push(plural(missingCount, 'track') + ' missing from disk');
                if (mismatchCount > 0) parts.push(mismatchCount + ' title' + (mismatchCount === 1 ? ' differs' : 's differ') + ' from file metadata');

                document.getElementById('reconcile-summary').textContent = parts.join(', ');
                document.getElementById('reconcile-bar').classList.remove('hidden');
            })
            .catch(function (err) {
                if (seq !== loadSeq) return;
                setStatus(document.getElementById('tracks-status'), 'Unable to check the album folder: ' + err.message, 'error');
            });
    }

    function handleReconcileApply() {
        var albumId = selectedAlbumId;
        var report = reconcileReport;
        if (!albumId || !report) return;
        var status = document.getElementById('tracks-status');
        var btn = document.getElementById('btn-reconcile-apply');

        var added = report.album_only ? report.album_only.length : 0;
        var missing = (report.config_only || []).map(function (t) { return t.stem; });
        var message = 'Import tracks from disk?\n\n' +
            plural(added, 'new track') + ' will be added.\n' +
            plural(missing.length, 'track') + ' missing from disk will be removed' + (missing.length ? ': ' + missing.join(', ') : '') + '.\n\n' +
            'Existing track titles are kept. Unsaved edits in the track list will be lost.';
        if (!confirm(message)) return;

        btn.disabled = true;

        api('POST', albumUrl(albumId) + '/reconcile', { adopt_metadata_titles: false, keep_missing: false })
            .then(function (data) {
                loadAlbums();
                if (albumId !== selectedAlbumId) return;
                var applied = (data && data.applied) || {};
                var parts = [];
                if (applied.added > 0) parts.push(applied.added + ' added');
                if (applied.removed > 0) parts.push(applied.removed + ' removed');
                reloadSelected();
                setStatus(status, 'Tracks synced' + (parts.length ? ': ' + parts.join(', ') : ''), 'success');
            })
            .catch(function (err) {
                setStatus(status, err.message, 'error');
            })
            .finally(function () {
                btn.disabled = false;
            });
    }

    function renderTrackList(tracks) {
        var container = document.getElementById('track-list');
        container.innerHTML = '';

        if (!tracks.length) {
            container.innerHTML = '<div class="empty-state">No tracks in this album.</div>';
            return;
        }

        tracks.forEach(function (track) {
            var item = document.createElement('div');
            var name = track.title || track.stem;
            item.className = 'track-item';
            item.draggable = true;
            item.setAttribute('data-stem', track.stem);

            item.innerHTML =
                '<span class="drag-handle" aria-hidden="true">&#x2261;</span>' +
                '<span class="track-stem">' + escapeHtml(track.stem) + '</span>' +
                '<input type="text" class="track-title-input" value="' + escapeAttr(track.title) + '" placeholder="Title" aria-label="' + escapeAttr('Title for ' + track.stem) + '">' +
                '<input type="text" class="track-display-idx" value="' + escapeAttr(track.display_index || '') + '" placeholder="#" aria-label="' + escapeAttr('Display number for ' + track.stem) + '">' +
                '<button type="button" class="btn-small track-move" data-dir="up" aria-label="' + escapeAttr('Move ' + name + ' up') + '">&#x2191;</button>' +
                '<button type="button" class="btn-small track-move" data-dir="down" aria-label="' + escapeAttr('Move ' + name + ' down') + '">&#x2193;</button>';

            // Drag events
            item.addEventListener('dragstart', onDragStart);
            item.addEventListener('dragover', onDragOver);
            item.addEventListener('drop', onDrop);
            item.addEventListener('dragend', onDragEnd);

            container.appendChild(item);
        });
    }

    function handleTrackMove(e) {
        var btn = e.target.closest('.track-move');
        if (!btn) return;
        var item = btn.closest('.track-item');
        var list = item.parentNode;
        if (btn.getAttribute('data-dir') === 'up') {
            if (item.previousElementSibling) list.insertBefore(item, item.previousElementSibling);
        } else if (item.nextElementSibling) {
            list.insertBefore(item.nextElementSibling, item);
        }
        btn.focus();
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
        if (!draggedItem || draggedItem === this || draggedItem.parentNode !== this.parentNode) return;

        var items = Array.from(this.parentNode.children);
        if (items.indexOf(draggedItem) < items.indexOf(this)) {
            this.parentNode.insertBefore(draggedItem, this.nextSibling);
        } else {
            this.parentNode.insertBefore(draggedItem, this);
        }
    }

    function onDragEnd() {
        this.classList.remove('dragging');
        draggedItem = null;
    }

    function handleSaveTracks() {
        var albumId = selectedAlbumId;
        var status = document.getElementById('tracks-status');
        var btn = document.getElementById('btn-save-tracks');

        if (!albumId) {
            setStatus(status, 'No album selected', 'error');
            return;
        }

        var body = {
            title: document.getElementById('album-title').value,
            artist: document.getElementById('album-artist').value
        };
        var tracks = Array.prototype.map.call(document.querySelectorAll('#track-list .track-item'), function (item) {
            return {
                stem: item.getAttribute('data-stem'),
                title: item.querySelector('.track-title-input').value,
                display_index: item.querySelector('.track-display-idx').value || undefined
            };
        });
        if (tracks.length) {
            body.tracks = tracks;
        }

        btn.disabled = true;

        api('PUT', albumUrl(albumId) + '/tracks', body)
            .then(function () {
                loadAlbums();
                refreshConfig();
                if (albumId !== selectedAlbumId) return;
                // Save stays disabled until the reload refills the form; a blank artist field would clear the artist.
                reloadSelected();
                setStatus(status, 'Saved', 'success');
            })
            .catch(function (err) {
                if (albumId !== selectedAlbumId) return;
                btn.disabled = false;
                setStatus(status, err.message, 'error');
            });
    }

    // --- Analytics ---
    function loadAnalytics(albumId, seq) {
        // Always clear stale data first
        document.getElementById('analytics-overview').innerHTML = '';
        document.getElementById('track-stats-body').innerHTML = '<tr><td colspan="6">Loading...</td></tr>';
        document.getElementById('sessions-body').innerHTML = '<tr><td colspan="4">Loading...</td></tr>';

        return api('GET', albumUrl(albumId) + '/analytics')
            .then(function (data) {
                if (seq !== loadSeq) return;
                renderOverview(data.overall);
                renderTrackStats(data.tracks, data.heatmaps);
                renderSessions(data.sessions);
            })
            .catch(function (err) {
                if (seq !== loadSeq) return;
                document.getElementById('track-stats-body').innerHTML = '<tr><td colspan="6">' + escapeHtml('Unable to load analytics: ' + err.message) + '</td></tr>';
                document.getElementById('sessions-body').innerHTML = '<tr><td colspan="4">-</td></tr>';
            });
    }

    function renderOverview(overall) {
        var container = document.getElementById('analytics-overview');
        if (!overall) {
            container.innerHTML = '';
            return;
        }
        container.innerHTML =
            '<div class="stat-card"><div class="stat-value">' + (overall.total_sessions || 0) + '</div><div class="stat-label">Sessions</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + (overall.avg_tracks_per_session || 0).toFixed(1) + '</div><div class="stat-label">Avg Tracks/Session</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + escapeHtml(overall.most_completed ? trackTitle(overall.most_completed) : '-') + '</div><div class="stat-label">Most Completed</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + escapeHtml(overall.least_completed ? trackTitle(overall.least_completed) : '-') + '</div><div class="stat-label">Least Completed</div></div>';
    }

    function renderTrackStats(tracks, heatmaps) {
        var tbody = document.getElementById('track-stats-body');
        tbody.innerHTML = '';

        if (!tracks || !tracks.length) {
            tbody.innerHTML = '<tr><td colspan="6">No data yet</td></tr>';
            return;
        }

        tracks.forEach(function (t) {
            var displayTitle = trackTitle(t.stem);
            var subline = displayTitle !== t.stem
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

        if (maxCount === 0) {
            return '<span class="heatmap heatmap-empty" title="No dropout events for this track">' + bins.map(function () {
                return '<span class="heatmap-bin heatmap-level-0"></span>';
            }).join('') + '</span>';
        }

        return '<span class="heatmap">' + bins.map(function (b) {
            var count = Number(b.count || 0);
            var intensity = count / maxCount;
            var level = Math.max(1, Math.min(5, Math.ceil(intensity * 5)));
            var tooltip = buildHeatmapTooltip(stem, b, count, totalCount);
            return '<span class="heatmap-bin heatmap-level-' + level + '" data-tooltip="' + escapeAttr(tooltip) + '" aria-label="' + escapeAttr(tooltip) + '"></span>';
        }).join('') + '</span>';
    }

    function buildHeatmapTooltip(stem, bin, count, totalCount) {
        var startPct = Math.round(Number(bin.bin_start || 0) * 100);
        var endPct = Math.round(Number(bin.bin_end || 0) * 100);
        var rangeLabel = 'Range: ' + startPct + '%-' + endPct + '% of track';

        var share = totalCount > 0 ? Math.round((count / totalCount) * 100) : 0;
        var countLabel = 'Dropouts: ' + count + (totalCount > 0 ? ' (' + share + '% of track dropouts)' : '');

        return trackTitle(stem) + '\n' + rangeLabel + '\n' + countLabel;
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
                '<td>' + escapeHtml(formatDateTime(s.started_at)) + '</td>' +
                '<td>' + escapeHtml(formatDateTime(s.last_seen_at)) + '</td>' +
                '<td>' + Number(s.tracks_heard || 0) + '</td>' +
                '<td><code>' + escapeHtml(s.ip_hash || '') + '</code></td>';
            tbody.appendChild(tr);
        });
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
