package album

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"

	"acetate/internal/albums"
)

// ScanTracks lists the album folder's MP3 files as tracks sorted by stem,
// titled from their ID3 tag or, failing that, from the file name.
func ScanTracks(albumPath string) ([]albums.Track, error) {
	entries, err := os.ReadDir(albumPath)
	if err != nil {
		return nil, fmt.Errorf("scan album directory: %w", err)
	}

	var tracks []albums.Track
	for _, entry := range entries {
		name := entry.Name()
		// Streaming opens stem+".mp3", so only that exact extension is playable.
		if entry.IsDir() || !strings.HasSuffix(name, ".mp3") {
			continue
		}

		stem := strings.TrimSuffix(name, ".mp3")
		// Skip anything that could not be requested back: dotfiles such as macOS
		// AppleDouble sidecars, and names whose edges the request path would trim.
		if !ValidateStem(stem) || strings.TrimSpace(stem) != stem {
			log.Printf("skipping unrequestable track file %q in %s", name, albumPath)
			continue
		}

		title, err := readMP3Title(filepath.Join(albumPath, name))
		if err != nil || title == "" {
			title = deriveTitle(stem)
		}
		tracks = append(tracks, albums.Track{Stem: stem, Title: title})
	}

	sort.Slice(tracks, func(i, j int) bool { return tracks[i].Stem < tracks[j].Stem })
	return tracks, nil
}

var numericPrefixRe = regexp.MustCompile(`^\d+[-_]?`)

// deriveTitle converts a stem like "01-gathering" to "Gathering".
func deriveTitle(stem string) string {
	title := numericPrefixRe.ReplaceAllString(stem, "")
	if title == "" {
		title = stem
	}
	title = strings.NewReplacer("-", " ", "_", " ").Replace(title)
	words := strings.Fields(title)
	if len(words) == 0 {
		return stem
	}
	for i, w := range words {
		r := []rune(strings.ToLower(w))
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

// readMP3Title returns the ID3v2.3/2.4 TIT2 title, else the ID3v1 title, else "".
func readMP3Title(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if title, err := readID3v2Title(f); err != nil || title != "" {
		return title, err
	}

	info, err := f.Stat()
	if err != nil || info.Size() < 128 {
		return "", err
	}
	tag := make([]byte, 128)
	if _, err := f.ReadAt(tag, info.Size()-128); err != nil {
		return "", err
	}
	if !bytes.Equal(tag[:3], []byte("TAG")) {
		return "", nil
	}
	return strings.TrimSpace(latin1(bytes.TrimRight(tag[3:33], "\x00 "))), nil
}

func readID3v2Title(r io.ReadSeeker) (string, error) {
	header := make([]byte, 10)
	if _, err := io.ReadFull(r, header); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return "", nil
		}
		return "", err
	}
	version := header[3]
	if !bytes.Equal(header[:3], []byte("ID3")) || (version != 3 && version != 4) {
		return "", nil
	}
	end := int64(10 + syncSafe(header[6:10]))
	pos := int64(10)

	if header[5]&0x40 != 0 { // extended header
		size := make([]byte, 4)
		if _, err := io.ReadFull(r, size); err != nil {
			return "", nil
		}
		if version == 4 {
			pos += int64(syncSafe(size)) // v2.4 counts the size field itself
		} else {
			pos += 4 + int64(binary.BigEndian.Uint32(size))
		}
	}

	// Walk frame headers and seek past bodies, so embedded artwork is never read.
	frame := make([]byte, 10)
	for pos+10 <= end {
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return "", err
		}
		if _, err := io.ReadFull(r, frame); err != nil || frame[0] == 0 {
			return "", nil
		}
		size := int64(binary.BigEndian.Uint32(frame[4:8]))
		if version == 4 {
			size = int64(syncSafe(frame[4:8]))
		}
		if size <= 0 || pos+10+size > end {
			return "", nil
		}
		if string(frame[:4]) == "TIT2" {
			if size > 4096 {
				return "", nil
			}
			body := make([]byte, size)
			if _, err := io.ReadFull(r, body); err != nil {
				return "", nil
			}
			return decodeID3Text(body), nil
		}
		pos += 10 + size
	}
	return "", nil
}

func syncSafe(b []byte) int {
	return int(b[0]&0x7f)<<21 | int(b[1]&0x7f)<<14 | int(b[2]&0x7f)<<7 | int(b[3]&0x7f)
}

func decodeID3Text(frame []byte) string {
	if len(frame) < 2 {
		return ""
	}
	payload := frame[1:]
	switch frame[0] {
	case 0: // ISO-8859-1
		if i := bytes.IndexByte(payload, 0); i >= 0 {
			payload = payload[:i]
		}
		return strings.TrimSpace(latin1(payload))
	case 3: // UTF-8
		if i := bytes.IndexByte(payload, 0); i >= 0 {
			payload = payload[:i]
		}
		return strings.TrimSpace(strings.ToValidUTF8(string(payload), ""))
	case 1, 2: // UTF-16 with BOM, UTF-16BE
		var order binary.ByteOrder = binary.BigEndian
		if frame[0] == 1 && len(payload) >= 2 {
			if payload[0] == 0xff && payload[1] == 0xfe {
				order = binary.LittleEndian
			}
			if (payload[0] == 0xff && payload[1] == 0xfe) || (payload[0] == 0xfe && payload[1] == 0xff) {
				payload = payload[2:]
			}
		}
		u16 := make([]uint16, 0, len(payload)/2)
		for i := 0; i+1 < len(payload); i += 2 {
			v := order.Uint16(payload[i:])
			if v == 0 {
				break
			}
			u16 = append(u16, v)
		}
		return strings.TrimSpace(string(utf16.Decode(u16)))
	}
	return ""
}

// latin1 decodes ISO-8859-1, whose bytes are exactly the first 256 code points.
func latin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}
