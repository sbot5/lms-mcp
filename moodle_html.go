package main

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

type moodlePageLinks struct {
	Files, Pages []string
	CoursePage   bool
}

func (c *moodleClient) parsePage(raw []byte, pageURL string, courseID int) (moodlePageLinks, error) {
	root, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return moodlePageLinks{}, fmt.Errorf("invalid Moodle HTML")
	}
	base, _ := url.Parse(pageURL)
	result := moodlePageLinks{}
	files, pages := map[string]bool{}, map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				if a.Key == "id" && strings.HasPrefix(a.Val, "page-course-view-") {
					result.CoursePage = true
				}
				if a.Key == "data-courseid" && a.Val == strconv.Itoa(courseID) {
					result.CoursePage = true
				}
				if a.Key != "href" && a.Key != "src" && a.Key != "data" {
					continue
				}
				u, err := base.Parse(a.Val)
				if err != nil || !c.allowed(u) {
					continue
				}
				u.Fragment = ""
				rel := strings.TrimPrefix(u.Path, strings.TrimRight(c.base.Path, "/"))
				if strings.HasPrefix(rel, "/pluginfile.php/") {
					for _, area := range []string{"/mod_resource/", "/mod_folder/", "/mod_page/", "/mod_assign/introattachment/", "/course/section/", "/course/summary/"} {
						if strings.Contains(rel, area) {
							files[cleanMoodleURL(u.String())] = true
							break
						}
					}
					continue
				}
				q := u.Query()
				id, err := strconv.Atoi(q.Get("id"))
				if err != nil || id <= 0 {
					continue
				}
				switch rel {
				case "/mod/resource/view.php", "/mod/folder/view.php", "/mod/page/view.php", "/mod/assign/view.php", "/course/section.php":
					u.RawQuery = url.Values{"id": {strconv.Itoa(id)}}.Encode()
					pages[u.String()] = true
				case "/course/view.php":
					if id == courseID && q.Get("section") != "" {
						if section, err := strconv.Atoi(q.Get("section")); err == nil && section >= 0 {
							u.RawQuery = url.Values{"id": {strconv.Itoa(id)}, "section": {strconv.Itoa(section)}}.Encode()
							pages[u.String()] = true
						}
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	for u := range files {
		result.Files = append(result.Files, u)
	}
	for u := range pages {
		result.Pages = append(result.Pages, u)
	}
	slices.Sort(result.Files)
	slices.Sort(result.Pages)
	return result, nil
}

// Only known read-only course/material view routes are followed. Forms, quizzes,
// submissions, logout links and external hosts are never followed.
func (c *moodleClient) cookieMaterials(ctx context.Context, courseID int) ([]moodleMaterial, error) {
	first := c.endpoint(fmt.Sprintf("/course/view.php?id=%d", courseID))
	queue := []string{first}
	seen := map[string]bool{}
	materialURLs := map[string]bool{}
	for len(queue) > 0 {
		pageURL := queue[0]
		queue = queue[1:]
		if seen[pageURL] {
			continue
		}
		if len(seen) >= 250 {
			return nil, fmt.Errorf("Moodle course exceeded 250 material pages; export a narrower course")
		}
		seen[pageURL] = true
		r, err := c.request(ctx, "GET", pageURL, "", "", "", true)
		if err != nil {
			return nil, err
		}
		final, _ := url.Parse(r.URL)
		if strings.Contains(final.Path, "/pluginfile.php/") {
			materialURLs[cleanMoodleURL(r.URL)] = true
			continue
		}
		links, err := c.parsePage(r.Body, r.URL, courseID)
		if err != nil {
			return nil, err
		}
		if pageURL == first && !links.CoursePage {
			return nil, fmt.Errorf("Moodle course page not recognized; check login, course ID or site theme")
		}
		if strings.HasSuffix(final.Path, "/mod/resource/view.php") && len(links.Files) == 0 {
			return nil, fmt.Errorf("Moodle resource page has no supported file; check access or site theme")
		}
		for _, f := range links.Files {
			materialURLs[f] = true
		}
		queue = append(queue, links.Pages...)
	}
	items := []moodleMaterial{}
	for raw := range materialURLs {
		u, _ := url.Parse(raw)
		name := path.Base(u.Path)
		items = append(items, moodleMaterial{Key: cleanMoodleURL(raw), URL: raw, Name: name, Section: "Course files"})
	}
	slices.SortFunc(items, func(a, b moodleMaterial) int { return strings.Compare(a.Key, b.Key) })
	return items, nil
}
