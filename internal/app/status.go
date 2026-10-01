package app

import (
	"time"
)

type courseStatus struct {
	Course             string    `json:"course"`
	Directory          string    `json:"directory"`
	LastSuccess        time.Time `json:"last_success"`
	Threads            int       `json:"cached_threads"`
	Lessons            int       `json:"cached_lessons"`
	DiscussionsEnabled bool      `json:"discussions_enabled"`
	LessonsEnabled     bool      `json:"lessons_enabled"`
	Error              string    `json:"error,omitempty"`
	Materials          int       `json:"cached_materials,omitempty"`
}

func syncStatus(cfg syncConfig) ([]courseStatus, error) {
	r := []courseStatus{}
	for _, co := range allStorageCourses(cfg) {
		s, err := readState(co)
		v := courseStatus{Course: co.Code, Directory: co.Directory, DiscussionsEnabled: enabled(co.Discussions), LessonsEnabled: co.Lessons}
		if err != nil {
			v.Error = err.Error()
		} else {
			v.LastSuccess = s.LastSuccess
			v.Threads = len(s.Threads)
			v.Lessons = len(s.Lessons)
			v.Materials = len(s.MoodleFiles)
		}
		r = append(r, v)
	}
	return r, nil
}
