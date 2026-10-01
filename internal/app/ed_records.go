package app

import (
	"fmt"
	"strings"

	"github.com/sbot5/lms-mcp/internal/ed"
	"github.com/sbot5/lms-mcp/internal/render"
)

type record map[string]any

func roleOf(users map[int]ed.User, id int, anonymous bool) string {
	if anonymous {
		return "anonymous"
	}
	role := users[id].CourseRole
	if role == "" {
		return "unknown"
	}
	return role
}
func isStaff(role string) bool { return role == "admin" || role == "mentor" }

func threadRecord(t ed.ThreadDetail, users map[int]ed.User, co courseConfig) (record, error) {
	md, txt, err := render.Body(t.Content, t.Document, nil)
	if err != nil {
		return nil, err
	}
	role := roleOf(users, t.UserID, t.IsAnonymous)
	replies := []record{}
	var walk func([]ed.Reply, int) error
	walk = func(rs []ed.Reply, depth int) error {
		for _, r := range rs {
			if r.DeletedAt != "" || (r.IsPrivate && !co.IncludePrivate) {
				continue
			}
			md, txt, err := render.Body(r.Content, r.Document, nil)
			if err != nil {
				return err
			}
			role := roleOf(users, r.UserID, r.IsAnonymous)
			replies = append(replies, record{"id": r.ID, "kind": r.Type, "depth": depth, "parent_id": r.ParentID, "author_role": role, "author_is_staff": isStaff(role), "is_endorsed": r.IsEndorsed, "is_accepted": t.AcceptedID != nil && r.ID == *t.AcceptedID, "is_resolved": r.IsResolved, "is_private": r.IsPrivate, "by_bot": r.CreatedByBotID != nil, "vote_count": r.VoteCount, "created_at": r.CreatedAt, "updated_at": r.UpdatedAt, "markdown": md, "text": txt})
			if err := walk(r.Comments, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(t.Answers, 0); err != nil {
		return nil, err
	}
	if err := walk(t.Comments, 0); err != nil {
		return nil, err
	}
	return record{"course": record{"id": co.ID, "code": co.Code, "name": co.Name}, "thread_id": t.ID, "number": t.Number, "url": co.url("discussion", t.ID), "type": t.Type, "category": t.Category, "subcategory": t.Subcategory, "subsubcategory": t.Subsubcategory, "title": t.Title, "created_at": t.CreatedAt, "updated_at": t.UpdatedAt, "author_role": role, "author_is_staff": isStaff(role), "is_answered": t.IsAnswered, "is_staff_answered": t.IsStaffAnswered, "is_student_answered": t.IsStudentAnswered, "is_endorsed": t.IsEndorsed, "is_pinned": t.IsPinned, "is_private": t.IsPrivate, "is_locked": t.IsLocked, "is_megathread": t.IsMegathread, "accepted_answer_id": t.AcceptedID, "vote_count": t.VoteCount, "view_count": t.ViewCount, "reply_count": len(replies), "markdown": md, "text": txt, "replies": replies}, nil
}

func discussionMarkdown(co courseConfig, records []record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — Discussion\n\n%d threads. Full bodies and replies are in `_discussion.jsonl`.\n\n", co.Code, len(records))
	for _, r := range records {
		badge := ""
		if r["is_pinned"] == true {
			badge = " [pinned]"
		}
		fmt.Fprintf(&b, "- #%v%s [%s](%v) — %v replies · %v\n", r["number"], badge, render.Escape(fmt.Sprint(r["title"])), r["url"], r["reply_count"], r["author_role"])
	}
	return b.String()
}
