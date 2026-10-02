// Package config loads lms-mcp's user configuration and resolves the data
// directory and per-provider enablement. It holds no credentials; those come
// from the secrets package. Relative paths resolve against the config file's
// directory.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Config is the on-disk user configuration (config.json).
type Config struct {
	// DataDir is where the SQLite index, logs and course folders live.
	// Relative to the config file's directory. Defaults to "data".
	DataDir string `json:"data_dir"`

	// Ed holds Ed Discussion/Lessons settings.
	Ed EdConfig `json:"ed"`

	// Moodle holds Moodle settings. Disabled when BaseURL is empty and no
	// MOODLE_BASE_URL is set.
	Moodle MoodleConfig `json:"moodle"`

	// CourseCodePattern extracts a course code from Ed/Moodle course names
	// for pairing. Defaults to CoursePattern.
	CourseCodePattern string `json:"course_code_pattern,omitempty"`

	// Exclude lists course codes to skip (case-insensitive).
	Exclude []string `json:"exclude,omitempty"`

	// MaxFileMB caps a single downloaded file. Default 200.
	MaxFileMB int `json:"max_file_mb,omitempty"`

	dir string // directory of the loaded config file; not serialized
}

// EdConfig configures the Ed provider.
type EdConfig struct {
	Enabled bool   `json:"enabled"`
	Region  string `json:"region,omitempty"` // au (default), us, eu
}

// MoodleConfig configures the Moodle provider.
type MoodleConfig struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"base_url,omitempty"` // overridden by MOODLE_BASE_URL
}

// CoursePattern is the default course-code regex (e.g. ABC1234 or ABCD1234).
const CoursePattern = `[A-Z]{3,4}[0-9]{4}`

// defaults applied by Load when fields are unset.
const (
	defaultDataDir   = "data"
	defaultRegion    = "au"
	defaultMaxFileMB = 200
)

var validRegion = map[string]bool{"au": true, "us": true, "eu": true}

// Load reads and validates the config at path, applies defaults, and records
// the config directory for DataPath. A missing file yields a default config
// rooted at path's directory, so first run works before `setup` writes one.
func Load(path string) (*Config, error) {
	dir := filepath.Dir(path)
	c := &Config{dir: dir}
	b, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		// leave zero config; defaults applied below
	case err != nil:
		return nil, fmt.Errorf("read config: %w", err)
	default:
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(c); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
		c.dir = dir
	}
	if err := c.applyDefaultsAndValidate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) applyDefaultsAndValidate() error {
	if c.DataDir == "" {
		c.DataDir = defaultDataDir
	}
	if c.Ed.Region == "" {
		c.Ed.Region = defaultRegion
	}
	if !validRegion[c.Ed.Region] {
		return fmt.Errorf("ed.region must be au, us or eu")
	}
	if c.MaxFileMB == 0 {
		c.MaxFileMB = defaultMaxFileMB
	}
	if c.MaxFileMB < 1 || c.MaxFileMB > 4096 {
		return fmt.Errorf("max_file_mb must be between 1 and 4096")
	}
	if c.CourseCodePattern == "" {
		c.CourseCodePattern = CoursePattern
	}
	if _, err := regexp.Compile(c.CourseCodePattern); err != nil {
		return fmt.Errorf("course_code_pattern is not a valid regex")
	}
	// MOODLE_BASE_URL overrides the JSON base URL; its presence enables Moodle.
	if env := strings.TrimSpace(os.Getenv("MOODLE_BASE_URL")); env != "" {
		c.Moodle.BaseURL = env
		c.Moodle.Enabled = true
	}
	if c.Moodle.BaseURL != "" {
		if !strings.HasPrefix(c.Moodle.BaseURL, "https://") && !strings.HasPrefix(c.Moodle.BaseURL, "http://") {
			return fmt.Errorf("moodle.base_url must be an http(s) URL")
		}
		c.Moodle.BaseURL = strings.TrimRight(c.Moodle.BaseURL, "/")
	} else {
		c.Moodle.Enabled = false
	}
	return nil
}

// DataPath resolves a path inside the data directory (relative paths resolve
// against the config file's directory).
func (c *Config) DataPath(elem ...string) string {
	base := c.DataDir
	if !filepath.IsAbs(base) {
		base = filepath.Join(c.dir, base)
	}
	return filepath.Join(append([]string{base}, elem...)...)
}

// DBPath is the SQLite database path inside the data directory.
func (c *Config) DBPath() string { return c.DataPath("lms.db") }

// CourseCode returns the first course code found in name, uppercased, or "".
func (c *Config) CourseCode(name string) string {
	re := regexp.MustCompile(c.CourseCodePattern)
	return re.FindString(strings.ToUpper(name))
}

// IsExcluded reports whether a course code is excluded (case-insensitive).
func (c *Config) IsExcluded(code string) bool {
	for _, e := range c.Exclude {
		if strings.EqualFold(strings.TrimSpace(e), code) {
			return true
		}
	}
	return false
}
