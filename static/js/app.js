// Acetate — SPA state machine
(() => {
    window.Acetate = {
        state: "gate", // 'gate', 'selector', or 'player'
        albums: null, // list of accessible albums from auth response
        currentAlbum: null, // {slug, title, artist}
        albumData: null,
        currentTrackIndex: -1,
        albumLoadSeq: 0,
        pendingDeepLinkSearch: "",

        init: function () {
            this.pendingDeepLinkSearch = this.extractDeepLinkSearch(
                window.location.search,
            );

            // Always clear any existing listener session so visitors must re-enter the passphrase.
            // The input stays disabled until then so the logout cannot clear a fresh login cookie.
            var input = document.getElementById("passphrase");
            var logout = new AbortController();
            setTimeout(() => logout.abort(), 5000);
            fetch("/api/auth", {
                method: "DELETE",
                credentials: "same-origin",
                signal: logout.signal,
            })
                .catch(() => {})
                .then(() => {
                    input.disabled = false;
                    input.focus();
                });
            this.showGate();

            // Register service worker
            if ("serviceWorker" in navigator) {
                navigator.serviceWorker.register("/sw.js").catch(() => {});
            }

            // Handle back button
            window.addEventListener("popstate", () => {
                if (
                    Acetate.state === "player" &&
                    Acetate.albums &&
                    Acetate.albums.length > 1
                ) {
                    Acetate.showAlbumSelector();
                } else if (
                    Acetate.state === "player" ||
                    Acetate.state === "selector"
                ) {
                    Acetate.showGate();
                }
            });
        },

        // Returns the album-scoped API base, e.g. "/api/albums/my-album"
        albumApiBase: function () {
            if (!this.currentAlbum) return "/api/albums/_";
            return (
                "/api/albums/" + this.encodePathSegment(this.currentAlbum.slug)
            );
        },

        currentTrack: function () {
            var tracks = this.albumData && this.albumData.tracks;
            return (tracks && tracks[this.currentTrackIndex]) || null;
        },

        // Called with a non-empty album list (gate.js handles the empty case).
        onAuthenticated: function (authData) {
            this.notifyServiceWorker("AUTHENTICATED");
            this.albums = authData.albums;

            var linkedSlug = new URLSearchParams(this.pendingDeepLinkSearch).get(
                "album",
            );
            var linked = this.albums.find((a) => a.slug === linkedSlug);
            if (linked) {
                this.selectAlbum(linked);
            } else if (this.albums.length === 1) {
                this.selectAlbum(this.albums[0]);
            } else {
                this.showAlbumSelector();
            }
        },

        selectAlbum: function (album) {
            this.currentAlbum = album;

            var nextSearch = window.location.search;
            if (
                this.pendingDeepLinkSearch &&
                nextSearch !== this.pendingDeepLinkSearch
            ) {
                nextSearch = this.pendingDeepLinkSearch;
            }
            history.pushState(
                { screen: "player" },
                "",
                window.location.pathname + nextSearch + window.location.hash,
            );

            this.loadAlbumData();
        },

        showAlbumSelector: function () {
            this.stopPlayback();
            this.state = "selector";
            history.pushState(
                { screen: "selector" },
                "",
                window.location.pathname,
            );
            document.getElementById("gate").classList.remove("active");
            document.getElementById("player").classList.remove("active");
            document.getElementById("selector").classList.add("active");

            if (typeof AcetateSelector !== "undefined") {
                AcetateSelector.render(this.albums);
            }
            this.focusSoon(document.querySelector("#album-grid .album-card"));
        },

        // Stops audio (even mid-buffer) and invalidates any in-flight album load.
        stopPlayback: function () {
            if (typeof AcetatePlayer !== "undefined") {
                AcetatePlayer.pause();
            }
            this.albumLoadSeq++;
            this.currentAlbum = null;
            this.albumData = null;
        },

        loadAlbumData: function () {
            var token = ++this.albumLoadSeq;
            fetch(this.albumApiBase() + "/tracks", { credentials: "same-origin" })
                .then((r) => {
                    if (token !== Acetate.albumLoadSeq) return;
                    if (r.status === 401) {
                        Acetate.handleUnauthorized();
                        return;
                    }
                    if (!r.ok) throw new Error("tracks_unavailable");
                    return r.json().then((data) => {
                        if (token === Acetate.albumLoadSeq) {
                            Acetate.onAlbumDataLoaded(data);
                        }
                    });
                })
                .catch(() => {
                    if (token !== Acetate.albumLoadSeq) return;
                    Acetate.showGate();
                    AcetateGate.showStatus("Couldn't load the album, try again");
                });
        },

        onAlbumDataLoaded: function (data) {
            Acetate.albumData = data;
            document.title = data.title + " — Acetate";
            Acetate.showPlayer();

            if (typeof AcetateTracklist !== "undefined") {
                AcetateTracklist.setDownloadsEnabled(!!data.downloads_enabled);
                AcetateTracklist.render(data.tracks);
            }

            if (!data.tracks || data.tracks.length === 0) {
                return;
            }

            var target = Acetate.resolveInitialPlaybackTarget(data);
            AcetatePlayer.loadTrack(target.index, { startTime: target.time });
            Acetate.warmOfflineCaches(data);
        },

        // A mid-album failure may be an expired session; only a 401 sends the listener back.
        checkSession: function () {
            if (!this.currentAlbum) return;
            var slug = this.currentAlbum.slug;
            fetch(this.albumApiBase() + "/tracks", {
                credentials: "same-origin",
                cache: "no-store",
            })
                .then((r) => {
                    if (
                        r.status === 401 &&
                        Acetate.currentAlbum &&
                        Acetate.currentAlbum.slug === slug
                    ) {
                        Acetate.handleUnauthorized();
                    }
                })
                .catch(() => {});
        },

        handleUnauthorized: function () {
            this.notifyServiceWorker("UNAUTHENTICATED");
            this.showGate();
            AcetateGate.showStatus("Session expired, enter the passphrase again");
        },

        showGate: function () {
            this.stopPlayback();
            this.state = "gate";
            this.albums = null;
            document.getElementById("gate").classList.add("active");
            document.getElementById("player").classList.remove("active");
            document.getElementById("selector").classList.remove("active");
            AcetateGate.showStatus("");
            var input = document.getElementById("passphrase");
            input.value = "";
            this.focusSoon(input);
        },

        // Focus after the screen's visibility transition has started.
        focusSoon: function (el) {
            if (el) setTimeout(() => el.focus(), 100);
        },

        scrollBehavior: () =>
            window.matchMedia("(prefers-reduced-motion: reduce)").matches
                ? "auto"
                : "smooth",

        showPlayer: function () {
            this.state = "player";
            document.getElementById("gate").classList.remove("active");
            document.getElementById("selector").classList.remove("active");
            document.getElementById("player").classList.add("active");

            // Load cover
            var cover = document.getElementById("cover");
            cover.classList.remove("fallback");
            cover.src = this.albumApiBase() + "/cover";
            cover.onerror = function () {
                this.onerror = null;
                this.classList.add("fallback");
                this.src = Acetate.makeCoverFallback();
            };
            this.focusSoon(document.getElementById("btn-play"));
        },

        resolveInitialPlaybackTarget: function (albumData) {
            var tracks = albumData && albumData.tracks ? albumData.tracks : [];
            if (!tracks.length) return { index: 0, time: 0 };

            // A link naming another album stays pending in case that album is picked later.
            var linkedSlug = new URLSearchParams(this.pendingDeepLinkSearch).get(
                "album",
            );
            if (
                this.pendingDeepLinkSearch &&
                (!linkedSlug || linkedSlug === this.currentAlbum.slug)
            ) {
                var deepLink = this.parseDeepLinkTarget(
                    tracks,
                    this.pendingDeepLinkSearch,
                );
                this.pendingDeepLinkSearch = "";
                if (deepLink) return deepLink;
            }

            var playbackState = AcetatePlayer.getStoredPlaybackState();
            if (playbackState) {
                if (
                    playbackState.album_fingerprint &&
                    playbackState.album_fingerprint !==
                        this.makeAlbumFingerprint(albumData)
                ) {
                    return { index: 0, time: 0 };
                }

                var resumeIdx = this.findTrackIndexByStem(
                    tracks,
                    playbackState.track_stem,
                );
                if (resumeIdx >= 0) {
                    return {
                        index: resumeIdx,
                        time: this.normalizeTime(playbackState.time_seconds),
                    };
                }
            }

            return { index: 0, time: 0 };
        },

        parseDeepLinkTarget: function (tracks, search) {
            var params = new URLSearchParams(
                search || window.location.search || "",
            );
            var hasTrack = params.has("track");
            var hasTime = params.has("t");
            if (!hasTrack && !hasTime) return null;

            var idx = 0;
            if (hasTrack) {
                idx = this.parseTrackIndexParam(params.get("track"), tracks);
                if (idx < 0) return null;
            }

            return {
                index: idx,
                time: this.parseTimeParam(params.get("t")),
            };
        },

        parseTrackIndexParam: function (raw, tracks) {
            if (!raw || !tracks || !tracks.length) return -1;
            var value = String(raw).trim();
            if (!value) return -1;

            if (/^\d+$/.test(value)) {
                var n = parseInt(value, 10);
                if (n >= 1 && n <= tracks.length) return n - 1;
                if (n >= 0 && n < tracks.length) return n;
            }

            var lowered = value.toLowerCase();
            if (lowered.slice(-4) === ".mp3") {
                lowered = lowered.slice(0, -4);
            }
            for (var i = 0; i < tracks.length; i++) {
                if (String(tracks[i].stem || "").toLowerCase() === lowered)
                    return i;
                if (String(tracks[i].title || "").toLowerCase() === lowered)
                    return i;
            }

            return -1;
        },

        parseTimeParam: function (raw) {
            if (!raw) return 0;
            var value = String(raw).trim();
            if (!value) return 0;

            if (/^\d+(\.\d+)?$/.test(value)) {
                return this.normalizeTime(parseFloat(value));
            }

            var parts = value.split(":");
            if (parts.length >= 2 && parts.length <= 3) {
                var total = 0;
                for (var i = 0; i < parts.length; i++) {
                    if (!/^\d+(\.\d+)?$/.test(parts[i])) return 0;
                    total = total * 60 + parseFloat(parts[i]);
                }
                return this.normalizeTime(total);
            }
            return 0;
        },

        normalizeTime: function (seconds) {
            if (
                typeof seconds !== "number" ||
                !isFinite(seconds) ||
                seconds < 0
            )
                return 0;
            return seconds;
        },

        findTrackIndexByStem: function (tracks, stem) {
            if (!stem) return -1;
            var needle = String(stem).toLowerCase();
            for (var i = 0; i < tracks.length; i++) {
                if (String(tracks[i].stem || "").toLowerCase() === needle)
                    return i;
            }
            return -1;
        },

        makeAlbumFingerprint: function (albumData) {
            if (!albumData || !albumData.tracks) return "";
            var stems = albumData.tracks.map((t) => t.stem).join("|");
            return stems;
        },

        warmOfflineCaches: function (albumData) {
            if (!albumData || !albumData.tracks) return;
            var base = this.albumApiBase();

            fetch(base + "/cover", { credentials: "same-origin" }).catch(
                () => {},
            );

            albumData.tracks.forEach((track) => {
                fetch(
                    base + "/lyrics/" + Acetate.encodePathSegment(track.stem),
                    { credentials: "same-origin" },
                ).catch(() => {});
            });
        },

        encodePathSegment: (value) =>
            encodeURIComponent(value).replace(
                /[!'()*]/g,
                (ch) => "%" + ch.charCodeAt(0).toString(16).toUpperCase(),
            ),

        extractDeepLinkSearch: function (search) {
            var params = new URLSearchParams(search || "");
            if (!params.has("album") && !params.has("track") && !params.has("t")) {
                return "";
            }
            return search || "";
        },

        makeCoverFallback: function (overrideTitle) {
            var title =
                overrideTitle ||
                (this.albumData && this.albumData.title
                    ? this.albumData.title
                    : "Acetate");
            title = String(title).replace(/[<>&]/g, "").slice(0, 28);
            var svg =
                "<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 300 300'>" +
                "<rect width='300' height='300' fill='#110f0d'/>" +
                "<rect x='12' y='12' width='276' height='276' fill='none' stroke='#292620' stroke-width='2'/>" +
                "<text x='150' y='155' text-anchor='middle' fill='#d4cfc4' font-size='26' font-family='serif'>" +
                title +
                "</text>" +
                "</svg>";
            return "data:image/svg+xml;utf8," + encodeURIComponent(svg);
        },

        notifyServiceWorker: function (state) {
            if (!("serviceWorker" in navigator)) return;

            var message = { type: state };
            if (navigator.serviceWorker.controller) {
                navigator.serviceWorker.controller.postMessage(message);
                return;
            }

            navigator.serviceWorker.ready
                .then((reg) => {
                    if (reg.active) {
                        reg.active.postMessage(message);
                    }
                })
                .catch(() => {});
        },
    };

    document.addEventListener("DOMContentLoaded", () => {
        Acetate.init();
    });
})();
