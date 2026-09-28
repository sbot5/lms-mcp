package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

func runWhoami(ctx context.Context, c *edClient, _ []string) error {
	res, err := c.whoami(ctx)
	if err != nil {
		return err
	}
	if c.includeIdentity {
		fmt.Printf("%s (Ed user %d)\n", res.Name, res.UserID)
	} else {
		fmt.Println("Ed courses (account identity hidden)")
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tYEAR\tSESSION\tROLE\tCODE\tNAME")
	for _, co := range res.Courses {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", co.ID, co.Year, co.Session, co.Role, co.Code, co.Name)
	}
	return tw.Flush()
}

func runThreads(ctx context.Context, c *edClient, args []string) error {
	courseID, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("course id must be a number, got %q", args[0])
	}
	threads, err := c.threads(ctx, courseID)
	if err != nil {
		return err
	}
	if !c.includePrivate {
		threads = slices.DeleteFunc(threads, func(t edThread) bool { return t.IsPrivate })
	}
	if len(threads) == 0 {
		fmt.Println("no threads")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NUMBER\tID\tTYPE\tPINNED\tREPLIES\tUPDATED\tTITLE")
	lo, hi, pinned := threads[0].Number, threads[0].Number, 0
	for _, t := range threads {
		mark := ""
		if t.IsPinned {
			mark, pinned = "pinned", pinned+1
		}
		lo, hi = min(lo, t.Number), max(hi, t.Number)
		fmt.Fprintf(tw, "#%d\t%d\t%s\t%s\t%d\t%s\t%s\n", t.Number, t.ID, t.Type, mark, t.ReplyCount, shortTime(t.UpdatedAt), t.Title)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	// Ed numbers threads course-wide and the list holds only the ones this user can see, so gaps are normal.
	fmt.Printf("%d threads (%d pinned); numbers #%d-#%d, %d of them not in the list\n",
		len(threads), pinned, lo, hi, hi-lo+1-len(threads))
	return nil
}

func runThread(ctx context.Context, c *edClient, args []string) error {
	id, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("thread id must be a number, got %q", args[0])
	}
	t, users, err := c.thread(ctx, id)
	if err != nil {
		return err
	}
	_, text, err := renderBody(t.Content, t.Document, nil)
	if err != nil {
		return err
	}
	by := author(users, t.UserID)
	if t.IsAnonymous {
		by = "anonymous"
	}

	if t.IsPrivate && !c.includePrivate {
		return fmt.Errorf("private posts are disabled in privacy.include_private_posts")
	}
	fmt.Printf("#%d %s\n", t.Number, t.Title)
	fmt.Println((courseConfig{ID: t.CourseID, Region: c.region}).url("discussion", t.ID))
	fmt.Printf("%s by %s, created %s, updated %s", t.Type, by, shortTime(t.CreatedAt), shortTime(t.UpdatedAt))
	if t.IsPinned {
		fmt.Print(", pinned")
	}
	fmt.Printf("\n\n%s\n", strings.TrimSpace(text))

	n := 0
	var renderErr error
	var walk func(replies []edReply, depth int)
	walk = func(replies []edReply, depth int) {
		for _, r := range replies {
			if r.DeletedAt != "" || (r.IsPrivate && !c.includePrivate) {
				continue
			}
			_, body, err := renderBody(r.Content, r.Document, nil)
			if err != nil {
				renderErr = err
				return
			}
			by := author(users, r.UserID)
			if r.IsAnonymous {
				by = "anonymous"
			}
			n++
			indent := strings.Repeat("    ", depth)
			fmt.Printf("\n%s[%s] %s, %s", indent, r.Type, by, shortTime(r.CreatedAt))
			if r.IsEndorsed {
				fmt.Print(", endorsed")
			}
			if r.DeletedAt != "" {
				fmt.Print(", deleted")
			}
			fmt.Println()
			for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
				fmt.Println(indent + line)
			}
			walk(r.Comments, depth+1)
		}
	}
	walk(t.Answers, 0)
	walk(t.Comments, 0)
	if renderErr != nil {
		return renderErr
	}
	fmt.Printf("\n%d replies (Ed's reply_count: %d)\n", n, t.ReplyCount)
	return nil
}

// author labels who wrote a post. Staff are named; classmates are not, so their names stay out of anything printed.
func author(users map[int]edUser, id int) string {
	u, ok := users[id]
	switch {
	case !ok:
		return "unknown user"
	case u.CourseRole == "student":
		return "a student"
	case isStaff(u.CourseRole):
		return u.Name + " (" + u.CourseRole + ")"
	default:
		return "a participant"
	}
}

// shortTime formats an Ed timestamp as "2006-01-02 15:04" in the UTC offset Ed gave it.
func shortTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Format("2006-01-02 15:04")
}
