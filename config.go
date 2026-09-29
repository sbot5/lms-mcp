package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type courseConfig struct {
	ID                  int    `json:"id"`
	Code                string `json:"code"`
	Name                string `json:"name"`
	Directory           string `json:"directory"`
	Lessons             bool   `json:"lessons"`
	Discussions         *bool  `json:"discussions,omitempty"`
	Region              string `json:"-"`
	DownloadAttachments *bool  `json:"-"`
	IncludePrivate      bool   `json:"-"`
}

type privacyConfig struct {
	IncludeIdentity     bool `json:"include_identity"`
	IncludePrivatePosts bool `json:"include_private_posts"`
}
type scheduleConfig struct {
	DailyAt string `json:"daily_at"`
}
type syncConfig struct {
	Courses             []courseConfig `json:"courses"`
	FullRefreshHours    int            `json:"full_refresh_hours"`
	Region              string         `json:"region"`
	EnvFile             string         `json:"env_file,omitempty"`
	DownloadAttachments *bool          `json:"download_attachments,omitempty"`
	Privacy             privacyConfig  `json:"privacy"`
	Schedule            scheduleConfig `json:"schedule"`
	Moodle              *moodleConfig  `json:"moodle,omitempty"`
}

func enabled(value *bool) bool { return value == nil || *value }
func (co courseConfig) region() string {
	if co.Region == "" {
		return "au"
	}
	return co.Region
}
func (co courseConfig) url(kind string, id int) string {
	return fmt.Sprintf("https://edstem.org/%s/courses/%d/%s/%d", co.region(), co.ID, kind, id)
}
func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "config.json"
	}
	return filepath.Join(dir, "lms-mcp", "config.json")
}

func loadConfig(path string) (syncConfig, error) {
	var cfg syncConfig
	f, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err = d.Decode(&cfg); err != nil {
		return cfg, err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return cfg, fmt.Errorf("config must contain exactly one JSON object")
	}
	return normalizeConfig(cfg, path)
}

func normalizeConfig(cfg syncConfig, path string) (syncConfig, error) {
	var err error
	path, err = filepath.Abs(path)
	if err != nil {
		return cfg, err
	}
	if cfg.Region == "" {
		cfg.Region = "au"
	}
	if cfg.Region != "au" && cfg.Region != "us" && cfg.Region != "eu" {
		return cfg, fmt.Errorf("region must be au, us or eu (source link region)")
	}
	if cfg.Schedule.DailyAt == "" {
		cfg.Schedule.DailyAt = "09:00"
	}
	if _, err = time.Parse("15:04", cfg.Schedule.DailyAt); err != nil {
		return cfg, fmt.Errorf("schedule.daily_at must be HH:mm")
	}
	if cfg.EnvFile != "" && !filepath.IsAbs(cfg.EnvFile) {
		cfg.EnvFile = filepath.Join(filepath.Dir(path), cfg.EnvFile)
	}
	if len(cfg.Courses) == 0 && (cfg.Moodle == nil || len(cfg.Moodle.Courses) == 0) {
		return cfg, fmt.Errorf("config must contain courses")
	}
	if cfg.FullRefreshHours == 0 {
		cfg.FullRefreshHours = 24
	}
	if cfg.FullRefreshHours < 1 || cfg.FullRefreshHours > 8760 {
		return cfg, fmt.Errorf("full_refresh_hours must be 1..8760")
	}
	ids := map[int]bool{}
	dirs := map[string]bool{}
	for i := range cfg.Courses {
		co := &cfg.Courses[i]
		co.Region = cfg.Region
		co.DownloadAttachments = cfg.DownloadAttachments
		co.IncludePrivate = cfg.Privacy.IncludePrivatePosts
		if co.ID <= 0 || co.Code == "" || co.Directory == "" {
			return cfg, fmt.Errorf("each course requires positive id, code and directory")
		}
		if !filepath.IsAbs(co.Directory) {
			co.Directory = filepath.Join(filepath.Dir(path), co.Directory)
		}
		co.Directory, err = filepath.Abs(co.Directory)
		if err != nil {
			return cfg, err
		}
		key := filepath.Clean(co.Directory)
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if ids[co.ID] || dirs[key] {
			return cfg, fmt.Errorf("duplicate course id or directory")
		}
		ids[co.ID] = true
		dirs[key] = true
	}
	for a := range dirs {
		for b := range dirs {
			if a != b && strings.HasPrefix(a, b+string(filepath.Separator)) {
				return cfg, fmt.Errorf("course directories must not overlap")
			}
		}
	}
	if cfg.Moodle != nil {
		if err := normalizeMoodle(cfg.Moodle, path, dirs); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}
