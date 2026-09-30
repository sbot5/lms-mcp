package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sbot5/lms-mcp/internal/ed"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func configFixture(t *testing.T) (string, syncConfig) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := syncConfig{Region: "us", EnvFile: ".env", Courses: []courseConfig{{ID: 1, Code: "TEST", Directory: "output", Lessons: true}}}
	if err := saveNewConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	normalized, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, normalized
}

func TestConfigurationDefaultsValidationAndOfflineCommands(t *testing.T) {
	path, cfg := configFixture(t)
	if cfg.FullRefreshHours != 24 || cfg.Schedule.DailyAt != "09:00" || !filepath.IsAbs(cfg.Courses[0].Directory) || cfg.Courses[0].Region != "us" {
		t.Fatalf("bad defaults: %+v", cfg)
	}
	for _, cmd := range [][]string{{"config", "validate", "-config", path}, {"status", "-config", path}, {"whats-new", "-config", path}} {
		var out bytes.Buffer
		if err := Run(context.Background(), cmd, strings.NewReader(""), &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		if out.Len() == 0 {
			t.Fatal("no result")
		}
	}
	for _, raw := range []string{`{"courses":[]} {}`, `{"unexpected":true}`, `null`, `{"courses":[{"id":1,"code":"X","directory":"x"}],"region":"../../x"}`, `{"courses":[{"id":1,"code":"X","directory":"x"}],"schedule":{"daily_at":"25:00"}}`, `{"courses":[{"id":1,"code":"X","directory":"x"},{"id":2,"code":"Y","directory":"x/nested"}]}`} {
		os.WriteFile(path, []byte(raw), 0600)
		if _, err := loadConfig(path); err == nil {
			t.Fatalf("accepted invalid config %s", raw)
		}
	}
}

func TestWizardSelectsCoursesWithoutOverwriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	answers := []string{"2,2", filepath.Join(t.TempDir(), "exports"), "no", "no", "08:30"}
	prompt := func(_, fallback string) (string, error) {
		a := answers[0]
		answers = answers[1:]
		if a == "" {
			a = fallback
		}
		return a, nil
	}
	courses := []ed.Course{{ID: 1, Code: "ONE"}, {ID: 2, Code: "TWO"}}
	if err := configureCourses(path, "/private/.env", "us", courses, prompt, io.Discard); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Courses) != 1 || cfg.Courses[0].ID != 2 || cfg.Courses[0].Lessons || enabled(cfg.DownloadAttachments) || cfg.Schedule.DailyAt != "08:30" {
		t.Fatalf("wrong setup: %+v", cfg)
	}
	if err := saveNewConfig(path, cfg); err == nil {
		t.Fatal("wizard overwrote existing configuration")
	}
	if _, err := selectedCourses("0,3", 2); err == nil {
		t.Fatal("invalid selection accepted")
	}
}

func TestPrivatePostsDoNotReachCLIOrUpdatePage(t *testing.T) {
	detail := testDetail()
	detail.IsPrivate = true
	detail.Title = "DO_NOT_EXPORT"
	c := mockClient(t, func(r *http.Request) (int, any) {
		if strings.Contains(r.URL.Path, "/courses/") {
			ts := []ed.Thread{}
			if r.URL.Query().Get("offset") == "0" {
				ts = append(ts, detail.Thread)
			}
			return 200, map[string]any{"threads": ts}
		}
		return 200, map[string]any{"thread": detail}
	})
	for _, fn := range []func() error{func() error { return runThreads(context.Background(), c, []string{"1"}) }, func() error { return runThread(context.Background(), c, []string{"10"}) }} {
		old := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		os.Stdout = w
		_ = fn()
		w.Close()
		os.Stdout = old
		raw, _ := io.ReadAll(r)
		r.Close()
		if strings.Contains(string(raw), detail.Title) {
			t.Fatal("private title leaked to stdout")
		}
	}
	co := testCourse(t)
	co.IncludePrivate = true
	if _, err := syncCourse(context.Background(), c, syncConfig{FullRefreshHours: 24}, co, true); err != nil {
		t.Fatal(err)
	}
	co.IncludePrivate = false
	if _, err := syncCourse(context.Background(), c, syncConfig{FullRefreshHours: 24}, co, true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"_discussion.jsonl", "_discussion.md", "_updates.md"} {
		raw, err := os.ReadFile(filepath.Join(co.Directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), detail.Title) {
			t.Fatalf("private title remains in %s", name)
		}
	}
}

func TestDisabledDiscussionsMakeNoThreadRequests(t *testing.T) {
	co := testCourse(t)
	off := false
	co.Discussions = &off
	c := mockClient(t, func(r *http.Request) (int, any) { t.Fatalf("unexpected request %s", r.URL.Path); return 500, nil })
	if _, err := syncCourse(context.Background(), c, syncConfig{FullRefreshHours: 24}, co, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(co.Directory, "_discussion.jsonl")); !os.IsNotExist(err) {
		t.Fatal("disabled discussion export created")
	}
}

func TestRegionIsolationAndLinks(t *testing.T) {
	co := testCourse(t)
	co.Region = "us"
	r, err := threadRecord(testDetail(), nil, co)
	if err != nil || !strings.Contains(r["url"].(string), "/us/") {
		t.Fatalf("wrong source URL: %v %v", r, err)
	}
	s, _ := readState(co)
	if err = writeOutputs(co, &s, map[string][]byte{}); err != nil {
		t.Fatal(err)
	}
	co.Region = "au"
	if _, err = readState(co); err == nil {
		t.Fatal("cross-region checkpoint accepted")
	}
}

func TestMCPDiscoveryIdentityAndPrivateThreadBoundary(t *testing.T) {
	path, _ := configFixture(t)
	detail := testDetail()
	detail.IsPrivate = true
	c := mockClient(t, func(r *http.Request) (int, any) {
		if r.URL.Path == "/api/user" {
			return 200, map[string]any{"user": map[string]any{"id": 99, "name": "PRIVATE_ACCOUNT_NAME"}, "courses": []any{}}
		}
		return 200, map[string]any{"thread": detail}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := newMCPServer(c, path).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 5 {
		t.Fatalf("tools: %v %v", list, err)
	}
	identity, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ed_whoami", Arguments: map[string]any{}})
	if err != nil || identity.IsError {
		t.Fatalf("identity: %v %v", identity, err)
	}
	b, _ := json.Marshal(identity)
	if strings.Contains(string(b), "PRIVATE_ACCOUNT_NAME") || strings.Contains(string(b), "user_id") {
		t.Fatal("account identity leaked")
	}
	private, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_thread", Arguments: map[string]any{"thread_id": 10}})
	if err != nil || !private.IsError {
		t.Fatalf("private post returned: %v %v", private, err)
	}
	news, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "whats_new", Arguments: map[string]any{}})
	if err != nil || news.IsError {
		t.Fatalf("offline news: %v %v", news, err)
	}
}

func TestEnvQuotesBOMAndNoSecretInErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte("\ufeffexport ED_API_TOKEN=\"dummy-secret\"\n"), 0600)
	token, err := loadToken(p)
	if err != nil || token != "dummy-secret" {
		t.Fatalf("quoted token rejected: %v", err)
	}
	os.WriteFile(p, []byte("ED_API_TOKEN=\"dummy-secret"), 0600)
	_, err = loadToken(p)
	if err == nil || strings.Contains(err.Error(), "dummy-secret") {
		t.Fatal("unsafe parse error")
	}
}

func TestSafeTargetAllowsOSAncestorLinksButRejectsOutputLinks(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "actual")
	root := filepath.Join(base, "alias", "course")
	if err := os.MkdirAll(filepath.Join(real, "course"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(base, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := safeTarget(root, "file.md"); err != nil {
		t.Fatalf("legitimate ancestor rejected: %v", err)
	}
	if err := os.Symlink(real, filepath.Join(root, "redirect")); err != nil {
		t.Fatal(err)
	}
	if _, err := safeTarget(root, filepath.Join("redirect", "file.md")); err == nil {
		t.Fatal("symlink within output accepted")
	}
	if _, err := safeTarget(filepath.Join(base, "alias"), "file.md"); err == nil {
		t.Fatal("symlink root accepted")
	}
}
