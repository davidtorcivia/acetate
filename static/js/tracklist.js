// Acetate — Track list
(function () {
    'use strict';

    var container, toggle, items;

    window.AcetateTracklist = {
        render: render,
        setActive: setActive
    };

    function init() {
        container = document.getElementById('tracklist');
        toggle = document.getElementById('tracklist-toggle');
        items = document.getElementById('tracklist-items');

        if (toggle) {
            toggle.addEventListener('click', function () {
                var expanded = container.classList.toggle('expanded');
                container.classList.toggle('collapsed', !expanded);
                toggle.classList.toggle('open', expanded);
                toggle.setAttribute('aria-expanded', expanded ? 'true' : 'false');
            });

            var expandedInitially = container && container.classList.contains('expanded');
            toggle.setAttribute('aria-expanded', expandedInitially ? 'true' : 'false');
        }
    }

    function render(tracks) {
        if (!items) return;
        items.innerHTML = '';

        tracks.forEach(function (track, index) {
            var li = document.createElement('li');
            li.dataset.index = index;

            var num = document.createElement('span');
            num.className = 'track-num';
            num.textContent = track.display_index || String(index + 1);

            var title = document.createElement('span');
            title.className = 'track-title-text';
            title.textContent = track.title;

            li.appendChild(num);
            li.appendChild(title);
            li.tabIndex = 0;
            li.setAttribute('role', 'button');
            li.setAttribute('aria-label', 'Play ' + track.title);

            li.addEventListener('click', function () {
                AcetatePlayer.loadTrack(index);
                AcetatePlayer.play();
            });
            li.addEventListener('keydown', function (e) {
                if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    AcetatePlayer.loadTrack(index);
                    AcetatePlayer.play();
                }
            });

            items.appendChild(li);
        });
    }

    function setActive(index) {
        if (!items) return;
        var lis = items.querySelectorAll('li');
        lis.forEach(function (li) {
            var isActive = parseInt(li.dataset.index, 10) === index;
            li.classList.toggle('active', isActive);
            if (isActive) {
                li.setAttribute('aria-current', 'true');
            } else {
                li.removeAttribute('aria-current');
            }
        });
    }

    document.addEventListener('DOMContentLoaded', init);
})();
