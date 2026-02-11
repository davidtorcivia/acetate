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
            });
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

            li.addEventListener('click', function () {
                AcetatePlayer.loadTrack(index);
                AcetatePlayer.play();
            });

            items.appendChild(li);
        });
    }

    function setActive(index) {
        if (!items) return;
        var lis = items.querySelectorAll('li');
        lis.forEach(function (li) {
            li.classList.toggle('active', parseInt(li.dataset.index) === index);
        });
    }

    document.addEventListener('DOMContentLoaded', init);
})();
