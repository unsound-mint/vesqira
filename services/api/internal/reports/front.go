package reports

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Front matter is an optional block of "key: value" lines between two "---" lines at the very top of a body, as
// Obsidian writes it. Two keys mean something: date (YYYY-MM-DD, the date the report is about) and summary (one line
// shown under the title). Other keys, such as the properties a Notion import carries, are kept but never shown or
// published. It is not YAML: one line per key, no nesting, no quoting. apps/web/src/reports/front.ts parses the same
// format and both are tested against packages/contracts/fixtures/report-blocks.json.
const (
	frontMaxLines   = 64
	MaxSummaryRunes = 600
)

var frontKeyLine = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*:( .*)?$`)

// SplitFront returns the front matter lines (without the --- fences) and the rest of the body. A body that does not
// start with a well-formed block has no front matter and is returned whole.
func SplitFront(body string) (front []string, rest string) {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if !strings.HasPrefix(body, "---\n") {
		return nil, body
	}
	lines := strings.Split(body, "\n")
	for i := 1; i < len(lines) && i <= frontMaxLines; i++ {
		l := strings.TrimRight(lines[i], " ")
		if l == "---" {
			return lines[1:i], strings.Join(lines[i+1:], "\n")
		}
		if l != "" && !frontKeyLine.MatchString(l) {
			return nil, body
		}
	}
	return nil, body
}

// FrontValue is the value of key in the body's front matter, "" when absent. The first occurrence wins.
func FrontValue(body, key string) string {
	front, _ := SplitFront(body)
	for _, l := range front {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(k, key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ReportDate is the front matter date when it is a real YYYY-MM-DD date, otherwise "".
func ReportDate(body string) string {
	d := FrontValue(body, "date")
	if _, err := time.Parse("2006-01-02", d); err != nil || len(d) != 10 {
		return ""
	}
	return d
}

// ReportSummary is the front matter summary, cut to MaxSummaryRunes.
func ReportSummary(body string) string {
	s := FrontValue(body, "summary")
	if utf8.RuneCountInString(s) > MaxSummaryRunes {
		s = string([]rune(s)[:MaxSummaryRunes-1]) + "…"
	}
	return s
}
