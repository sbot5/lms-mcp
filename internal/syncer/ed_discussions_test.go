package syncer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEdDiscussionIncrementalNestedPrivacyAndFullRefresh(t *testing.T) {
	text := "first reply"
	details := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Query().Has("view") {
			t.Error("side-effecting request")
		}
		switch r.URL.Path {
		case "/api/user":
			fmt.Fprint(w, whoamiJSON)
		case "/api/courses/10/threads":
			if r.URL.Query().Get("offset") == "0" {
				fmt.Fprint(w, `{"threads":[{"id":9,"course_id":10,"user_id":1,"title":"My question","reply_count":2}]}`)
			} else {
				fmt.Fprint(w, `{"threads":[]}`)
			}
		case "/api/threads/9":
			details++
			fmt.Fprintf(w, `{"thread":{"id":9,"course_id":10,"user_id":1,"title":"My question","is_private":true,"content":"<paragraph>Question</paragraph>","answers":[{"id":90,"user_id":2,"content":"<paragraph>%s</paragraph>","comments":[{"id":91,"user_id":3,"content":"<paragraph>nested</paragraph>"}]}]},"users":[{"id":1,"name":"Private learner","course_role":"student"},{"id":2,"name":"Staff member","course_role":"admin"},{"id":3,"name":"Private classmate","course_role":"student"}]}`, text)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	db := openStore(t)
	p := testProviders(t, server.URL)
	courses, err := DiscoverEd(context.Background(), p, db)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := collectEdDiscussions(context.Background(), p, db, courses[0], false)
	if err != nil || len(snap.Items) != 3 {
		t.Fatalf("snapshot %+v %v", snap, err)
	}
	if snap.Items[0].AuthorDisplay != "Student" || snap.Items[1].AuthorDisplay != "Staff member" || snap.Items[2].ParentID != "ed:reply:90" || snap.Items[2].AuthorDisplay != "Student" {
		t.Fatalf("privacy/tree %+v", snap.Items)
	}
	if _, err = db.ApplyEdSnapshot(context.Background(), courses[0].ID, snap, 1000); err != nil {
		t.Fatal(err)
	}
	_, err = collectEdDiscussions(context.Background(), p, db, courses[0], false)
	if err != nil || details != 1 {
		t.Fatalf("incremental details=%d %v", details, err)
	}
	text = "edited reply"
	full, err := collectEdDiscussions(context.Background(), p, db, courses[0], true)
	if err != nil || details != 2 || full.Items[1].ContentHash == snap.Items[1].ContentHash {
		t.Fatalf("full refresh missed edit %v", err)
	}
}
