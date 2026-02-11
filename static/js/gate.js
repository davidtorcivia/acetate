// Acetate — Gate (passphrase input)
(function () {
    'use strict';

    var input = null;

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

        fetch('/api/auth', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            credentials: 'same-origin',
            body: JSON.stringify({ passphrase: passphrase })
        })
            .then(function (r) {
                input.disabled = false;
                if (r.ok) {
                    Acetate.onAuthenticated();
                } else if (r.status === 401) {
                    // Wrong passphrase — shake
                    input.value = '';
                    input.classList.add('shake');
                    input.focus();
                    setTimeout(function () { input.classList.remove('shake'); }, 600);
                } else if (r.status === 429) {
                    // Rate limited — do nothing, just clear
                    input.value = '';
                    input.focus();
                } else {
                    // Server error — pulse
                    input.value = '';
                    input.classList.add('pulse');
                    input.focus();
                }
            })
            .catch(function () {
                // Network error — pulse
                input.disabled = false;
                input.value = '';
                input.classList.add('pulse');
                input.focus();
            });
    }

    document.addEventListener('DOMContentLoaded', init);
})();
