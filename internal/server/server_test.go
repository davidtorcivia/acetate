package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"acetate/internal/albums"
	"acetate/internal/database"

	"golang.org/x/crypto/bcrypt"
)

type testEnv struct {
	srv       *Server
	ts        *httptest.Server
	albumDir  string
	dataDir   string
	albumSlug string
	albumID   int64
}

const (
	testAdminUsername = "admin"
	testAdminPassword = "test-admin-pass-123"
)

func setupTest(t *testing.T) *testEnv {
	return setupTestWithBootstrap(t, testAdminUsername, testAdminPassword, "")
}

func setupTestWithBootstrap(t *testing.T, username, password, passwordHash string) *testEnv {
	t.Helper()

	albumDir := t.TempDir()
	dataDir := t.TempDir()

	// Create test album files
	os.WriteFile(filepath.Join(albumDir, "01-gathering.mp3"), []byte("fake-mp3-data"), 0644)
	os.WriteFile(filepath.Join(albumDir, "01-gathering.lrc"), []byte("[00:00.00] Test lyric line\n[00:05.00] Second line"), 0644)
	os.WriteFile(filepath.Join(albumDir, "02-hollow.mp3"), []byte("fake-mp3-data-2"), 0644)
	os.WriteFile(filepath.Join(albumDir, "cover.jpg"), []byte("fake-jpeg"), 0644)

	// Open database
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if strings.TrimSpace(username) != "" || strings.TrimSpace(password) != "" || strings.TrimSpace(passwordHash) != "" {
		if err := EnsureAdminBootstrap(db, username, password, passwordHash); err != nil {
			t.Fatalf("bootstrap admin user: %v", err)
		}
	}

	// Create album store and seed album, tracks, and listener password
	store := albums.NewStore(db)

	alb, err := store.CreateAlbum("Album Title", "Test Artist", albumDir)
	if err != nil {
		t.Fatalf("create album: %v", err)
	}

	if err := store.SetTracks(alb.ID, []albums.Track{
		{Stem: "01-gathering", Title: "Gathering", DisplayIndex: "1"},
		{Stem: "02-hollow", Title: "Hollow", DisplayIndex: "2"},
	}); err != nil {
		t.Fatalf("set tracks: %v", err)
	}

	if _, err := store.CreatePassword("Default", "testpass", []int64{alb.ID}); err != nil {
		t.Fatalf("create password: %v", err)
	}

	srv := New(Config{
		ListenAddr:             ":0",
		DataPath:               dataDir,
		AnalyticsRetentionDays: 0,
		MaintenanceInterval:    time.Hour,
		DB:                     db,
		AlbumStore:             store,
	})

	ts := httptest.NewServer(srv.routes())
	t.Cleanup(func() {
		ts.Close()
		srv.collector.Close()
		srv.sessions.Close()
		srv.rateLimiter.Close()
		srv.cfIPs.Close()
	})

	return &testEnv{srv: srv, ts: ts, albumDir: albumDir, dataDir: dataDir, albumSlug: alb.Slug, albumID: alb.ID}
}

func (env *testEnv) authenticate(t *testing.T) []*http.Cookie {
	t.Helper()

	body, _ := json.Marshal(map[string]string{"passphrase": "testpass"})
	resp, err := env.ts.Client().Post(env.ts.URL+"/api/auth", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("auth request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auth status = %d, want 200", resp.StatusCode)
	}

	// Parse response to verify album slug is returned
	var authResp struct {
		Status string `json:"status"`
		Albums []struct {
			Slug   string `json:"slug"`
			Title  string `json:"title"`
			Artist string `json:"artist"`
		} `json:"albums"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		t.Fatalf("decode auth response: %v", err)
	}
	if authResp.Status != "ok" {
		t.Fatalf("auth response status = %q, want ok", authResp.Status)
	}
	if len(authResp.Albums) > 0 {
		env.albumSlug = authResp.Albums[0].Slug
	}

	return resp.Cookies()
}

func (env *testEnv) authenticateAdmin(t *testing.T) []*http.Cookie {
	t.Helper()

	body, _ := json.Marshal(map[string]string{
		"username": testAdminUsername,
		"password": testAdminPassword,
	})
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/admin/api/auth", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", env.ts.URL)
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("admin auth request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin auth status = %d, want 200", resp.StatusCode)
	}

	return resp.Cookies()
}

func (env *testEnv) authenticateAdminAs(t *testing.T, username, password string) ([]*http.Cookie, map[string]interface{}, int) {
	t.Helper()

	body, _ := json.Marshal(map[string]string{
		"username": username,
		"password": password,
	})
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/admin/api/auth", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", env.ts.URL)
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("admin auth request: %v", err)
	}
	defer resp.Body.Close()

	payload := map[string]interface{}{}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	return resp.Cookies(), payload, resp.StatusCode
}

func TestAuthFlow(t *testing.T) {
	env := setupTest(t)

	// Wrong passphrase → 401
	body, _ := json.Marshal(map[string]string{"passphrase": "wrong"})
	resp, err := env.ts.Client().Post(env.ts.URL+"/api/auth", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("auth request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong pass status = %d, want 401", resp.StatusCode)
	}

	// Correct passphrase → 200 with cookie
	cookies := env.authenticate(t)
	found := false
	for _, c := range cookies {
		if c.Name == "acetate_session" && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Error("no session cookie set")
	}
}

func TestSessionGating(t *testing.T) {
	env := setupTest(t)

	// Without session → 401
	resp, err := env.ts.Client().Get(env.ts.URL + "/api/albums/" + env.albumSlug + "/tracks")
	if err != nil {
		t.Fatalf("tracks request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-session tracks status = %d, want 401", resp.StatusCode)
	}

	// With session → 200
	cookies := env.authenticate(t)
	req, _ := http.NewRequest("GET", env.ts.URL+"/api/albums/"+env.albumSlug+"/tracks", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err = env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("tracks request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("authed tracks status = %d, want 200", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	tracks, ok := result["tracks"].([]interface{})
	if !ok || len(tracks) != 2 {
		t.Errorf("expected 2 tracks, got %v", result["tracks"])
	}
}

func TestStreamTrack(t *testing.T) {
	env := setupTest(t)
	cookies := env.authenticate(t)

	req, _ := http.NewRequest("GET", env.ts.URL+"/api/albums/"+env.albumSlug+"/stream/01-gathering", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("stream status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/mpeg" {
		t.Errorf("content-type = %q, want audio/mpeg", ct)
	}
}

func TestStreamTrackPathTraversal(t *testing.T) {
	env := setupTest(t)
	cookies := env.authenticate(t)

	badStems := []string{"../etc/passwd", "track.mp3", "track/../../etc/passwd"}
	for _, stem := range badStems {
		req, _ := http.NewRequest("GET", env.ts.URL+"/api/albums/"+env.albumSlug+"/stream/"+stem, nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		resp, err := env.ts.Client().Do(req)
		if err != nil {
			t.Fatalf("request for %q: %v", stem, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("stem %q should not return 200", stem)
		}
	}
}

func TestStreamTrackStemWithSpaces(t *testing.T) {
	env := setupTest(t)

	stem := "03 - Space Name (1)"
	if err := os.WriteFile(filepath.Join(env.albumDir, stem+".mp3"), []byte("fake-space-mp3"), 0644); err != nil {
		t.Fatalf("write track: %v", err)
	}

	// Add the new track to the album's track list via albumStore
	existingTracks, err := env.srv.albumStore.GetTracks(env.albumID)
	if err != nil {
		t.Fatalf("get tracks: %v", err)
	}
	existingTracks = append(existingTracks, albums.Track{Stem: stem, Title: "Space Name"})
	if err := env.srv.albumStore.SetTracks(env.albumID, existingTracks); err != nil {
		t.Fatalf("set tracks: %v", err)
	}

	cookies := env.authenticate(t)
	req, _ := http.NewRequest("GET", env.ts.URL+"/api/albums/"+env.albumSlug+"/stream/03%20-%20Space%20Name%20%281%29", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}

	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}
}

func TestLyrics(t *testing.T) {
	env := setupTest(t)
	cookies := env.authenticate(t)

	req, _ := http.NewRequest("GET", env.ts.URL+"/api/albums/"+env.albumSlug+"/lyrics/01-gathering", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("lyrics request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("lyrics status = %d, want 200", resp.StatusCode)
	}

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	if result["format"] != "lrc" {
		t.Errorf("lyrics format = %q, want lrc", result["format"])
	}
}

func TestCover(t *testing.T) {
	env := setupTest(t)
	cookies := env.authenticate(t)

	req, _ := http.NewRequest("GET", env.ts.URL+"/api/albums/"+env.albumSlug+"/cover", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("cover request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("cover status = %d, want 200", resp.StatusCode)
	}
	if etag := resp.Header.Get("ETag"); etag == "" {
		t.Error("cover should have ETag")
	}
}

func TestCoverSupportsJPEGFallback(t *testing.T) {
	env := setupTest(t)
	cookies := env.authenticate(t)

	if err := os.Remove(filepath.Join(env.albumDir, "cover.jpg")); err != nil {
		t.Fatalf("remove cover.jpg: %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.albumDir, "cover.jpeg"), []byte("fake-jpeg-2"), 0644); err != nil {
		t.Fatalf("write cover.jpeg: %v", err)
	}

	req, _ := http.NewRequest("GET", env.ts.URL+"/api/albums/"+env.albumSlug+"/cover", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("cover request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cover status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/jpeg") {
		t.Fatalf("content-type = %q, want image/jpeg", ct)
	}
}

func TestAdminAuth(t *testing.T) {
	env := setupTest(t)

	// Wrong password → 401
	body, _ := json.Marshal(map[string]string{
		"username": testAdminUsername,
		"password": "wrong-password",
	})
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/admin/api/auth", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", env.ts.URL)
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("admin auth request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password status = %d, want 401", resp.StatusCode)
	}

	// Correct credentials → 200
	cookies := env.authenticateAdmin(t)
	found := false
	for _, c := range cookies {
		if c.Name == "acetate_admin" && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Error("no admin session cookie set")
	}
}

func TestAdminSetupFlowWhenNoAdminUsers(t *testing.T) {
	env := setupTestWithBootstrap(t, "", "", "")

	statusResp, err := env.ts.Client().Get(env.ts.URL + "/admin/api/setup/status")
	if err != nil {
		t.Fatalf("setup status request: %v", err)
	}
	defer statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("setup status = %d, want 200", statusResp.StatusCode)
	}

	var statusPayload struct {
		NeedsSetup bool `json:"needs_setup"`
	}
	if err := json.NewDecoder(statusResp.Body).Decode(&statusPayload); err != nil {
		t.Fatalf("decode setup status: %v", err)
	}
	if !statusPayload.NeedsSetup {
		t.Fatal("expected needs_setup=true")
	}

	body, _ := json.Marshal(map[string]string{
		"username": "firstadmin",
		"password": "first-admin-pass-123",
	})
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/admin/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", env.ts.URL)
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("setup request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup status = %d, want 201", resp.StatusCode)
	}

	foundCookie := false
	for _, c := range resp.Cookies() {
		if c.Name == "acetate_admin" && c.Value != "" {
			foundCookie = true
			break
		}
	}
	if !foundCookie {
		t.Fatal("expected admin session cookie after setup")
	}

	statusResp2, err := env.ts.Client().Get(env.ts.URL + "/admin/api/setup/status")
	if err != nil {
		t.Fatalf("setup status request after create: %v", err)
	}
	defer statusResp2.Body.Close()
	if err := json.NewDecoder(statusResp2.Body).Decode(&statusPayload); err != nil {
		t.Fatalf("decode setup status after create: %v", err)
	}
	if statusPayload.NeedsSetup {
		t.Fatal("expected needs_setup=false after first admin creation")
	}
}

func TestAdminSetupRejectedWhenAlreadyConfigured(t *testing.T) {
	env := setupTest(t)

	body, _ := json.Marshal(map[string]string{
		"username": "newadmin",
		"password": "another-admin-pass-123",
	})
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/admin/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", env.ts.URL)
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("setup request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

func TestAdminRejectsMissingOrigin(t *testing.T) {
	env := setupTest(t)

	body, _ := json.Marshal(map[string]string{
		"username": testAdminUsername,
		"password": testAdminPassword,
	})
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/admin/api/auth", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("admin auth request: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestAdminUpdateTracksValidation(t *testing.T) {
	env := setupTest(t)
	adminCookies := env.authenticateAdmin(t)

	body, _ := json.Marshal(map[string]interface{}{
		"tracks": []map[string]string{
			{"stem": "../bad", "title": "Bad"},
			{"stem": "02-hollow", "title": "Hollow"},
		},
	})
	req, _ := http.NewRequest(http.MethodPut, env.ts.URL+fmt.Sprintf("/admin/api/albums/%d/tracks", env.albumID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", env.ts.URL)
	for _, c := range adminCookies {
		req.AddCookie(c)
	}
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("update tracks request: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAdminUploadCoverRejectsNonImage(t *testing.T) {
	env := setupTest(t)
	adminCookies := env.authenticateAdmin(t)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("cover", "cover.txt")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte("not-an-image")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	writer.Close()

	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+fmt.Sprintf("/admin/api/albums/%d/cover", env.albumID), &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Origin", env.ts.URL)
	for _, c := range adminCookies {
		req.AddCookie(c)
	}
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("upload cover request: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSPAFallback(t *testing.T) {
	env := setupTest(t)

	resp, err := env.ts.Client().Get(env.ts.URL + "/listening-room")
	if err != nil {
		t.Fatalf("SPA fallback request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body bytes.Buffer
	if _, err := body.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(body.String(), "id=\"gate\"") {
		t.Fatalf("expected index shell, body=%q", body.String())
	}
}

func TestSecurityHeadersDoNotAllowInlineStyles(t *testing.T) {
	env := setupTest(t)

	resp, err := env.ts.Client().Get(env.ts.URL + "/")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	csp := resp.Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("expected Content-Security-Policy header")
	}
	if strings.Contains(csp, "'unsafe-inline'") {
		t.Fatalf("unexpected unsafe-inline in CSP: %q", csp)
	}
}

func TestAnalyticsEndpoint(t *testing.T) {
	env := setupTest(t)
	cookies := env.authenticate(t)

	events := []map[string]interface{}{
		{"event_type": "play", "track_stem": "01-gathering"},
		{"event_type": "pause", "track_stem": "01-gathering", "position_seconds": 30.5},
	}
	body, _ := json.Marshal(events)

	req, _ := http.NewRequest("POST", env.ts.URL+"/api/albums/"+env.albumSlug+"/analytics", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}

	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("analytics request: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("analytics status = %d, want 204", resp.StatusCode)
	}
}

func TestLogout(t *testing.T) {
	env := setupTest(t)
	cookies := env.authenticate(t)

	req, _ := http.NewRequest("DELETE", env.ts.URL+"/api/auth", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("logout request: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("logout status = %d, want 200", resp.StatusCode)
	}

	// Session should be invalid now
	req, _ = http.NewRequest("GET", env.ts.URL+"/api/session", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err = env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("post-logout request: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("post-logout status = %d, want 401", resp.StatusCode)
	}
}

func TestAdminAuthWithHashedBootstrapPassword(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("hashed-admin-pass-123"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("generate hash: %v", err)
	}

	env := setupTestWithBootstrap(t, "admin2", "", string(hash))

	body, _ := json.Marshal(map[string]string{
		"username": "admin2",
		"password": "hashed-admin-pass-123",
	})
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/admin/api/auth", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", env.ts.URL)
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("admin auth request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestAdminUpdateOwnPassword(t *testing.T) {
	env := setupTest(t)
	adminCookies := env.authenticateAdmin(t)

	body, _ := json.Marshal(map[string]string{
		"current_password": testAdminPassword,
		"new_password":     "new-admin-pass-456",
	})
	req, _ := http.NewRequest(http.MethodPut, env.ts.URL+"/admin/api/admin-password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", env.ts.URL)
	for _, c := range adminCookies {
		req.AddCookie(c)
	}

	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("update admin password request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Old password should no longer work.
	oldAuthBody, _ := json.Marshal(map[string]string{
		"username": testAdminUsername,
		"password": testAdminPassword,
	})
	oldAuthReq, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/admin/api/auth", bytes.NewReader(oldAuthBody))
	oldAuthReq.Header.Set("Content-Type", "application/json")
	oldAuthReq.Header.Set("Origin", env.ts.URL)
	oldAuthResp, err := env.ts.Client().Do(oldAuthReq)
	if err != nil {
		t.Fatalf("old auth request: %v", err)
	}
	oldAuthResp.Body.Close()
	if oldAuthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password auth status = %d, want 401", oldAuthResp.StatusCode)
	}

	// New password should work.
	newAuthBody, _ := json.Marshal(map[string]string{
		"username": testAdminUsername,
		"password": "new-admin-pass-456",
	})
	newAuthReq, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/admin/api/auth", bytes.NewReader(newAuthBody))
	newAuthReq.Header.Set("Content-Type", "application/json")
	newAuthReq.Header.Set("Origin", env.ts.URL)
	newAuthResp, err := env.ts.Client().Do(newAuthReq)
	if err != nil {
		t.Fatalf("new auth request: %v", err)
	}
	newAuthResp.Body.Close()
	if newAuthResp.StatusCode != http.StatusOK {
		t.Fatalf("new password auth status = %d, want 200", newAuthResp.StatusCode)
	}
}

func TestAdminAuthLockoutAfterRepeatedFailures(t *testing.T) {
	env := setupTest(t)

	for i := 0; i < 5; i++ {
		_, _, status := env.authenticateAdminAs(t, testAdminUsername, "wrong-password")
		if status != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", i+1, status)
		}
	}

	_, _, status := env.authenticateAdminAs(t, testAdminUsername, "wrong-password")
	if status != http.StatusTooManyRequests {
		t.Fatalf("locked attempt status = %d, want 429", status)
	}

	_, _, status = env.authenticateAdminAs(t, testAdminUsername, testAdminPassword)
	if status != http.StatusTooManyRequests {
		t.Fatalf("locked correct-password attempt status = %d, want 429", status)
	}
}

func TestAdminUserManagementAndForcedPasswordResetFlow(t *testing.T) {
	env := setupTest(t)
	adminCookies := env.authenticateAdmin(t)

	var baseAdminID int64
	if err := env.srv.db.QueryRow("SELECT id FROM admin_users WHERE username = ?", testAdminUsername).Scan(&baseAdminID); err != nil {
		t.Fatalf("query base admin id: %v", err)
	}

	// Cannot deactivate own account.
	selfDeactivateBody, _ := json.Marshal(map[string]interface{}{
		"is_active": false,
	})
	selfDeactivateReq, _ := http.NewRequest(http.MethodPut, env.ts.URL+"/admin/api/admin-users/"+strconv.FormatInt(baseAdminID, 10), bytes.NewReader(selfDeactivateBody))
	selfDeactivateReq.Header.Set("Content-Type", "application/json")
	selfDeactivateReq.Header.Set("Origin", env.ts.URL)
	for _, c := range adminCookies {
		selfDeactivateReq.AddCookie(c)
	}
	selfDeactivateResp, err := env.ts.Client().Do(selfDeactivateReq)
	if err != nil {
		t.Fatalf("self deactivate request: %v", err)
	}
	selfDeactivateResp.Body.Close()
	if selfDeactivateResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("self deactivate status = %d, want 400", selfDeactivateResp.StatusCode)
	}

	// Create second admin and require password reset.
	createBody, _ := json.Marshal(map[string]interface{}{
		"username":               "opsadmin",
		"password":               "ops-admin-pass-123",
		"require_password_reset": true,
	})
	createReq, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/admin/api/admin-users", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Origin", env.ts.URL)
	for _, c := range adminCookies {
		createReq.AddCookie(c)
	}
	createResp, err := env.ts.Client().Do(createReq)
	if err != nil {
		t.Fatalf("create admin user request: %v", err)
	}
	if createResp.StatusCode != http.StatusCreated {
		createResp.Body.Close()
		t.Fatalf("create admin user status = %d, want 201", createResp.StatusCode)
	}
	var created struct {
		ID                   int64  `json:"id"`
		Username             string `json:"username"`
		RequirePasswordReset bool   `json:"require_password_reset"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		createResp.Body.Close()
		t.Fatalf("decode created admin user: %v", err)
	}
	createResp.Body.Close()
	if created.ID <= 0 || created.Username != "opsadmin" || !created.RequirePasswordReset {
		t.Fatalf("unexpected created admin user payload: %+v", created)
	}

	// New admin is forced into password-reset mode.
	opsCookies, authPayload, status := env.authenticateAdminAs(t, "opsadmin", "ops-admin-pass-123")
	if status != http.StatusOK {
		t.Fatalf("ops admin login status = %d, want 200", status)
	}
	if resetRaw, ok := authPayload["password_reset_required"].(bool); !ok || !resetRaw {
		t.Fatalf("expected password_reset_required=true, got payload=%v", authPayload)
	}

	analyticsReq, _ := http.NewRequest(http.MethodGet, env.ts.URL+fmt.Sprintf("/admin/api/albums/%d/analytics", env.albumID), nil)
	for _, c := range opsCookies {
		analyticsReq.AddCookie(c)
	}
	analyticsResp, err := env.ts.Client().Do(analyticsReq)
	if err != nil {
		t.Fatalf("analytics request in reset mode: %v", err)
	}
	analyticsResp.Body.Close()
	if analyticsResp.StatusCode != http.StatusForbidden {
		t.Fatalf("analytics status in reset mode = %d, want 403", analyticsResp.StatusCode)
	}

	configReq, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/admin/api/config", nil)
	for _, c := range opsCookies {
		configReq.AddCookie(c)
	}
	configResp, err := env.ts.Client().Do(configReq)
	if err != nil {
		t.Fatalf("config request in reset mode: %v", err)
	}
	if configResp.StatusCode != http.StatusOK {
		configResp.Body.Close()
		t.Fatalf("config status in reset mode = %d, want 200", configResp.StatusCode)
	}
	var configPayload struct {
		PasswordResetRequired bool `json:"password_reset_required"`
	}
	if err := json.NewDecoder(configResp.Body).Decode(&configPayload); err != nil {
		configResp.Body.Close()
		t.Fatalf("decode config payload: %v", err)
	}
	configResp.Body.Close()
	if !configPayload.PasswordResetRequired {
		t.Fatal("expected password_reset_required=true in config")
	}

	updatePassBody, _ := json.Marshal(map[string]string{
		"current_password": "ops-admin-pass-123",
		"new_password":     "ops-admin-pass-456",
	})
	updatePassReq, _ := http.NewRequest(http.MethodPut, env.ts.URL+"/admin/api/admin-password", bytes.NewReader(updatePassBody))
	updatePassReq.Header.Set("Content-Type", "application/json")
	updatePassReq.Header.Set("Origin", env.ts.URL)
	for _, c := range opsCookies {
		updatePassReq.AddCookie(c)
	}
	updatePassResp, err := env.ts.Client().Do(updatePassReq)
	if err != nil {
		t.Fatalf("update ops admin password request: %v", err)
	}
	updatePassResp.Body.Close()
	if updatePassResp.StatusCode != http.StatusOK {
		t.Fatalf("update ops admin password status = %d, want 200", updatePassResp.StatusCode)
	}

	// Session is revoked after password change.
	oldSessionConfigReq, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/admin/api/config", nil)
	for _, c := range opsCookies {
		oldSessionConfigReq.AddCookie(c)
	}
	oldSessionConfigResp, err := env.ts.Client().Do(oldSessionConfigReq)
	if err != nil {
		t.Fatalf("old-session config request: %v", err)
	}
	oldSessionConfigResp.Body.Close()
	if oldSessionConfigResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old-session config status = %d, want 401", oldSessionConfigResp.StatusCode)
	}

	newOpsCookies, newAuthPayload, status := env.authenticateAdminAs(t, "opsadmin", "ops-admin-pass-456")
	if status != http.StatusOK {
		t.Fatalf("ops admin relogin status = %d, want 200", status)
	}
	if resetRaw, ok := newAuthPayload["password_reset_required"].(bool); !ok || resetRaw {
		t.Fatalf("expected password_reset_required=false, got payload=%v", newAuthPayload)
	}

	analyticsReq2, _ := http.NewRequest(http.MethodGet, env.ts.URL+fmt.Sprintf("/admin/api/albums/%d/analytics", env.albumID), nil)
	for _, c := range newOpsCookies {
		analyticsReq2.AddCookie(c)
	}
	analyticsResp2, err := env.ts.Client().Do(analyticsReq2)
	if err != nil {
		t.Fatalf("analytics request after reset: %v", err)
	}
	analyticsResp2.Body.Close()
	if analyticsResp2.StatusCode != http.StatusOK {
		t.Fatalf("analytics status after reset = %d, want 200", analyticsResp2.StatusCode)
	}

	// Active admin can deactivate another account.
	deactivateBody, _ := json.Marshal(map[string]interface{}{
		"is_active": false,
	})
	deactivateReq, _ := http.NewRequest(http.MethodPut, env.ts.URL+"/admin/api/admin-users/"+strconv.FormatInt(created.ID, 10), bytes.NewReader(deactivateBody))
	deactivateReq.Header.Set("Content-Type", "application/json")
	deactivateReq.Header.Set("Origin", env.ts.URL)
	for _, c := range adminCookies {
		deactivateReq.AddCookie(c)
	}
	deactivateResp, err := env.ts.Client().Do(deactivateReq)
	if err != nil {
		t.Fatalf("deactivate ops admin request: %v", err)
	}
	deactivateResp.Body.Close()
	if deactivateResp.StatusCode != http.StatusOK {
		t.Fatalf("deactivate ops admin status = %d, want 200", deactivateResp.StatusCode)
	}

	_, _, status = env.authenticateAdminAs(t, "opsadmin", "ops-admin-pass-456")
	if status != http.StatusUnauthorized {
		t.Fatalf("disabled admin login status = %d, want 401", status)
	}
}

func TestAdminConfigEndpoint(t *testing.T) {
	env := setupTest(t)
	adminCookies := env.authenticateAdmin(t)

	req, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/admin/api/config", nil)
	for _, c := range adminCookies {
		req.AddCookie(c)
	}

	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("config request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var payload map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode config payload: %v", err)
	}

	if _, ok := payload["password_hash"]; ok {
		t.Fatalf("password_hash should not be exposed in config payload: %+v", payload)
	}

	albumCount, ok := payload["album_count"].(float64)
	if !ok || albumCount < 1 {
		t.Fatalf("expected album_count >= 1, got payload=%+v", payload)
	}
}

func TestAdminFounderCannotBeDeactivatedByAnotherAdmin(t *testing.T) {
	env := setupTest(t)
	adminCookies := env.authenticateAdmin(t)

	var founderID int64
	if err := env.srv.db.QueryRow("SELECT id FROM admin_users WHERE username = ?", testAdminUsername).Scan(&founderID); err != nil {
		t.Fatalf("query founder admin id: %v", err)
	}

	createBody, _ := json.Marshal(map[string]interface{}{
		"username":               "sreadmin",
		"password":               "sre-admin-pass-123",
		"require_password_reset": false,
	})
	createReq, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/admin/api/admin-users", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Origin", env.ts.URL)
	for _, c := range adminCookies {
		createReq.AddCookie(c)
	}
	createResp, err := env.ts.Client().Do(createReq)
	if err != nil {
		t.Fatalf("create admin user request: %v", err)
	}
	createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create admin status = %d, want 201", createResp.StatusCode)
	}

	otherCookies, _, status := env.authenticateAdminAs(t, "sreadmin", "sre-admin-pass-123")
	if status != http.StatusOK {
		t.Fatalf("secondary admin auth status = %d, want 200", status)
	}

	deactivateFounderBody, _ := json.Marshal(map[string]interface{}{
		"is_active": false,
	})
	deactivateFounderReq, _ := http.NewRequest(http.MethodPut, env.ts.URL+"/admin/api/admin-users/"+strconv.FormatInt(founderID, 10), bytes.NewReader(deactivateFounderBody))
	deactivateFounderReq.Header.Set("Content-Type", "application/json")
	deactivateFounderReq.Header.Set("Origin", env.ts.URL)
	for _, c := range otherCookies {
		deactivateFounderReq.AddCookie(c)
	}

	deactivateFounderResp, err := env.ts.Client().Do(deactivateFounderReq)
	if err != nil {
		t.Fatalf("founder deactivate request: %v", err)
	}
	defer deactivateFounderResp.Body.Close()
	if deactivateFounderResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("founder deactivate status = %d, want 400", deactivateFounderResp.StatusCode)
	}

	var payload map[string]string
	if err := json.NewDecoder(deactivateFounderResp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode founder deactivate payload: %v", err)
	}
	if !strings.Contains(strings.ToLower(payload["error"]), "original admin") {
		t.Fatalf("unexpected founder protection error payload: %+v", payload)
	}
}

func TestAdminCanRenameAnotherUser(t *testing.T) {
	env := setupTest(t)
	adminCookies := env.authenticateAdmin(t)

	createBody, _ := json.Marshal(map[string]interface{}{
		"username":               "oldname",
		"password":               "rename-pass-123",
		"require_password_reset": false,
	})
	createReq, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/admin/api/admin-users", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Origin", env.ts.URL)
	for _, c := range adminCookies {
		createReq.AddCookie(c)
	}
	createResp, err := env.ts.Client().Do(createReq)
	if err != nil {
		t.Fatalf("create admin user request: %v", err)
	}
	if createResp.StatusCode != http.StatusCreated {
		createResp.Body.Close()
		t.Fatalf("create admin status = %d, want 201", createResp.StatusCode)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		createResp.Body.Close()
		t.Fatalf("decode created admin: %v", err)
	}
	createResp.Body.Close()
	if created.ID <= 0 {
		t.Fatalf("invalid created admin id: %+v", created)
	}

	renameBody, _ := json.Marshal(map[string]interface{}{
		"username": "newname",
	})
	renameReq, _ := http.NewRequest(http.MethodPut, env.ts.URL+"/admin/api/admin-users/"+strconv.FormatInt(created.ID, 10), bytes.NewReader(renameBody))
	renameReq.Header.Set("Content-Type", "application/json")
	renameReq.Header.Set("Origin", env.ts.URL)
	for _, c := range adminCookies {
		renameReq.AddCookie(c)
	}

	renameResp, err := env.ts.Client().Do(renameReq)
	if err != nil {
		t.Fatalf("rename admin request: %v", err)
	}
	renameResp.Body.Close()
	if renameResp.StatusCode != http.StatusOK {
		t.Fatalf("rename admin status = %d, want 200", renameResp.StatusCode)
	}

	_, _, status := env.authenticateAdminAs(t, "oldname", "rename-pass-123")
	if status != http.StatusUnauthorized {
		t.Fatalf("old username auth status = %d, want 401", status)
	}

	_, _, status = env.authenticateAdminAs(t, "newname", "rename-pass-123")
	if status != http.StatusOK {
		t.Fatalf("new username auth status = %d, want 200", status)
	}
}

func TestAdminAnalyticsFiltersByStem(t *testing.T) {
	env := setupTest(t)
	adminCookies := env.authenticateAdmin(t)

	_, _ = env.srv.db.Exec("INSERT INTO events (session_id, event_type, track_stem, created_at) VALUES ('s1', 'play', '01-gathering', datetime('now'))")
	_, _ = env.srv.db.Exec("INSERT INTO events (session_id, event_type, track_stem, created_at) VALUES ('s2', 'play', '02-hollow', datetime('now'))")

	req, _ := http.NewRequest(http.MethodGet, env.ts.URL+fmt.Sprintf("/admin/api/albums/%d/analytics?stems=01-gathering", env.albumID), nil)
	for _, c := range adminCookies {
		req.AddCookie(c)
	}

	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("analytics request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var payload struct {
		Tracks []struct {
			Stem string `json:"stem"`
		} `json:"tracks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Tracks) != 1 || payload.Tracks[0].Stem != "01-gathering" {
		t.Fatalf("unexpected tracks payload: %+v", payload.Tracks)
	}
}

func TestAdminReconcilePreview(t *testing.T) {
	env := setupTest(t)
	adminCookies := env.authenticateAdmin(t)

	if err := os.WriteFile(filepath.Join(env.albumDir, "03-new-song.mp3"), []byte("fake"), 0644); err != nil {
		t.Fatalf("write new song: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, env.ts.URL+fmt.Sprintf("/admin/api/albums/%d/reconcile", env.albumID), nil)
	for _, c := range adminCookies {
		req.AddCookie(c)
	}

	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("reconcile request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var payload struct {
		AlbumOnly []struct {
			Stem string `json:"stem"`
		} `json:"album_only"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.AlbumOnly) != 1 || payload.AlbumOnly[0].Stem != "03-new-song" {
		t.Fatalf("unexpected album_only payload: %+v", payload.AlbumOnly)
	}
}

func TestAdminOpsHealth(t *testing.T) {
	env := setupTest(t)
	adminCookies := env.authenticateAdmin(t)

	req, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/admin/api/ops/health", nil)
	for _, c := range adminCookies {
		req.AddCookie(c)
	}

	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("health request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var payload map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != "ok" {
		t.Fatalf("unexpected health status: %v", payload["status"])
	}
}

func TestAdminExportEventsCSV(t *testing.T) {
	env := setupTest(t)
	adminCookies := env.authenticateAdmin(t)
	listenerCookies := env.authenticate(t)

	events := []map[string]interface{}{
		{"event_type": "play", "track_stem": "01-gathering"},
	}
	body, _ := json.Marshal(events)
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/albums/"+env.albumSlug+"/analytics", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range listenerCookies {
		req.AddCookie(c)
	}
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("analytics request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("analytics status = %d, want 204", resp.StatusCode)
	}

	exportReq, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/admin/api/export/events?format=csv", nil)
	for _, c := range adminCookies {
		exportReq.AddCookie(c)
	}
	exportResp, err := env.ts.Client().Do(exportReq)
	if err != nil {
		t.Fatalf("export request: %v", err)
	}
	defer exportResp.Body.Close()

	if exportResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", exportResp.StatusCode)
	}
	if ct := exportResp.Header.Get("Content-Type"); !strings.Contains(ct, "text/csv") {
		t.Fatalf("content-type = %q, want text/csv", ct)
	}

	data, err := io.ReadAll(exportResp.Body)
	if err != nil {
		t.Fatalf("read export body: %v", err)
	}
	bodyText := string(data)
	if !strings.Contains(bodyText, "event_type") || !strings.Contains(bodyText, "play") {
		t.Fatalf("unexpected csv body: %q", bodyText)
	}
}

// --- Multi-album tests ---

func TestMultiAlbumAuthReturnsAlbumList(t *testing.T) {
	env := setupTest(t)

	// Create a second album and link the same password to both
	album2Dir := t.TempDir()
	os.WriteFile(filepath.Join(album2Dir, "track-a.mp3"), []byte("fake"), 0644)

	alb2, err := env.srv.albumStore.CreateAlbum("Second Album", "Artist 2", album2Dir)
	if err != nil {
		t.Fatalf("create album 2: %v", err)
	}
	env.srv.albumStore.SetTracks(alb2.ID, []albums.Track{
		{Stem: "track-a", Title: "Track A"},
	})

	// Create a password that grants access to both albums
	pw, err := env.srv.albumStore.CreatePassword("Multi", "multipass", []int64{env.albumID, alb2.ID})
	if err != nil {
		t.Fatalf("create multi password: %v", err)
	}
	_ = pw

	// Auth with multi-album password
	body, _ := json.Marshal(map[string]string{"passphrase": "multipass"})
	resp, err := env.ts.Client().Post(env.ts.URL+"/api/auth", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auth status = %d, want 200", resp.StatusCode)
	}

	var authResp struct {
		Status string `json:"status"`
		Albums []struct {
			Slug  string `json:"slug"`
			Title string `json:"title"`
		} `json:"albums"`
	}
	json.NewDecoder(resp.Body).Decode(&authResp)

	if len(authResp.Albums) != 2 {
		t.Fatalf("expected 2 albums, got %d", len(authResp.Albums))
	}
}

func TestMultiAlbumAccessDeniedForUnauthorizedAlbum(t *testing.T) {
	env := setupTest(t)

	// Create a second album NOT linked to the default password
	album2Dir := t.TempDir()
	os.WriteFile(filepath.Join(album2Dir, "secret-track.mp3"), []byte("fake"), 0644)

	alb2, err := env.srv.albumStore.CreateAlbum("Secret Album", "Secret Artist", album2Dir)
	if err != nil {
		t.Fatalf("create secret album: %v", err)
	}
	env.srv.albumStore.SetTracks(alb2.ID, []albums.Track{
		{Stem: "secret-track", Title: "Secret Track"},
	})

	// Auth with default password (only has access to album 1)
	cookies := env.authenticate(t)

	// Try to access the secret album's tracks
	req, _ := http.NewRequest("GET", env.ts.URL+"/api/albums/"+alb2.Slug+"/tracks", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("tracks request: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthorized album access status = %d, want 403", resp.StatusCode)
	}
}

func TestMultiAlbumStreamFromCorrectDirectory(t *testing.T) {
	env := setupTest(t)

	// Create a second album with its own tracks
	album2Dir := t.TempDir()
	os.WriteFile(filepath.Join(album2Dir, "unique-track.mp3"), []byte("album2-audio-data"), 0644)

	alb2, err := env.srv.albumStore.CreateAlbum("Album Two", "Artist Two", album2Dir)
	if err != nil {
		t.Fatalf("create album 2: %v", err)
	}
	env.srv.albumStore.SetTracks(alb2.ID, []albums.Track{
		{Stem: "unique-track", Title: "Unique"},
	})

	// Create password for album 2
	env.srv.albumStore.CreatePassword("PW2", "album2pass", []int64{alb2.ID})

	// Auth with album 2 password
	body, _ := json.Marshal(map[string]string{"passphrase": "album2pass"})
	resp, err := env.ts.Client().Post(env.ts.URL+"/api/auth", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	resp.Body.Close()
	cookies := resp.Cookies()

	// Stream from album 2
	req, _ := http.NewRequest("GET", env.ts.URL+"/api/albums/"+alb2.Slug+"/stream/unique-track", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	streamResp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer streamResp.Body.Close()

	if streamResp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", streamResp.StatusCode)
	}

	streamBody, _ := io.ReadAll(streamResp.Body)
	if string(streamBody) != "album2-audio-data" {
		t.Fatalf("stream returned wrong data: %q", string(streamBody))
	}

	// Verify can't access album 1's tracks with album 2's password
	req2, _ := http.NewRequest("GET", env.ts.URL+"/api/albums/"+env.albumSlug+"/tracks", nil)
	for _, c := range cookies {
		req2.AddCookie(c)
	}
	resp2, err := env.ts.Client().Do(req2)
	if err != nil {
		t.Fatalf("cross-album request: %v", err)
	}
	resp2.Body.Close()

	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-album access status = %d, want 403", resp2.StatusCode)
	}
}

func TestAdminAlbumCRUD(t *testing.T) {
	env := setupTest(t)
	adminCookies := env.authenticateAdmin(t)

	// List albums
	req, _ := http.NewRequest("GET", env.ts.URL+"/admin/api/albums", nil)
	for _, c := range adminCookies {
		req.AddCookie(c)
	}
	resp, _ := env.ts.Client().Do(req)
	var listResp struct {
		Albums []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"albums"`
	}
	json.NewDecoder(resp.Body).Decode(&listResp)
	resp.Body.Close()
	if len(listResp.Albums) != 1 {
		t.Fatalf("expected 1 album, got %d", len(listResp.Albums))
	}

	// Create a new album
	newAlbumDir := t.TempDir()
	createBody, _ := json.Marshal(map[string]string{
		"title":      "New Album",
		"artist":     "New Artist",
		"album_path": newAlbumDir,
	})
	createReq, _ := http.NewRequest("POST", env.ts.URL+"/admin/api/albums", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Origin", env.ts.URL)
	for _, c := range adminCookies {
		createReq.AddCookie(c)
	}
	createResp, _ := env.ts.Client().Do(createReq)
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create album status = %d, want 201", createResp.StatusCode)
	}
	var created struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	}
	json.NewDecoder(createResp.Body).Decode(&created)
	createResp.Body.Close()

	if created.ID == 0 || created.Slug == "" {
		t.Fatalf("expected created album with ID and slug, got %+v", created)
	}

	// Delete the album
	deleteReq, _ := http.NewRequest("DELETE", env.ts.URL+"/admin/api/albums/"+strconv.FormatInt(created.ID, 10), nil)
	deleteReq.Header.Set("Origin", env.ts.URL)
	for _, c := range adminCookies {
		deleteReq.AddCookie(c)
	}
	deleteResp, _ := env.ts.Client().Do(deleteReq)
	deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusOK {
		t.Fatalf("delete album status = %d, want 200", deleteResp.StatusCode)
	}

	// Verify it's gone
	req2, _ := http.NewRequest("GET", env.ts.URL+"/admin/api/albums", nil)
	for _, c := range adminCookies {
		req2.AddCookie(c)
	}
	resp2, _ := env.ts.Client().Do(req2)
	json.NewDecoder(resp2.Body).Decode(&listResp)
	resp2.Body.Close()
	if len(listResp.Albums) != 1 {
		t.Fatalf("expected 1 album after delete, got %d", len(listResp.Albums))
	}
}

func TestAdminPasswordCRUD(t *testing.T) {
	env := setupTest(t)
	adminCookies := env.authenticateAdmin(t)

	// Create a password linked to the test album
	createBody, _ := json.Marshal(map[string]interface{}{
		"label":      "Test PW",
		"passphrase": "new-listener-pass",
		"album_ids":  []int64{env.albumID},
	})
	createReq, _ := http.NewRequest("POST", env.ts.URL+"/admin/api/passwords", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Origin", env.ts.URL)
	for _, c := range adminCookies {
		createReq.AddCookie(c)
	}
	createResp, _ := env.ts.Client().Do(createReq)
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create password status = %d, want 201", createResp.StatusCode)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	json.NewDecoder(createResp.Body).Decode(&created)
	createResp.Body.Close()

	// Verify the new password works for auth
	authBody, _ := json.Marshal(map[string]string{"passphrase": "new-listener-pass"})
	authResp, _ := env.ts.Client().Post(env.ts.URL+"/api/auth", "application/json", bytes.NewReader(authBody))
	if authResp.StatusCode != http.StatusOK {
		t.Fatalf("auth with new password status = %d, want 200", authResp.StatusCode)
	}
	authResp.Body.Close()

	// List passwords
	listReq, _ := http.NewRequest("GET", env.ts.URL+"/admin/api/passwords", nil)
	for _, c := range adminCookies {
		listReq.AddCookie(c)
	}
	listResp, _ := env.ts.Client().Do(listReq)
	var pwList struct {
		Passwords []struct {
			ID    int64  `json:"id"`
			Label string `json:"label"`
		} `json:"passwords"`
	}
	json.NewDecoder(listResp.Body).Decode(&pwList)
	listResp.Body.Close()
	if len(pwList.Passwords) != 2 { // Default + Test PW
		t.Fatalf("expected 2 passwords, got %d", len(pwList.Passwords))
	}

	// Delete the new password
	deleteReq, _ := http.NewRequest("DELETE", env.ts.URL+fmt.Sprintf("/admin/api/passwords/%d", created.ID), nil)
	deleteReq.Header.Set("Origin", env.ts.URL)
	for _, c := range adminCookies {
		deleteReq.AddCookie(c)
	}
	deleteResp, _ := env.ts.Client().Do(deleteReq)
	deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusOK {
		t.Fatalf("delete password status = %d, want 200", deleteResp.StatusCode)
	}

	// Verify deleted password no longer works
	authResp2, _ := env.ts.Client().Post(env.ts.URL+"/api/auth", "application/json", bytes.NewReader(authBody))
	authResp2.Body.Close()
	if authResp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("deleted password auth status = %d, want 401", authResp2.StatusCode)
	}
}

func TestSessionWithoutPasswordIDDenied(t *testing.T) {
	env := setupTest(t)

	// Manually insert a session without password_id to simulate a legacy session
	_, err := env.srv.db.Exec(
		"INSERT INTO sessions (id, started_at, last_seen_at, ip_hash) VALUES (?, ?, ?, ?)",
		"deadbeef"+strings.Repeat("00", 28), time.Now().UTC(), time.Now().UTC(), "test",
	)
	if err != nil {
		t.Fatalf("insert legacy session: %v", err)
	}

	// Try to access an album with the legacy session (no password_id)
	req, _ := http.NewRequest("GET", env.ts.URL+"/api/albums/"+env.albumSlug+"/tracks", nil)
	req.AddCookie(&http.Cookie{Name: "acetate_session", Value: "deadbeef" + strings.Repeat("00", 28)})
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("legacy session access status = %d, want 403", resp.StatusCode)
	}
}
