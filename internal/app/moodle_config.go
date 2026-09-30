package app

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

type moodleConfig struct {
	BaseURL string         `json:"base_url"`
	Auth    string         `json:"auth"`
	EnvFile string         `json:"env_file"`
	Courses []moodleCourse `json:"courses"`
}
type moodleCourse struct {
	ID          int    `json:"id"`
	Code        string `json:"code"`
	Directory   string `json:"directory"`
	CalendarEnv string `json:"calendar_url_env,omitempty"`
}

var envKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func normalizeMoodle(cfg *moodleConfig, configPath string, otherDirs map[string]bool) error {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("moodle.base_url must be a site URL without credentials, query or fragment")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return fmt.Errorf("Moodle requires HTTPS (HTTP allowed only for loopback testing)")
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Auth != "cookie" && cfg.Auth != "token" {
		return fmt.Errorf("moodle.auth must be cookie or token")
	}
	if cfg.EnvFile == "" {
		return fmt.Errorf("moodle.env_file is required; keep credentials outside config")
	}
	if !filepath.IsAbs(cfg.EnvFile) {
		cfg.EnvFile = filepath.Join(filepath.Dir(configPath), cfg.EnvFile)
	}
	if len(cfg.Courses) == 0 {
		return fmt.Errorf("moodle.courses must not be empty")
	}
	ids := map[int]bool{}
	for i := range cfg.Courses {
		co := &cfg.Courses[i]
		if co.ID <= 0 || co.Code == "" || co.Directory == "" || ids[co.ID] {
			return fmt.Errorf("Moodle courses require unique positive IDs, codes and directories")
		}
		ids[co.ID] = true
		if co.CalendarEnv != "" && !envKeyPattern.MatchString(co.CalendarEnv) {
			return fmt.Errorf("calendar_url_env must name an environment-file variable, not a URL")
		}
		if !filepath.IsAbs(co.Directory) {
			co.Directory = filepath.Join(filepath.Dir(configPath), co.Directory)
		}
		co.Directory, err = filepath.Abs(co.Directory)
		if err != nil {
			return err
		}
		key := filepath.Clean(co.Directory)
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		for old := range otherDirs {
			if key == old || strings.HasPrefix(key, old+string(filepath.Separator)) || strings.HasPrefix(old, key+string(filepath.Separator)) {
				return fmt.Errorf("Ed and Moodle output directories must not overlap")
			}
		}
		otherDirs[key] = true
	}
	return nil
}
func (co moodleCourse) storage(cfg moodleConfig) courseConfig {
	off := false
	return courseConfig{ID: co.ID, Code: "moodle:" + co.Code, Directory: co.Directory, Region: "moodle:" + cfg.BaseURL, Discussions: &off}
}
func allStorageCourses(cfg syncConfig) []courseConfig {
	r := append([]courseConfig{}, cfg.Courses...)
	if cfg.Moodle != nil {
		for _, co := range cfg.Moodle.Courses {
			r = append(r, co.storage(*cfg.Moodle))
		}
	}
	return r
}
