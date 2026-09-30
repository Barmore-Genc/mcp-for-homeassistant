package mcpserver

import (
	"testing"
	"time"
)

var testNow = time.Date(2026, 3, 9, 15, 4, 0, 0, time.UTC)

func TestParseDate(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", "2026-03-09"},
		{"today", "2026-03-09"},
		{"yesterday", "2026-03-08"},
		{"2026-01-15", "2026-01-15"},
		{"-30d", "2026-02-07"},
		{"-3m", "2025-12-09"},
		{"-1y", "2025-03-09"},
		{"2026-01", "2026-01-01"},
	} {
		got, err := parseDate(c.in, testNow)
		if err != nil {
			t.Errorf("parseDate(%q): %v", c.in, err)
			continue
		}
		if got.Format("2006-01-02") != c.want {
			t.Errorf("parseDate(%q) = %s, want %s", c.in, got.Format("2006-01-02"), c.want)
		}
	}
	if _, err := parseDate("sometime last spring", testNow); err == nil {
		t.Error("an unparseable date was accepted")
	}
}

func TestTruncateMarksWhatItCut(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Fatalf("got %q", got)
	}
	if got := truncate("a much longer memo than fits", 10); got != "a much lon…" {
		t.Fatalf("got %q", got)
	}
}
