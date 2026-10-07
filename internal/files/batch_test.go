package files

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func publish(t *testing.T, root, text string) {
	t.Helper()
	b, err := NewBatch(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.StageBytes("lessons/1/lesson.md", []byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := b.Finish(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationRollbackAndLocalConflict(t *testing.T) {
	root := t.TempDir()
	publish(t, root, "original")
	path := filepath.Join(root, "lessons", "1", "lesson.md")
	b, err := NewBatch(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.StageBytes("lessons/1/lesson.md", []byte("replacement")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "original" {
		t.Fatal("staging replaced original")
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := b.Rollback(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if string(data) != "original" {
		t.Fatal("rollback lost original")
	}
	if err := os.WriteFile(path, []byte("local edit"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err = NewBatch(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Rollback()
	if _, err := b.StageBytes("lessons/1/lesson.md", []byte("new")); err == nil {
		t.Fatal("overwrote local edit")
	}
}

func TestRollbackRefusesNewerOwner(t *testing.T) {
	root := t.TempDir()
	publish(t, root, "old")
	old, _ := NewBatch(root, 0)
	old.StageBytes("lessons/1/lesson.md", []byte("stale owner"))
	if err := old.Commit(); err != nil {
		t.Fatal(err)
	}
	publish(t, root, "new owner")
	if err := old.Rollback(); err == nil {
		t.Fatal("stale owner undid new publication")
	}
	data, _ := os.ReadFile(filepath.Join(root, "lessons", "1", "lesson.md"))
	if string(data) != "new owner" {
		t.Fatal("new owner bytes lost")
	}
}

func TestRollbackRefusesIdenticalNewerPublication(t *testing.T) {
	root := t.TempDir()
	publish(t, root, "original")
	old, err := NewBatch(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.StageBytes("lessons/1/lesson.md", []byte("identical new bytes")); err != nil {
		t.Fatal(err)
	}
	if err := old.Commit(); err != nil {
		t.Fatal(err)
	}
	publish(t, root, "identical new bytes")
	if err := old.Rollback(); err == nil {
		t.Fatal("stale owner rolled back an identical later publication")
	}
	data, _ := os.ReadFile(filepath.Join(root, "lessons", "1", "lesson.md"))
	if string(data) != "identical new bytes" {
		t.Fatal("new owner's bytes were replaced")
	}
}

func TestCommitRefusesChangedManifest(t *testing.T) {
	root := t.TempDir()
	publish(t, root, "old")
	a, _ := NewBatch(root, 0)
	defer a.Rollback()
	a.StageBytes("other/file.md", []byte("a"))
	b, _ := NewBatch(root, 0)
	b.StageBytes("new/file.md", []byte("b"))
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	b.Finish()
	if err := a.Commit(); err == nil {
		t.Fatal("stale manifest replaced newer manifest")
	}
	if _, err := os.Stat(filepath.Join(root, "other", "file.md")); !os.IsNotExist(err) {
		t.Fatal("stale bytes published")
	}
}

func TestConditionalDownloadAndFull(t *testing.T) {
	root := t.TempDir()
	var conditional []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		condition := r.Header.Get("If-None-Match") == "tag" && r.Header.Get("If-Modified-Since") == "date"
		conditional = append(conditional, condition)
		if condition {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", "tag")
		w.Header().Set("Last-Modified", "date")
		w.Header().Set("Content-Type", "application/pdf")
		io.WriteString(w, "%PDF test")
	}))
	defer srv.Close()
	open := func(ctx context.Context, tag, modified string) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		req.Header.Set("If-None-Match", tag)
		req.Header.Set("If-Modified-Since", modified)
		return srv.Client().Do(req)
	}
	for _, full := range []bool{false, false, true} {
		b, _ := NewBatch(root, 0)
		r, err := b.StageDownload(context.Background(), "lessons/1/file.pdf", open, full)
		if err != nil || r.SHA256 == "" || r.Status != "ok" {
			t.Fatalf("record=%+v err=%v", r, err)
		}
		if err := b.Commit(); err != nil {
			t.Fatal(err)
		}
		b.Finish()
	}
	if len(conditional) != 3 || conditional[0] || !conditional[1] || conditional[2] {
		t.Fatal(conditional)
	}
}

func TestBoundedVideoAndOversizedDownloads(t *testing.T) {
	for _, tc := range []struct {
		name, mime, body, status string
		size                     int64
	}{
		{"video", "video/mp4", "do not read", "video_link", 12},
		{"declared-large", "application/pdf", "do not read", "too_large", 2 << 20},
		{"stream-large", "application/pdf", strings.Repeat("x", (1<<20)+1), "too_large", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := NewBatch(t.TempDir(), 1)
			defer b.Rollback()
			r, err := b.StageDownload(context.Background(), "file.bin", func(context.Context, string, string) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body)), ContentLength: tc.size, Header: http.Header{"Content-Type": []string{tc.mime}}}, nil
			}, false)
			if err != nil || r.Status != tc.status || r.LocalPath != "" || r.SHA256 != "" {
				t.Fatalf("%+v %v", r, err)
			}
			if len(b.ops) != 0 {
				t.Fatal("policy skip staged bytes")
			}
		})
	}
}

func TestUnsafePathsAndUnmanagedTarget(t *testing.T) {
	b, _ := NewBatch(t.TempDir(), 0)
	defer b.Rollback()
	for _, path := range []string{"../x", "C:/x", "x/../y", "x\\y", "x/CON", ".mirror.json", "x/../y", "x/y."} {
		if _, err := b.StageBytes(path, []byte("x")); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	if err := os.WriteFile(filepath.Join(b.root, "file.md"), []byte("unmanaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.StageBytes("file.md", []byte("x")); err == nil {
		t.Fatal("unmanaged target replaced")
	}
}

func TestSymlinkRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skip("symlinks unavailable")
	}
	b, _ := NewBatch(root, 0)
	defer b.Rollback()
	if _, err := b.StageBytes("linked/file.md", []byte("x")); err == nil {
		t.Fatal("followed symlink")
	}
}
