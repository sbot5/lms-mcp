package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("MOODLE_BASE_URL", "")
	p := writeConfig(t, `{"ed":{"enabled":true}}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "data" || c.Ed.Region != "au" || c.MaxFileMB != 200 {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if c.CourseCodePattern != CoursePattern {
		t.Fatalf("pattern default = %q", c.CourseCodePattern)
	}
	if got, want := c.DBPath(), filepath.Join(filepath.Dir(p), "data", "lms.db"); got != want {
		t.Fatalf("DBPath = %q, want %q", got, want)
	}
	if c.Moodle.Enabled {
		t.Fatal("Moodle should be disabled without a base URL")
	}
}

func TestMissingFileIsDefault(t *testing.T) {
	t.Setenv("MOODLE_BASE_URL", "")
	c, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("missing file should default, got %v", err)
	}
	if c.DataDir != "data" {
		t.Fatalf("unexpected: %+v", c)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	t.Setenv("MOODLE_BASE_URL", "")
	if _, err := Load(writeConfig(t, `{"nope":1}`)); err == nil {
		t.Fatal("unknown field should be rejected")
	}
}

func TestMoodleBaseURLEnvOverride(t *testing.T) {
	t.Setenv("MOODLE_BASE_URL", "https://example.edu/moodle/")
	c, err := Load(writeConfig(t, `{"moodle":{"base_url":"https://ignored.example"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Moodle.Enabled || c.Moodle.BaseURL != "https://example.edu/moodle" {
		t.Fatalf("env override failed: enabled=%v base=%q", c.Moodle.Enabled, c.Moodle.BaseURL)
	}
}

func TestInvalidRegion(t *testing.T) {
	t.Setenv("MOODLE_BASE_URL", "")
	if _, err := Load(writeConfig(t, `{"ed":{"region":"xx"}}`)); err == nil {
		t.Fatal("invalid region should fail")
	}
}

func TestCourseCodeAndExclude(t *testing.T) {
	t.Setenv("MOODLE_BASE_URL", "")
	c, _ := Load(writeConfig(t, `{"exclude":["abc1234"]}`))
	if got := c.CourseCode("Algorithms ABC1234 S2"); got != "ABC1234" {
		t.Fatalf("CourseCode = %q", got)
	}
	if got := c.CourseCode("fit lowercase name"); got != "" {
		t.Fatalf("CourseCode on no-match = %q", got)
	}
	if !c.IsExcluded("ABC1234") {
		t.Fatal("ABC1234 should be excluded (case-insensitive)")
	}
}
