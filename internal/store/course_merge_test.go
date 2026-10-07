package store

import "testing"

func TestMergeCoursePreservesRecords(t *testing.T) {
	db := openTemp(t)
	source := Course{ID: "moodle-500", Code: "ABC1234", Title: "Synthetic Moodle unit", MoodleCourseID: 500, Active: true}
	destination := Course{ID: "ABC1234-2026S2", Code: "ABC1234", Term: "2026S2", Title: "Synthetic Ed unit", EdCourseID: 1, Active: true}
	for _, c := range []Course{source, destination} {
		if err := db.UpsertCourse(c); err != nil {
			t.Fatal(err)
		}
	}
	item := Item{ID: "moodle:cm:123", CourseID: source.ID, Provider: "moodle", Title: "Synthetic resource", BodyMD: "Synthetic searchable text", ContentHash: "content", MetaJSON: `{"synthetic":true}`}
	if _, err := db.UpsertItem(item); err != nil {
		t.Fatal(err)
	}
	historical := Item{ID: "moodle:cm:124", CourseID: source.ID, Provider: "moodle", Title: "Synthetic removed resource", RemovedAt: 100}
	if _, err := db.UpsertItem(historical); err != nil {
		t.Fatal(err)
	}
	file := File{ID: "synthetic-file", ItemID: item.ID, Name: "synthetic.pdf", LocalPath: "synthetic.pdf", SHA256: "synthetic-hash"}
	if err := db.UpsertFile(file); err != nil {
		t.Fatal(err)
	}
	event := Event{ID: "synthetic-event", CourseID: source.ID, ItemID: item.ID, Kind: "new_material", Title: "Synthetic change", Summary: "Preserved history", Baseline: true, NotifiedAt: 123}
	if err := db.InsertEvent(event); err != nil {
		t.Fatal(err)
	}
	grade := Grade{CourseID: source.ID, ItemKey: "moodle:assign:123", Name: "Synthetic grade", Grade: "8", GradeMax: "10", FeedbackMD: "Synthetic feedback", Hash: "synthetic-grade"}
	if _, err := db.UpsertGrade(grade); err != nil {
		t.Fatal(err)
	}
	deadline := Deadline{CourseID: source.ID, ItemID: item.ID, Kind: "assign", DueAt: 500, SubmissionStatus: "submitted", Completed: true}
	if err := db.UpsertDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := db.MergeCourse(source.ID, destination.ID); err != nil {
		t.Fatal(err)
	}
	destination.MoodleCourseID = source.MoodleCourseID
	if courses, err := db.ListCourses(); err != nil || len(courses) != 1 || courses[0] != destination {
		t.Fatalf("merged course metadata = %+v, err=%v", courses, err)
	}
	for _, expected := range []Item{item, historical} {
		expected.CourseID = destination.ID
		if got, ok, err := db.GetItem(expected.ID); err != nil || !ok || got != expected {
			t.Fatalf("item metadata changed: got=%+v want=%+v ok=%v err=%v", got, expected, ok, err)
		}
	}
	if got, ok, err := db.GetFile(file.ID); err != nil || !ok || got != file {
		t.Fatalf("file metadata changed: got=%+v ok=%v err=%v", got, ok, err)
	}
	event.CourseID = destination.ID
	if got, err := db.ListEvents(EventFilter{CourseID: destination.ID}); err != nil || len(got) != 1 || got[0] != event {
		t.Fatalf("event history changed: got=%+v err=%v", got, err)
	}
	grade.CourseID = destination.ID
	if got, err := db.ListGrades(destination.ID); err != nil || len(got) != 1 || got[0] != grade {
		t.Fatalf("grade changed: got=%+v err=%v", got, err)
	}
	deadline.CourseID = destination.ID
	if got, err := db.ListDeadlines(0, 0); err != nil || len(got) != 1 || got[0] != deadline {
		t.Fatalf("deadline changed: got=%+v err=%v", got, err)
	}
	if hits, err := db.Search("searchable", ItemFilter{CourseID: destination.ID}); err != nil || len(hits) != 1 || hits[0].ItemID != item.ID {
		t.Fatalf("merged item not searchable under destination: hits=%+v err=%v", hits, err)
	}
}

func TestMergeCourseConflictRollsBack(t *testing.T) {
	for _, kind := range []string{"grade", "deadline", "provider identity", "missing destination"} {
		t.Run(kind, func(t *testing.T) {
			db := openTemp(t)
			source := Course{ID: "source", Code: "ABC1234", MoodleCourseID: 500, Active: true}
			destination := Course{ID: "destination", Code: "ABC1234", EdCourseID: 1, Active: true}
			if kind == "provider identity" {
				destination.MoodleCourseID = 501
			}
			if err := db.UpsertCourse(source); err != nil {
				t.Fatal(err)
			}
			if kind != "missing destination" {
				if err := db.UpsertCourse(destination); err != nil {
					t.Fatal(err)
				}
			}
			item := Item{ID: "synthetic-item", CourseID: source.ID, Title: "Synthetic resource", MetaJSON: `{"synthetic":true}`}
			if _, err := db.UpsertItem(item); err != nil {
				t.Fatal(err)
			}
			event := Event{ID: "synthetic-event", CourseID: source.ID, Summary: "Preserved history"}
			if err := db.InsertEvent(event); err != nil {
				t.Fatal(err)
			}
			for _, courseID := range []string{source.ID, destination.ID} {
				if kind == "grade" {
					if _, err := db.UpsertGrade(Grade{CourseID: courseID, ItemKey: "same-key", FeedbackMD: courseID}); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "deadline" {
					if err := db.UpsertDeadline(Deadline{CourseID: courseID, ItemID: "same-item", Kind: "assign", SubmissionStatus: courseID}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := db.MergeCourse(source.ID, destination.ID); err == nil {
				t.Fatal("conflicting merge must return an error")
			}
			courses, err := db.ListCourses()
			if err != nil {
				t.Fatal(err)
			}
			byID := map[string]Course{}
			for _, c := range courses {
				byID[c.ID] = c
			}
			if byID[source.ID] != source || (kind != "missing destination" && byID[destination.ID] != destination) {
				t.Fatalf("failed merge changed courses: %+v", byID)
			}
			if got, ok, err := db.GetItem(item.ID); err != nil || !ok || got != item {
				t.Fatalf("failed merge changed item: got=%+v ok=%v err=%v", got, ok, err)
			}
			if got, err := db.ListEvents(EventFilter{CourseID: source.ID}); err != nil || len(got) != 1 || got[0] != event {
				t.Fatalf("failed merge changed event: got=%+v err=%v", got, err)
			}
			if kind == "grade" {
				for _, courseID := range []string{source.ID, destination.ID} {
					if got, err := db.ListGrades(courseID); err != nil || len(got) != 1 || got[0].FeedbackMD != courseID {
						t.Fatalf("failed merge lost grade: got=%+v err=%v", got, err)
					}
				}
			}
			if kind == "deadline" {
				if got, err := db.ListDeadlines(0, 0); err != nil || len(got) != 2 {
					t.Fatalf("failed merge lost deadline: got=%+v err=%v", got, err)
				}
			}
		})
	}
}
