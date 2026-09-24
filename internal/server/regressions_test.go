package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"acetate/internal/albums"
)

// do sends a request with an Origin header (so admin mutations pass the CSRF
// check) and returns the status and body.
func (env *testEnv) do(t *testing.T, method, path string, body interface{}, cookies []*http.Cookie) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, env.ts.URL+path, rd)
	req.Header.Set("Origin", env.ts.URL)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (env *testEnv) albumTitle(t *testing.T) string {
	t.Helper()
	var title string
	if err := env.srv.db.QueryRow("SELECT title FROM albums WHERE id = ?", env.albumID).Scan(&title); err != nil {
		t.Fatal(err)
	}
	return title
}

func TestUpdateTracksRejectedLeavesAlbumUntouched(t *testing.T) {
	env := setupTest(t)
	admin := env.authenticateAdmin(t)

	status, _ := env.do(t, "PUT", fmt.Sprintf("/admin/api/albums/%d/tracks", env.albumID), map[string]interface{}{
		"title":  "Renamed",
		"tracks": []map[string]string{{"stem": "01-gathering", "title": "Only one"}},
	}, admin)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	if got := env.albumTitle(t); got != "Album Title" {
		t.Fatalf("album title = %q after rejected update, want unchanged", got)
	}
}

func TestUpdateAlbumCanClearArtistAndKeepsSlugOnSameTitle(t *testing.T) {
	env := setupTest(t)
	admin := env.authenticateAdmin(t)

	// Same title in different case regenerates the same slug; it must not collide with itself.
	status, body := env.do(t, "PUT", fmt.Sprintf("/admin/api/albums/%d", env.albumID), map[string]interface{}{
		"title":  "album title",
		"artist": "",
	}, admin)
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%s", status, body)
	}
	var slug, artist string
	env.srv.db.QueryRow("SELECT slug, artist FROM albums WHERE id = ?", env.albumID).Scan(&slug, &artist)
	if slug != env.albumSlug {
		t.Errorf("slug = %q, want %q", slug, env.albumSlug)
	}
	if artist != "" {
		t.Errorf("artist = %q, want cleared", artist)
	}
}

func TestPassphraseRotationRevokesListenerSessions(t *testing.T) {
	env := setupTest(t)
	listener := env.authenticate(t)
	admin := env.authenticateAdmin(t)

	var pwID int64
	env.srv.db.QueryRow("SELECT id FROM listener_passwords WHERE label = 'Default'").Scan(&pwID)
	newPass := "rotated-pass"
	status, body := env.do(t, "PUT", fmt.Sprintf("/admin/api/passwords/%d", pwID), map[string]interface{}{
		"label": "Default", "passphrase": newPass, "album_ids": []int64{env.albumID},
	}, admin)
	if status != http.StatusOK {
		t.Fatalf("rotate status = %d body=%s", status, body)
	}

	status, _ = env.do(t, "GET", "/api/albums/"+env.albumSlug+"/tracks", nil, listener)
	if status != http.StatusUnauthorized {
		t.Fatalf("old session after rotation = %d, want 401", status)
	}
}

func TestPassphraseLengthLimits(t *testing.T) {
	env := setupTest(t)
	admin := env.authenticateAdmin(t)

	long := strings.Repeat("x", maxPassphraseLen+1)
	status, _ := env.do(t, "POST", "/admin/api/passwords", map[string]interface{}{
		"label": "Long", "passphrase": long, "album_ids": []int64{env.albumID},
	}, admin)
	if status != http.StatusBadRequest {
		t.Fatalf("create with %d-byte passphrase = %d, want 400", len(long), status)
	}

	// Listener login trims like creation does.
	status, _ = env.do(t, "POST", "/api/auth", map[string]string{"passphrase": "  testpass  "}, nil)
	if status != http.StatusOK {
		t.Fatalf("login with padded passphrase = %d, want 200", status)
	}
}

func TestAdminPasswordPolicyRejectsOverBcryptLimit(t *testing.T) {
	env := setupTest(t)
	admin := env.authenticateAdmin(t)

	status, body := env.do(t, "POST", "/admin/api/admin-users", map[string]interface{}{
		"username": "second", "password": strings.Repeat("a1", 40),
	}, admin)
	if status != http.StatusBadRequest {
		t.Fatalf("create admin with 80-byte password = %d body=%s, want 400", status, body)
	}

	status, body = env.do(t, "POST", "/admin/api/admin-users", map[string]interface{}{
		"username": "Bad Name!", "password": "valid-password-123",
	}, admin)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "invalid username") {
		t.Fatalf("bad username = %d body=%s", status, body)
	}
}

func TestAdminLoginAcceptsPaddedPassword(t *testing.T) {
	env := setupTest(t)
	_, _, status := env.authenticateAdminAs(t, testAdminUsername, " "+testAdminPassword+" ")
	if status != http.StatusOK {
		t.Fatalf("padded admin password login = %d, want 200", status)
	}
}

func TestLogoutWithoutSessionSucceeds(t *testing.T) {
	env := setupTest(t)
	status, _ := env.do(t, "DELETE", "/api/auth", nil, []*http.Cookie{{Name: listenerCookie, Value: "forged"}})
	if status != http.StatusOK {
		t.Fatalf("logout = %d, want 200", status)
	}
	env.srv.collector.FlushNow(t.Context())
	var n int
	env.srv.db.QueryRow("SELECT COUNT(*) FROM events WHERE event_type = 'session_end'").Scan(&n)
	if n != 0 {
		t.Fatalf("forged cookie recorded %d session_end events", n)
	}
}

func TestHeadRequestsServed(t *testing.T) {
	env := setupTest(t)
	resp, err := env.ts.Client().Head(env.ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD / = %d, want 200", resp.StatusCode)
	}
}

func TestStreamStemWithPercentAndDownloadName(t *testing.T) {
	env := setupTest(t)
	stem := "50% Off"
	if err := os.WriteFile(filepath.Join(env.albumDir, stem+".mp3"), []byte("mp3"), 0644); err != nil {
		t.Fatal(err)
	}
	tracks, _ := env.srv.albumStore.GetTracks(env.albumID)
	tracks = append(tracks, albums.Track{Stem: stem, Title: "Café / Night"})
	if err := env.srv.albumStore.SetTracks(env.albumID, tracks); err != nil {
		t.Fatal(err)
	}
	env.srv.db.Exec("UPDATE albums SET downloads_enabled = 1 WHERE id = ?", env.albumID)
	cookies := env.authenticate(t)

	// Canonical escaping (RawPath empty) and non-canonical escaping (RawPath set) must both resolve.
	for _, path := range []string{"50%25%20Off", "50%25%20Of%66"} {
		req, _ := http.NewRequest("GET", env.ts.URL+"/api/albums/"+env.albumSlug+"/stream/"+path+"?dl=1", nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		resp, err := env.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, resp.StatusCode)
		}
		_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
		if err != nil || params["filename"] != "Café  Night.mp3" {
			t.Fatalf("%s: Content-Disposition filename = %q (%v)", path, params["filename"], err)
		}
	}
}

func TestCreateAlbumConfinedToAlbumBase(t *testing.T) {
	env := setupTest(t)
	admin := env.authenticateAdmin(t)
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "inside"), 0755); err != nil {
		t.Fatal(err)
	}
	env.srv.albumBasePath = base

	for path, want := range map[string]int{
		"inside":        http.StatusCreated,
		".":             http.StatusCreated,
		"../":           http.StatusBadRequest,
		"inside/../../": http.StatusBadRequest,
		env.albumDir:    http.StatusBadRequest,
	} {
		status, body := env.do(t, "POST", "/admin/api/albums", map[string]string{"title": "T " + path, "album_path": path}, admin)
		if status != want {
			t.Errorf("album_path %q: status %d body=%s, want %d", path, status, body, want)
		}
	}
}

func TestStaticAssetsRevalidate(t *testing.T) {
	env := setupTest(t)

	resp, err := env.ts.Client().Get(env.ts.URL + "/js/app.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	etag := resp.Header.Get("ETag")
	if resp.StatusCode != http.StatusOK || etag == "" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("app.js: status %d etag %q cache %q", resp.StatusCode, etag, resp.Header.Get("Cache-Control"))
	}

	req, _ := http.NewRequest("GET", env.ts.URL+"/js/app.js", nil)
	req.Header.Set("If-None-Match", etag)
	resp, err = env.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional app.js = %d, want 304", resp.StatusCode)
	}

	for path, want := range map[string]int{"/js/missing.js": 404, "/robots.txt": 200, "/admin/nope": 200, "/admin/api/nope": 404} {
		resp, err := env.ts.Client().Get(env.ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s = %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestBackupIncludesAlbumCovers(t *testing.T) {
	env := setupTest(t)
	admin := env.authenticateAdmin(t)
	coverDir := filepath.Join(env.dataDir, "covers", fmt.Sprint(env.albumID))
	if err := os.MkdirAll(coverDir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(coverDir, "cover_override.jpg"), []byte("jpeg"), 0644)

	status, body := env.do(t, "GET", "/admin/api/export/backup", nil, admin)
	if status != http.StatusOK {
		t.Fatalf("backup status = %d", status)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"acetate.db", "manifest.json", fmt.Sprintf("covers/%d/cover_override.jpg", env.albumID)} {
		if !names[want] {
			t.Errorf("backup missing %s (has %v)", want, names)
		}
	}
}

func TestExportCSVNeutralizesFormulas(t *testing.T) {
	env := setupTest(t)
	admin := env.authenticateAdmin(t)
	env.srv.db.Exec("INSERT INTO events (session_id, event_type, track_stem, album_id) VALUES ('s', 'play', '=HYPERLINK(1)', ?)", env.albumID)

	status, body := env.do(t, "GET", fmt.Sprintf("/admin/api/export/events?format=csv&album_id=%d", env.albumID), nil, admin)
	if status != http.StatusOK || !strings.Contains(string(body), "'=HYPERLINK(1)") {
		t.Fatalf("export = %d body=%s", status, body)
	}
	status, body = env.do(t, "GET", "/admin/api/export/events?format=json&album_id=999", nil, admin)
	if status != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("empty json export = %d body=%q", status, body)
	}
}

func TestPasswordRejectsUnknownAlbumAndMissingID(t *testing.T) {
	env := setupTest(t)
	admin := env.authenticateAdmin(t)

	status, _ := env.do(t, "POST", "/admin/api/passwords", map[string]interface{}{
		"label": "x", "passphrase": "some-pass", "album_ids": []int64{9999},
	}, admin)
	if status != http.StatusBadRequest {
		t.Errorf("create with unknown album = %d, want 400", status)
	}
	status, _ = env.do(t, "PUT", "/admin/api/passwords/9999", map[string]interface{}{
		"label": "x", "album_ids": []int64{env.albumID},
	}, admin)
	if status != http.StatusNotFound {
		t.Errorf("update missing password = %d, want 404", status)
	}
}
