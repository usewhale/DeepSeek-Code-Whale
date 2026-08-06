package tui

import (
	"strconv"
	"strings"
)

type sessionChoiceDisplay struct {
	Number       string
	Updated      string
	Branch       string
	Conversation string
}

func isSessionHeaderRow(row string) bool {
	return strings.HasSuffix(strings.TrimSpace(row), ":")
}

func displaySessionChoiceRow(row string) string {
	// The TUI owns the left gutter for selection. Preserve the numeric column
	// from the app row, but hide the app-level current-session marker.
	if strings.HasPrefix(row, "*") {
		return " " + strings.TrimPrefix(row, "*")
	}
	return row
}

func parseSessionChoiceDisplay(row string) (sessionChoiceDisplay, bool) {
	fields := strings.Fields(displaySessionChoiceRow(row))
	if len(fields) < 3 {
		return sessionChoiceDisplay{}, false
	}
	number := fields[0]
	if number == "" || strings.TrimSuffix(number, ")") == "#" {
		return sessionChoiceDisplay{}, false
	}
	if _, err := strconv.Atoi(strings.TrimSuffix(number, ")")); err != nil {
		return sessionChoiceDisplay{}, false
	}
	updated := fields[1]
	branchIndex := 2
	if len(fields) >= 4 && fields[2] == "ago" {
		updated = fields[1] + " " + fields[2]
		branchIndex = 3
	}
	if branchIndex >= len(fields) {
		return sessionChoiceDisplay{}, false
	}
	branch := fields[branchIndex]
	conversation := strings.Join(fields[branchIndex+1:], " ")
	return sessionChoiceDisplay{
		Number:       number,
		Updated:      updated,
		Branch:       branch,
		Conversation: conversation,
	}, true
}

func sessionChoiceNumberAt(rows []string, idx int) int {
	if idx < 0 || idx >= len(rows) {
		return 0
	}
	s := strings.TrimSpace(rows[idx])
	if strings.HasPrefix(s, "*") {
		s = strings.TrimSpace(strings.TrimPrefix(s, "*"))
	}
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0
	}
	n := strings.TrimSuffix(f[0], ")")
	v, err := strconv.Atoi(n)
	if err != nil || v < 1 {
		return 0
	}
	return v
}

func firstSessionChoiceIndex(rows []string) int {
	for i := range rows {
		if sessionChoiceNumberAt(rows, i) > 0 {
			return i
		}
	}
	return 0
}

func prevSessionChoiceIndex(rows []string, cur int) int {
	if len(rows) == 0 {
		return 0
	}
	for i := cur - 1; i >= 0; i-- {
		if sessionChoiceNumberAt(rows, i) > 0 {
			return i
		}
	}
	// Wrap around: nothing selectable above, continue from the bottom.
	for i := len(rows) - 1; i > cur; i-- {
		if sessionChoiceNumberAt(rows, i) > 0 {
			return i
		}
	}
	return cur
}

func nextSessionChoiceIndex(rows []string, cur int) int {
	if len(rows) == 0 {
		return 0
	}
	for i := cur + 1; i < len(rows); i++ {
		if sessionChoiceNumberAt(rows, i) > 0 {
			return i
		}
	}
	// Wrap around: nothing selectable below, continue from the top.
	for i := 0; i < cur; i++ {
		if sessionChoiceNumberAt(rows, i) > 0 {
			return i
		}
	}
	return cur
}
