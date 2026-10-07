package store

import (
	"context"
	"database/sql/driver"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"
)

func snapshotFixture(t *testing.T) (*DB, ContentSnapshot) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "lms.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	snap := ContentSnapshot{Kinds: []string{"announcement", "lesson", "reply"}, Items: []Item{
		{ID: "ed:thread:1", CourseID: "TEST", Provider: "ed", Kind: "announcement", Title: "New class", BodyMD: "Original announcement", ContentHash: "a1"},
		{ID: "ed:lesson:2", CourseID: "TEST", Provider: "ed", Kind: "lesson", Title: "Lesson", BodyMD: "Original lecture", ContentHash: "l1"},
		{ID: "ed:reply:3", CourseID: "TEST", Provider: "ed", Kind: "reply", ParentID: "ed:thread:1", BodyMD: "Original reply", ContentHash: "r1", MetaJSON: `{"reply_to_me":true}`},
	}, Deadlines: []Deadline{{CourseID: "TEST", ItemID: "ed:lesson:2", Kind: "lesson", DueAt: 2000}}, Grades: []Grade{{CourseID: "TEST", ItemKey: "ed:lesson:2", Name: "Lesson", Grade: "8", Hash: "g1"}}}
	return db, snap
}

func TestEdSnapshotExplicitDeadlineClearAndUnavailableFields(t *testing.T) {
	db, snap := snapshotFixture(t)
	snap.Deadlines[0].OpensAt = 500
	snap.Deadlines[0].CutoffAt = 2500
	snap.Deadlines[0].SubmissionStatus = "completed"
	snap.Deadlines[0].Completed = true
	if _, err := db.ApplyEdSnapshot(context.Background(), "TEST", snap, 1000); err != nil {
		t.Fatal(err)
	}
	snap.Deadlines[0] = Deadline{CourseID: "TEST", ItemID: "ed:lesson:2", Kind: "lesson", DueAt: 0}
	snap.DeadlineFields = map[string]DeadlinePresence{"ed:lesson:2": {Due: true}}
	if _, err := db.ApplyEdSnapshot(context.Background(), "TEST", snap, 1001); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListDeadlines(0, 0)
	if err != nil || len(rows) != 1 || rows[0].DueAt != 0 || rows[0].OpensAt != 500 || rows[0].CutoffAt != 2500 || !rows[0].Completed || rows[0].SubmissionStatus != "completed" {
		t.Fatalf("window merge: %+v %v", rows, err)
	}
	active, _ := db.ListDeadlines(1000, 0)
	if len(active) != 0 {
		t.Fatal("cleared deadline remains upcoming")
	}
	no := false
	events, _ := db.ListEvents(EventFilter{Baseline: &no})
	if len(events) != 1 || events[0].Kind != "deadline_removed" {
		t.Fatalf("deadline clear event: %+v", events)
	}
}

var leaseDelayOnce sync.Once
var leaseDelayRegistration error

func TestEdSnapshotLeaseExpiryBeforeCommitRollsBack(t *testing.T) {
	leaseDelayOnce.Do(func() {
		leaseDelayRegistration = sqlite.RegisterScalarFunction("m2_lease_delay", 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
			time.Sleep(1100 * time.Millisecond)
			return int64(0), nil
		})
	})
	if leaseDelayRegistration != nil {
		t.Fatal(leaseDelayRegistration)
	}
	db, snap := snapshotFixture(t)
	if _, err := db.pool.Exec(`CREATE TRIGGER m2_delay BEFORE INSERT ON items WHEN NEW.id='ed:thread:1' BEGIN SELECT m2_lease_delay(); END`); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.AcquireLease("sync", "held", 1); err != nil || !ok {
		t.Fatalf("lease: %v %v", ok, err)
	}
	snap.LeaseHolder = "held"
	if _, err := db.ApplyEdSnapshot(context.Background(), "TEST", snap, 1000); err == nil {
		t.Fatal("expired lease published snapshot")
	}
	if _, ok, _ := db.GetMeta("ed:last_success:TEST"); ok {
		t.Fatal("expired lease published checkpoint")
	}
	items, _ := db.ListItems(ItemFilter{CourseID: "TEST"})
	if len(items) != 0 {
		t.Fatal("expired lease published items")
	}
}

func TestEdSnapshotBaselineIdempotentAndRepeatedEdits(t *testing.T) {
	db, snap := snapshotFixture(t)
	first, err := db.ApplyEdSnapshot(context.Background(), "TEST", snap, 1000)
	if err != nil || first.Changes != 5 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	no := false
	events, err := db.ListEvents(EventFilter{Baseline: &no})
	if err != nil || len(events) != 0 {
		t.Fatalf("baseline leaked: %+v %v", events, err)
	}
	second, err := db.ApplyEdSnapshot(context.Background(), "TEST", snap, 1001)
	if err != nil || second.Changes != 0 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	snap.Items[1].BodyMD, snap.Items[1].ContentHash = "Edited lecture", "l2"
	if result, err := db.ApplyEdSnapshot(context.Background(), "TEST", snap, 1002); err != nil || result.Changes != 1 {
		t.Fatalf("edit=%+v %v", result, err)
	}
	snap.Items[1].BodyMD, snap.Items[1].ContentHash = "Original lecture", "l1"
	if result, err := db.ApplyEdSnapshot(context.Background(), "TEST", snap, 1003); err != nil || result.Changes != 1 {
		t.Fatalf("revert=%+v %v", result, err)
	}
	events, err = db.ListEvents(EventFilter{Baseline: &no})
	if err != nil || len(events) != 2 {
		t.Fatalf("repeat edit lost: %d %v", len(events), err)
	}
}

func TestEdSnapshotRollbackKeepsBodyEventsAndCheckpoint(t *testing.T) {
	db, snap := snapshotFixture(t)
	if _, err := db.ApplyEdSnapshot(context.Background(), "TEST", snap, 1000); err != nil {
		t.Fatal(err)
	}
	snap.Items[0].BodyMD, snap.Items[0].ContentHash = "Changed", "a2"
	snap.Files = []File{{ID: "bad-file", ItemID: "outside"}}
	if _, err := db.ApplyEdSnapshot(context.Background(), "TEST", snap, 1001); err == nil {
		t.Fatal("invalid file accepted")
	}
	it, _, _ := db.GetItem("ed:thread:1")
	if it.BodyMD != "Original announcement" {
		t.Fatal("failed transaction changed body")
	}
	checkpoint, _, _ := db.GetMeta("ed:last_success:TEST")
	if checkpoint != "1000" {
		t.Fatal("failed transaction advanced checkpoint")
	}
	events, _ := db.ListEvents(EventFilter{})
	if len(events) != 5 {
		t.Fatal("failed transaction published events")
	}
}

func TestEdSnapshotUnknownInventoryRetainsHistory(t *testing.T) {
	db, snap := snapshotFixture(t)
	if _, err := db.ApplyEdSnapshot(context.Background(), "TEST", snap, 1000); err != nil {
		t.Fatal(err)
	}
	snap.Items = snap.Items[:1]
	snap.Kinds = []string{"announcement"}
	snap.Grades = nil
	snap.Deadlines = nil
	if _, err := db.ApplyEdSnapshot(context.Background(), "TEST", snap, 1001); err != nil {
		t.Fatal(err)
	}
	it, _, _ := db.GetItem("ed:lesson:2")
	if it.RemovedAt != 0 {
		t.Fatal("unavailable inventory removed history")
	}
	snap.Kinds = append(snap.Kinds, "lesson")
	if _, err := db.ApplyEdSnapshot(context.Background(), "TEST", snap, 1002); err != nil {
		t.Fatal(err)
	}
	it, _, _ = db.GetItem("ed:lesson:2")
	if it.RemovedAt != 1002 || it.BodyMD != "Original lecture" {
		t.Fatal("removed material lost archive")
	}
	deadlines, _ := db.ListDeadlines(0, 0)
	if len(deadlines) != 0 {
		t.Fatal("removed material kept active deadline")
	}
}

func TestEdSnapshotCancellationAndLostLeaseDoNotPublish(t *testing.T) {
	db, snap := snapshotFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.ApplyEdSnapshot(ctx, "TEST", snap, 1000); err == nil {
		t.Fatal("canceled snapshot accepted")
	}
	snap.LeaseHolder = "expected"
	db.AcquireLease("sync", "other", 600)
	if _, err := db.ApplyEdSnapshot(context.Background(), "TEST", snap, 1000); err == nil {
		t.Fatal("lost lease accepted")
	}
	if _, ok, _ := db.GetMeta("ed:last_success:TEST"); ok {
		t.Fatal("rejected snapshot published checkpoint")
	}
}

func TestEdSnapshotUnavailableContentPreservesCachedBody(t *testing.T) {
	db, snap := snapshotFixture(t)
	if _, err := db.ApplyEdSnapshot(context.Background(), "TEST", snap, 1000); err != nil {
		t.Fatal(err)
	}
	snap.Items[1].BodyMD = ""
	snap.Items[1].ContentHash = "unavailable"
	snap.Items[1].MetaJSON = `{"content_unavailable":true}`
	result, err := db.ApplyEdSnapshot(context.Background(), "TEST", snap, 1001)
	if err != nil || result.Changes != 0 {
		t.Fatalf("unavailable=%+v %v", result, err)
	}
	it, _, _ := db.GetItem("ed:lesson:2")
	if it.BodyMD != "Original lecture" || it.ContentHash != "l1" {
		t.Fatal("unavailable lesson replaced cached body")
	}
}
