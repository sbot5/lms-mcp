package syncer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	db    *store.DB
	build func() (*Providers, error)
	hold  string

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
	return &Service{db: db, build: build, hold: newHolder(), jobs: map[string]*JobStatus{}}
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
	s.mu.Unlock()

	go s.run(id, scope, full)
	return *st
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
	defer func() { _ = s.db.ReleaseLease(leaseName, s.hold) }()

	runID := "run-" + id
	_ = s.db.StartSyncRun(store.SyncRun{ID: runID, Scope: scope, Trigger: "api", StartedAt: time.Now().Unix(), Status: "running"})

	// Keep the lease alive for long runs.
	stopRenew := make(chan struct{})
	go s.renew(stopRenew)
	defer close(stopRenew)

	courses, err := s.sync(ctx, scope, full)

	s.mu.Lock()
	st := s.jobs[id]
	st.Finished = time.Now().Unix()
	st.Courses = courses
	status := "ok"
	if err != nil {
		st.Phase, st.Message, status = "error", err.Error(), "error"
	} else {
		st.Phase = "done"
	}
	s.current = ""
	s.mu.Unlock()

	counts, _ := json.Marshal(map[string]int{"courses": courses})
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	_ = s.db.FinishSyncRun(runID, status, string(counts), errMsg, time.Now().Unix())
}

func (s *Service) renew(stop <-chan struct{}) {
	t := time.NewTicker(3 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			_, _ = s.db.RenewLease(leaseName, s.hold, 600)
		}
	}
}

// sync is the actual work. M1 discovers and persists courses; provider content
// sync is added in later milestones. Returns the number of active courses.
func (s *Service) sync(ctx context.Context, scope string, full bool) (int, error) {
	p, err := s.build()
	if err != nil {
		return 0, err
	}
	var active []store.Course
	if p.Ed != nil && (scope == "all" || scope == "ed") {
		eds, err := DiscoverEd(ctx, p, s.db)
		if err != nil {
			return 0, err
		}
		active = append(active, eds...)
	}
	// Moodle discovery is wired when the session client lands.
	return len(active), nil
}
