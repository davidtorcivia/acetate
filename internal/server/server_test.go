package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
		ListenAddr: ":0",
		AlbumPath:  albumDir,
		DataPath:   dataDir,
		AdminToken: "test-admin-token",
		DB:         db,
		ConfigMgr:  cfgMgr,
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
	resp, err := env.ts.Client().Post(env.ts.URL+"/admin/api/auth", "application/json", bytes.NewReader(body))
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

func TestAdminAuth(t *testing.T) {
	env := setupTest(t)

	// Wrong token → 401
	body, _ := json.Marshal(map[string]string{"token": "wrong"})
	resp, err := env.ts.Client().Post(env.ts.URL+"/admin/api/auth", "application/json", bytes.NewReader(body))
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
