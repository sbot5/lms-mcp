// Package mcpserver serves lms-mcp's read-only MCP tools over stdio. This file
// holds the transport-agnostic helpers: opaque cursors, output budgeting and
// text truncation, so every tool returns a small, paginated, object-rooted
// result (see docs/research/mcp-clients.md).
package mcpserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// BudgetChars is the soft cap on a single tool result, about 8k tokens. Clients
// such as Claude Code warn past ~10k tokens and Codex truncates near there.
const BudgetChars = 30000

// DefaultLimit and MaxLimit bound list pagination.
const (
	DefaultLimit = 25
	MaxLimit     = 100
)

// Cursor is the opaque pagination token exchanged with clients.
type Cursor struct {
	Offset int `json:"o"`
}

// EncodeCursor renders an offset as an opaque string. Offset 0 yields "" so the
// first page carries no cursor.
func EncodeCursor(offset int) string {
	if offset <= 0 {
		return ""
	}
	b, _ := json.Marshal(Cursor{Offset: offset})
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor parses a cursor. The empty string means "start at 0".
func DecodeCursor(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, fmt.Errorf("invalid cursor")
	}
	var c Cursor
	if err := json.Unmarshal(b, &c); err != nil || c.Offset < 0 {
		return 0, fmt.Errorf("invalid cursor")
	}
	return c.Offset, nil
}

// ClampLimit normalizes a requested limit into [1, MaxLimit], defaulting 0.
func ClampLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultLimit
	case limit > MaxLimit:
		return MaxLimit
	default:
		return limit
	}
}

// Page describes a slice of a larger list for a tool result.
type Page struct {
	Offset     int
	Limit      int
	Total      int
	NextCursor string
	HasMore    bool
}

// Paginate computes the [lo:hi] bounds for a page over total items and the
// resulting Page metadata. Callers slice their own data with lo:hi.
func Paginate(total, offset, limit int) (lo, hi int, p Page) {
	limit = ClampLimit(limit)
	if offset < 0 {
		offset = 0
	}
	lo = min(offset, total)
	hi = min(lo+limit, total)
	p = Page{Offset: offset, Limit: limit, Total: total, HasMore: hi < total}
	if p.HasMore {
		p.NextCursor = EncodeCursor(hi)
	}
	return lo, hi, p
}

// ClampText truncates s to at most max runes, appending a marker when cut. It
// returns the possibly-truncated string and whether truncation happened.
func ClampText(s string, max int) (string, bool) {
	if max <= 0 {
		return "", len(s) > 0
	}
	r := []rune(s)
	if len(r) <= max {
		return s, false
	}
	const marker = "\n…[truncated]"
	mlen := len([]rune(marker))
	if max <= mlen {
		// Too small to fit the marker; hard-truncate to the cap.
		return string(r[:max]), true
	}
	return string(r[:max-mlen]) + marker, true
}

// EstimateTokens is a rough token estimate (~4 chars per token) for sizing
// results against a client budget.
func EstimateTokens(s string) int { return (len([]rune(s)) + 3) / 4 }

// FitText clamps s so its estimated size stays within BudgetChars worth of
// characters, for a single large text field.
func FitText(s string) (string, bool) { return ClampText(s, BudgetChars) }

// JoinLimited joins lines with "\n" but stops once the budget is reached,
// returning the joined text and whether it was cut. Useful for line-oriented
// list bodies.
func JoinLimited(lines []string, budget int) (string, bool) {
	var b strings.Builder
	for i, ln := range lines {
		if b.Len()+len(ln)+1 > budget {
			return b.String(), true
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(ln)
	}
	return b.String(), false
}
