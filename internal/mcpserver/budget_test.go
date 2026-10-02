package mcpserver

import (
	"strings"
	"testing"
)

func TestCursorRoundTrip(t *testing.T) {
	if EncodeCursor(0) != "" {
		t.Fatal("offset 0 must encode to empty")
	}
	for _, off := range []int{1, 25, 1000} {
		s := EncodeCursor(off)
		got, err := DecodeCursor(s)
		if err != nil || got != off {
			t.Fatalf("round trip %d: got %d err %v", off, got, err)
		}
	}
	if _, err := DecodeCursor("not-base64!!"); err == nil {
		t.Fatal("garbage cursor should error")
	}
	if n, err := DecodeCursor(""); err != nil || n != 0 {
		t.Fatalf("empty cursor should be 0, got %d %v", n, err)
	}
}

func TestClampLimit(t *testing.T) {
	cases := map[int]int{0: DefaultLimit, -5: DefaultLimit, 10: 10, 1000: MaxLimit}
	for in, want := range cases {
		if got := ClampLimit(in); got != want {
			t.Errorf("ClampLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestPaginate(t *testing.T) {
	lo, hi, p := Paginate(100, 0, 25)
	if lo != 0 || hi != 25 || !p.HasMore || p.NextCursor == "" {
		t.Fatalf("page1: lo=%d hi=%d %+v", lo, hi, p)
	}
	off, _ := DecodeCursor(p.NextCursor)
	lo, hi, p = Paginate(100, off, 25)
	if lo != 25 || hi != 50 {
		t.Fatalf("page2 bounds lo=%d hi=%d", lo, hi)
	}
	// Last page.
	lo, hi, p = Paginate(100, 90, 25)
	if lo != 90 || hi != 100 || p.HasMore || p.NextCursor != "" {
		t.Fatalf("last page: lo=%d hi=%d %+v", lo, hi, p)
	}
	// Offset past the end is clamped.
	lo, hi, p = Paginate(10, 999, 25)
	if lo != 10 || hi != 10 || p.HasMore {
		t.Fatalf("over-offset: lo=%d hi=%d %+v", lo, hi, p)
	}
}

func TestClampText(t *testing.T) {
	if s, cut := ClampText("short", 100); cut || s != "short" {
		t.Fatalf("short text should be untouched: %q cut=%v", s, cut)
	}
	long := strings.Repeat("x", 500)
	s, cut := ClampText(long, 50)
	if !cut || len([]rune(s)) > 50 {
		t.Fatalf("long text should be truncated to <=50 runes, got %d cut=%v", len([]rune(s)), cut)
	}
	if !strings.Contains(s, "truncated") {
		t.Fatal("truncation marker missing")
	}
	// CJK runes counted by rune, not byte.
	cjk := strings.Repeat("字", 100)
	s, cut = ClampText(cjk, 10)
	if !cut || len([]rune(s)) > 10 {
		t.Fatalf("cjk truncation wrong: %d runes", len([]rune(s)))
	}
}

func TestJoinLimited(t *testing.T) {
	lines := []string{"aaaa", "bbbb", "cccc", "dddd"}
	out, cut := JoinLimited(lines, 10)
	if !cut {
		t.Fatal("expected truncation under a tight budget")
	}
	if strings.Count(out, "\n") > 2 {
		t.Fatalf("joined too much: %q", out)
	}
	out, cut = JoinLimited(lines, 10000)
	if cut || out != "aaaa\nbbbb\ncccc\ndddd" {
		t.Fatalf("full join wrong: %q cut=%v", out, cut)
	}
}
