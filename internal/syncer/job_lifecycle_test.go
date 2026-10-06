package syncer

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sbot5/lms-mcp/internal/config"
	"github.com/sbot5/lms-mcp/internal/httpx"
	"github.com/sbot5/lms-mcp/internal/moodle"
	"github.com/sbot5/lms-mcp/internal/store"
)

func waitForJob(t *testing.T, svc *Service, id string) JobStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, ok := svc.Status(id)
		if !ok {
			t.Fatalf("job %q was not retained", id)
		}
		if st.Phase != "running" {
			return st
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("job %q did not finish", id)
	return JobStatus{}
}

func TestServiceRejectsInvalidScope(t *testing.T) {
	for _, scope := range []string{"", "ALL", "other", "course:ABC1234", "ed "} {
		t.Run(scope, func(t *testing.T) {
			db := openStore(t)
			var builds atomic.Int32
			svc := NewService(db, func() (*Providers, error) {
				builds.Add(1)
				return &Providers{}, nil
			})
			st := svc.Start(scope, false)
			if st.Phase != "error" || st.ID != "" || !strings.Contains(st.Message, "scope") {
				t.Fatalf("invalid scope status = %+v", st)
			}
			if _, ok := svc.Current(); ok || builds.Load() != 0 {
				t.Fatal("invalid scope started a job")
			}
			if _, ok, err := db.LastSyncRun(scope); err != nil || ok {
				t.Fatalf("invalid scope recorded a run: ok=%v err=%v", ok, err)
			}
			if ok, err := db.AcquireLease(leaseName, "other-service", 60); err != nil || !ok {
				t.Fatalf("invalid scope took the lease: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestServiceStartReturnsInitialSnapshot(t *testing.T) {
	db := openStore(t)
	svc := NewService(db, func() (*Providers, error) {
		return nil, errors.New("synthetic build failure")
	})
	for range 100 {
		initial := svc.Start("all", false)
		if initial.Phase != "running" || initial.Finished != 0 || initial.Message != "" {
			t.Fatalf("Start returned mutated status: %+v", initial)
		}
		final := waitForJob(t, svc, initial.ID)
		if final.Phase != "error" || final.Message != "synthetic build failure" {
			t.Fatalf("unexpected final status: %+v", final)
		}
		if initial.Phase != "running" || initial.Finished != 0 {
			t.Fatalf("initial snapshot changed: %+v", initial)
		}
	}
}

func TestServiceKeepsRunningUntilPersistenceAndLeaseCleanup(t *testing.T) {
	db, writer := lifecycleStoreWithWriter(t)

	locked := make(chan *sql.Tx, 1)
	lockErrors := make(chan error, 1)
	requestDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(requestDone)
		tx, err := writer.Begin()
		if err == nil {
			_, err = tx.Exec(`INSERT INTO meta ("key", value) VALUES ('synthetic-lock', 'held')`)
		}
		if err != nil {
			if tx != nil {
				_ = tx.Rollback()
			}
			lockErrors <- err
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		locked <- tx
		_, _ = w.Write([]byte(`{"user":{"id":1},"courses":[]}`))
	}))
	t.Cleanup(srv.Close)
	p := testProviders(t, srv.URL)
	svc := NewService(db, func() (*Providers, error) { return p, nil })
	initial := svc.Start("ed", false)
	var tx *sql.Tx
	select {
	case tx = <-locked:
	case err := <-lockErrors:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("provider request did not acquire the external write lock")
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	<-requestDone

	// The response has no courses to persist, so the external writer now
	// blocks FinishSyncRun. The job must remain current during that write.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		st, _ := svc.Status(initial.ID)
		if st.Phase != "running" || st.Finished != 0 {
			t.Fatalf("terminal status published before persistence: %+v", st)
		}
		current, ok := svc.Current()
		if !ok || current.ID != initial.ID {
			t.Fatalf("job no longer current before persistence: %+v ok=%v", current, ok)
		}
		time.Sleep(time.Millisecond)
	}
	if next := svc.Start("ed", false); next.ID != initial.ID {
		t.Fatalf("Start replaced the job during persistence: %+v", next)
	}
	run, ok, err := db.LastSyncRun("ed")
	if err != nil || !ok || run.Status != "running" {
		t.Fatalf("blocked sync run = %+v ok=%v err=%v", run, ok, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	final := waitForJob(t, svc, initial.ID)
	if final.Phase != "done" {
		t.Fatalf("job did not finish after unlocking: %+v", final)
	}
	run, ok, err = db.LastSyncRun("ed")
	if err != nil || !ok || run.Status != "ok" || run.FinishedAt == 0 {
		t.Fatalf("terminal sync run = %+v ok=%v err=%v", run, ok, err)
	}
	if ok, err := db.AcquireLease(leaseName, "other-service", 60); err != nil || !ok {
		t.Fatalf("terminal job retained its lease: ok=%v err=%v", ok, err)
	}
	if _, ok := svc.Current(); ok {
		t.Fatal("terminal job remained current")
	}
}

func lifecycleStoreWithWriter(t *testing.T) (*store.DB, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lms.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	return db, writer
}

func TestServiceCancelsSyncWhenLeaseRenewalFails(t *testing.T) {
	for _, failure := range []string{"lost", "renewal error"} {
		t.Run(failure, func(t *testing.T) {
			db, writer := lifecycleStoreWithWriter(t)
			requestStarted := make(chan struct{}, 1)
			requestCanceled := make(chan struct{}, 1)
			unblock := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestStarted <- struct{}{}
				select {
				case <-r.Context().Done():
					requestCanceled <- struct{}{}
				case <-unblock:
				}
			}))
			t.Cleanup(srv.Close)
			t.Cleanup(func() { close(unblock) })
			p := testProviders(t, srv.URL)
			svc := NewService(db, func() (*Providers, error) { return p, nil })
			svc.renewEvery = 5 * time.Millisecond
			initial := svc.Start("ed", false)
			select {
			case <-requestStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("provider request did not start")
			}
			message := "sync lease was lost"
			if failure == "lost" {
				if err := db.ReleaseLease(leaseName, svc.hold); err != nil {
					t.Fatal(err)
				}
				if ok, err := db.AcquireLease(leaseName, "other-service", 60); err != nil || !ok {
					t.Fatalf("another service could not take the lease: ok=%v err=%v", ok, err)
				}
			} else {
				_, err := writer.Exec(`CREATE TRIGGER reject_renewal BEFORE UPDATE ON leases BEGIN SELECT RAISE(ABORT, 'synthetic renewal failure'); END`)
				if err != nil {
					t.Fatal(err)
				}
				message = "could not renew sync lease"
			}
			final := waitForJob(t, svc, initial.ID)
			if final.Phase != "error" || !strings.Contains(final.Message, message) {
				t.Fatalf("lease failure reported success: %+v", final)
			}
			select {
			case <-requestCanceled:
			case <-time.After(5 * time.Second):
				t.Fatal("lease failure did not cancel the provider request")
			}
			run, ok, err := db.LastSyncRun("ed")
			if err != nil || !ok || run.Status != "error" || !strings.Contains(run.Error, message) {
				t.Fatalf("lease failure was not persisted: %+v ok=%v err=%v", run, ok, err)
			}
			if failure == "lost" {
				if ok, err := db.AcquireLease(leaseName, "third-service", 60); err != nil || ok {
					t.Fatalf("cleanup deleted the other service's lease: ok=%v err=%v", ok, err)
				}
			}
		})
	}
}

func TestServicePersistenceFailureDoesNotReportSuccess(t *testing.T) {
	for _, phase := range []string{"start", "finish"} {
		t.Run(phase, func(t *testing.T) {
			db, writer := lifecycleStoreWithWriter(t)
			statement := `CREATE TRIGGER reject_start BEFORE INSERT ON sync_runs BEGIN SELECT RAISE(ABORT, 'synthetic start failure'); END`
			message := "could not record sync run"
			if phase == "finish" {
				statement = `CREATE TRIGGER reject_finish BEFORE UPDATE ON sync_runs BEGIN SELECT RAISE(ABORT, 'synthetic finish failure'); END`
				message = "could not record sync outcome"
			}
			if _, err := writer.Exec(statement); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"user":{"id":1},"courses":[]}`))
			}))
			t.Cleanup(srv.Close)
			p := testProviders(t, srv.URL)
			var builds atomic.Int32
			svc := NewService(db, func() (*Providers, error) {
				builds.Add(1)
				return p, nil
			})
			final := waitForJob(t, svc, svc.Start("ed", false).ID)
			if final.Phase != "error" || !strings.Contains(final.Message, message) {
				t.Fatalf("persistence failure reported success: %+v", final)
			}
			if phase == "start" && builds.Load() != 0 {
				t.Fatal("provider sync ran after its run could not be recorded")
			}
			if ok, err := db.AcquireLease(leaseName, "other-service", 60); err != nil || !ok {
				t.Fatalf("persistence failure retained the lease: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestServiceRequestedProviderUnavailable(t *testing.T) {
	for _, scope := range []string{"ed", "moodle"} {
		for _, reason := range []string{"disabled in config", "no synthetic credentials", ""} {
			t.Run(scope+"/"+reason, func(t *testing.T) {
				db := openStore(t)
				p := &Providers{Cfg: &config.Config{}, EdReason: reason, MoodleReason: reason}
				svc := NewService(db, func() (*Providers, error) { return p, nil })
				final := waitForJob(t, svc, svc.Start(scope, false).ID)
				if final.Phase != "error" || !strings.Contains(final.Message, "not available") {
					t.Fatalf("unavailable provider reported success: %+v", final)
				}
				if run, ok, err := db.LastSyncRun(scope); err != nil || !ok || run.Status != "error" {
					t.Fatalf("unavailable provider run = %+v ok=%v err=%v", run, ok, err)
				}
			})
		}
	}
}

func lifecycleMoodleProvider(t *testing.T, p *Providers, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	doer, err := httpx.New(httpx.Options{Guard: httpx.MoodleGuard{Base: base}, RequestsPerS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	p.Moodle = moodle.NewSession(doer, base, "MoodleSession=synthetic", false)
	return srv
}

func TestServiceAllContinuesAfterProviderFailure(t *testing.T) {
	for _, failing := range []string{"ed", "moodle"} {
		t.Run(failing, func(t *testing.T) {
			var edCalls, moodleCalls atomic.Int32
			edSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				edCalls.Add(1)
				if failing == "ed" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				_, _ = w.Write([]byte(whoamiJSON))
			}))
			t.Cleanup(edSrv.Close)
			p := testProviders(t, edSrv.URL)
			lifecycleMoodleProvider(t, p, func(w http.ResponseWriter, r *http.Request) {
				moodleCalls.Add(1)
				if failing == "moodle" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				switch r.URL.Path {
				case "/user/preferences.php":
					_, _ = w.Write([]byte(`<script>M.cfg = {"sesskey":"synthetic"};</script>`))
				case "/lib/ajax/service.php":
					_, _ = w.Write([]byte(`[{"error":false,"data":{"courses":[{"id":500,"shortname":"DEF5678","fullname":"Synthetic unit","visible":1}]}}]`))
				default:
					http.NotFound(w, r)
				}
			})
			db := openStore(t)
			svc := NewService(db, func() (*Providers, error) { return p, nil })
			final := waitForJob(t, svc, svc.Start("all", false).ID)
			if final.Phase != "error" || final.Courses != 1 {
				t.Fatalf("partial run = %+v", final)
			}
			if edCalls.Load() == 0 || moodleCalls.Load() == 0 {
				t.Fatalf("healthy provider was skipped: ed=%d moodle=%d", edCalls.Load(), moodleCalls.Load())
			}
			courses, err := db.ListCourses()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, c := range courses {
				if failing == "ed" && c.MoodleCourseID == 500 || failing == "moodle" && c.EdCourseID == 10 {
					found = true
				}
			}
			if !found {
				t.Fatalf("healthy provider data was not persisted: %+v", courses)
			}
		})
	}
}

func TestServiceAllAggregatesProviderErrors(t *testing.T) {
	edSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(edSrv.Close)
	p := testProviders(t, edSrv.URL)
	lifecycleMoodleProvider(t, p, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	svc := NewService(openStore(t), func() (*Providers, error) { return p, nil })
	final := waitForJob(t, svc, svc.Start("all", false).ID)
	if final.Phase != "error" || !strings.Contains(final.Message, "Ed sync failed") || !strings.Contains(final.Message, "Moodle sync failed") {
		t.Fatalf("provider failures were not aggregated: %+v", final)
	}
}

func TestServiceAllMissingCredentialsDoesNotSkipHealthyProvider(t *testing.T) {
	edSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(whoamiJSON))
	}))
	t.Cleanup(edSrv.Close)
	p := testProviders(t, edSrv.URL)
	p.Cfg.Moodle.Enabled = true
	p.MoodleReason = "no synthetic credentials"
	svc := NewService(openStore(t), func() (*Providers, error) { return p, nil })
	final := waitForJob(t, svc, svc.Start("all", false).ID)
	if final.Phase != "error" || final.Courses != 1 || !strings.Contains(final.Message, p.MoodleReason) {
		t.Fatalf("missing credentials were hidden: %+v", final)
	}
}

func TestServiceAllCountsPairedCourseOnce(t *testing.T) {
	edSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(whoamiJSON))
	}))
	t.Cleanup(edSrv.Close)
	p := testProviders(t, edSrv.URL)
	lifecycleMoodleProvider(t, p, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/preferences.php":
			_, _ = w.Write([]byte(`<script>M.cfg = {"sesskey":"synthetic"};</script>`))
		case "/lib/ajax/service.php":
			_, _ = w.Write([]byte(`[{"error":false,"data":{"courses":[{"id":500,"shortname":"ABC1234","fullname":"Synthetic unit","visible":1}]}}]`))
		default:
			http.NotFound(w, r)
		}
	})
	db := openStore(t)
	svc := NewService(db, func() (*Providers, error) { return p, nil })
	final := waitForJob(t, svc, svc.Start("all", false).ID)
	if final.Phase != "done" || final.Courses != 1 {
		t.Fatalf("paired course count = %+v", final)
	}
	run, ok, err := db.LastSyncRun("all")
	if err != nil || !ok || run.CountsJSON != `{"courses":1}` {
		t.Fatalf("persisted paired course count = %+v ok=%v err=%v", run, ok, err)
	}
}

func TestServiceAllWithoutProvidersFails(t *testing.T) {
	for _, p := range []*Providers{nil, {Cfg: &config.Config{}}} {
		svc := NewService(openStore(t), func() (*Providers, error) { return p, nil })
		if final := waitForJob(t, svc, svc.Start("all", false).ID); final.Phase != "error" {
			t.Fatalf("no providers reported success: %+v", final)
		}
	}
}

func TestServiceSyncValidatesScope(t *testing.T) {
	svc := NewService(openStore(t), func() (*Providers, error) {
		t.Fatal("invalid scope built providers")
		return nil, nil
	})
	if _, err := svc.sync(context.Background(), "unknown", false); err == nil {
		t.Fatal("sync accepted an invalid scope")
	}
}
