// Package files stages bounded file mirrors and publishes them only after all
// remote collection has succeeded. The caller can roll publication back when
// its database transaction fails.
package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const manifestName = ".mirror.json"

type mirrorManifest struct {
	Version    int               `json:"version"`
	Generation string            `json:"generation"`
	Records    map[string]Record `json:"files"`
}

// Record describes published bytes. A non-ok status represents a policy skip;
// old local bytes are retained and are never represented as the new download.
type Record struct {
	LocalPath, SHA256, MIME, ETag, LastModified, Status string
	Size                                                int64
}

type operation struct {
	rel, staged, backup     string
	expected, publishedHash string
	backed, published       bool
}

// Batch owns staged bytes and metadata. It is used by one collector at a time.
// Finish retains successfully replaced originals under .history/<batch>/.
type Batch struct {
	root, stage, history string
	limit                int64
	previous, records    map[string]Record
	ops                  map[string]*operation
	committed, finished  bool
	manifestHash         string
}

// NewBatch loads only the mirror's managed manifest, never arbitrary files.
func NewBatch(root string, maxFileMB int) (*Batch, error) {
	if maxFileMB == 0 {
		maxFileMB = 200
	}
	if maxFileMB < 1 || maxFileMB > 4096 {
		return nil, fmt.Errorf("files: invalid file size limit")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("files: invalid mirror root")
	}
	if err := safeParents(abs, true); err != nil {
		return nil, err
	}
	b := &Batch{root: abs, limit: int64(maxFileMB) << 20, previous: map[string]Record{}, records: map[string]Record{}, ops: map[string]*operation{}}
	manifest := filepath.Join(abs, manifestName)
	if err := safeParents(manifest, false); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(manifest)
	if err == nil {
		hash := sha256.Sum256(data)
		b.manifestHash = hex.EncodeToString(hash[:])
		var managed mirrorManifest
		if err := json.Unmarshal(data, &managed); err != nil || managed.Version != 1 || managed.Generation == "" || managed.Records == nil {
			return nil, fmt.Errorf("files: invalid managed manifest")
		}
		b.previous = managed.Records
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("files: cannot read managed manifest")
	}
	for rel, record := range b.previous {
		if _, err := b.path(rel); err != nil {
			return nil, fmt.Errorf("files: invalid managed path")
		}
		b.records[rel] = record
	}
	b.stage, err = os.MkdirTemp(abs, ".stage-")
	if err != nil {
		return nil, fmt.Errorf("files: cannot create staging directory")
	}
	b.history = filepath.Join(abs, ".history", filepath.Base(b.stage))
	return b, nil
}

// SafeName normalizes an untrusted display filename for Windows and Unix.
// IDs, rather than this display name, must determine the owning directory.
func SafeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
		name = "_" + name
	}
	if len(name) > 180 {
		name = string([]rune(name)[:min(80, len([]rune(name)))])
	}
	return name
}

func (b *Batch) path(rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.ContainsAny(rel, ":\\") || strings.HasPrefix(rel, ".") {
		return "", fmt.Errorf("files: unsafe mirror path")
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." || SafeName(part) != part {
			return "", fmt.Errorf("files: unsafe mirror path")
		}
	}
	path := filepath.Join(b.root, filepath.FromSlash(rel))
	if err := safeParents(path, false); err != nil {
		return "", err
	}
	return path, nil
}

// safeParents rejects symlinks at every existing component, including the root.
func safeParents(path string, create bool) error {
	parent := filepath.Dir(path)
	if parent != path {
		if err := safeParents(parent, create); err != nil {
			return err
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if create {
			if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("files: cannot create mirror directory")
			}
			return safeParents(path, false)
		}
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("files: unsafe or unreadable mirror path")
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (b *Batch) verify(rel string) (bool, error) {
	path, err := b.path(rel)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return false, fmt.Errorf("files: target is not a regular file")
	}
	previous, ok := b.previous[rel]
	if !ok || previous.SHA256 == "" {
		return false, fmt.Errorf("files: refusing to overwrite an unmanaged local file")
	}
	hash, err := hashFile(path)
	if err != nil || hash != previous.SHA256 {
		return false, fmt.Errorf("files: managed file was edited locally")
	}
	return true, nil
}

// StageBytes stages generated Markdown without changing its published version.
func (b *Batch) StageBytes(rel string, data []byte) (Record, error) {
	return b.stageReader(rel, bytes.NewReader(data), Record{MIME: "text/markdown"})
}

// OpenDownload returns a guarded GET response; only this callback talks HTTP.
type OpenDownload func(context.Context, string, string) (*http.Response, error)

// StageDownload streams at most limit+1 bytes and conditions requests only
// when the managed local version is present and its hash still matches.
func (b *Batch) StageDownload(ctx context.Context, rel string, open OpenDownload, full bool) (Record, error) {
	if b.committed || b.finished {
		return Record{}, fmt.Errorf("files: batch no longer accepts downloads")
	}
	exists, err := b.verify(rel)
	if err != nil {
		return Record{}, err
	}
	previous := b.previous[rel]
	etag, modified := "", ""
	if exists && !full {
		etag, modified = previous.ETag, previous.LastModified
	}
	response, err := open(ctx, etag, modified)
	if err != nil {
		return Record{}, err
	}
	if response == nil || response.Body == nil {
		return Record{}, fmt.Errorf("files: missing download response")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		if !exists || full {
			return Record{}, fmt.Errorf("files: unexpected not-modified response")
		}
		previous.LocalPath = filepath.Join(b.root, filepath.FromSlash(rel))
		return previous, nil
	}
	if response.StatusCode != http.StatusOK {
		return Record{}, fmt.Errorf("files: download returned HTTP %d", response.StatusCode)
	}
	mime := strings.Split(response.Header.Get("Content-Type"), ";")[0]
	if strings.HasPrefix(strings.ToLower(mime), "video/") {
		return Record{MIME: mime, Status: "video_link", Size: max(0, response.ContentLength)}, nil
	}
	if response.ContentLength > b.limit {
		return Record{MIME: mime, Status: "too_large", Size: response.ContentLength}, nil
	}
	return b.stageReader(rel, response.Body, Record{MIME: mime, ETag: response.Header.Get("ETag"), LastModified: response.Header.Get("Last-Modified")})
}

func (b *Batch) stageReader(rel string, reader io.Reader, record Record) (Record, error) {
	if b.committed || b.finished {
		return Record{}, fmt.Errorf("files: batch no longer accepts staged files")
	}
	if _, exists := b.ops[rel]; exists {
		return Record{}, fmt.Errorf("files: duplicate staged path")
	}
	if _, err := b.verify(rel); err != nil {
		return Record{}, err
	}
	file, err := os.CreateTemp(b.stage, "bytes-")
	if err != nil {
		return Record{}, fmt.Errorf("files: cannot stage file")
	}
	h := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(file, h), io.LimitReader(reader, b.limit+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(file.Name())
		return Record{}, fmt.Errorf("files: download or staging write failed")
	}
	if size > b.limit {
		_ = os.Remove(file.Name())
		return Record{MIME: record.MIME, Status: "too_large", Size: size}, nil
	}
	record.Size, record.SHA256, record.Status = size, hex.EncodeToString(h.Sum(nil)), "ok"
	record.LocalPath = filepath.Join(b.root, filepath.FromSlash(rel))
	b.records[rel] = record
	b.ops[rel] = &operation{rel: rel, staged: file.Name(), expected: b.previous[rel].SHA256, publishedHash: record.SHA256}
	return record, nil
}

// Commit publishes every staged file and the manifest. A failed Commit undoes
// its earlier renames before returning; no SQL state should be advanced yet.
func (b *Batch) Commit() error {
	if b.finished || b.committed {
		return fmt.Errorf("files: batch was already committed or finished")
	}
	if err := matchFile(filepath.Join(b.root, manifestName), b.manifestHash); err != nil {
		return fmt.Errorf("files: managed manifest changed during collection")
	}
	for rel := range b.ops {
		if _, err := b.verify(rel); err != nil {
			return err
		}
	}
	// A fresh generation also distinguishes a later owner that publishes
	// identical bytes. Hashes alone cannot detect that lost-lease case.
	manifest, err := json.MarshalIndent(mirrorManifest{Version: 1, Generation: filepath.Base(b.stage), Records: b.records}, "", "  ")
	if err != nil {
		return err
	}
	manifestStage := filepath.Join(b.stage, "manifest")
	if err := os.WriteFile(manifestStage, manifest, 0600); err != nil {
		return fmt.Errorf("files: cannot stage manifest")
	}
	keys := make([]string, 0, len(b.ops))
	for key := range b.ops {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	keys = append(keys, manifestName)
	manifestDigest := sha256.Sum256(manifest)
	b.ops[manifestName] = &operation{rel: manifestName, staged: manifestStage, expected: b.manifestHash, publishedHash: hex.EncodeToString(manifestDigest[:])}
	for _, rel := range keys {
		op := b.ops[rel]
		target := filepath.Join(b.root, filepath.FromSlash(rel))
		op.backup = filepath.Join(b.history, filepath.FromSlash(rel))
		if err := safeParents(filepath.Dir(target), true); err != nil {
			return errors.Join(err, b.Rollback())
		}
		if err := safeParents(target, false); err != nil {
			return errors.Join(err, b.Rollback())
		}
		if rel == manifestName {
			if err := matchFile(target, op.expected); err != nil {
				return errors.Join(fmt.Errorf("files: managed manifest changed before publication"), b.Rollback())
			}
		} else if _, err := b.verify(rel); err != nil {
			return errors.Join(err, b.Rollback())
		}
		if _, err := os.Lstat(target); err == nil {
			if err := safeParents(filepath.Dir(op.backup), true); err != nil {
				return errors.Join(err, b.Rollback())
			}
			if err := os.Rename(target, op.backup); err != nil {
				return errors.Join(fmt.Errorf("files: cannot back up previous version"), b.Rollback())
			}
			op.backed = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.Join(fmt.Errorf("files: cannot inspect published target"), b.Rollback())
		}
		if err := os.Rename(op.staged, target); err != nil {
			return errors.Join(fmt.Errorf("files: cannot publish staged file"), b.Rollback())
		}
		op.published = true
	}
	b.committed = true
	return nil
}

// Rollback restores original files after either staging or publication failure.
// It leaves backups intact if restoration fails, so they remain recoverable.
func (b *Batch) Rollback() error {
	if b.finished {
		return nil
	}
	// Refuse the whole rollback if another owner or a local editor has replaced
	// any published bytes. In particular, a lost lease must never undo a newer
	// successful sync. Keep staging and backups available for recovery.
	for _, op := range b.ops {
		if !op.published && !op.backed {
			continue
		}
		target := filepath.Join(b.root, filepath.FromSlash(op.rel))
		expected := ""
		if op.published {
			expected = op.publishedHash
		}
		if err := matchFile(target, expected); err != nil {
			return fmt.Errorf("files: refusing rollback of a changed published file")
		}
		if op.backed {
			if err := matchFile(op.backup, op.expected); err != nil {
				return fmt.Errorf("files: refusing rollback of a changed backup")
			}
		}
	}
	var failures []error
	for _, op := range b.ops {
		target := filepath.Join(b.root, filepath.FromSlash(op.rel))
		if !op.published && !op.backed {
			continue
		}
		if err := safeParents(target, false); err != nil {
			failures = append(failures, err)
			continue
		}
		if op.published {
			// Recheck at each destructive step as well as in the preflight; a
			// large batch may take time to roll back after losing its lease.
			if err := matchFile(target, op.publishedHash); err != nil {
				failures = append(failures, fmt.Errorf("files: refusing rollback of a changed published file"))
				continue
			}
			if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				failures = append(failures, fmt.Errorf("files: cannot remove rolled-back version"))
				continue
			}
			op.published = false
		}
		if op.backed {
			if err := matchFile(target, ""); err != nil {
				failures = append(failures, fmt.Errorf("files: refusing to overwrite a newly published target"))
				continue
			}
			if err := safeParents(op.backup, false); err != nil {
				failures = append(failures, err)
				continue
			}
			if err := matchFile(op.backup, op.expected); err != nil {
				failures = append(failures, fmt.Errorf("files: refusing rollback of a changed backup"))
				continue
			}
			if err := os.Rename(op.backup, target); err != nil {
				failures = append(failures, fmt.Errorf("files: cannot restore previous version"))
				continue
			}
			op.backed = false
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	b.finished = true
	if err := safeParents(b.stage, false); err != nil {
		return err
	}
	return os.RemoveAll(b.stage)
}

// matchFile treats the empty hash as a required absent target.
func matchFile(path, expected string) error {
	if err := safeParents(path, false); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && expected == "" {
		return nil
	}
	if err != nil || expected == "" || !info.Mode().IsRegular() {
		return fmt.Errorf("files: file no longer matches the expected version")
	}
	hash, err := hashFile(path)
	if err != nil || hash != expected {
		return fmt.Errorf("files: file no longer matches the expected version")
	}
	return nil
}

// Finish releases staging files after SQL commit; version backups are retained.
func (b *Batch) Finish() error {
	if b.finished {
		return nil
	}
	if !b.committed {
		return fmt.Errorf("files: cannot finish an unpublished batch")
	}
	b.finished = true
	if err := safeParents(b.stage, false); err != nil {
		return err
	}
	return os.RemoveAll(b.stage)
}
