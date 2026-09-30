package mcpserver

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseDate accepts an ISO date or one of the relative forms an agent is likely
// to reach for. Relative forms resolve in the server's local timezone, which is
// whatever TZ the container runs with, because "today" in UTC is the wrong day
// for part of every day for most of the world.
func parseDate(s string, now time.Time) (time.Time, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch s {
	case "", "today":
		return today, nil
	case "yesterday":
		return today.AddDate(0, 0, -1), nil
	case "tomorrow":
		return today.AddDate(0, 0, 1), nil
	}
	// "-30d", "-6m", "-1y": a window measured back from today, which is how a
	// question like "the last three months" arrives.
	if len(s) > 2 && s[0] == '-' {
		if n, err := strconv.Atoi(s[1 : len(s)-1]); err == nil {
			switch s[len(s)-1] {
			case 'd':
				return today.AddDate(0, 0, -n), nil
			case 'w':
				return today.AddDate(0, 0, -7*n), nil
			case 'm':
				return today.AddDate(0, -n, 0), nil
			case 'y':
				return today.AddDate(-n, 0, 0), nil
			}
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01", s, now.Location()); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("could not read %q as a date; use YYYY-MM-DD, 'today', 'yesterday' or an offset like '-30d'", s)
}

// truncate keeps free-text fields from dominating a list line. The ellipsis is
// there so the model can tell the difference between a short memo and a cut one.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}
