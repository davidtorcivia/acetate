package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"acetate/internal/config"

	"golang.org/x/crypto/bcrypt"
)

var envLineRe = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

type envLine struct {
	raw   string
	key   string
	isKV  bool
	value string
}

func main() {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("Acetate Setup Wizard")
	fmt.Println("====================")
	fmt.Println()

	cwd, err := os.Getwd()
	if err != nil {
		fatalf("resolve working directory: %v", err)
	}

	albumPath := promptPath(reader, "Album folder", "./album")
	dataPath := promptPath(reader, "Data folder", "./data")
	listenAddr := promptText(reader, "Listen address", ":8080")

	albumAbs, err := filepath.Abs(albumPath)
	if err != nil {
		fatalf("resolve album path: %v", err)
	}
	dataAbs, err := filepath.Abs(dataPath)
	if err != nil {
		fatalf("resolve data path: %v", err)
	}

	info, err := os.Stat(albumAbs)
	if err != nil || !info.IsDir() {
		fatalf("album folder does not exist or is not a directory: %s", albumAbs)
	}

	if err := os.MkdirAll(dataAbs, 0755); err != nil {
		fatalf("create data folder: %v", err)
	}

	stems, err := scanAlbum(albumAbs)
	if err != nil {
		fatalf("scan album folder: %v", err)
	}
	if len(stems) == 0 {
		fmt.Println()
		fmt.Println("WARNING: no .mp3 files found yet. You can still continue, but listeners will see no tracks.")
	}

	cfgMgr, err := config.NewManager(dataAbs, albumAbs)
	if err != nil {
		fatalf("load/create config: %v", err)
	}

	cfg := cfgMgr.Get()
	fmt.Println()
	fmt.Printf("Discovered tracks in config: %d\n", len(cfg.Tracks))
	for i, t := range cfg.Tracks {
		fmt.Printf("  %02d. %s (%s)\n", i+1, t.Title, t.Stem)
	}

	cfg.Title = promptText(reader, "Album title", fallback(cfg.Title, "Album Title"))
	cfg.Artist = promptText(reader, "Artist", fallback(cfg.Artist, "Artist Name"))

	if promptYesNo(reader, "Configure listener passphrase now?", cfg.Password == "") {
		cfg.Password = promptHashedPassphrase(reader)
	}

	if err := cfgMgr.Update(cfg); err != nil {
		fatalf("save config: %v", err)
	}

	adminToken := promptAdminToken(reader)

	envPath := filepath.Join(cwd, ".env")
	updates := map[string]string{
		"LISTEN_ADDR": listenAddr,
		"ALBUM_PATH":  albumPath,
		"DATA_PATH":   dataPath,
	}
	if adminToken != "" {
		updates["ADMIN_TOKEN"] = adminToken
	}

	if err := writeEnvFile(envPath, updates, adminToken == ""); err != nil {
		fatalf("write .env: %v", err)
	}

	fmt.Println()
	fmt.Println("Setup complete.")
	fmt.Printf("Config: %s\n", filepath.Join(dataAbs, "config.json"))
	fmt.Printf("Env:    %s\n", envPath)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("1) Start server locally: go run ./cmd/server")
	fmt.Println("2) Or run in Docker:     docker compose up -d --build")
	fmt.Println("3) Open listener UI:      http://localhost:8080")
	if adminToken != "" {
		fmt.Println("4) Open admin UI:         http://localhost:8080/admin")
	}
}

func promptPath(reader *bufio.Reader, label, def string) string {
	for {
		v := promptText(reader, label, def)
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		return v
	}
}

func promptText(reader *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}

	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func promptYesNo(reader *bufio.Reader, question string, defYes bool) bool {
	def := "y/N"
	if defYes {
		def = "Y/n"
	}

	for {
		fmt.Printf("%s [%s]: ", question, def)
		line, _ := reader.ReadString('\n')
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "" {
			return defYes
		}
		if line == "y" || line == "yes" {
			return true
		}
		if line == "n" || line == "no" {
			return false
		}
	}
}

func promptHashedPassphrase(reader *bufio.Reader) string {
	for {
		fmt.Print("Listener passphrase: ")
		pass1, _ := reader.ReadString('\n')
		pass1 = strings.TrimSpace(pass1)
		if pass1 == "" {
			fmt.Println("Passphrase cannot be empty.")
			continue
		}

		fmt.Print("Confirm passphrase: ")
		pass2, _ := reader.ReadString('\n')
		pass2 = strings.TrimSpace(pass2)
		if pass1 != pass2 {
			fmt.Println("Passphrases do not match. Try again.")
			continue
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(pass1), bcrypt.DefaultCost)
		if err != nil {
			fatalf("hash passphrase: %v", err)
		}
		return string(hash)
	}
}

func promptAdminToken(reader *bufio.Reader) string {
	existing := strings.TrimSpace(os.Getenv("ADMIN_TOKEN"))

	fmt.Println()
	if existing != "" {
		fmt.Println("An ADMIN_TOKEN is already set in your environment.")
		if promptYesNo(reader, "Reuse existing ADMIN_TOKEN?", true) {
			return existing
		}
	}

	if !promptYesNo(reader, "Enable admin interface?", true) {
		return ""
	}

	fmt.Print("Admin token mode [g=generate, i=input] [g]: ")
	mode, _ := reader.ReadString('\n')
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" || mode == "g" {
		token, err := generateToken()
		if err != nil {
			fatalf("generate admin token: %v", err)
		}
		fmt.Println("Generated admin token and wrote it to .env")
		return token
	}
	if mode == "i" {
		for {
			fmt.Print("Admin token: ")
			v, _ := reader.ReadString('\n')
			v = strings.TrimSpace(v)
			if v != "" {
				return v
			}
			fmt.Println("Admin token cannot be empty.")
		}
	}

	fmt.Println("Unknown mode. Using generated token.")
	token, err := generateToken()
	if err != nil {
		fatalf("generate admin token: %v", err)
	}
	return token
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func scanAlbum(albumPath string) ([]string, error) {
	entries, err := os.ReadDir(albumPath)
	if err != nil {
		return nil, err
	}

	var stems []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.EqualFold(filepath.Ext(name), ".mp3") {
			stems = append(stems, strings.TrimSuffix(name, filepath.Ext(name)))
		}
	}
	return stems, nil
}

func writeEnvFile(path string, updates map[string]string, removeAdmin bool) error {
	lines := []envLine{}
	if data, err := os.ReadFile(path); err == nil {
		rawLines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
		for _, raw := range rawLines {
			if strings.TrimSpace(raw) == "" {
				lines = append(lines, envLine{raw: raw})
				continue
			}
			m := envLineRe.FindStringSubmatch(raw)
			if m == nil {
				lines = append(lines, envLine{raw: raw})
				continue
			}
			lines = append(lines, envLine{
				raw:   raw,
				key:   m[1],
				isKV:  true,
				value: m[2],
			})
		}
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(lines)+len(updates))
	for _, line := range lines {
		if !line.isKV {
			out = append(out, line.raw)
			continue
		}

		key := line.key
		if removeAdmin && key == "ADMIN_TOKEN" {
			seen[key] = true
			continue
		}

		if newValue, ok := updates[key]; ok {
			out = append(out, key+"="+formatEnvValue(newValue))
			seen[key] = true
		} else {
			out = append(out, line.raw)
		}
	}

	for key, value := range updates {
		if seen[key] {
			continue
		}
		out = append(out, key+"="+formatEnvValue(value))
	}

	content := strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
	return os.WriteFile(path, []byte(content), 0644)
}

func formatEnvValue(v string) string {
	if v == "" {
		return `""`
	}

	needsQuotes := false
	for _, r := range v {
		if r == ' ' || r == '#' || r == '"' || r == '\'' || r == '\t' {
			needsQuotes = true
			break
		}
	}
	if !needsQuotes {
		return v
	}

	escaped := strings.ReplaceAll(v, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func fallback(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}
