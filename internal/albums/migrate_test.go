package albums

import (
	"os"
	"path/filepath"
	"testing"

	"acetate/internal/database"
)

func TestMigrateLegacyCover(t *testing.T) {
	dataPath := t.TempDir()
	db, err := database.Open(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	legacy := filepath.Join(dataPath, "cover_override.jpg")
	os.WriteFile(legacy, []byte("jpeg"), 0644)

	// No album yet: the file waits.
	if err := MigrateLegacyCover(db, dataPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatal("legacy cover moved before any album existed")
	}

	alb, err := NewStore(db).CreateAlbum("A", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyCover(db, dataPath); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dataPath, "covers", "1", "cover_override.jpg")
	if alb.ID != 1 {
		t.Fatalf("album id = %d", alb.ID)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("cover not moved to %s: %v", dest, err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("legacy cover still present")
	}
}
