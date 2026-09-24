// Acetate — Gate (passphrase input)
(function () {
    'use strict';

    var input = null;

    window.AcetateGate = {
        showStatus: showStatus
    };

    function showStatus(message) {
        var el = document.getElementById('gate-status');
        if (el) el.textContent = message;
    }

    function init() {
        input = document.getElementById('passphrase');
        if (!input) return;

        input.addEventListener('keydown', function (e) {
            if (e.key === 'Enter') {
                e.preventDefault();
                submit();
            }
        });

        // Resume AudioContext on gate interaction (for later use)
        input.addEventListener('keydown', function () {
            if (typeof AcetateOscilloscope !== 'undefined') {
                AcetateOscilloscope.resumeContext();
            }
        }, { once: true });
    }

    function submit() {
        var passphrase = input.value.trim();
        if (!passphrase) return;

        input.disabled = true;
        input.classList.remove('shake', 'pulse');
        showStatus('');

        fetch('/api/auth', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            credentials: 'same-origin',
            body: JSON.stringify({ passphrase: passphrase })
        })
            .then(function (r) {
                input.disabled = false;
                if (r.ok) {
                    return r.json().then(function (data) {
                        if (!data || !data.albums || data.albums.length === 0) {
                            input.value = '';
                            showStatus('This passphrase has no albums');
                            input.focus();
                            return;
                        }
                        Acetate.onAuthenticated(data);
                    });
                } else if (r.status === 401) {
                    // Wrong passphrase — shake
                    input.value = '';
                    input.classList.add('shake');
                    showStatus('Incorrect passphrase');
                    input.focus();
                    setTimeout(function () { input.classList.remove('shake'); }, 600);
                } else if (r.status === 429) {
                    var wait = parseInt(r.headers.get('Retry-After'), 10);
                    input.value = '';
                    showStatus(wait > 0
                        ? 'Too many attempts, try again in ' + wait + (wait === 1 ? ' second' : ' seconds')
                        : 'Too many attempts, try again later');
                    input.focus();
                } else {
                    // Server error — pulse
                    input.value = '';
                    input.classList.add('pulse');
                    showStatus('Something went wrong, try again');
                    input.focus();
                }
            })
            .catch(function () {
                // Network error — pulse
                input.disabled = false;
                input.value = '';
                input.classList.add('pulse');
                showStatus('Can\'t reach the server');
                input.focus();
            });
    }

    document.addEventListener('DOMContentLoaded', init);
})();
