package syncer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/sbot5/lms-mcp/internal/store"
)

const leaseName = "sync"

// Service runs sync jobs. One sync runs at a time per machine, enforced by a
// store lease; other processes see a blocked status rather than racing.
type Service struct {
	db         *store.DB
	build      func() (*Providers, error)
	hold       string
	renewEvery time.Duration

	mu      sync.Mutex
	jobs    map[string]*JobStatus
	current string // in-process running job id, "" if none
}

// JobStatus is a snapshot of one sync job.
type JobStatus struct {
	ID       string `json:"id"`
	Scope    string `json:"scope"`
	Phase    string `json:"phase"` // running, done, error, blocked
	Started  int64  `json:"started_at,omitempty"`
	Finished int64  `json:"finished_at,omitempty"`
	Courses  int    `json:"courses,omitempty"`
	Message  string `json:"message,omitempty"`
}

// NewService builds a sync service. build is called per run so configuration
// and credential changes take effect without a restart.
func NewService(db *store.DB, build func() (*Providers, error)) *Service {
	return &Service{db: db, build: build, hold: newHolder(), renewEvery: 3 * time.Minute, jobs: map[string]*JobStatus{}}
}

func newHolder() string {
	host, _ := os.Hostname()
	var b [4]byte
	rand.Read(b[:])
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(b[:]))
}

// Start begins a sync and returns the initial status. If a sync is already
// running (in this process or another), it returns that/blocked status without
// starting a second one. full is accepted for later milestones.
func (s *Service) Start(scope string, full bool) JobStatus {
	if !validScope(scope) {
		return JobStatus{Scope: scope, Phase: "error", Message: "scope must be all, ed or moodle"}
	}
	s.mu.Lock()
	if s.current != "" {
		st := *s.jobs[s.current]
		s.mu.Unlock()
		return st
	}
	ok, err := s.db.AcquireLease(leaseName, s.hold, 600)
	if err != nil {
		s.mu.Unlock()
		return JobStatus{Phase: "error", Message: "could not acquire sync lease"}
	}
	if !ok {
		s.mu.Unlock()
		return JobStatus{Phase: "blocked", Message: "another sync is already running"}
	}
	id := newJobID()
	st := &JobStatus{ID: id, Scope: scope, Phase: "running", Started: time.Now().Unix()}
	s.jobs[id] = st
	s.current = id
	initial := *st
	s.mu.Unlock()

	go s.run(id, scope, full)
	return initial
}

// Status returns a snapshot of a job by id.
func (s *Service) Status(id string) (JobStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.jobs[id]
	if !ok {
		return JobStatus{}, false
	}
	return *st, true
}

// Current returns the running job, if any.
func (s *Service) Current() (JobStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == "" {
		return JobStatus{}, false
	}
	return *s.jobs[s.current], true
}

func newJobID() string {
	var b [6]byte
	rand.Read(b[:])
	return "job-" + hex.EncodeToString(b[:])
}

// run executes the sync on a detached context and records the outcome.
func (s *Service) run(id, scope string, full bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// Keep the lease alive for long runs.
	stopRenew := make(chan struct{})
	renewDone := make(chan error, 1)
	go func() {
		renewDone <- s.renew(ctx, cancel, stopRenew)
	}()

	runID := "run-" + id
	err := s.db.StartSyncRun(store.SyncRun{ID: runID, Scope: scope, Trigger: "api", StartedAt: time.Now().Unix(), Status: "running"})
	started := err == nil
	var courses int
	if started {
		courses, err = s.sync(ctx, scope, full)
	} else {
		err = errors.New("could not record sync run")
	}

	// Wait for renewal to exit before recording the outcome, so lease loss
	// cannot be hidden behind a successful sync result or affect the next run.
	close(stopRenew)
	err = errors.Join(err, <-renewDone)
	if started {
		counts, _ := json.Marshal(map[string]int{"courses": courses})
		status, errMsg := "ok", ""
		if err != nil {
			status, errMsg = "error", err.Error()
		}
		if finishErr := s.db.FinishSyncRun(runID, status, string(counts), errMsg, time.Now().Unix()); finishErr != nil {
			err = errors.Join(err, errors.New("could not record sync outcome"))
		}
	}

	// Do not allow another Start until persistence and all lease work finish.
	if releaseErr := s.db.ReleaseLease(leaseName, s.hold); releaseErr != nil {
		err = errors.Join(err, errors.New("could not release sync lease"))
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.jobs[id]
	st.Finished = time.Now().Unix()
	st.Courses = courses
	if err != nil {
		st.Phase, st.Message = "error", err.Error()
	} else {
		st.Phase = "done"
	}
	s.current = ""
}

func (s *Service) renew(ctx context.Context, cancel context.CancelFunc, stop <-chan struct{}) error {
	t := time.NewTicker(s.renewEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return nil
		case <-ctx.Done():
			return nil
		case <-t.C:
			ok, err := s.db.RenewLease(leaseName, s.hold, 600)
			if err != nil {
				cancel()
				return errors.New("could not renew sync lease")
			}
			if !ok {
				cancel()
				return errors.New("sync lease was lost")
			}
		}
	}
}

// sync is the actual work. M1 discovers and persists courses; provider content
// sync is added in later milestones. Returns the number of active courses.
func (s *Service) sync(ctx context.Context, scope string, full bool) (int, error) {
	if !validScope(scope) {
		return 0, errors.New("scope must be all, ed or moodle")
	}
	p, err := s.build()
	if err != nil {
		return 0, err
	}
	if p == nil {
		return 0, errors.New("no sync providers are available")
	}
	var active []store.Course
	var errs []error
	available := false
	if scope == "all" || scope == "ed" {
		if p.Ed != nil {
			available = true
			eds, err := DiscoverEd(ctx, p, s.db)
			if err != nil {
				errs = append(errs, fmt.Errorf("Ed sync failed: %w", err))
			} else {
				active = append(active, eds...)
			}
		} else if scope == "ed" || (p.Cfg != nil && p.Cfg.Ed.Enabled) {
			errs = append(errs, unavailableProvider("Ed", p.EdReason))
		}
	}
	if scope == "all" || scope == "moodle" {
		if p.Moodle != nil {
			available = true
			ms, err := DiscoverMoodle(ctx, p, s.db)
			if err != nil {
				errs = append(errs, fmt.Errorf("Moodle sync failed: %w", err))
			} else {
				active = append(active, ms...)
			}
		} else if scope == "moodle" || (p.Cfg != nil && p.Cfg.Moodle.Enabled) {
			errs = append(errs, unavailableProvider("Moodle", p.MoodleReason))
		}
	}
	if !available && len(errs) == 0 {
		errs = append(errs, errors.New("no sync providers are available"))
	}
	// A course paired across both providers is one active course.
	byID := make(map[string]struct{}, len(active))
	for _, c := range active {
		byID[c.ID] = struct{}{}
	}
	return len(byID), errors.Join(errs...)
}

func validScope(scope string) bool {
	return scope == "all" || scope == "ed" || scope == "moodle"
}

func unavailableProvider(name, reason string) error {
	if reason == "" {
		reason = "disabled or credentials are unavailable"
	}
	return fmt.Errorf("%s is not available: %s", name, reason)
}
