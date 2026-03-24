// Acetate — Album Selector
(function () {
    'use strict';

    window.AcetateSelector = {
        render: function (albums) {
            var grid = document.getElementById('album-grid');
            if (!grid) return;
            grid.innerHTML = '';

            if (!albums || albums.length === 0) return;

            albums.forEach(function (album) {
                var card = document.createElement('div');
                card.className = 'album-card';
                card.setAttribute('role', 'button');
                card.setAttribute('tabindex', '0');

                var coverImg = document.createElement('img');
                coverImg.className = 'album-card-cover';
                coverImg.alt = album.title;
                coverImg.src = '/api/albums/' + Acetate.encodePathSegment(album.slug) + '/cover';
                coverImg.onerror = function () {
                    this.onerror = null;
                    this.src = Acetate.makeCoverFallback(album.title);
                };

                var title = document.createElement('div');
                title.className = 'album-card-title';
                title.textContent = album.title;

                var artist = document.createElement('div');
                artist.className = 'album-card-artist';
                artist.textContent = album.artist || '';

                card.appendChild(coverImg);
                card.appendChild(title);
                card.appendChild(artist);

                card.addEventListener('click', function () {
                    Acetate.selectAlbum(album);
                });
                card.addEventListener('keydown', function (e) {
                    if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault();
                        Acetate.selectAlbum(album);
                    }
                });

                grid.appendChild(card);
            });
        }
    };
})();
