package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"acetate/internal/config"
	"acetate/internal/database"

	"golang.org/x/crypto/bcrypt"
)

type testEnv struct {
	srv      *Server
	ts       *httptest.Server
	albumDir string
	dataDir  string
}

func setupTest(t *testing.T) *testEnv {
	return setupTestWithAdmin(t, "test-admin-token", "")
}

func setupTestWithAdmin(t *testing.T, adminToken, adminTokenHash string) *testEnv {
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

	// Create config with a password
	hash, _ := bcrypt.GenerateFromPassword([]byte("testpass"), bcrypt.MinCost)
	cfgMgr, err := config.NewManager(dataDir, albumDir)
	if err != nil {
		t.Fatalf("config manager: %v", err)
	}
	cfg := cfgMgr.Get()
	cfg.Password = string(hash)
	cfgMgr.Update(cfg)

	srv := New(Config{
		ListenAddr:             ":0",
		AlbumPath:              albumDir,
		DataPath:               dataDir,
		AdminToken:             adminToken,
		AdminTokenHash:         adminTokenHash,
		AnalyticsRetentionDays: 0,
		MaintenanceInterval:    time.Hour,
		DB:                     db,
		ConfigMgr:              cfgMgr,
	})

	ts := httptest.NewServer(srv.routes())
	t.Cleanup(func() {
		ts.Close()
		srv.collector.Close()
		srv.sessions.Close()
		srv.rateLimiter.Close()
		srv.cfIPs.Close()
	})

	return &testEnv{srv: srv, ts: ts, albumDir: albumDir, dataDir: dataDir}
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

	return resp.Cookies()
}

func (env *testEnv) authenticateAdmin(t *testing.T) []*http.Cookie {
	t.Helper()

	body, _ := json.Marshal(map[string]string{"token": "test-admin-token"})
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
	resp, err := env.ts.Client().Get(env.ts.URL + "/api/tracks")
	if err != nil {
		t.Fatalf("tracks request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-session tracks status = %d, want 401", resp.StatusCode)
	}

	// With session → 200
	cookies := env.authenticate(t)
	req, _ := http.NewRequest("GET", env.ts.URL+"/api/tracks", nil)
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

	req, _ := http.NewRequest("GET", env.ts.URL+"/api/stream/01-gathering", nil)
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
		req, _ := http.NewRequest("GET", env.ts.URL+"/api/stream/"+stem, nil)
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

	cfg := env.srv.config.Get()
	cfg.Tracks = append(cfg.Tracks, config.Track{Stem: stem, Title: "Space Name"})
	if err := env.srv.config.Update(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	cookies := env.authenticate(t)
	req, _ := http.NewRequest("GET", env.ts.URL+"/api/stream/03%20-%20Space%20Name%20%281%29", nil)
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

	req, _ := http.NewRequest("GET", env.ts.URL+"/api/lyrics/01-gathering", nil)
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

	req, _ := http.NewRequest("GET", env.ts.URL+"/api/cover", nil)
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

	req, _ := http.NewRequest("GET", env.ts.URL+"/api/cover", nil)
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

	// Wrong token → 401
	body, _ := json.Marshal(map[string]string{"token": "wrong"})
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/admin/api/auth", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", env.ts.URL)
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("admin auth request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token status = %d, want 401", resp.StatusCode)
	}

	// Correct token → 200
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

func TestAdminRejectsMissingOrigin(t *testing.T) {
	env := setupTest(t)

	body, _ := json.Marshal(map[string]string{"token": "test-admin-token"})
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
	req, _ := http.NewRequest(http.MethodPut, env.ts.URL+"/admin/api/tracks", bytes.NewReader(body))
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

	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/admin/api/cover", &body)
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

func TestAnalyticsEndpoint(t *testing.T) {
	env := setupTest(t)
	cookies := env.authenticate(t)

	events := []map[string]interface{}{
		{"event_type": "play", "track_stem": "01-gathering"},
		{"event_type": "pause", "track_stem": "01-gathering", "position_seconds": 30.5},
	}
	body, _ := json.Marshal(events)

	req, _ := http.NewRequest("POST", env.ts.URL+"/api/analytics", bytes.NewReader(body))
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
	req, _ = http.NewRequest("GET", env.ts.URL+"/api/tracks", nil)
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

func TestAdminAuthWithHashedToken(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("hashed-admin-token"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("generate hash: %v", err)
	}

	env := setupTestWithAdmin(t, "", string(hash))

	body, _ := json.Marshal(map[string]string{"token": "hashed-admin-token"})
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

func TestAdminAnalyticsFiltersByStem(t *testing.T) {
	env := setupTest(t)
	adminCookies := env.authenticateAdmin(t)

	_, _ = env.srv.db.Exec("INSERT INTO events (session_id, event_type, track_stem, created_at) VALUES ('s1', 'play', '01-gathering', datetime('now'))")
	_, _ = env.srv.db.Exec("INSERT INTO events (session_id, event_type, track_stem, created_at) VALUES ('s2', 'play', '02-hollow', datetime('now'))")

	req, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/admin/api/analytics?stems=01-gathering", nil)
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

	req, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/admin/api/reconcile", nil)
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
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/analytics", bytes.NewReader(body))
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
