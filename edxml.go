package main

import (
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"regexp"
	"strconv"
	"strings"
)

type xmlNode struct {
	tag, text string
	attrs     map[string]string
	children  []*xmlNode
}

func parseEdXML(src string) (*xmlNode, error) {
	d := xml.NewDecoder(strings.NewReader("<root>" + src + "</root>"))
	d.Entity = map[string]string{"nbsp": " "}
	root := &xmlNode{}
	stack := []*xmlNode{root}
	for {
		t, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid Ed XML: %w", err)
		}
		p := stack[len(stack)-1]
		switch v := t.(type) {
		case xml.StartElement:
			n := &xmlNode{tag: v.Name.Local, attrs: map[string]string{}}
			for _, a := range v.Attr {
				n.attrs[a.Name.Local] = a.Value
			}
			p.children = append(p.children, n)
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			p.children = append(p.children, &xmlNode{text: string(v)})
		}
	}
	return root, nil
}

func plain(n *xmlNode) string {
	if n.tag == "break" {
		return "\n"
	}
	s := n.text
	for _, c := range n.children {
		s += plain(c)
	}
	return s
}

func mdEscape(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
func mdURL(s string) string {
	return "<" + strings.NewReplacer("<", "%3C", ">", "%3E", "\n", "%0A", "\r", "%0D", " ", "%20").Replace(s) + ">"
}
func fence(s, lang string) string {
	f := "```"
	for strings.Contains(s, f) {
		f += "`"
	}
	return "\n\n" + f + lang + "\n" + strings.TrimRight(s, "\n") + "\n" + f + "\n\n"
}

// Unknown tags retain their contents. XML remains a data format, never executed.
func renderNode(n *xmlNode, assets map[string]string) string {
	if n.tag == "" {
		if n.text != "" {
			return mdEscape(n.text)
		}
	}
	inner := func() string {
		var b strings.Builder
		for _, c := range n.children {
			b.WriteString(renderNode(c, assets))
		}
		return b.String()
	}
	link := func(u string) string {
		if p, ok := assets[u]; ok {
			return "../_assets/" + p
		}
		return u
	}
	switch n.tag {
	case "mention":
		return "@[用户]"
	case "paragraph":
		return "\n\n" + strings.TrimSpace(inner()) + "\n\n"
	case "heading":
		level, _ := strconv.Atoi(n.attrs["level"])
		level = min(6, max(2, level+1))
		return "\n\n" + strings.Repeat("#", level) + " " + strings.TrimSpace(inner()) + "\n\n"
	case "bold":
		return "**" + inner() + "**"
	case "italic":
		return "*" + inner() + "*"
	case "strike":
		return "~~" + inner() + "~~"
	case "underline":
		return "<u>" + inner() + "</u>"
	case "break":
		return "  \n"
	case "code":
		s := plain(n)
		f := "`"
		for strings.Contains(s, f) {
			f += "`"
		}
		return f + " " + s + " " + f
	case "math":
		return "$" + plain(n) + "$"
	case "link":
		return "[" + strings.TrimSpace(inner()) + "](" + mdURL(link(n.attrs["href"])) + ")"
	case "image":
		return "![" + mdEscape(n.attrs["alt"]) + "](" + mdURL(link(n.attrs["src"])) + ")"
	case "file":
		u := n.attrs["url"]
		if u == "" {
			u = n.attrs["href"]
		}
		if u == "" {
			u = n.attrs["src"]
		}
		title := n.attrs["filename"]
		if title == "" {
			title = strings.TrimSpace(plain(n))
		}
		if title == "" {
			title = "Attachment"
		}
		return "\n\n[" + mdEscape(title) + "](" + mdURL(link(u)) + ")\n\n"
	case "video":
		return "\n\n[Video](" + mdURL(n.attrs["src"]) + ")\n\n"
	case "snippet":
		var parts []string
		for _, child := range n.children {
			if child.tag == "snippet-file" {
				parts = append(parts, plain(child))
			}
		}
		if len(parts) > 0 {
			return fence(strings.Join(parts, "\n"), n.attrs["language"])
		}
		return fence(plain(n), n.attrs["language"])
	case "pre":
		return fence(plain(n), n.attrs["language"])
	case "web-snippet":
		var b strings.Builder
		for _, c := range n.children {
			b.WriteString(fence(plain(c), c.attrs["language"]))
		}
		return b.String()
	case "list":
		var lines []string
		index := 0
		for _, c := range n.children {
			if c.tag != "list-item" {
				continue
			}
			index++
			prefix := "- "
			if n.attrs["style"] == "number" || n.attrs["style"] == "ordered" {
				prefix = fmt.Sprintf("%d. ", index)
			}
			body := strings.TrimSpace(renderNode(c, assets))
			lines = append(lines, prefix+strings.ReplaceAll(body, "\n", "\n"+strings.Repeat(" ", len(prefix))))
		}
		return "\n\n" + strings.Join(lines, "\n") + "\n\n"
	case "blockquote", "callout":
		s := strings.TrimSpace(inner())
		if n.tag == "callout" {
			s = "[!" + strings.ToUpper(n.attrs["type"]) + "]\n" + s
		}
		return "\n\n> " + strings.ReplaceAll(s, "\n", "\n> ") + "\n\n"
	case "table":
		var rows [][]string
		cols := 0
		for _, row := range n.children {
			if row.tag != "table-row" {
				continue
			}
			cells := []string{}
			for _, cell := range row.children {
				if cell.tag == "table-cell" {
					cells = append(cells, strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(renderNode(cell, assets)), "|", "\\|"), "\n", "<br>"))
				}
			}
			cols = max(cols, len(cells))
			rows = append(rows, cells)
		}
		var b strings.Builder
		b.WriteString("\n\n")
		for i, row := range rows {
			for len(row) < cols {
				row = append(row, "")
			}
			b.WriteString("| " + strings.Join(row, " | ") + " |\n")
			if i == 0 {
				b.WriteString("|" + strings.Repeat(" --- |", cols) + "\n")
			}
		}
		b.WriteString("\n")
		return b.String()
	case "spoiler":
		return "\n\n<details>\n<summary>Spoiler</summary>\n\n" + strings.TrimSpace(inner()) + "\n\n</details>\n\n"
	default:
		return inner()
	}
}

var mentionPattern = regexp.MustCompile(`(?s)<mention\b[^>]*>(.*?)</mention>`)

func renderBody(content, document string, assets map[string]string) (string, string, error) {
	for _, m := range mentionPattern.FindAllStringSubmatch(content, -1) {
		name := strings.TrimSpace(html.UnescapeString(m[1]))
		if name != "" {
			document = strings.ReplaceAll(document, name, "@[用户]")
		}
	}
	if strings.TrimSpace(content) == "" {
		return strings.TrimSpace(document), strings.TrimSpace(document), nil
	}
	n, err := parseEdXML(content)
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(renderNode(n, assets)), strings.TrimSpace(document), nil
}
