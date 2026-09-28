package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func initConfig(ctx context.Context, path, envFile, region string, in io.Reader, out io.Writer) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("configuration already exists; edit it instead of overwriting")
	} else if !os.IsNotExist(err) {
		return err
	}
	scan := bufio.NewScanner(in)
	prompt := func(label, fallback string) (string, error) {
		fmt.Fprintf(out, "%s [%s]: ", label, fallback)
		if !scan.Scan() {
			if err := scan.Err(); err != nil {
				return "", err
			}
			return "", fmt.Errorf("configuration cancelled (input closed)")
		}
		s := strings.TrimSpace(scan.Text())
		if s == "" {
			s = fallback
		}
		return s, nil
	}
	var err error
	if envFile == "" {
		envFile, err = prompt("Path to your private .env file", filepath.Join(filepath.Dir(path), ".env"))
		if err != nil {
			return err
		}
	}
	envFile, err = filepath.Abs(envFile)
	if err != nil {
		return err
	}
	token, err := loadToken(envFile)
	if err != nil {
		return err
	}
	if region == "" {
		region, err = prompt("Ed source-link region (au/us/eu)", "au")
		if err != nil {
			return err
		}
	}
	c := newEdClient(token)
	account, err := c.whoami(ctx)
	if err != nil {
		return err
	}
	return configureCourses(path, envFile, region, account.Courses, prompt, out)
}

func configureCourses(path, envFile, region string, courses []edCourse, prompt func(string, string) (string, error), out io.Writer) error {
	if len(courses) == 0 {
		return fmt.Errorf("no enrolled courses were returned")
	}
	for i, co := range courses {
		fmt.Fprintf(out, "%d. %s — %s (%s %s)\n", i+1, co.Code, co.Name, co.Year, co.Session)
	}
	selection, err := prompt("Select course numbers separated by commas (or all)", "")
	if err != nil {
		return err
	}
	indices, err := selectedCourses(selection, len(courses))
	if err != nil {
		return err
	}
	root, err := prompt("Output folder", filepath.Join(filepath.Dir(path), "courses"))
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	lessons, err := prompt("Download Lessons as well as discussions? (yes/no)", "yes")
	if err != nil {
		return err
	}
	wantLessons, err := yesNo(lessons)
	if err != nil {
		return err
	}
	attachments, err := prompt("Download lesson attachments? (yes/no)", "yes")
	if err != nil {
		return err
	}
	wantAttachments, err := yesNo(attachments)
	if err != nil {
		return err
	}
	daily, err := prompt("Daily local time (HH:mm; install the task separately)", "09:00")
	if err != nil {
		return err
	}
	cfg := syncConfig{Region: region, EnvFile: envFile, FullRefreshHours: 24, DownloadAttachments: &wantAttachments, Schedule: scheduleConfig{DailyAt: daily}, Courses: []courseConfig{}}
	for _, i := range indices {
		co := courses[i]
		cfg.Courses = append(cfg.Courses, courseConfig{ID: co.ID, Code: co.Code, Name: co.Name, Directory: filepath.Join(root, fmt.Sprintf("%s-%d", safeName(co.Code, 50), co.ID)), Lessons: wantLessons})
	}
	cfg, err = normalizeConfig(cfg, path)
	if err != nil {
		return err
	}
	if err = saveNewConfig(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "Saved %s\nRun: lms-mcp config validate -config %q\nThen: lms-mcp sync -config %q\nScheduling is not installed automatically. See README.\n", path, path, path)
	return nil
}

func selectedCourses(value string, count int) ([]int, error) {
	if strings.EqualFold(strings.TrimSpace(value), "all") {
		r := make([]int, count)
		for i := range r {
			r[i] = i
		}
		return r, nil
	}
	var result []int
	seen := map[int]bool{}
	for _, s := range strings.Split(value, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 1 || n > count {
			return nil, fmt.Errorf("select course numbers between 1 and %d", count)
		}
		if !seen[n] {
			result = append(result, n-1)
			seen[n] = true
		}
	}
	return result, nil
}
func yesNo(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "y":
		return true, nil
	case "no", "n":
		return false, nil
	default:
		return false, fmt.Errorf("answer yes or no")
	}
}
func saveNewConfig(path string, cfg syncConfig) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

type courseStatus struct {
	Course             string    `json:"course"`
	Directory          string    `json:"directory"`
	LastSuccess        time.Time `json:"last_success"`
	Threads            int       `json:"cached_threads"`
	Lessons            int       `json:"cached_lessons"`
	DiscussionsEnabled bool      `json:"discussions_enabled"`
	LessonsEnabled     bool      `json:"lessons_enabled"`
	Error              string    `json:"error,omitempty"`
}

func syncStatus(cfg syncConfig) ([]courseStatus, error) {
	r := []courseStatus{}
	for _, co := range cfg.Courses {
		s, err := readState(co)
		v := courseStatus{Course: co.Code, Directory: co.Directory, DiscussionsEnabled: enabled(co.Discussions), LessonsEnabled: co.Lessons}
		if err != nil {
			v.Error = err.Error()
		} else {
			v.LastSuccess = s.LastSuccess
			v.Threads = len(s.Threads)
			v.Lessons = len(s.Lessons)
		}
		r = append(r, v)
	}
	return r, nil
}
