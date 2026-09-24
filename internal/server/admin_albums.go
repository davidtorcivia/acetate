package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"acetate/internal/album"
	"acetate/internal/albums"
)

// maxPassphraseLen is bcrypt's input limit; longer passphrases fail to hash.
const maxPassphraseLen = 72

// checkAlbumIDs answers 400 and returns false when ids names a missing album.
func (s *Server) checkAlbumIDs(w http.ResponseWriter, ids []int64) bool {
	unknown, err := s.albumStore.UnknownAlbumIDs(ids)
	if err != nil {
		log.Printf("check album ids error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return false
	}
	if len(unknown) > 0 {
		jsonError(w, fmt.Sprintf("unknown album id %d", unknown[0]), http.StatusBadRequest)
		return false
	}
	return true
}

func validatePassphrase(p string) error {
	if p == "" {
		return errors.New("passphrase is required")
	}
	if len(p) > maxPassphraseLen {
		return fmt.Errorf("passphrase must be at most %d bytes", maxPassphraseLen)
	}
	return nil
}

// resolveAlbumPath resolves a folder name (or path) against ALBUM_PATH and
// refuses anything outside it. Without a base path any directory is allowed.
func (s *Server) resolveAlbumPath(p string) (string, error) {
	if s.albumBasePath != "" {
		base, err := filepath.Abs(s.albumBasePath)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(base, p)
		}
		rel, err := filepath.Rel(base, filepath.Clean(p))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", errors.New("album_path must be inside the album directory")
		}
	}
	p = filepath.Clean(p)
	if info, err := os.Stat(p); err != nil || !info.IsDir() {
		return "", errors.New("album_path is not a readable directory")
	}
	return p, nil
}

// --- Album CRUD ---

func (s *Server) handleAdminListAlbums(w http.ResponseWriter, r *http.Request) {
	allAlbums, err := s.albumStore.ListAlbums()
	if err != nil {
		log.Printf("list albums error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	type albumResp struct {
		ID               int64  `json:"id"`
		Slug             string `json:"slug"`
		Title            string `json:"title"`
		Artist           string `json:"artist"`
		AlbumPath        string `json:"album_path"`
		DownloadsEnabled bool   `json:"downloads_enabled"`
		TrackCount       int    `json:"track_count"`
		CreatedAt        string `json:"created_at"`
		UpdatedAt        string `json:"updated_at"`
	}

	trackCounts, _ := s.albumStore.GetAllTrackCounts()

	resp := make([]albumResp, 0, len(allAlbums))
	for _, a := range allAlbums {
		resp = append(resp, albumResp{
			ID:               a.ID,
			Slug:             a.Slug,
			Title:            a.Title,
			Artist:           a.Artist,
			AlbumPath:        a.AlbumPath,
			DownloadsEnabled: a.DownloadsEnabled,
			TrackCount:       trackCounts[a.ID],
			CreatedAt:        a.CreatedAt,
			UpdatedAt:        a.UpdatedAt,
		})
	}

	jsonOK(w, map[string]interface{}{"albums": resp})
}

func (s *Server) handleAdminCreateAlbum(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title     string `json:"title"`
		Artist    string `json:"artist"`
		AlbumPath string `json:"album_path"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	title := trimAndCollapseSpaces(req.Title)
	artist := trimAndCollapseSpaces(req.Artist)
	albumPath := strings.TrimSpace(req.AlbumPath)

	if title == "" || albumPath == "" {
		jsonError(w, "title and album_path are required", http.StatusBadRequest)
		return
	}
	if len(title) > 256 || len(artist) > 256 {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	albumPath, err := s.resolveAlbumPath(albumPath)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	alb, err := s.albumStore.CreateAlbum(title, artist, albumPath)
	if err != nil {
		log.Printf("create album error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Import the tracks sitting in the folder. Best effort: a bad scan leaves the
	// album empty and the admin can still use "Import Tracks from Disk".
	if diskTracks, err := album.ScanTracks(alb.AlbumPath); err != nil {
		log.Printf("scan tracks for album %d: %v", alb.ID, err)
	} else if err := s.albumStore.SetTracks(alb.ID, diskTracks); err != nil {
		log.Printf("set tracks for album %d: %v", alb.ID, err)
	}

	jsonCreated(w, alb)
}

func (s *Server) handleAdminGetAlbum(w http.ResponseWriter, r *http.Request) {
	alb := s.adminAlbumFromRequest(w, r)
	if alb == nil {
		return
	}
	jsonOK(w, alb)
}

func (s *Server) handleAdminUpdateAlbum(w http.ResponseWriter, r *http.Request) {
	alb := s.adminAlbumFromRequest(w, r)
	if alb == nil {
		return
	}

	var req struct {
		Title            *string `json:"title"`
		Artist           *string `json:"artist"`
		DownloadsEnabled *bool   `json:"downloads_enabled"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	title, artist, ok := applyAlbumMetadata(alb, req.Title, req.Artist)
	if !ok {
		jsonError(w, "bad request: title and artist must be at most 256 characters", http.StatusBadRequest)
		return
	}
	if title != alb.Title || artist != alb.Artist {
		if err := s.albumStore.UpdateAlbum(alb.ID, title, artist); err != nil {
			log.Printf("update album error: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	if req.DownloadsEnabled != nil {
		if err := s.albumStore.SetDownloadsEnabled(alb.ID, *req.DownloadsEnabled); err != nil {
			log.Printf("update downloads_enabled error: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleAdminDeleteAlbum(w http.ResponseWriter, r *http.Request) {
	alb := s.adminAlbumFromRequest(w, r)
	if alb == nil {
		return
	}

	if err := s.albumStore.DeleteAlbum(alb.ID); err != nil {
		log.Printf("delete album error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := os.RemoveAll(s.coverDir(alb.ID)); err != nil {
		log.Printf("remove cover for album %d: %v", alb.ID, err)
	}

	jsonOK(w, map[string]string{"status": "ok"})
}

// --- Password CRUD ---

func (s *Server) handleAdminListPasswords(w http.ResponseWriter, r *http.Request) {
	passwords, err := s.albumStore.ListPasswords()
	if err != nil {
		log.Printf("list passwords error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	type pwResp struct {
		ID        int64   `json:"id"`
		Label     string  `json:"label"`
		AlbumIDs  []int64 `json:"album_ids"`
		CreatedAt string  `json:"created_at"`
		UpdatedAt string  `json:"updated_at"`
	}

	resp := make([]pwResp, 0, len(passwords))
	for _, p := range passwords {
		albumIDs := p.AlbumIDs
		if albumIDs == nil {
			albumIDs = []int64{}
		}
		resp = append(resp, pwResp{
			ID:        p.ID,
			Label:     p.Label,
			AlbumIDs:  albumIDs,
			CreatedAt: p.CreatedAt,
			UpdatedAt: p.UpdatedAt,
		})
	}

	jsonOK(w, map[string]interface{}{"passwords": resp})
}

func (s *Server) handleAdminCreatePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label      string  `json:"label"`
		Passphrase string  `json:"passphrase"`
		AlbumIDs   []int64 `json:"album_ids"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	passphrase := strings.TrimSpace(req.Passphrase)
	if err := validatePassphrase(passphrase); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = "Password"
	}
	if !s.checkAlbumIDs(w, req.AlbumIDs) {
		return
	}

	pw, err := s.albumStore.CreatePassword(label, passphrase, req.AlbumIDs)
	if err != nil {
		log.Printf("create password error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	jsonCreated(w, map[string]interface{}{"id": pw.ID, "label": pw.Label, "album_ids": pw.AlbumIDs})
}

func (s *Server) handleAdminUpdatePassword(w http.ResponseWriter, r *http.Request) {
	id, ok := urlID(w, r)
	if !ok {
		return
	}

	var req struct {
		Label      string  `json:"label"`
		Passphrase *string `json:"passphrase"`
		AlbumIDs   []int64 `json:"album_ids"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	label := strings.TrimSpace(req.Label)
	var passphrase *string
	if req.Passphrase != nil {
		p := strings.TrimSpace(*req.Passphrase)
		if err := validatePassphrase(p); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		passphrase = &p
	}

	if !s.checkAlbumIDs(w, req.AlbumIDs) {
		return
	}
	if err := s.albumStore.UpdatePassword(id, label, passphrase, req.AlbumIDs); err != nil {
		if errors.Is(err, albums.ErrNotFound) {
			jsonError(w, "not found", http.StatusNotFound)
			return
		}
		log.Printf("update password error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleAdminDeletePassword(w http.ResponseWriter, r *http.Request) {
	id, ok := urlID(w, r)
	if !ok {
		return
	}

	if err := s.albumStore.DeletePassword(id); err != nil {
		log.Printf("delete password error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleAdminListAlbumFolders(w http.ResponseWriter, r *http.Request) {
	if s.albumBasePath == "" {
		jsonOK(w, map[string]interface{}{"folders": []string{}})
		return
	}

	entries, err := os.ReadDir(s.albumBasePath)
	if err != nil {
		log.Printf("list album folders error: %v", err)
		jsonError(w, "cannot read album directory", http.StatusInternalServerError)
		return
	}

	folders := make([]string, 0)
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		folders = append(folders, name)
	}

	jsonOK(w, map[string]interface{}{"folders": folders})
}
