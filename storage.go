package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func digest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func jsonHash(v any) string     { b, _ := json.Marshal(v); return digest(b) }
func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".lms-tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

type threadCache struct {
	Fingerprint string    `json:"fingerprint"`
	CheckedAt   time.Time `json:"checked_at"`
	Record      record    `json:"record"`
}
type lessonCache struct {
	Hash   string            `json:"hash"`
	Path   string            `json:"path"`
	Title  string            `json:"title"`
	Assets map[string]string `json:"assets,omitempty"`
}
type change struct {
	DetectedAt time.Time `json:"detected_at"`
	Course     string    `json:"course"`
	Kind       string    `json:"kind"`
	Action     string    `json:"action"`
	ID         int       `json:"id"`
	Number     int       `json:"number,omitempty"`
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	Staff      bool      `json:"staff"`
	Private    bool      `json:"private,omitempty"`
}
type syncState struct {
	Version     int                 `json:"version"`
	CourseID    int                 `json:"course_id"`
	Region      string              `json:"region,omitempty"`
	LastSuccess time.Time           `json:"last_success"`
	Threads     map[int]threadCache `json:"threads"`
	Lessons     map[int]lessonCache `json:"lessons"`
	Files       map[string]string   `json:"files"`
	Changes     []change            `json:"changes"`
}

func readState(co courseConfig) (syncState, error) {
	s := syncState{Version: 1, CourseID: co.ID, Region: co.region(), Threads: map[int]threadCache{}, Lessons: map[int]lessonCache{}, Files: map[string]string{}, Changes: []change{}}
	b, err := os.ReadFile(filepath.Join(co.Directory, ".lms-sync", "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.Region = "" // Legacy checkpoints without a region were created for au.
	if err = json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("state is corrupt; refusing to reset it: %w", err)
	}
	if s.Version != 1 || s.CourseID != co.ID || s.Threads == nil || s.Lessons == nil || s.Files == nil {
		return s, fmt.Errorf("state version/course/maps mismatch")
	}
	if s.Region == "" {
		s.Region = "au"
	}
	if s.Region != co.region() {
		return s, fmt.Errorf("state region differs from configuration; use a separate output directory")
	}
	return s, nil
}

// A kernel lock survives neither process exit nor crashes; no stale lockfile recovery needed.
func lockCourse(co courseConfig) (*os.File, error) {
	if _, err := safeTarget(co.Directory, filepath.Join(".lms-sync", "sync.lock")); err != nil {
		return nil, err
	}
	dir := filepath.Join(co.Directory, ".lms-sync")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return acquireLock(filepath.Join(dir, "sync.lock"))
}

func safeTarget(root, rel string) (string, error) {
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("unsafe output path %q", rel)
	}
	p := filepath.Join(root, rel)
	// Never follow links or Windows junctions through generated output directories.
	for cur := p; ; cur = filepath.Dir(cur) {
		info, err := os.Lstat(cur)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink output refused: %s", cur)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if cur == filepath.Dir(cur) {
			break
		}
	}
	return p, nil
}

func readPending(co courseConfig) (map[string][]string, error) {
	journalPath, err := safeTarget(co.Directory, filepath.Join(".lms-sync", "pending.json"))
	if err != nil {
		return nil, err
	}
	pending := map[string][]string{}
	if b, err := os.ReadFile(journalPath); err == nil {
		if err = json.Unmarshal(b, &pending); err != nil {
			return nil, fmt.Errorf("invalid recovery journal: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if pending == nil {
		return nil, fmt.Errorf("invalid null recovery journal")
	}
	return pending, nil
}

func writeOutputs(co courseConfig, s *syncState, files map[string][]byte) error {
	// A durable intent journal distinguishes our own interrupted writes from local edits.
	journalPath := filepath.Join(co.Directory, ".lms-sync", "pending.json")
	pending, err := readPending(co)
	if err != nil {
		return err
	}
	// Preflight every managed file before touching any of them.
	for rel, data := range files {
		path, err := safeTarget(co.Directory, rel)
		if err != nil {
			return err
		}
		old, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if expected := s.Files[rel]; expected != "" && digest(old) != expected && !slices.Contains(pending[rel], digest(old)) && !bytes.Equal(old, data) {
			return fmt.Errorf("local edit conflict: %s (preserved; move your edited copy aside before retrying)", path)
		}
	}
	// Preserve previous pending hashes for files not part of this attempt (e.g. a renamed lesson).
	for rel, data := range files {
		h := digest(data)
		if !slices.Contains(pending[rel], h) {
			pending[rel] = append(pending[rel], h)
		}
	}
	intent, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	if err = atomicWrite(journalPath, intent); err != nil {
		return err
	}
	for rel, data := range files {
		path := filepath.Join(co.Directory, rel)
		old, err := os.ReadFile(path)
		if err == nil && bytes.Equal(old, data) {
			s.Files[rel] = digest(data)
			continue
		}
		if err == nil {
			backup := filepath.Join(co.Directory, ".lms-sync", "backups", digest(old), rel)
			if err = atomicWrite(backup, old); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = atomicWrite(path, data); err != nil {
			return err
		}
		s.Files[rel] = digest(data)
	}
	// A lesson may have been renamed after an interrupted run. Retain ownership of
	// successfully written old paths even when this run no longer renders them.
	for rel, hashes := range pending {
		if _, written := files[rel]; written {
			continue
		}
		path, err := safeTarget(co.Directory, rel)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if h := digest(b); slices.Contains(hashes, h) {
			s.Files[rel] = h
		}
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err = atomicWrite(filepath.Join(co.Directory, ".lms-sync", "state.json"), b); err != nil {
		return err
	}
	return os.Remove(journalPath)
}

func safeName(s string, maxLen int) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return -1
		}
		return r
	}, s)
	s = strings.Trim(s, " .\t\n")
	r := []rune(s)
	if len(r) > maxLen {
		s = string(r[:maxLen])
	}
	s = strings.TrimRight(s, " .")
	if s == "" {
		s = "untitled"
	}
	stem := strings.ToUpper(strings.Split(s, ".")[0])
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '0' && stem[3] <= '9') {
		s = "_" + s
	}
	return s
}
