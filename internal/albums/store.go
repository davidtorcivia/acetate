package albums

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Album represents an album in the database.
type Album struct {
	ID               int64  `json:"id"`
	Slug             string `json:"slug"`
	Title            string `json:"title"`
	Artist           string `json:"artist"`
	AlbumPath        string `json:"album_path"`
	DownloadsEnabled bool   `json:"downloads_enabled"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// Track represents a track within an album.
type Track struct {
	ID           int64  `json:"id"`
	AlbumID      int64  `json:"album_id"`
	Stem         string `json:"stem"`
	Title        string `json:"title"`
	DisplayIndex string `json:"display_index"`
	SortOrder    int    `json:"sort_order"`
}

// Password represents a listener password.
type Password struct {
	ID           int64   `json:"id"`
	Label        string  `json:"label"`
	PasswordHash string  `json:"-"`
	AlbumIDs     []int64 `json:"album_ids"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// Store provides database-backed album, track, and password management.
type Store struct {
	db *sql.DB
}

// NewStore creates a new album store.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// --- Album CRUD ---

// CreateAlbum inserts a new album and returns it.
func (s *Store) CreateAlbum(title, artist, albumPath string) (*Album, error) {
	slug, err := s.uniqueSlug(generateSlug(title))
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Exec(
		"INSERT INTO albums (slug, title, artist, album_path, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		slug, title, artist, albumPath, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create album: %w", err)
	}
	id, _ := res.LastInsertId()
	return &Album{ID: id, Slug: slug, Title: title, Artist: artist, AlbumPath: albumPath, CreatedAt: now, UpdatedAt: now}, nil
}

// GetAlbum returns an album by ID.
func (s *Store) GetAlbum(id int64) (*Album, error) {
	a := &Album{}
	err := s.db.QueryRow(
		"SELECT id, slug, title, artist, album_path, downloads_enabled, created_at, updated_at FROM albums WHERE id = ?", id,
	).Scan(&a.ID, &a.Slug, &a.Title, &a.Artist, &a.AlbumPath, &a.DownloadsEnabled, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get album: %w", err)
	}
	return a, nil
}

// GetAlbumBySlug returns an album by slug.
func (s *Store) GetAlbumBySlug(slug string) (*Album, error) {
	a := &Album{}
	err := s.db.QueryRow(
		"SELECT id, slug, title, artist, album_path, downloads_enabled, created_at, updated_at FROM albums WHERE slug = ?", slug,
	).Scan(&a.ID, &a.Slug, &a.Title, &a.Artist, &a.AlbumPath, &a.DownloadsEnabled, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get album by slug: %w", err)
	}
	return a, nil
}

// ListAlbums returns all albums ordered by ID.
func (s *Store) ListAlbums() ([]Album, error) {
	rows, err := s.db.Query("SELECT id, slug, title, artist, album_path, downloads_enabled, created_at, updated_at FROM albums ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("list albums: %w", err)
	}
	defer rows.Close()

	var albums []Album
	for rows.Next() {
		var a Album
		if err := rows.Scan(&a.ID, &a.Slug, &a.Title, &a.Artist, &a.AlbumPath, &a.DownloadsEnabled, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		albums = append(albums, a)
	}
	return albums, rows.Err()
}

// UpdateAlbum updates album metadata. Regenerates slug if title changes.
func (s *Store) UpdateAlbum(id int64, title, artist string) error {
	existing, err := s.GetAlbum(id)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("album not found")
	}

	slug := existing.Slug
	if title != existing.Title {
		slug, err = s.uniqueSlug(generateSlug(title))
		if err != nil {
			return err
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.Exec(
		"UPDATE albums SET slug = ?, title = ?, artist = ?, updated_at = ? WHERE id = ?",
		slug, title, artist, now, id,
	)
	return err
}

// SetDownloadsEnabled updates the downloads_enabled flag for an album.
func (s *Store) SetDownloadsEnabled(id int64, enabled bool) error {
	val := 0
	if enabled {
		val = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(
		"UPDATE albums SET downloads_enabled = ?, updated_at = ? WHERE id = ?",
		val, now, id,
	)
	return err
}

// DeleteAlbum removes an album and its tracks and password links.
func (s *Store) DeleteAlbum(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tx.Exec("DELETE FROM album_tracks WHERE album_id = ?", id)
	tx.Exec("DELETE FROM password_album_access WHERE album_id = ?", id)
	tx.Exec("DELETE FROM albums WHERE id = ?", id)
	return tx.Commit()
}

// AlbumCount returns the total number of albums.
func (s *Store) AlbumCount() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM albums").Scan(&count)
	return count, err
}

// --- Track CRUD ---

// GetTracks returns tracks for an album ordered by sort_order.
func (s *Store) GetTracks(albumID int64) ([]Track, error) {
	rows, err := s.db.Query(
		"SELECT id, album_id, stem, title, display_index, sort_order FROM album_tracks WHERE album_id = ? ORDER BY sort_order",
		albumID,
	)
	if err != nil {
		return nil, fmt.Errorf("get tracks: %w", err)
	}
	defer rows.Close()

	var tracks []Track
	for rows.Next() {
		var t Track
		if err := rows.Scan(&t.ID, &t.AlbumID, &t.Stem, &t.Title, &t.DisplayIndex, &t.SortOrder); err != nil {
			return nil, err
		}
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

// SetTracks replaces all tracks for an album.
func (s *Store) SetTracks(albumID int64, tracks []Track) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM album_tracks WHERE album_id = ?", albumID); err != nil {
		return err
	}

	stmt, err := tx.Prepare(
		"INSERT INTO album_tracks (album_id, stem, title, display_index, sort_order) VALUES (?, ?, ?, ?, ?)",
	)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for i, t := range tracks {
		if _, err := stmt.Exec(albumID, t.Stem, t.Title, t.DisplayIndex, i); err != nil {
			return fmt.Errorf("insert track %q: %w", t.Stem, err)
		}
	}

	return tx.Commit()
}

// StemInAlbum checks if a stem exists in an album's track list.
func (s *Store) StemInAlbum(albumID int64, stem string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM album_tracks WHERE album_id = ? AND stem = ?", albumID, stem,
	).Scan(&count)
	return count > 0, err
}

// GetAllTrackCounts returns a map of album_id → track count for all albums in one query.
func (s *Store) GetAllTrackCounts() (map[int64]int, error) {
	rows, err := s.db.Query("SELECT album_id, COUNT(*) FROM album_tracks GROUP BY album_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[int64]int)
	for rows.Next() {
		var albumID int64
		var count int
		if err := rows.Scan(&albumID, &count); err != nil {
			return nil, err
		}
		counts[albumID] = count
	}
	return counts, rows.Err()
}

// --- Password CRUD ---

// CreatePassword creates a new listener password linked to the given albums.
func (s *Store) CreatePassword(label, passphrase string, albumIDs []int64) (*Password, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(passphrase), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	return s.CreatePasswordWithHash(label, string(hash), albumIDs)
}

// CreatePasswordWithHash creates a password entry using a pre-computed hash.
func (s *Store) CreatePasswordWithHash(label, passwordHash string, albumIDs []int64) (*Password, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		"INSERT INTO listener_passwords (label, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)",
		label, passwordHash, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create password: %w", err)
	}
	id, _ := res.LastInsertId()

	for _, albumID := range albumIDs {
		if _, err := tx.Exec(
			"INSERT INTO password_album_access (password_id, album_id) VALUES (?, ?)",
			id, albumID,
		); err != nil {
			return nil, fmt.Errorf("link password to album: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	retIDs := albumIDs
	if retIDs == nil {
		retIDs = make([]int64, 0)
	}
	return &Password{ID: id, Label: label, AlbumIDs: retIDs, CreatedAt: now, UpdatedAt: now}, nil
}

// ListPasswords returns all passwords with their linked album IDs.
func (s *Store) ListPasswords() ([]Password, error) {
	rows, err := s.db.Query(
		"SELECT id, label, password_hash, created_at, updated_at FROM listener_passwords ORDER BY id",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var passwords []Password
	for rows.Next() {
		var p Password
		if err := rows.Scan(&p.ID, &p.Label, &p.PasswordHash, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		passwords = append(passwords, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Load album links for each password
	for i := range passwords {
		albumIDs, err := s.getAlbumIDsForPassword(passwords[i].ID)
		if err != nil {
			return nil, err
		}
		passwords[i].AlbumIDs = albumIDs
	}

	return passwords, nil
}

// UpdatePassword updates a password's label, optionally its passphrase, and album links.
func (s *Store) UpdatePassword(id int64, label string, passphrase *string, albumIDs []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)

	if passphrase != nil {
		hash, err := bcrypt.GenerateFromPassword([]byte(*passphrase), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		if _, err := tx.Exec(
			"UPDATE listener_passwords SET label = ?, password_hash = ?, updated_at = ? WHERE id = ?",
			label, string(hash), now, id,
		); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(
			"UPDATE listener_passwords SET label = ?, updated_at = ? WHERE id = ?",
			label, now, id,
		); err != nil {
			return err
		}
	}

	if albumIDs != nil {
		if _, err := tx.Exec("DELETE FROM password_album_access WHERE password_id = ?", id); err != nil {
			return err
		}
		for _, albumID := range albumIDs {
			if _, err := tx.Exec(
				"INSERT INTO password_album_access (password_id, album_id) VALUES (?, ?)",
				id, albumID,
			); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

// DeletePassword removes a password and its album links.
func (s *Store) DeletePassword(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tx.Exec("DELETE FROM password_album_access WHERE password_id = ?", id)
	tx.Exec("DELETE FROM listener_passwords WHERE id = ?", id)
	return tx.Commit()
}

// VerifyPassword checks a passphrase against all stored password hashes.
// Returns the matching password ID and accessible album IDs, or (0, nil) if no match.
func (s *Store) VerifyPassword(passphrase string) (int64, []int64, error) {
	rows, err := s.db.Query("SELECT id, password_hash FROM listener_passwords")
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return 0, nil, err
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(passphrase)) == nil {
			albumIDs, err := s.getAlbumIDsForPassword(id)
			if err != nil {
				return 0, nil, err
			}
			return id, albumIDs, nil
		}
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}

	return 0, nil, nil
}

// GetAlbumsForPassword returns the albums a password grants access to.
func (s *Store) GetAlbumsForPassword(passwordID int64) ([]Album, error) {
	rows, err := s.db.Query(
		`SELECT a.id, a.slug, a.title, a.artist, a.album_path, a.downloads_enabled, a.created_at, a.updated_at
		 FROM albums a
		 INNER JOIN password_album_access pa ON pa.album_id = a.id
		 WHERE pa.password_id = ?
		 ORDER BY a.id`, passwordID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var albums []Album
	for rows.Next() {
		var a Album
		if err := rows.Scan(&a.ID, &a.Slug, &a.Title, &a.Artist, &a.AlbumPath, &a.DownloadsEnabled, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		albums = append(albums, a)
	}
	return albums, rows.Err()
}

// PasswordHasAlbumAccess checks if a password grants access to a specific album.
func (s *Store) PasswordHasAlbumAccess(passwordID, albumID int64) (bool, error) {
	var count int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM password_album_access WHERE password_id = ? AND album_id = ?",
		passwordID, albumID,
	).Scan(&count)
	return count > 0, err
}

func (s *Store) getAlbumIDsForPassword(passwordID int64) ([]int64, error) {
	rows, err := s.db.Query(
		"SELECT album_id FROM password_album_access WHERE password_id = ? ORDER BY album_id", passwordID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// --- Slug generation ---

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func generateSlug(title string) string {
	slug := strings.ToLower(strings.TrimSpace(title))
	slug = slugRe.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "album"
	}
	return slug
}

func (s *Store) uniqueSlug(base string) (string, error) {
	slug := base
	for i := 2; ; i++ {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM albums WHERE slug = ?", slug).Scan(&count); err != nil {
			return "", err
		}
		if count == 0 {
			return slug, nil
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}
}
