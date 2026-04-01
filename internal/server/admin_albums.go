package server

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

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

	// Resolve relative folder names against the album base path
	if s.albumBasePath != "" && !filepath.IsAbs(albumPath) {
		albumPath = filepath.Join(s.albumBasePath, albumPath)
	}

	alb, err := s.albumStore.CreateAlbum(title, artist, albumPath)
	if err != nil {
		log.Printf("create album error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
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
		Title            string `json:"title"`
		Artist           string `json:"artist"`
		DownloadsEnabled *bool  `json:"downloads_enabled"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	title := alb.Title
	artist := alb.Artist
	if req.Title != "" {
		title = trimAndCollapseSpaces(req.Title)
	}
	if req.Artist != "" {
		artist = trimAndCollapseSpaces(req.Artist)
	}

	if err := s.albumStore.UpdateAlbum(alb.ID, title, artist); err != nil {
		log.Printf("update album error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
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
	if passphrase == "" {
		jsonError(w, "passphrase is required", http.StatusBadRequest)
		return
	}

	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = "Password"
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
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, "bad request", http.StatusBadRequest)
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
		if p == "" {
			jsonError(w, "passphrase cannot be empty", http.StatusBadRequest)
			return
		}
		passphrase = &p
	}

	if err := s.albumStore.UpdatePassword(id, label, passphrase, req.AlbumIDs); err != nil {
		log.Printf("update password error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleAdminDeletePassword(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, "bad request", http.StatusBadRequest)
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
		if e.IsDir() {
			folders = append(folders, e.Name())
		}
	}

	jsonOK(w, map[string]interface{}{"folders": folders})
}
